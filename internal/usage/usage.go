// Package usage stores cumulative token accounting per conversation.
//
// Only counts and timestamps are kept: no prompts, no replies, no
// reasoning text and no credentials. The provider is the only source of
// truth — HAL never estimates tokens — so a turn without reported usage
// is recorded as incomplete rather than guessed at.
package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Turn is the provider-reported usage of one finished turn.
type Turn struct {
	Input     int       `json:"input_tokens,omitempty"`
	Output    int       `json:"output_tokens,omitempty"`
	Reasoning int       `json:"reasoning_tokens,omitempty"`
	Cached    int       `json:"cached_input_tokens,omitempty"`
	At        time.Time `json:"at,omitempty"`
}

// Session is the cumulative accounting for one conversation.
type Session struct {
	Input      int  `json:"input_tokens,omitempty"`
	Output     int  `json:"output_tokens,omitempty"`
	Reasoning  int  `json:"reasoning_tokens,omitempty"`
	Cached     int  `json:"cached_input_tokens,omitempty"`
	Turns      int  `json:"completed_turns,omitempty"`
	Incomplete int  `json:"incomplete_turns,omitempty"`
	Last       Turn `json:"last_turn,omitempty"`
}

// Total reports whether any usage has been recorded for the session.
func (s Session) Total() bool {
	return s.Input > 0 || s.Output > 0 || s.Reasoning > 0 || s.Cached > 0 || s.Turns > 0 || s.Incomplete > 0
}

// File is the on-disk shape: one record per conversation name.
type File struct {
	Sessions map[string]Session `json:"sessions"`
}

// Store persists usage records for every conversation.
type Store struct {
	mu   sync.Mutex
	path string
	data File
}

// Open loads the usage file. A missing file starts empty; a corrupt one
// is preserved beside the new one rather than silently discarded.
func Open(path string) (*Store, error) {
	s := &Store{path: path, data: File{Sessions: map[string]Session{}}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	if err := json.Unmarshal(raw, &s.data); err != nil || s.data.Sessions == nil {
		if len(raw) > 0 {
			_ = os.Rename(path, path+".corrupt")
		}
		s.data = File{Sessions: map[string]Session{}}
		return s, nil
	}
	return s, nil
}

// Get returns the record for a conversation.
func (s *Store) Get(session string) Session {
	if s == nil {
		return Session{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Sessions[session]
}

// AddTurn records one finished turn. reported says whether the provider
// sent usage: when it did not, the turn counts as incomplete and no token
// numbers are invented. Reasoning tokens are stored separately and are
// not added to the output total, because providers include them there.
func (s *Store) AddTurn(session string, turn Turn, reported bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.data.Sessions[session]
	if !reported {
		rec.Incomplete++
		s.data.Sessions[session] = rec
		return s.saveLocked()
	}
	rec.Input += turn.Input
	rec.Output += turn.Output
	rec.Reasoning += turn.Reasoning
	rec.Cached += turn.Cached
	rec.Turns++
	rec.Last = turn
	s.data.Sessions[session] = rec
	return s.saveLocked()
}

// saveLocked writes the file atomically with restrictive permissions.
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("usage: %w", err)
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("usage: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "usage-*.tmp")
	if err != nil {
		return fmt.Errorf("usage: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("usage: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("usage: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("usage: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("usage: %w", err)
	}
	return nil
}
