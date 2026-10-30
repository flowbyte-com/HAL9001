package agent

import (
	"encoding/json"

	"os"

	"github.com/sipeed/picoclaw/pkg/mpm"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
	dir string
	mu  sync.Mutex
}

func NewActiveMemoryStore(workspace string) *ActiveMemoryStore {
	dir := filepath.Join(workspace, ".deepclaw", "active-memory")
	_ = os.MkdirAll(dir, 0o700)
	return &ActiveMemoryStore{dir: dir}
}

func (s *ActiveMemoryStore) path(sessionKey string) string {
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
	return &ws, nil
}

func (s *ActiveMemoryStore) Save(ws *ActiveWorkingSet) error {
	if ws == nil {
		return nil
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
	ws := &ActiveWorkingSet{
		SessionKey: "",
		ProjectID:  project,
		Objective:  strings.TrimSpace(objective),
		UpdatedAt:  time.Now(),
	}
	if prev != nil {
		if ws.Objective == "" {
			ws.Objective = prev.Objective
		}
		if ws.ProjectID == "" {
			ws.ProjectID = prev.ProjectID
		}
	}
	seen := map[string]bool{}
	var records []ActiveMemoryRecord
	add := func(rec ActiveMemoryRecord) {
		if rec.ID == "" || seen[rec.ID] {
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
	if prev != nil {
		for _, r := range prev.Records {
			add(ActiveMemoryRecord{ID: r.ID, Content: r.Content, Tags: append([]string(nil), r.Tags...), Score: r.Score})
		}
	}
	ws.Records = records
	return ws
}
