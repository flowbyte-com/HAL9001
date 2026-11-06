package workspace

import (
	"bufio"
	"encoding/json"
	"errors"
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

// Record is one audited event. The trail began as the history of proposed
// writes and now also carries tool activity, but the promise is unchanged:
// it is bookkeeping for the operator, not a conversation store. It records
// what happened, never the document or command text. A digest identifies
// content without reproducing it.
type Record struct {
	Time      time.Time `json:"ts"`
	Session   string    `json:"session,omitempty"`
	Operation string    `json:"operation"`
	Path      string    `json:"path,omitempty"`
	Digest    string    `json:"digest,omitempty"`
	Bytes     int       `json:"bytes,omitempty"`
	Overwrite bool      `json:"overwrite,omitempty"`
	// Decision is "approved" or "rejected" for a write review.
	Decision string `json:"decision,omitempty"`
	// Outcome is "written", "failed", "not-run", "ok", "denied",
	// "cancelled" or "timeout".
	Outcome string `json:"outcome"`
	// Reason is a short, content-free explanation for a refusal or a
	// failure.
	Reason string `json:"reason,omitempty"`
	// Kind distinguishes a guarded write decision ("write", the default
	// for older records) from a tool invocation ("tool").
	Kind string `json:"kind,omitempty"`
	// Tool is the model-callable tool name. It is never the command text.
	Tool string `json:"tool,omitempty"`
	// Mode is "444" or "666" at the execution boundary.
	Mode string `json:"mode,omitempty"`
	// Action names the tool operation, for example "read" or "record".
	Action string `json:"action,omitempty"`
	// ExitCode is present only when a command really ran.
	ExitCode *int `json:"exit_code,omitempty"`
	// DurationMS is wall-clock execution time.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Truncated reports that the tool bounded its output.
	Truncated bool `json:"truncated,omitempty"`
	// CommandHash identifies a shell command without storing it.
	CommandHash string `json:"command_hash,omitempty"`
	// Expected is the pre-execution expectation code, if one was
	// declared ("missing" or "exit:17"). ExpectedMet records whether the
	// actual result matched it.
	Expected    string `json:"expected,omitempty"`
	ExpectedMet bool   `json:"expected_met,omitempty"`
}

// ToolRecord is one execution-boundary record for a model-callable tool.
// Callers put paths and work directories in Target, never file contents or
// command text. A shell command is represented by its hash only.
type ToolRecord struct {
	Time        time.Time
	Session     string
	Mode        string
	Tool        string
	Action      string
	Target      string
	Outcome     string
	Reason      string
	ExitCode    *int
	Bytes       int
	DurationMS  int64
	Truncated   bool
	CommandHash string
	Expected    string
	ExpectedMet bool
}

// AppendTool records one tool invocation in the same bounded trail as the
// guarded write decisions.
func (a *Audit) AppendTool(r ToolRecord) error {
	if a == nil {
		return nil
	}
	return a.Append(Record{
		Time:        r.Time,
		Session:     r.Session,
		Operation:   "tool",
		Path:        r.Target,
		Outcome:     r.Outcome,
		Reason:      r.Reason,
		Kind:        "tool",
		Tool:        r.Tool,
		Mode:        r.Mode,
		Action:      r.Action,
		ExitCode:    r.ExitCode,
		DurationMS:  r.DurationMS,
		Bytes:       r.Bytes,
		Truncated:   r.Truncated,
		CommandHash: r.CommandHash,
		Expected:    r.Expected,
		ExpectedMet: r.ExpectedMet,
	})
}

// Audit is the bounded JSONL trail under the application data directory.
// A nil *Audit is safe: Append and Prune do nothing.
//
// The trail is reached through a handle on its own directory, like the
// workspace itself: the file name is resolved inside that directory and
// can never be redirected by a link planted in its place.
type Audit struct {
	path string
	dir  string
	base string
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	return &Audit{path: path, dir: dir, base: filepath.Base(path), now: time.Now}, nil
}

// handle opens the confined handle on the trail's directory.
func (a *Audit) handle() (*os.Root, error) {
	root, err := os.OpenRoot(a.dir)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	return root, nil
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
	root, err := a.handle()
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile(a.base, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
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
	// The rewrite is done through a handle on the trail's own directory,
	// so the temporary file and the rename stay inside it even if the
	// directory is replaced while the trail is pruned.
	root, err := a.handle()
	if err != nil {
		return err
	}
	defer root.Close()
	tmp, err := tempRel(".")
	if err != nil {
		return err
	}
	if err := root.WriteFile(tmp, buf, 0o600); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if err := root.Rename(tmp, a.base); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("workspace: %w", err)
	}
	return nil
}

// readAll reads every record, skipping lines that are not valid records
// (a truncated tail must not hide the rest of the trail).
func (a *Audit) readAll() ([]Record, error) {
	root, err := a.handle()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(a.base)
	if errors.Is(err, os.ErrNotExist) {
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
