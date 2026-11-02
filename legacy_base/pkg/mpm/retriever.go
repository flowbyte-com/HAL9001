package mpm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Executable   string
	Workspace    string
	HomeDir      string
	ProjectID    string
	BudgetTokens int
	Scope        string
	Limit        int
	Timeout      time.Duration
}

type Record struct {
	ID        string   `json:"id"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	Score     float64  `json:"combined_score"`
	Pointer   string   `json:"pointer"`
	CreatedAt int64    `json:"created_at"`
}

type Retrieval struct {
	Query     string   `json:"query"`
	Records   []Record `json:"records"`
	Text      string   `json:"text"`
	Tokens    int      `json:"tokens"`
	Truncated bool     `json:"truncated"`
}

type Retriever struct {
	cfg Config

	mu         sync.Mutex
	lastQuery  string
	lastResult *Retrieval
	lastErr    error
}

func NewRetriever(cfg Config) (*Retriever, error) {
	if strings.TrimSpace(cfg.Workspace) == "" {
		return nil, fmt.Errorf("mpm: workspace is required")
	}
	if strings.TrimSpace(cfg.ProjectID) == "" {
		return nil, fmt.Errorf("mpm: project id is required")
	}
	if cfg.Executable == "" {
		cfg.Executable = "mpm"
	}
	if cfg.BudgetTokens <= 0 {
		cfg.BudgetTokens = 350
	}
	if cfg.Scope == "" {
		cfg.Scope = "local"
	}
	if cfg.Limit <= 0 {
		cfg.Limit = 10
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.HomeDir == "" {
		cfg.HomeDir = filepath.Join(cfg.Workspace, "..", "home")
	}
	if _, err := exec.LookPath(cfg.Executable); err != nil {
		if !filepath.IsAbs(cfg.Executable) {
			return nil, fmt.Errorf("mpm: executable not found: %w", err)
		}
		if _, err := os.Stat(cfg.Executable); err != nil {
			return nil, fmt.Errorf("mpm: executable not found: %w", err)
		}
	}
	if _, err := os.Stat(cfg.Workspace); err != nil {
		return nil, fmt.Errorf("mpm: workspace does not exist: %w", err)
	}
	return &Retriever{cfg: cfg}, nil
}

func NewRetrieverFromEnv() (*Retriever, error) {
	if os.Getenv("DEEPCLAW_MPM_ENABLED") != "1" {
		return nil, nil
	}
	cfg := Config{
		Executable:   os.Getenv("DEEPCLAW_MPM_EXECUTABLE"),
		Workspace:    os.Getenv("MPM_WORKSPACE"),
		HomeDir:      os.Getenv("DEEPCLAW_MPM_HOME"),
		ProjectID:    os.Getenv("DEEPCLAW_MPM_PROJECT"),
		BudgetTokens: atoiOrZero(os.Getenv("DEEPCLAW_MPM_BUDGET")),
		Scope:        os.Getenv("DEEPCLAW_MPM_SCOPE"),
		Limit:        atoiOrZero(os.Getenv("DEEPCLAW_MPM_LIMIT")),
	}
	if cfg.ProjectID == "" {
		cfg.ProjectID = "deepclaw"
	}
	if cfg.Executable == "" {
		cfg.Executable = os.Getenv("MPM_EXECUTABLE")
	}
	if cfg.Executable == "" {
		cfg.Executable = "mpm"
	}
	if cfg.HomeDir == "" && cfg.Workspace != "" {
		cfg.HomeDir = filepath.Join(cfg.Workspace, "..", "home")
	}
	return NewRetriever(cfg)
}

func atoiOrZero(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func (r *Retriever) Config() Config { return r.cfg }

func (r *Retriever) Retrieve(ctx context.Context, query string) (*Retrieval, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return &Retrieval{Query: query}, nil
	}

	r.mu.Lock()
	if r.lastQuery == query && r.lastResult != nil {
		res := *r.lastResult
		r.mu.Unlock()
		return &res, r.lastErr
	}
	r.mu.Unlock()

	res, err := r.retrieve(ctx, query)

	r.mu.Lock()
	r.lastQuery = query
	r.lastResult = res
	r.lastErr = err
	r.mu.Unlock()

	if res == nil {
		res = &Retrieval{Query: query}
	}
	return res, err
}

func (r *Retriever) retrieve(ctx context.Context, query string) (*Retrieval, error) {
	payload := map[string]any{
		"action": "query",
		"params": map[string]any{
			"query":      query,
			"limit":      r.cfg.Limit,
			"projection": "full",
			"scope":      r.cfg.Scope,
		},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("mpm: marshal payload: %w", err)
	}

	timeout := r.cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(callCtx, r.cfg.Executable, "call", "mpm_memory", "--payload", string(payloadBytes))
	cmd.Env = append(os.Environ(),
		"MPM_WORKSPACE="+r.cfg.Workspace,
		"HOME="+r.cfg.HomeDir,
		"XDG_CONFIG_HOME="+filepath.Join(r.cfg.HomeDir, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(r.cfg.HomeDir, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(r.cfg.HomeDir, ".local", "share"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("mpm: call failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var envelope struct {
		Success  bool   `json:"success"`
		Error    string `json:"error"`
		Memories []struct {
			ID        string   `json:"id"`
			Content   string   `json:"content"`
			Tags      []string `json:"tags"`
			Score     float64  `json:"combined_score"`
			Pointer   string   `json:"pointer"`
			CreatedAt int64    `json:"created_at"`
			SessionID string   `json:"session_id"`
		} `json:"memories"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		return nil, fmt.Errorf("mpm: parse response: %w: %s", err, strings.TrimSpace(stdout.String()))
	}
	if !envelope.Success {
		if envelope.Error == "" {
			envelope.Error = "mpm query failed"
		}
		return nil, fmt.Errorf("mpm: %s", envelope.Error)
	}

	projectTag := "project:" + r.cfg.ProjectID
	var records []Record
	for _, m := range envelope.Memories {
		if !containsString(m.Tags, projectTag) {
			continue
		}
		records = append(records, Record{
			ID:        m.ID,
			Content:   m.Content,
			Tags:      append([]string(nil), m.Tags...),
			Score:     m.Score,
			Pointer:   m.Pointer,
			CreatedAt: m.CreatedAt,
		})
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Score > records[j].Score })

	text, truncated := formatRecords(records, r.cfg.BudgetTokens)
	_ = text
	retrieval := &Retrieval{
		Query:     query,
		Records:   records,
		Text:      text,
		Tokens:    estimateTokens(text),
		Truncated: truncated,
	}
	if len(records) == 0 {
		retrieval.Text = ""
		retrieval.Tokens = 0
		retrieval.Truncated = false
	}
	return retrieval, nil
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

func formatRecords(records []Record, budget int) (string, bool) {
	if len(records) == 0 {
		return "", false
	}
	if budget <= 0 {
		budget = 350
	}
	var b strings.Builder
	b.WriteString("## Retrieved MPM memories\n\n")
	truncated := false
	for _, rec := range records {
		line := fmt.Sprintf("- (%s, score=%.3f) %s", rec.ID, rec.Score, strings.TrimSpace(rec.Content))
		if len(rec.Tags) > 0 {
			line += fmt.Sprintf(" [tags: %s]", strings.Join(rec.Tags, ", "))
		}
		line += "\n"
		if estimateTokens(b.String()+line) > budget {
			truncated = true
			break
		}
		b.WriteString(line)
	}
	if strings.TrimSpace(b.String()) == "## Retrieved MPM memories" {
		if truncated {
			return "## Retrieved MPM memories\n\n[truncated to fit memory budget]\n", true
		}
		return "", false
	}
	if truncated {
		b.WriteString("\n[truncated to fit memory budget]\n")
	}
	return b.String(), truncated
}
