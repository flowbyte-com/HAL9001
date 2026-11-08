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
	"unicode"
	"unicode/utf8"

	"github.com/flowbyte-com/HAL9001/internal/attachments"
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
	// ToolLedger is the verified record of tool executions that produced
	// or accompanied this assistant answer. It is absent on user and
	// system messages and on older sessions. It is evidence, never
	// authority: nothing here can enable a tool or change the current
	// permission.
	ToolLedger *ToolLedger `json:"tool_ledger,omitempty"`
	// Attachments names the files the operator attached to this
	// message. It is a reference, not a copy: the record holds the
	// name, the size, the checksum and the location of the stored
	// copy, never the contents, so a session file never grows with the
	// size of the files that were discussed.
	Attachments []attachments.Meta `json:"attachments,omitempty"`
}

// ToolLedgerVersion is the explicit schema version of ToolLedger. A
// ledger with any other version is ignored rather than guessed at.
const ToolLedgerVersion = 1

// Bounds keep one turn's evidence small even when a provider asks for many
// tool calls. The ledger never stores raw stdout, file contents, command
// text, environment or credentials.
const (
	MaxToolExecutionsPerTurn = 64
	MaxToolSummaryBytes      = 512
	MaxToolTargetBytes       = 1024
	MaxToolIDBytes           = 128
	MaxToolNameBytes         = 64
)

// ToolStatus is the outcome of one tool execution. It is intentionally
// separate from the live UI status: a historical record says what happened,
// not what is currently available.
type ToolStatus string

const (
	ToolSuccess   ToolStatus = "success"
	ToolFailure   ToolStatus = "failure"
	ToolCancelled ToolStatus = "cancelled"
	ToolRefused   ToolStatus = "refused"
)

