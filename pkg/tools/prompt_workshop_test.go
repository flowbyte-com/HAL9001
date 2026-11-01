package tools

import (
	"context"
	"strings"
	"testing"
)

func TestDraftPromptTool(t *testing.T) {
	tool := &DraftPromptTool{}
	if tool.Name() != "draft_prompt" {
		t.Fatalf("Name() = %q", tool.Name())
	}
	result := tool.Execute(context.Background(), map[string]any{
		"objective":    "Write a release checklist.",
		"requirements": "Cover backend\nCover frontend\n",
		"acceptance":   "All boxes checkable",
	})
	if result == nil || !strings.Contains(result.ForLLM, "## Objective") {
		t.Fatalf("draft should render a brief, got %+v", result)
	}
	if !strings.Contains(result.ForLLM, "## Acceptance criteria") {
		t.Errorf("acceptance section missing:\n%s", result.ForLLM)
	}
	if strings.Contains(result.ForLLM, "## Background") {
		t.Errorf("empty background must be omitted:\n%s", result.ForLLM)
	}

	// Missing objective still renders, flagged as incomplete.
	partial := tool.Execute(context.Background(), map[string]any{})
	if !strings.Contains(partial.ForLLM, "Incomplete brief") {
		t.Errorf("empty objective should warn, got:\n%s", partial.ForLLM)
	}
}

func TestReviewPromptTool(t *testing.T) {
	tool := &ReviewPromptTool{}
	if tool.Name() != "review_prompt" {
		t.Fatalf("Name() = %q", tool.Name())
	}
	clean := tool.Execute(context.Background(), map[string]any{
		"text": "# Objective\n\nDo the thing.\n\n# Acceptance criteria\n\nDone means done.\n",
	})
	if !strings.Contains(clean.ForLLM, "No issues found") {
		t.Errorf("clean prompt should pass, got:\n%s", clean.ForLLM)
	}
	messy := tool.Execute(context.Background(), map[string]any{
		"text": "fix it as appropriate with gpt-4, password included",
	})
	if !strings.Contains(messy.ForLLM, "ambiguity") || !strings.Contains(messy.ForLLM, "portability") {
		t.Errorf("messy prompt should report findings, got:\n%s", messy.ForLLM)
	}
}
