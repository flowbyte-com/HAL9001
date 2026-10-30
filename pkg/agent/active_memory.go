package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/mpm"
)

// ActiveMemoryRecord is a bounded resident MPM record.
type ActiveMemoryRecord struct {
	ID      string   `json:"id"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
	Score   float64  `json:"score"`
}

// ActiveWorkingSet is a small, session-scoped task view. TaskState carries the
// evidence-backed short-term task state added in 004A; it is deliberately
// separate from the resident MPM retrieval cache in Records.
type ActiveWorkingSet struct {
	SessionKey string               `json:"session_key"`
	ProjectID  string               `json:"project_id"`
	Objective  string               `json:"objective"`
	Records    []ActiveMemoryRecord `json:"records"`
	TaskState  *TaskState           `json:"task_state,omitempty"`
	UpdatedAt  time.Time            `json:"updated_at"`
}

// ActiveMemoryStore persists bounded working sets as session-local files.
type ActiveMemoryStore struct {
	dir       string
	projectID string
	mu        sync.Mutex
	// stateMu serializes read-modify-write updates of one session's sidecar so
	// concurrent tool callbacks cannot lose task-state entries.
	stateMu sync.Mutex
}

// NewActiveMemoryStore creates a project-aware store. The project argument is
// optional only for compatibility with callers outside DeepClaw mode; when it
// is empty, the store keeps the legacy unscoped path and Load/Save cannot
// establish ownership, so DeepClaw callers must pass it.
func NewActiveMemoryStore(workspace string, projectIDs ...string) *ActiveMemoryStore {
	projectID := ""
	if len(projectIDs) > 0 {
		projectID = strings.TrimSpace(projectIDs[0])
	}
	return NewActiveMemoryStoreAt(filepath.Join(workspace, ".deepclaw", "active-memory"), projectID)
}

// NewActiveMemoryStoreAt creates a store rooted at an explicit directory. 004A
// uses it to place task state under PicoClaw home, outside the agent-visible
// workspace, where isolation can protect it from shell and file tools.
func NewActiveMemoryStoreAt(dir, projectID string) *ActiveMemoryStore {
	_ = os.MkdirAll(dir, 0o700)
	return &ActiveMemoryStore{dir: filepath.Clean(dir), projectID: strings.TrimSpace(projectID)}
}

// Dir returns the absolute directory that holds this store's sidecars.
func (s *ActiveMemoryStore) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// StatePath returns the sidecar path for a session. It is used to protect the
// file from agent-controlled shell and file tools.
func (s *ActiveMemoryStore) StatePath(sessionKey string) string {
	if s == nil {
		return ""
	}
	return s.path(sessionKey)
}

func (s *ActiveMemoryStore) path(sessionKey string) string {
	if s.projectID != "" {
		sessionKey = projectScopedSessionKey(s.projectID, sessionKey)
	}
	name := strings.ReplaceAll(sessionKey, ":", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	if name == "" || name == "." {
		name = "default"
	}
	return filepath.Join(s.dir, name+".json")
}

func (s *ActiveMemoryStore) Load(sessionKey string) (*ActiveWorkingSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path(sessionKey))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ws ActiveWorkingSet
	if err := json.Unmarshal(data, &ws); err != nil {
		return nil, err
	}
	if ws.SessionKey != sessionKey {
		return nil, nil
	}
	// Ownership is established by both the stored project and every resident
	// record's tag. A matching session key alone is never sufficient: the old
	// implementation could write the current project into ProjectID while
	// retaining records from a previous project.
	if s.projectID != "" && strings.TrimSpace(ws.ProjectID) != s.projectID {
		return nil, nil
	}
	ws.Records = filterActiveRecordsByProject(ws.Records, s.projectID)
	if ws.TaskState != nil && !ws.TaskState.ValidFor(s.projectID, sessionKey) {
		ws.TaskState = nil
	}
	return &ws, nil
}

func (s *ActiveMemoryStore) Save(ws *ActiveWorkingSet) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.saveLocked(ws)
}

// saveLocked writes the sidecar while the caller holds stateMu. Writes go to a
// temporary file and are renamed into place so a concurrent reader never sees a
// partially written task state.
func (s *ActiveMemoryStore) saveLocked(ws *ActiveWorkingSet) error {
	if ws == nil {
		return nil
	}
	if s.projectID != "" && strings.TrimSpace(ws.ProjectID) != s.projectID {
		return fmt.Errorf(
			"active memory project mismatch: store=%q working_set=%q",
			s.projectID,
			ws.ProjectID,
		)
	}
	if s.projectID != "" {
		ws.Records = filterActiveRecordsByProject(ws.Records, s.projectID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		return err
	}
	path := s.path(ws.SessionKey)
	tmp, err := os.CreateTemp(s.dir, ".active-memory-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// UpdateTaskState loads the sidecar, applies fn to the session's task state and
// saves it atomically with respect to other task-state updates. The state is
// created when absent and discarded at load time when it belongs to another
// project or session.
func (s *ActiveMemoryStore) UpdateTaskState(sessionKey string, fn func(*TaskState)) (*TaskState, error) {
	if s == nil || strings.TrimSpace(sessionKey) == "" {
		return nil, nil
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	ws, err := s.Load(sessionKey)
	if err != nil {
		return nil, err
	}
	created := false
	if ws == nil {
		ws = &ActiveWorkingSet{SessionKey: sessionKey, ProjectID: s.projectID}
		created = true
	}
	if ws.TaskState == nil || !ws.TaskState.ValidFor(s.projectID, sessionKey) {
		ws.TaskState = NewTaskState(s.projectID, sessionKey)
		created = true
	}
	if fn != nil {
		fn(ws.TaskState)
	}
	ws.TaskState.SessionKey = sessionKey
	ws.TaskState.ProjectID = s.projectID
	ws.SessionKey = sessionKey
	ws.ProjectID = s.projectID
	ws.UpdatedAt = time.Now().UTC()
	if created && len(ws.Records) == 0 && ws.Objective == "" &&
		len(ws.TaskState.Entries) == 0 && strings.TrimSpace(ws.TaskState.Objective) == "" {
		return ws.TaskState, nil
	}
	if err := s.saveLocked(ws); err != nil {
		return nil, err
	}
	return ws.TaskState, nil
}

// TaskState returns a snapshot of the session's task state, or nil.
func (s *ActiveMemoryStore) TaskState(sessionKey string) (*TaskState, error) {
	if s == nil {
		return nil, nil
	}
	ws, err := s.Load(sessionKey)
	if err != nil || ws == nil {
		return nil, err
	}
	return ws.TaskState, nil
}

func (s *ActiveMemoryStore) Delete(sessionKey string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.Remove(s.path(sessionKey))
}

func activeRecordProject(rec ActiveMemoryRecord) string {
	for _, tag := range rec.Tags {
		tag = strings.TrimSpace(tag)
		if strings.HasPrefix(tag, "project:") {
			return strings.TrimSpace(strings.TrimPrefix(tag, "project:"))
		}
	}
	return ""
}

func filterActiveRecordsByProject(records []ActiveMemoryRecord, projectID string) []ActiveMemoryRecord {
	if projectID == "" {
		return records
	}
	filtered := make([]ActiveMemoryRecord, 0, len(records))
	for _, rec := range records {
		if activeRecordProject(rec) == projectID {
			filtered = append(filtered, rec)
		}
	}
	return filtered
}

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// Render turns a working set into bounded prompt text.
func (ws *ActiveWorkingSet) Render(budget int) string {
	if ws == nil {
		return ""
	}
	if budget <= 0 {
		budget = 500
	}
	var b strings.Builder
	b.WriteString("Active working memory (session-scoped; verify before acting):\n")
	if strings.TrimSpace(ws.Objective) != "" {
		b.WriteString("Objective: " + strings.TrimSpace(ws.Objective) + "\n")
	}
	b.WriteString("Resident MPM records:\n")
	for _, rec := range ws.Records {
		line := "- (" + rec.ID + ") " + strings.TrimSpace(rec.Content) + "\n"
		if estimateTokens(b.String()+line) > budget {
			b.WriteString("[truncated to fit working-memory budget]\n")
			break
		}
		b.WriteString(line)
	}
	if len(ws.Records) == 0 {
		b.WriteString("- none\n")
	}
	return b.String()
}

// MergeActiveWorkingSet combines a previous working set with fresh retrieval.
func MergeActiveWorkingSet(prev *ActiveWorkingSet, retrieval *mpm.Retrieval, objective, project string, budget int) *ActiveWorkingSet {
	if budget <= 0 {
		budget = 500
	}
	project = strings.TrimSpace(project)
	ws := &ActiveWorkingSet{
		SessionKey: "",
		ProjectID:  project,
		Objective:  strings.TrimSpace(objective),
		UpdatedAt:  time.Now(),
	}
	// Previous state is trusted only when it declares the same project and
	// every resident record carries the matching project tag. A matching
	// session key is not enough; legacy unscoped or cross-project state must be
	// discarded rather than relabelled.
	prevBelongs := prev != nil && project != "" && strings.TrimSpace(prev.ProjectID) == project
	if prevBelongs {
		if ws.Objective == "" {
			ws.Objective = prev.Objective
		}
		// Task state is owned by the same sidecar. A retrieval-cache merge must
		// carry it forward or every MPM turn would silently erase captured
		// evidence.
		ws.TaskState = prev.TaskState
	}
	seen := map[string]bool{}
	var records []ActiveMemoryRecord
	add := func(rec ActiveMemoryRecord) {
		if rec.ID == "" || seen[rec.ID] {
			return
		}
		if project != "" && activeRecordProject(rec) != project {
			return
		}
		// Bound the resident set by estimated tokens.
		candidate := append([]ActiveMemoryRecord(nil), records...)
		candidate = append(candidate, rec)
		tmp := &ActiveWorkingSet{Records: candidate}
		if estimateTokens(tmp.Render(budget)) > budget && len(records) > 0 {
			return
		}
		seen[rec.ID] = true
		records = append(records, rec)
	}
	if retrieval != nil {
		for _, r := range retrieval.Records {
			add(ActiveMemoryRecord{ID: r.ID, Content: r.Content, Tags: append([]string(nil), r.Tags...), Score: r.Score})
		}
	}
	if prevBelongs {
		for _, r := range prev.Records {
			add(ActiveMemoryRecord{ID: r.ID, Content: r.Content, Tags: append([]string(nil), r.Tags...), Score: r.Score})
		}
	}
	ws.Records = records
	return ws
}
