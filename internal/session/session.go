// Package session stores HAL9001 conversations durably and model-independently:
// the discussion survives provider switches because the record format never
// mentions a provider. Storage is a JSONL file per session: one message per
// line, append-only. Conversation storage is narrowly authorized persistence —
// it is not memory admission and never executes anything.
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
}

// Store persists one session's messages.
type Store struct {
	mu   sync.Mutex
	path string
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
	if strings.TrimSpace(content) == "" {
		return Message{}, fmt.Errorf("session: refusing to store empty content")
	}
	switch role {
	case RoleUser, RoleAssistant, RoleSystem:
	default:
		return Message{}, fmt.Errorf("session: unknown role %q", role)
	}
	msg := Message{Role: role, Content: content, Timestamp: time.Now().UTC()}
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
