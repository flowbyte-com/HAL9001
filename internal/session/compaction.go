package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The record of a manual compaction.
//
// A compaction is a summary of the older part of a conversation. It
// does not replace the conversation: the transcript is never rewritten,
// so every word the operator and the model wrote is still there, exactly
// as it was. The record lives beside the transcript, in a file of its
// own, and the transcript is not touched at all.
//
// The file is written atomically. A temporary file in the same directory
// is written, flushed and renamed over the record, and a rename is
// atomic: a restart never finds a half-written compaction, it finds
// either the old record or the new one.
//
// A record that cannot be read is treated as absent. The conversation
// then runs on its full transcript, which is the safe direction: nothing
// is lost and nothing is invented, and the next compaction replaces the
// file.

// CompactionVersion is the schema version of a compaction record.
//
// A record with any other version is ignored rather than guessed at.
const CompactionVersion = 1

// Compaction is the record of one manual compaction.
//
// It carries the summary and the provenance of the summary: how much of
// the conversation it replaces, which exchanges those were, and what the
// request looked like before and after.
type Compaction struct {
	// Version is the schema version of the record.
	Version int `json:"version"`
	// Summary is the compacted representation of the covered part of
	// the conversation. It is a summary the model wrote; it is never a
	// transcript and never a verified record of what ran.
	Summary string `json:"summary"`
	// CoveredMessages is how many leading messages of the transcript
	// the summary replaces.
	//
	// The transcript is append-only, so a count of leading messages
	// names an exact, stable part of it: the messages that follow the
	// cut are the ones that were kept.
	CoveredMessages int `json:"covered_messages"`
	// CoveredExchanges counts the exchanges the summary replaces.
	CoveredExchanges int `json:"covered_exchanges"`
	// FirstTurnID and LastTurnID name the range of exchanges the
	// summary covers. They are the provenance of the summary: the
	// supporting conversation can be inspected by turn.
	FirstTurnID string `json:"first_turn_id,omitempty"`
	LastTurnID  string `json:"last_turn_id,omitempty"`
	// BeforeTokens and AfterTokens are the estimated sizes of the
	// stored context before and after the compaction.
	//
	// They are estimates of the context a request would carry, taken
	// with the same estimate every ordinary request is measured with.
	// They are not measurements of the tokens that were transmitted, and
	// they are not the provider's own usage: the cost of a compaction is
	// the provider's usage, and it is reported beside them.
	//
	// A record written before the estimates were kept carries zeroes
	// here. A zero means "not measured", never "empty", and such a
	// record is read exactly like every other one.
	BeforeTokens int `json:"before_tokens"`
	AfterTokens  int `json:"after_tokens"`
	// Limit is the context budget the request had to fit into.
	Limit int `json:"limit,omitempty"`
	// Created is when the compaction was made.
	Created time.Time `json:"created"`
}

// CompactionPath is the file the compaction record of the conversation
// lives in.
//
// It sits beside the transcript and is named after it, so a record and
// the conversation it belongs to are never separated.
func (s *Store) CompactionPath() string {
	return strings.TrimSuffix(s.path, ".jsonl") + ".compaction.json"
}

// ReadCompaction returns the compaction record of the conversation.
//
// A conversation without a record has none, and a record that cannot be
// read is treated as absent: the conversation then runs on its full
// transcript, which is the safe direction, and the next compaction
// replaces the file.
func (s *Store) ReadCompaction() *Compaction {
	if s == nil {
		return nil
	}
	data, err := os.ReadFile(s.CompactionPath())
	if err != nil {
		return nil
	}
	var c Compaction
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	if c.Version != CompactionVersion {
		return nil
	}
	if strings.TrimSpace(c.Summary) == "" {
		return nil
	}
	if c.CoveredMessages <= 0 {
		return nil
	}
	return &c
}

// WriteCompaction stores the record.
//
// The write is atomic: a temporary file in the same directory is
// written, flushed and renamed over the record. A crash therefore never
// leaves a half-written compaction behind, and a restart finds either
// the previous record or the new one.
func (s *Store) WriteCompaction(c Compaction) error {
	if c.Version == 0 {
		c.Version = CompactionVersion
	}
	if c.Version != CompactionVersion {
		return fmt.Errorf("session: the compaction record has the unsupported version %d", c.Version)
	}
	if strings.TrimSpace(c.Summary) == "" {
		return fmt.Errorf("session: refusing to store an empty compaction")
	}
	if c.CoveredMessages <= 0 {
		return fmt.Errorf("session: refusing to store a compaction that covers nothing")
	}
	if c.Created.IsZero() {
		c.Created = time.Now().UTC()
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeFileAtomic(s.CompactionPath(), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}

// ClearCompaction removes the compaction record.
//
// The conversation then runs on its full transcript again. The transcript
// itself is never touched.
func (s *Store) ClearCompaction() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.CompactionPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}

// writeFileAtomic writes a file so that a reader never sees half of it.
//
// The data goes into a temporary file in the same directory, the
// temporary file is flushed, and the temporary file is renamed over the
// target. The rename is atomic: the target is either the old content or
// the new one.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".compact-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// The temporary file is removed on every path that does not rename
	// it: a failed write must not leave debris beside the transcript.
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	// The directory entry is flushed as well: the rename itself is
	// atomic, but it only survives a crash once the directory knows
	// about it.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
