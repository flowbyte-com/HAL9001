package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 005D: trace persistence, recovery, redaction and join correctness. All
// offline; the mock provider drives real tool executions through the agent.

func TestToolTraceEndToEndJoined(t *testing.T) {
	if testing.Short() {
		t.Skip("trace e2e runs mock trials")
	}
	out := t.TempDir()
	summary, err := RunExperiment(RunOptions{
		Experiment: "D", RunID: "005d-trace-e2e", OutDir: out,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1,
		Scenarios:   []string{"multi_file_cart", "restart_parser"},
		Conditions:  []string{CodingConditionControl, CodingConditionTreatment},
		TrialRunner: testRunner(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]string{}
	for _, trial := range summary.Trials {
		if trial.Status != "pass" {
			t.Fatalf("%s status = %q", trial.TrialID, trial.Status)
		}
		if trial.ToolTrace == nil || trial.ToolTrace.Attempts == 0 {
			t.Fatalf("%s has no tool trace summary", trial.TrialID)
		}
		if trial.ToolTrace.Unknown != 0 {
			t.Fatalf("%s has %d unknown attempts", trial.TrialID, trial.ToolTrace.Unknown)
		}
		schemas[trial.Condition] = trial.Settings.ToolSchemaSHA
		// Per-turn file attribution must name the files the mock fixes.
		found := map[string]bool{}
		for _, turn := range trial.Turns {
			for _, f := range turn.FilesChanged {
				found[f] = true
			}
		}
		if trial.Scenario == "multi_file_cart" && !(found["discount.go"] && found["money.go"]) {
			t.Fatalf("%s files changed = %v, want the fixed files", trial.TrialID, found)
		}
		// The trace file must join every attempt with names and arguments.
		tracePath := filepath.Join(filepath.Dir(trial.ResultPath), "raw", toolTraceFileName)
		recovery, err := RecoverToolTrace(tracePath)
		if err != nil {
			t.Fatal(err)
		}
		if recovery.Attempts != trial.ToolTrace.Attempts || recovery.Unknown != 0 {
			t.Fatalf("%s recovery = %+v, want all joined", trial.TrialID, recovery)
		}
		joined, _, err := readJoinedTraceEvents(tracePath)
		if err != nil {
			t.Fatal(err)
		}
		sawBash, sawCommand := false, false
		for _, attempt := range joined {
			if attempt.Tool == "bash" {
				sawBash = true
				if command, _ := attempt.Arguments["command"].(string); strings.Contains(command, "check.sh") {
					sawCommand = true
				}
			}
			if attempt.Tool == "" {
				t.Fatalf("%s has a nameless attempt", trial.TrialID)
			}
		}
		if !sawBash || !sawCommand {
			t.Fatalf("%s trace lacks the expected bash attempts", trial.TrialID)
		}
	}
	if schemas[CodingConditionControl] != schemas[CodingConditionTreatment] || schemas[CodingConditionControl] == "" {
		t.Fatalf("tool schemas differ across conditions: %v", schemas)
	}
}

func TestToolTraceRecoveryAfterCrash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw", toolTraceFileName)
	mustAppend := func(attempt ToolAttempt) {
		t.Helper()
		if err := appendTraceEvent(path, attempt); err != nil {
			t.Fatal(err)
		}
	}
	// Child dispatched two calls, completed one, then died.
	mustAppend(ToolAttempt{ExecID: 1, Event: "start", TurnID: "t1", Tool: "bash", Arguments: map[string]any{"command": "go test ./..."}})
	mustAppend(ToolAttempt{ExecID: 2, Event: "start", TurnID: "t1", Tool: "read", Arguments: map[string]any{"path": "a.go"}})
	mustAppend(ToolAttempt{ExecID: 2, Event: "end", TurnID: "t1", Tool: "read", DurationMS: 3})
	mustAppend(ToolAttempt{ExecID: 2, Event: "end", TurnID: "t1", Tool: "read", DurationMS: 3})
	mustAppend(ToolAttempt{ExecID: 1, Event: "start", TurnID: "t1", Tool: "bash", Arguments: map[string]any{"command": "go test ./..."}})
	if err := os.WriteFile(path, append(mustReadFile(t, path), []byte("not json\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	recovery, err := RecoverToolTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Attempts != 2 || recovery.Executed != 1 || recovery.Unknown != 1 {
		t.Fatalf("recovery = %+v, want 2 attempts, 1 executed, 1 unknown", recovery)
	}
	if recovery.Duplicates != 2 {
		t.Fatalf("duplicates = %d, want 2", recovery.Duplicates)
	}
	summary := SummarizeRecovery(recovery)
	if summary.Attempts != 2 || summary.Unknown != 1 || summary.Executed != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestToolTraceSanitization(t *testing.T) {
	args := sanitizeTraceArgs(map[string]any{
		"command": "go test ./...",
		"content": "sk-live-abc123 secret JWT eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c here " + strings.Repeat("x", 2000),
		"count":   3,
	})
	command, _ := args["command"].(string)
	if command != "go test ./..." {
		t.Fatalf("ordinary argument altered: %q", command)
	}
	content, _ := args["content"].(string)
	if len([]rune(content)) > maxTraceStringRunes {
		t.Fatalf("content not truncated: %d runes", len([]rune(content)))
	}
	for _, secret := range []string{"sk-live-abc123", "eyJhbGciOiJIUzI1NiJ9", "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"} {
		if strings.Contains(content, secret) {
			t.Fatalf("secret leaked into trace args: %.80s", content)
		}
	}
	if _, err := RecoverToolTrace(filepath.Join(t.TempDir(), "absent.jsonl")); err != nil {
		t.Fatalf("missing trace file must not error: %v", err)
	}
}
