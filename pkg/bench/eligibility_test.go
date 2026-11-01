package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEligibilityGateBranches(t *testing.T) {
	eligible := EligibilityInput{
		TaskID: "x", BoundaryKind: "compaction",
		EvidenceMarkers:    []string{"edge 499->500", "750-cent diagnostic"},
		VisibleHistory:     "history without the finding",
		Summary:            "summary without the finding",
		RelevantToDecision: true, Capturable: true,
		WorkspaceRecoverable: true, RecoveryCostEstimate: 3,
	}
	report := EvaluateEligibility(eligible)
	if !report.Eligible || len(report.FailedCriteria) != 0 {
		t.Fatalf("eligible case = %+v", report)
	}
	if report.RecoveryCostGuess != 3 {
		t.Fatalf("recovery cost not carried: %+v", report)
	}

	cases := []struct {
		name   string
		mutate func(*EligibilityInput)
		want   string
	}{
		{"no boundary", func(in *EligibilityInput) { in.BoundaryKind = "none" }, "no-boundary"},
		{"no dependence", func(in *EligibilityInput) { in.RelevantToDecision = false }, "no-dependence"},
		{"not capturable", func(in *EligibilityInput) { in.Capturable = false }, "not-capturable"},
		{"markers undefined", func(in *EligibilityInput) { in.EvidenceMarkers = nil }, "markers-undefined"},
		{"survives in history", func(in *EligibilityInput) { in.VisibleHistory = "still has edge 499->500" }, "evidence-retained"},
		{"survives in summary", func(in *EligibilityInput) { in.Summary = "summary recalls 750-cent diagnostic" }, "evidence-retained"},
	}
	for _, tc := range cases {
		input := eligible
		input.EvidenceMarkers = append([]string(nil), eligible.EvidenceMarkers...)
		tc.mutate(&input)
		report := EvaluateEligibility(input)
		if report.Eligible {
			t.Fatalf("%s: unexpectedly eligible", tc.name)
		}
		found := false
		for _, criterion := range report.FailedCriteria {
			if criterion == tc.want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: failed=%v, want %s", tc.name, report.FailedCriteria, tc.want)
		}
	}
}

