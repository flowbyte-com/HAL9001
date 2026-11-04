// Package session stores HAL9001 conversations durably and model-independently:
// the discussion survives provider switches because the record format never
// mentions a provider. Storage is a JSONL file per session: one message per
// line, append-only. Conversation storage is narrowly authorized persistence —
// it is not memory admission and never executes anything.
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Role is a conversation participant.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// Message is one stored conversation turn.
type Message struct {
	Role      Role      `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"ts"`
	// TurnID identifies the exchange this message belongs to. It is
	// minted once, when the exchange starts, and never changes: it is
	// what an attribution binds to, so the attribution of an answer
	// stays with that answer across reopening.
	TurnID string `json:"turn_id,omitempty"`
	// Attribution names the skills whose instructions shaped this
	// answer. It is metadata about the turn, never part of Content:
	// the content is exactly what the model produced.
	Attribution *Attribution `json:"attribution,omitempty"`
}

// Attribution records which skills shaped one assistant answer.
//
// It is written once, at the end of the turn, with the version and the
// content hash of the instructions that were actually supplied to the
// model. A later update of a skill in the store therefore cannot change
// what an earlier answer is credited with.
type Attribution struct {
	// TurnID is the exchange the attribution belongs to. It repeats the
	// message's own turn id so a mismatch is detectable rather than
	// silent.
	TurnID string `json:"turn_id"`
	// Skills are the skills whose instructions were loaded into the
	// request that produced the answer. A skill that failed to load is
	// not here: it never reached the model.
	Skills []AttributedSkill `json:"skills,omitempty"`
	// Source says how the skill was chosen.
	Source string `json:"source,omitempty"`
}

// AttributedSkill is one skill credited with shaping an answer.
type AttributedSkill struct {
	// ID is the store's identifier for the exact version that was read.
	ID string `json:"id,omitempty"`
	// Name is the skill's name.
	Name string `json:"name"`
	// Source says how it was chosen: automatic, explicit or continued.
	Source string `json:"source,omitempty"`
	// Version is the version that was supplied.
	Version string `json:"version,omitempty"`
	// ContentHash is the hash of the instructions that were supplied.
	ContentHash string `json:"content_hash,omitempty"`
}

// Turn identifies one exchange: the operator's message, the answer and
// the metadata that belongs to them.
type Turn struct {
	// ID is the stable identity of the exchange.
	ID string
	// Attribution is the record of the skills that shaped the answer.
	// It is nil for an exchange that used none.
	Attribution *Attribution
}

// NewTurnID mints a fresh, opaque turn identity.
//
// The identity is opaque on purpose: it names an exchange, it says
// nothing about what was said in it.
func NewTurnID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A turn identity that cannot be random is still better than
		// none: the clock is unique enough within one session.
		return "turn-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "turn-" + hex.EncodeToString(b[:])
}

// Store persists one session's messages.
type Store struct {
	mu   sync.Mutex
	path string
}

// MaxTitleRunes bounds a display title. Titles are presentation only and
// never touch the filesystem.
const MaxTitleRunes = 80

// NormalizeTitle trims a display title and bounds its length. An empty
// result means "fall back to the date/time title".
func NormalizeTitle(title string) string {
	title = strings.TrimSpace(title)
	runes := []rune(title)
	if len(runes) > MaxTitleRunes {
		title = string(runes[:MaxTitleRunes])
	}
	return strings.TrimSpace(title)
}

// DefaultTitle formats a date/time title in the local timezone.
func DefaultTitle(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.Local().Format("2006-01-02 15:04")
}

