// Package usage stores cumulative token accounting per conversation.
//
// Only counts and timestamps are kept: no prompts, no replies, no
// reasoning text and no credentials. The provider is the only source of
// truth — HAL never estimates tokens — so a turn without reported usage
// is recorded as incomplete or failed rather than guessed at.
package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// maxHistory bounds the per-turn record so the file cannot grow without
// limit. Totals are never trimmed.
const maxHistory = 1000

// Turn is the provider-reported usage of one finished turn. Reported
// distinguishes "the provider said zero" from "the provider said nothing".
type Turn struct {
	Input     int       `json:"input_tokens,omitempty"`
	Output    int       `json:"output_tokens,omitempty"`
	Reasoning int       `json:"reasoning_tokens,omitempty"`
	Cached    int       `json:"cached_input_tokens,omitempty"`
	Duration  int64     `json:"duration_ms,omitempty"`
	Finish    string    `json:"finish,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Reported  bool      `json:"reported"`
	At        time.Time `json:"at,omitempty"`
}

// Session is the cumulative accounting for one conversation. Totals
// include failed turns that reported usage, because that quota was spent;
// the Failed counter keeps them distinguished from successful answers.
type Session struct {
	Input      int    `json:"input_tokens,omitempty"`
	Output     int    `json:"output_tokens,omitempty"`
	Reasoning  int    `json:"reasoning_tokens,omitempty"`
	Cached     int    `json:"cached_input_tokens,omitempty"`
	Turns      int    `json:"completed_turns,omitempty"`
	Incomplete int    `json:"incomplete_turns,omitempty"`
	Failed     int    `json:"failed_turns,omitempty"`
	Last       Turn   `json:"last_turn,omitempty"`
	History    []Turn `json:"history,omitempty"`
}

// Total reports whether any usage has been recorded for the session.
func (s Session) Total() bool {
	return s.Input > 0 || s.Output > 0 || s.Reasoning > 0 || s.Cached > 0 ||
		s.Turns > 0 || s.Incomplete > 0 || s.Failed > 0
}

// HasTokens reports whether any actual token counts exist. A session with
// only incomplete or failed turns has accounting but no numbers to show.
func (s Session) HasTokens() bool {
	return s.Input > 0 || s.Output > 0 || s.Reasoning > 0 || s.Cached > 0
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

// AddAnswer records one assistant answer. reported says whether the
// provider sent usage: without it the turn counts as incomplete and no
// numbers are invented. Reasoning tokens are stored separately and are
// not added to the output total, because providers already include them
// there. History grows in answer order, so the nth answer matches the
// nth record.
func (s *Store) AddAnswer(session string, turn Turn) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.data.Sessions[session]
	turn.At = time.Now().UTC()
	if turn.Reported {
		rec.Input += turn.Input
		rec.Output += turn.Output
		rec.Reasoning += turn.Reasoning
		rec.Cached += turn.Cached
		rec.Turns++
	} else {
		rec.Incomplete++
	}
	rec.Last = turn
	rec.History = append(rec.History, turn)
	if len(rec.History) > maxHistory {
		rec.History = rec.History[len(rec.History)-maxHistory:]
	}
	s.data.Sessions[session] = rec
	return s.saveLocked()
}

// AddFailed records a turn that produced no answer. Usage the provider
// did report is added to the totals (the quota was spent); a turn with no
// report at all only increments the failed counter. History is untouched,
// so per-answer records stay aligned with actual answers.
func (s *Store) AddFailed(session string, turn Turn) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.data.Sessions[session]
	turn.At = time.Now().UTC()
	if turn.Reported {
		rec.Input += turn.Input
		rec.Output += turn.Output
		rec.Reasoning += turn.Reasoning
		rec.Cached += turn.Cached
	}
	rec.Failed++
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
