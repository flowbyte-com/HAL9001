package promptwork

import (
	"strings"
	"testing"
	"time"
)

func TestBuildOmitsEmptySections(t *testing.T) {
	out := Build(Brief{Objective: "Summarize the outage."})
	if !strings.Contains(out, "## Objective") {
		t.Error("brief must render the objective section")
	}
	for _, section := range []string{"## Background", "## Requirements", "## Constraints", "## Acceptance"} {
		if strings.Contains(out, section) {
			t.Errorf("empty section %q must be omitted, got:\n%s", section, out)
		}
	}
	// A four-sentence brief renders as sentences plus headers, not padding.
	if strings.Contains(out, "Untitled") {
		t.Errorf("objective present, so no placeholder title expected:\n%s", out)
	}
}

func TestBuildRendersAllSections(t *testing.T) {
	out := Build(Brief{
		Objective:    "Ship the widget.",
		Background:   "Customers keep asking.",
		Requirements: []string{"It works", "", "It is fast"},
		Constraints:  []string{"No downtime"},
		Evidence:     []string{"Ticket #42"},
		Deliverables: []string{"Binary"},
		Acceptance:   []string{"Tests pass"},
		Audience:     "a general-purpose coding agent",
	})
	for _, section := range []string{
		"## Objective", "## Background", "## Requirements",
		"## Constraints and exclusions", "## Known evidence and open questions",
		"## Expected deliverables", "## Acceptance criteria",
	} {
		if !strings.Contains(out, section) {
			t.Errorf("brief missing section %q:\n%s", section, out)
		}
	}
	if strings.Contains(out, "\n\n\n") || strings.Contains(out, "- \n") {
		t.Errorf("blank list items must be dropped:\n%s", out)
	}
	if !strings.Contains(out, "general-purpose coding agent") {
		t.Errorf("audience should be noted:\n%s", out)
	}
}

func TestValidate(t *testing.T) {
	if notes := (Brief{}).Validate(); len(notes) != 3 {
		t.Fatalf("empty brief should report 3 notes, got %v", notes)
	}
	complete := Brief{
		Objective:    "Do it.",
		Deliverables: []string{"Done"},
		Acceptance:   []string{"Checked"},
	}
	if notes := complete.Validate(); len(notes) != 0 {
		t.Fatalf("complete brief should validate clean, got %v", notes)
	}
}

func TestReviewFindsHedgesGapsRisks(t *testing.T) {
	text := "Please fix the thing as appropriate etc. Use gpt-4 to do it. The password is hunter2."
	findings := Review(text)
	bySection := map[string]int{}
	for _, f := range findings {
		bySection[f.Section]++
		if f.Suggestion == "" {
			t.Errorf("finding %+v must carry a fix suggestion", f)
		}
	}
	if bySection["wording"] == 0 {
		t.Error("expected a hedge-word finding")
	}
	if bySection["structure"] == 0 {
		t.Error("expected a structural gap finding (no headings)")
	}
	if bySection["portability"] == 0 {
		t.Error("expected a vendor-reference finding")
	}
	if bySection["safety"] == 0 {
		t.Error("expected a secrets finding")
	}
}

func TestReviewCleanPrompt(t *testing.T) {
	text := "# Objective\n\nMigrate the database.\n\n# Acceptance criteria\n\nZero data loss; rollback tested.\n"
	if findings := Review(text); len(findings) != 0 {
		t.Fatalf("clean prompt should review clean, got %+v", findings)
	}
}

func TestReviewEmpty(t *testing.T) {
	if findings := Review("   "); len(findings) != 1 {
		t.Fatalf("empty prompt should yield exactly one finding, got %+v", findings)
	}
}

// Regression: wordBoundaryContains must terminate when the needle occurs
// mid-string without word boundaries and is then absent. The naive
// strings.Index arithmetic (index + i + 1 on a -1 miss) looped forever.
func TestWordBoundaryContainsTerminates(t *testing.T) {
	cases := []struct {
		haystack string
		needle   string
		want     bool
	}{
		{"abc", "b", false},  // occurs, no boundaries, then absent
		{"abc", "d", false},  // absent entirely
		{"a b c", "b", true}, // bounded on both sides
		{"gpt-4 is here", "gpt-4", true},
		{"xgpt-4", "gpt-4", false},
		{"gpt-40", "gpt-4", false},
		{"end with token gpt-4", "gpt-4", true},
		{"gpt-4 at start", "gpt-4", true},
		{"", "x", false},
		{"x", "", false},
		{"aaa", "aa", false}, // overlapping, no boundary-respecting hit
		{"aa aa", "aa", true},
		{"b b", "b", true},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, tc := range cases {
			if got := wordBoundaryContains(tc.haystack, tc.needle); got != tc.want {
				t.Errorf("wordBoundaryContains(%q, %q) = %v, want %v", tc.haystack, tc.needle, got, tc.want)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("wordBoundaryContains did not terminate (infinite loop)")
	}
}