// Meta is the per-session display record. The session ID (the file name)
// never changes; the title is presentation only.
type Meta struct {
	Title     string    `json:"title,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// metaPath returns the sidecar path. Titles never become file names, so
// user input cannot cause path traversal here.
func metaPath(dir, session string) string {
	return filepath.Join(dir, session+".meta.json")
}

// LoadMeta reads the display record. A missing or corrupt file returns
// false rather than failing the conversation.
func LoadMeta(dir, session string) (Meta, bool) {
	raw, err := os.ReadFile(metaPath(dir, session))
	if err != nil {
		return Meta{}, false
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Meta{}, false
	}
	return meta, true
}

// SaveMeta writes the display record.
func SaveMeta(dir, session string, meta Meta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	if err := os.WriteFile(metaPath(dir, session), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}

// Info describes one stored conversation for the session browser.
type Info struct {
	ID      string
	Title   string
	Created time.Time
	Updated time.Time
}

// Stat derives the display record for one stored conversation. Old
// sessions predate titles: their title and creation time come from the
// conversation itself, without writing anything.
func Stat(dir, id string) Info {
	info := Info{ID: id}
	meta, ok := LoadMeta(dir, id)
	created := meta.CreatedAt
	if st, err := os.Stat(filepath.Join(dir, id+".jsonl")); err == nil {
		info.Updated = st.ModTime().Local()
		if created.IsZero() {
			created = st.ModTime()
		}
	}
	if store, err := Open(dir, id); err == nil {
		if history, err := store.History(); err == nil && len(history) > 0 {
			first := history[0].Timestamp
			if !first.IsZero() && (created.IsZero() || first.Before(created)) {
				created = first
			}
		}
	}
	info.Created = created.Local()
	title := NormalizeTitle(meta.Title)
	if title == "" {
		title = DefaultTitle(created)
	}
	info.Title = title
	_ = ok
	return info
}

// RenameSession sets the display title of one conversation. Only the
// sidecar changes; the session file and its history are untouched.
func RenameSession(dir, id, title string) (string, error) {
	if !ValidName(id) {
		return "", fmt.Errorf("session: invalid session name %q", id)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); err != nil {
		return "", fmt.Errorf("session: unknown conversation %q", id)
	}
	meta, _ := LoadMeta(dir, id)
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = Stat(dir, id).Created.UTC()
	}
	meta.Title = NormalizeTitle(title)
	if err := SaveMeta(dir, id, meta); err != nil {
		return "", err
	}
	display := meta.Title
	if display == "" {
		display = DefaultTitle(meta.CreatedAt)
	}
	return display, nil
}

// ValidName reports whether a session name is safe to use as a file name.
func ValidName(session string) bool {
	if strings.TrimSpace(session) == "" {
		return false
	}
	if strings.ContainsAny(session, `/\`) || strings.Contains(session, "..") {
		return false
	}
	return true
}

// Open creates the session directory if needed and returns the store for
// the named session.
func Open(dir, session string) (*Store, error) {
	if !ValidName(session) {
		return nil, fmt.Errorf("session: invalid session name %q", session)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	return &Store{path: filepath.Join(dir, session+".jsonl")}, nil
}

// Append records one message. Empty content is rejected: the store keeps
// the conversation, not padding.
func (s *Store) Append(role Role, content string) (Message, error) {
	return s.AppendTurn(role, content, Turn{})
}

// AppendTurn records one message together with the exchange it belongs
// to.
//
// The turn identity and the attribution are stored beside the content,
// never inside it: the content stays exactly what the model produced, so
// a transcript of the conversation carries no interface metadata inside
// the messages themselves.
func (s *Store) AppendTurn(role Role, content string, turn Turn) (Message, error) {
	if strings.TrimSpace(content) == "" {
		return Message{}, fmt.Errorf("session: refusing to store empty content")
	}
	switch role {
	case RoleUser, RoleAssistant, RoleSystem:
	default:
		return Message{}, fmt.Errorf("session: unknown role %q", role)
	}
	msg := Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now().UTC(),
		TurnID:    turn.ID,
	}
	if turn.Attribution != nil {
		// The attribution repeats the turn identity it belongs to, so a
		// record that was moved by mistake is detectable.
		a := *turn.Attribution
		if a.TurnID == "" {
			a.TurnID = turn.ID
		}
		msg.Attribution = &a
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return Message{}, fmt.Errorf("session: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return Message{}, fmt.Errorf("session: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return Message{}, fmt.Errorf("session: %w", err)
	}
	return msg, nil
}

// History replays the session in order. A missing file is an empty
// conversation, not an error; a malformed line fails loudly so corruption
// is never silently absorbed into context.
func (s *Store) History() ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	defer f.Close()
	var out []Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var msg Message
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			return nil, fmt.Errorf("session: line %d: %w", line, err)
		}
		out = append(out, msg)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	return out, nil
}

// Path reports the backing file, for diagnostics.
func (s *Store) Path() string { return s.path }
