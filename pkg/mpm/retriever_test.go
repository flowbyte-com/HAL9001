package mpm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRetrieverFiltersProjectAndBudget(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "ws")
	if err := os.MkdirAll(filepath.Join(workspace, "src", "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "src", "db", "mpm.db"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	response := map[string]any{
		"success": true,
		"memories": []map[string]any{
			{
				"id":             "relevant",
				"content":        "DeepClaw relevant memory that should be returned.",
				"tags":           []string{"project:deepclaw", "topic:test"},
				"combined_score": 0.9,
			},
			{
				"id":             "other-project",
				"content":        "This must not leak across projects.",
				"tags":           []string{"project:other"},
				"combined_score": 0.99,
			},
			{
				"id":             "large",
				"content":        "This record is intentionally long enough to exceed the tiny budget and should be truncated away.",
				"tags":           []string{"project:deepclaw"},
				"combined_score": 0.5,
			},
		},
	}
	responseBytes, _ := json.Marshal(response)

	fakeBin := filepath.Join(dir, "mpm")
	script := "#!/bin/sh\nprintf '%s' '" + string(responseBytes) + "'\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	retriever, err := NewRetriever(Config{
		Executable:   fakeBin,
		Workspace:     workspace,
		HomeDir:      filepath.Join(dir, "home"),
		ProjectID:    "deepclaw",
		BudgetTokens: 40,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := retriever.Retrieve(context.Background(), "relevant memory")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 2 {
		t.Fatalf("expected 2 project-scoped records, got %d", len(got.Records))
	}
	for _, rec := range got.Records {
		if rec.ID == "other-project" {
			t.Fatal("project isolation failed: other-project leaked")
		}
	}
	if !got.Truncated {
		t.Fatalf("expected truncation, got text: %s", got.Text)
	}
	if got.Text == "" {
		t.Fatal("expected non-empty retrieved text")
	}
}

func TestNewRetrieverRequiresWorkspaceAndProject(t *testing.T) {
	if _, err := NewRetriever(Config{}); err == nil {
		t.Fatal("expected error for missing config")
	}
}