// ToolExecution is one verified historical tool execution. TurnID binds it
// to the assistant message that carries the ledger; CallID preserves the
// provider's call-to-result association where one was available.
type ToolExecution struct {
	TurnID     string     `json:"turn_id"`
	CallID     string     `json:"call_id,omitempty"`
	Tool       string     `json:"tool"`
	Action     string     `json:"action,omitempty"`
	Target     string     `json:"target,omitempty"`
	Mode       string     `json:"mode"`
	Status     ToolStatus `json:"status"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	Bytes      int        `json:"bytes,omitempty"`
	Truncated  bool       `json:"truncated,omitempty"`
	// Summary is a short, redacted description of the outcome, for
	// example "exit 17" or "wrote 13 bytes". It never contains the
	// command text, file contents or raw output.
	Summary string `json:"summary,omitempty"`
	// Expected is the stable expectation declared by the tool call
	// before execution, if any: "missing" or "exit:17". ExpectedMet
	// records whether the verified result matched it. The fields are
	// additive and optional; older ledgers simply have no expectation.
	Expected    string `json:"expected,omitempty"`
	ExpectedMet bool   `json:"expected_met,omitempty"`
}

// ToolLedger is the versioned evidence record attached to one assistant
// message. SessionID and TurnID make a copied or mismatched record
// detectable instead of silently misattributed.
type ToolLedger struct {
	Version    int             `json:"version"`
	SessionID  string          `json:"session_id"`
	TurnID     string          `json:"turn_id"`
	Executions []ToolExecution `json:"executions"`
}

// messageWire is the on-disk shape used by UnmarshalJSON. The ledger and
// the attachment references are carried as raw JSON so a damaged optional
// field can be ignored without losing the conversation around it.
type messageWire struct {
	Role        Role            `json:"role"`
	Content     string          `json:"content"`
	Timestamp   time.Time       `json:"ts"`
	TurnID      string          `json:"turn_id,omitempty"`
	Attribution *Attribution    `json:"attribution,omitempty"`
	ToolLedger  json.RawMessage `json:"tool_ledger,omitempty"`
	Attachments json.RawMessage `json:"attachments,omitempty"`
}

// UnmarshalJSON keeps old sessions and damaged optional ledgers loadable.
// Core message fields still fail loudly if they are malformed; a malformed
// or unsupported ledger is simply treated as absent evidence.
func (m *Message) UnmarshalJSON(data []byte) error {
	var w messageWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*m = Message{
		Role:        w.Role,
		Content:     w.Content,
		Timestamp:   w.Timestamp,
		TurnID:      w.TurnID,
		Attribution: w.Attribution,
		Attachments: parseAttachments(w.Attachments),
	}
	if len(w.ToolLedger) > 0 {
		m.ToolLedger = parseToolLedger(w.ToolLedger)
	}
	return nil
}

// parseAttachments reads the optional attachment references of a message.
//
// The references are optional metadata, and they are the only part of a
// record that a damaged file, an older version or a hand edit can make
// unreadable. A whole conversation must not be lost because of them:
// the field that cannot be read at all is treated as absent, a single
// damaged reference is dropped without taking the others with it, and
// every reference that can be read is sanitised before it is used.
func parseAttachments(raw json.RawMessage) []attachments.Meta {
	if len(raw) == 0 {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		// Not a list: the references are unreadable, but the message
		// around them is still good.
		return nil
	}
	out := make([]attachments.Meta, 0, len(items))
	for _, item := range items {
		var meta attachments.Meta
		if err := json.Unmarshal(item, &meta); err != nil {
			// One damaged reference does not lose the others.
			continue
		}
		out = append(out, attachments.SanitizeMeta(meta))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ToolExecutions returns the valid evidence attached to this message. A
// missing, older, mismatched or malformed ledger yields nil: evidence is
// never inferred from assistant text.
func (m Message) ToolExecutions() []ToolExecution {
	if m.Role != RoleAssistant || m.ToolLedger == nil {
		return nil
	}
	if m.ToolLedger.Version != ToolLedgerVersion {
		return nil
	}
	if m.TurnID == "" || m.ToolLedger.TurnID != m.TurnID {
		return nil
	}
	out := make([]ToolExecution, 0, len(m.ToolLedger.Executions))
	for _, e := range m.ToolLedger.Executions {
		if len(out) >= MaxToolExecutionsPerTurn {
			break
		}
		if e.TurnID != m.TurnID {
			continue
		}
		e = sanitizeToolExecution(e)
		if e.Tool == "" || !validToolStatus(e.Status) || !validToolMode(e.Mode) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func parseToolLedger(raw json.RawMessage) *ToolLedger {
	var w struct {
		Version    int               `json:"version"`
		SessionID  string            `json:"session_id"`
		TurnID     string            `json:"turn_id"`
		Executions []json.RawMessage `json:"executions"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil
	}
	if w.Version != ToolLedgerVersion {
		return nil
	}
	ledger := &ToolLedger{
		Version:   w.Version,
		SessionID: w.SessionID,
		TurnID:    w.TurnID,
	}
	for _, rawExec := range w.Executions {
		var e ToolExecution
		if err := json.Unmarshal(rawExec, &e); err != nil {
			// One damaged entry must not discard the rest of the ledger.
			continue
		}
		ledger.Executions = append(ledger.Executions, e)
	}
	if len(ledger.Executions) == 0 {
		return nil
	}
	return ledger
}

func buildToolLedger(sessionID, turnID string, tools []ToolExecution) *ToolLedger {
	if len(tools) == 0 {
		return nil
	}
	ledger := &ToolLedger{
		Version:   ToolLedgerVersion,
		SessionID: sessionID,
		TurnID:    turnID,
	}
	for _, e := range tools {
		if len(ledger.Executions) >= MaxToolExecutionsPerTurn {
			break
		}
		// The message this ledger is attached to is the authority for the
		// association; do not accept a conflicting turn id.
		e.TurnID = turnID
		e = sanitizeToolExecution(e)
		if e.Tool == "" || !validToolStatus(e.Status) || !validToolMode(e.Mode) {
			continue
		}
		ledger.Executions = append(ledger.Executions, e)
	}
	if len(ledger.Executions) == 0 {
		return nil
	}
	return ledger
}