func TestEligibilitySessionEvidence(t *testing.T) {
	dir := t.TempDir()
	session := "{\"role\":\"user\",\"content\":\"fix it\"}\n" +
		"{\"role\":\"assistant\",\"content\":\"on it\"}\n" +
		"{\"role\":\"tool\",\"content\":\"rate is 500\"}\n" +
		"not json\n"
	sessionPath := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(sessionPath, []byte(session), 0o600); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(dir, "s.meta.json")
	if err := os.WriteFile(metaPath, []byte(`{"summary":"compacted notes"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	history, summary, err := SessionEvidence(sessionPath, metaPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fix it", "on it", "rate is 500"} {
		if !containsStr(history, want) {
			t.Fatalf("history missing %q", want)
		}
	}
	if summary != "compacted notes" {
		t.Fatalf("summary = %q", summary)
	}
	if _, _, err := SessionEvidence(filepath.Join(dir, "absent.jsonl"), ""); err == nil {
		t.Fatal("missing session file must error")
	}
}

func containsStr(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// TestDeliberatelyIneligibleTrialIsReported verifies the gate against a
// real (mock) trial: ordinary mock trajectories retain everything, so the
// gate must report ineligible rather than silently counting the trial as
// memory evidence.
func TestEligibleTrialAfterGenuineLoss(t *testing.T) {
	if testing.Short() {
		t.Skip("eligible gate test runs a mock trial")
	}
	task, err := LoadCodingTask("tiered_migration")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.EvidenceMarkers) == 0 {
		t.Fatal("tiered_migration must declare evidence markers")
	}
	out := t.TempDir()
	summary, err := RunExperiment(RunOptions{
		Experiment: "D", RunID: "005g-gate-eligible", OutDir: out,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1,
		Scenarios:   []string{"tiered_migration"},
		Conditions:  []string{CodingConditionControl},
		TrialRunner: testRunner(t), VerboseMock: true, VerboseMockIters: 8,
		ContextWindow: 8192,
	})
	if err != nil {
		t.Fatal(err)
	}
	trial := summary.Trials[0]
	if trial.Status != "pass" {
		t.Fatalf("status = %q (%s), want a completed trial", trial.Status, trial.Error)
	}
	auto := 0
	for _, turn := range trial.Turns {
		auto += turn.AutoCompactions
	}
	if auto == 0 {
		t.Fatal("no automatic compaction occurred; loss was not exercised")
	}
	history, summ, err := SessionEvidence(sessionFileForGate(t, trial), sessionMetaForGate(t, trial))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range task.EvidenceMarkers {
		if containsStr(history, marker) || containsStr(summ, marker) {
			t.Fatalf("marker %q survived; no genuine loss", marker)
		}
	}
	report := EvaluateEligibility(EligibilityInput{
		TaskID: task.ID, BoundaryKind: "compaction",
		EvidenceMarkers:      task.EvidenceMarkers,
		VisibleHistory:       history,
		Summary:              summ,
		RelevantToDecision:   true,
		Capturable:           true,
		WorkspaceRecoverable: true,
		RecoveryCostEstimate: 2,
	})
	if !report.Eligible {
		t.Fatalf("expected eligible, got %+v", report)
	}
	if report.RecoverableNote == "" {
		t.Fatalf("recoverability must be recorded: %+v", report)
	}
}

func TestDeliberatelyIneligibleTrialIsReported(t *testing.T) {
	if testing.Short() {
		t.Skip("gate e2e runs a mock trial")
	}
	out := t.TempDir()
	summary, err := RunExperiment(RunOptions{
		Experiment: "D", RunID: "005g-gate-neg", OutDir: out,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1,
		Scenarios:   []string{"compaction_ledger", "restart_parser"},
		Conditions:  []string{CodingConditionControl},
		TrialRunner: testRunner(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, trial := range summary.Trials {
		history, summ, err := SessionEvidence(
			sessionFileForGate(t, trial), sessionMetaForGate(t, trial))
		if err != nil {
			t.Fatal(err)
		}
		// Markers from content that survives compaction in the mock:
		// t2's closing summary (restated findings) remains in visible
		// history, and the explicit compact keeps a non-empty summary.
		// A genuinely lost t1 detail would NOT be eligible here.
		report := EvaluateEligibility(EligibilityInput{
			TaskID:             trial.Scenario,
			BoundaryKind:       boundaryKindForGate(trial.Scenario),
			EvidenceMarkers:    []string{"still fails", "The ledger fix is complete", "Both parser tests"},
			VisibleHistory:     history,
			Summary:            summ,
			RelevantToDecision: true,
			Capturable:         true,
		})
		if report.Eligible {
			t.Fatalf("%s unexpectedly eligible: %+v", trial.Scenario, report)
		}
		if !containsCriterion(report.FailedCriteria, "evidence-retained") {
			t.Fatalf("%s failed=%v, want evidence-retained: mock history retains everything",
				trial.Scenario, report.FailedCriteria)
		}
	}
}

func containsCriterion(criteria []string, want string) bool {
	for _, criterion := range criteria {
		if criterion == want {
			return true
		}
	}
	return false
}

func sessionFileForGate(t *testing.T, trial TrialResult) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(trial.ResultPath), "workspace", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "sk_") && strings.HasSuffix(name, ".jsonl") && !strings.HasSuffix(name, ".meta.json") {
			return filepath.Join(dir, name)
		}
	}
	t.Fatalf("no session file in %s", dir)
	return ""
}

func sessionMetaForGate(t *testing.T, trial TrialResult) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(trial.ResultPath), "workspace", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "sk_") && strings.HasSuffix(name, ".meta.json") {
			return filepath.Join(dir, name)
		}
	}
	return ""
}

func boundaryKindForGate(scenario string) string {
	switch scenario {
	case "compaction_ledger":
		return "compaction"
	case "restart_parser":
		return "restart"
	default:
		return "none"
	}
}
