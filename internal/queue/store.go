package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The queue on disk.
//
// A message the operator typed while HAL was working belongs to the
// operator, not to the process. The pending items are therefore kept
// beside the session's own file, so a restart does not lose them.
//
// Only the pending items are written. An item the engine has already
// taken is in the session itself, and writing it here as well would be
// a second copy that could be sent twice.

// Store persists a queue between runs.
//
// A session's queue is kept beside the session's own file. The store
// resolves its path when it is used, so a session change moves the
// queue with it.
type Store interface {
	// Save writes the items that are still waiting. Writing an empty
	// list removes the file.
	Save(items []Item) error
	// Load returns the items that were waiting when the queue was last
	// saved. It returns no items when there is nothing saved.
	Load() ([]Item, error)
}

// FileStore keeps a queue in one file.
//
// The write is atomic: the file is written beside its target and
// renamed over it, so a crash mid-write cannot leave a half-written
// queue behind. The write is best effort - a queue that cannot be
// saved is a lost convenience, never a lost message in the session.
type FileStore struct {
	// Path is the file the queue is kept in.
	Path string
}

// Save writes the pending items.
func (s FileStore) Save(items []Item) error {
	path := strings.TrimSpace(s.Path)
	if path == "" {
		return nil
	}
	pending := make([]Item, 0, len(items))
	for _, item := range items {
		if item.State == StatePending {
			pending = append(pending, item)
		}
	}
	if len(pending) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("queue: removing %s: %w", path, err)
		}
		return nil
	}
	body, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return fmt.Errorf("queue: encoding the queue: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("queue: creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".queue-*.json")
	if err != nil {
		return fmt.Errorf("queue: writing the queue: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("queue: writing the queue: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("queue: writing the queue: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("queue: writing the queue: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("queue: writing the queue: %w", err)
	}
	return nil
}

// Load returns the items that were waiting when the queue was last
// saved.
//
// A file that cannot be read is reported, not swallowed: the operator
// is told that a queued message could not be restored rather than
// finding out later that it is gone.
func (s FileStore) Load() ([]Item, error) {
	path := strings.TrimSpace(s.Path)
	if path == "" {
		return nil, nil
	}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: reading %s: %w", path, err)
	}
	var items []Item
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("queue: reading %s: %w", path, err)
	}
	out := make([]Item, 0, len(items))
	for _, item := range items {
		// Only a pending item is restored. An item that was written in
		// another state belongs to a turn that already happened.
		if item.State != StatePending || strings.TrimSpace(item.Text) == "" {
			continue
		}
		item.State = StatePending
		out = append(out, item)
	}
	return out, nil
}
