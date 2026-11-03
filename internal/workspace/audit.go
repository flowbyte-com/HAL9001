package workspace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditRetention bounds the trail: records older than this are dropped
// when the log is pruned. Six days is enough to reconstruct a week's
// writes without keeping a permanent history.
const AuditRetention = 6 * 24 * time.Hour

// AuditInterval is how often the trail is pruned while the application
// runs. Pruning is cheap but not free, so it happens at most this often.
const AuditInterval = time.Hour

// Record is one audited decision about one proposed write.
//
// The trail is bookkeeping for the operator, not a conversation store: it
// records what happened to a proposal, never the document itself. The
// digest identifies the content without reproducing it.
type Record struct {
	Time      time.Time `json:"ts"`
	Session   string    `json:"session,omitempty"`
	Operation string    `json:"operation"`
	Path      string    `json:"path"`
	Digest    string    `json:"digest,omitempty"`
	Bytes     int       `json:"bytes,omitempty"`
	Overwrite bool      `json:"overwrite,omitempty"`
	// Decision is "approved" or "rejected".
	Decision string `json:"decision"`
	// Outcome is "written", "failed" or "not-run".
	Outcome string `json:"outcome"`
	// Reason is a short, content-free explanation for a refusal or a
	// failure.
	Reason string `json:"reason,omitempty"`
}

// Audit is the bounded JSONL trail under the application data directory.
// A nil *Audit is safe: Append and Prune do nothing.
type Audit struct {
	path string
	now  func() time.Time

	mu        sync.Mutex
	lastPrune time.Time
}

// OpenAudit prepares the trail file. The parent directory is created with
// the application's restrictive mode: this is trusted bookkeeping, not a
// model-accessible capability.
func OpenAudit(path string) (*Audit, error) {
	if path == "" {
		return nil, fmt.Errorf("workspace: empty audit path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	return &Audit{path: path, now: time.Now}, nil
}

// Path reports the trail file, for diagnostics.
func (a *Audit) Path() string {
	if a == nil {
		return ""
	}
	return a.path
}

// Append records one decision and prunes the trail when it is due.
func (a *Audit) Append(r Record) error {
	if a == nil {
		return nil
	}
	if r.Time.IsZero() {
		r.Time = a.now()
	}
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("workspace: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if a.lastPrune.IsZero() || a.now().Sub(a.lastPrune) >= AuditInterval {
		return a.pruneLocked()
	}
	return nil
}

// Prune drops records older than AuditRetention and rewrites the file
// atomically. It reports how many records were dropped.
func (a *Audit) Prune() (int, error) {
	if a == nil {
		return 0, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	before, err := a.readAll()
	if err != nil {
		return 0, err
	}
	if err := a.pruneLocked(); err != nil {
		return 0, err
	}
	after, err := a.readAll()
	if err != nil {
		return 0, err
	}
	return len(before) - len(after), nil
}

// Records reads the trail, oldest first.
func (a *Audit) Records() ([]Record, error) {
	if a == nil {
		return nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.readAll()
}

// pruneLocked keeps only recent records. The rewrite goes through a
// temporary file so a crash cannot leave half a trail behind.
func (a *Audit) pruneLocked() error {
	a.lastPrune = a.now()
	records, err := a.readAll()
	if err != nil {
		return err
	}
	cutoff := a.now().Add(-AuditRetention)
	kept := make([]Record, 0, len(records))
	for _, r := range records {
		if r.Time.IsZero() || r.Time.After(cutoff) {
			kept = append(kept, r)
		}
	}
	if len(kept) == len(records) {
		return nil
	}
	var buf []byte
	for _, r := range kept {
		data, err := json.Marshal(r)
		if err != nil {
			continue
		}
		buf = append(buf, data...)
		buf = append(buf, '\n')
	}
	tmp, err := tempName(filepath.Dir(a.path))
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if err := os.Rename(tmp, a.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("workspace: %w", err)
	}
	return nil
}

// readAll reads every record, skipping lines that are not valid records
// (a truncated tail must not hide the rest of the trail).
func (a *Audit) readAll() ([]Record, error) {
	f, err := os.Open(a.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	defer f.Close()
	var out []Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	return out, nil
}
