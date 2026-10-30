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

// ActiveWorkingSet is a small, session-scoped task view.
type ActiveWorkingSet struct {
	SessionKey string               `json:"session_key"`
	ProjectID  string               `json:"project_id"`
	Objective  string               `json:"objective"`
	Records    []ActiveMemoryRecord `json:"records"`
	UpdatedAt  time.Time            `json:"updated_at"`
}

// ActiveMemoryStore persists bounded working sets as session-local files.
type ActiveMemoryStore struct {
	dir       string
	projectID string
	mu        sync.Mutex
}

// NewActiveMemoryStore creates a project-aware store. The project argument is
// optional only for compatibility with callers outside DeepClaw mode; when it
// is empty, the store keeps the legacy unscoped path and Load/Save cannot
// establish ownership, so DeepClaw callers must pass it.
func NewActiveMemoryStore(workspace string, projectIDs ...string) *ActiveMemoryStore {
	dir := filepath.Join(workspace, ".deepclaw", "active-memory")
	_ = os.MkdirAll(dir, 0o700)
	projectID := ""
	if len(projectIDs) > 0 {
		projectID = strings.TrimSpace(projectIDs[0])
	}
	return &ActiveMemoryStore{dir: dir, projectID: projectID}
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
	return &ws, nil
}

func (s *ActiveMemoryStore) Save(ws *ActiveWorkingSet) error {
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
	return os.WriteFile(s.path(ws.SessionKey), data, 0o600)
}

func (s *ActiveMemoryStore) Delete(sessionKey string) {
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