// normalizeExpectedCode keeps only the two verified expectation codes.
// It prevents malformed or hand-edited evidence from displaying an
// expected-failure icon.
func normalizeExpectedCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "missing" {
		return code
	}
	if strings.HasPrefix(code, "exit:") {
		n, err := strconv.Atoi(strings.TrimPrefix(code, "exit:"))
		if err == nil && n >= 0 && n <= 255 {
			return code
		}
	}
	return ""
}

func sanitizeToolExecution(e ToolExecution) ToolExecution {
	e.CallID = safeEvidenceLabel(e.CallID)
	e.Tool = safeEvidenceLabel(e.Tool)
	e.Action = safeEvidenceLabel(e.Action)
	e.Target = singleLine(strings.TrimSpace(e.Target))
	e.Mode = strings.TrimSpace(e.Mode)
	e.Summary = singleLine(strings.TrimSpace(e.Summary))
	e.Expected = normalizeExpectedCode(e.Expected)
	if e.Expected == "" {
		e.ExpectedMet = false
	}
	e.CallID = truncateUTF8(e.CallID, MaxToolIDBytes)
	e.Tool = truncateUTF8(e.Tool, MaxToolNameBytes)
	e.Action = truncateUTF8(e.Action, MaxToolNameBytes)
	e.Target = truncateUTF8(e.Target, MaxToolTargetBytes)
	e.Summary = truncateUTF8(e.Summary, MaxToolSummaryBytes)
	if e.DurationMS < 0 {
		e.DurationMS = 0
	}
	if e.Bytes < 0 {
		e.Bytes = 0
	}
	return e
}

// safeEvidenceLabel keeps a tool or call name from introducing line breaks
// or structural separators into the trusted resume note. Unknown model
// tool names are still recorded, but only as a safe label.
func safeEvidenceLabel(s string) string {
	s = singleLine(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) ||
			strings.ContainsRune("_.:/-", r) {
			b.WriteRune(r)
			continue
		}
		b.WriteRune('_')
	}
	return b.String()
}

func singleLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		// Control and format characters can break a line or spoof
		// direction in the trusted resume note. They become spaces;
		// the ledger is metadata, not a rendering channel.
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func validToolStatus(s ToolStatus) bool {
	switch s {
	case ToolSuccess, ToolFailure, ToolCancelled, ToolRefused:
		return true
	}
	return false
}

func validToolMode(mode string) bool {
	return mode == "444" || mode == "666"
}

func truncateUTF8(s string, max int) string {
	if max < 1 || len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
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
	// Tools is the verified evidence of tool executions from this
	// exchange. It is attached to the assistant message and never used
	// as authority.
	Tools []ToolExecution
	// Attachments names the files the operator attached to this
	// exchange. The reference is stored with the user's message; the
	// contents stay in the upload directory and are never written into
	// the session.
	Attachments []attachments.Meta
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
	name string
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
	return &Store{path: filepath.Join(dir, session+".jsonl"), name: session}, nil
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
	if role == RoleAssistant && len(turn.Tools) > 0 {
		msg.ToolLedger = buildToolLedger(s.name, turn.ID, turn.Tools)
	}
	if role == RoleUser && len(turn.Attachments) > 0 {
		// The references are cleaned on the way in as well as on the
		// way out: a session record is a file on disk, and it should
		// hold nothing that could rewrite the interface.
		refs := make([]attachments.Meta, 0, len(turn.Attachments))
		for _, a := range turn.Attachments {
			refs = append(refs, attachments.SanitizeMeta(a))
		}
		msg.Attachments = refs
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
		if msg.ToolLedger != nil && msg.ToolLedger.SessionID != s.name {
			// A record copied from another conversation is not evidence
			// for this one.
			msg.ToolLedger = nil
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
