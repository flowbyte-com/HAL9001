package bench

import (
	"strings"
	"testing"
)

// 005D Phase 6: deterministic metric tests on synthetic traces. No live
// calls, no preserved-evidence dependency: every input is constructed here.

func TestClassifyTraceOutcome(t *testing.T) {
	code := 1
	cases := []struct {
		name     string
		text     string
		isError  bool
		outcome  string
		guard    string
		exitCode *int
	}{
		{"guard path", "Command blocked by safety guard (path outside working dir)", true, "guard_rejected", "path_outside", nil},
		{"guard dangerous", "Command blocked by safety guard (dangerous pattern detected)", true, "guard_rejected", "dangerous_pattern", nil},
		{"guard traversal", "Command blocked by safety guard (path traversal detected)", true, "guard_rejected", "traversal", nil},
		{"guard allowlist", "Command blocked by safety guard (not in allowlist)", true, "guard_rejected", "allowlist", nil},
		{"guard escaped", "Command blocked by safety guard (working directory escaped workspace)", true, "guard_rejected", "escaped", nil},
		{"hook denied", "Tool execution denied by hook: nope", true, "hook_denied", "", nil},
		{"exit one", "FAIL example.com/x 0.002s\nFAIL\nexit=1", true, "tool_error", "", &code},
		{"exit zero", "ok example.com/x 0.002s\nexit=0", false, "success", "", func() *int { z := 0; return &z }()},
		{"guard beats exit text", "Command blocked by safety guard (path outside working dir)\nexit=1", true, "guard_rejected", "path_outside", nil},
		{"plain error", "boom", true, "tool_error", "", nil},
		{"plain success", "ok 1.2s", false, "success", "", nil},
	}
	for _, tc := range cases {
		got := classifyTraceOutcome(tc.text, tc.isError)
		if got.Outcome != tc.outcome || got.GuardKind != tc.guard {
			t.Errorf("%s: got (%q,%q), want (%q,%q)", tc.name, got.Outcome, got.GuardKind, tc.outcome, tc.guard)
		}
		if (got.ExitCode == nil) != (tc.exitCode == nil) {
			t.Errorf("%s: exit code presence = %v, want %v", tc.name, got.ExitCode != nil, tc.exitCode != nil)
		} else if got.ExitCode != nil && *got.ExitCode != *tc.exitCode {
			t.Errorf("%s: exit = %d, want %d", tc.name, *got.ExitCode, *tc.exitCode)
		}
		if got.OutcomeHash == "" {
			t.Errorf("%s: missing outcome hash", tc.name)
		}
	}
}

func TestNormalizeTraceCommand(t *testing.T) {
	if got := normalizeTraceCommand("bash", map[string]any{"command": "  go test  ./...  "}); got != "bash:go test ./..." {
		t.Fatalf("got %q", got)
	}
	if got := normalizeTraceCommand("read", map[string]any{"path": " a.go "}); got != "read:a.go" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeTraceCommand("edit", map[string]any{"path": "a.go", "old_text": "x"}); !strings.HasPrefix(got, "edit:a.go\x00") {
		t.Fatalf("got %q", got)
	}
	if got := normalizeTraceCommand("write", map[string]any{}); got != "write:" {
		t.Fatalf("got %q", got)
	}
}

func outcomeFor(hash string, outcome string) TraceOutcome {
	return TraceOutcome{Outcome: outcome, OutcomeHash: hash}
}

func TestSummarizeTraceRepetitions(t *testing.T) {
	keys := []string{
		"bash:go test ./...", "bash:go test ./...", "bash:go test ./...",
		"bash:go test .",
	}
	outcomes := []TraceOutcome{
		outcomeFor("h1", "guard_rejected"), outcomeFor("h1", "guard_rejected"), outcomeFor("h1", "guard_rejected"),
		outcomeFor("h2", "success"),
	}
	reps := summarizeTraceRepetitions(keys, outcomes, []bool{false, false, false, false})
	byKey := map[string]TraceRepetition{}
	for _, rep := range reps {
		byKey[rep.Key] = rep
	}
	looped := byKey["bash:go test ./..."]
	if looped.Total != 3 || looped.Guarded != 3 || looped.MaxRun != 3 || !looped.Pointless {
		t.Fatalf("looped = %+v, want pointless x3 run 3", looped)
	}
	single := byKey["bash:go test ."]
	if single.Total != 1 || single.Pointless {
		t.Fatalf("single = %+v", single)
	}
	// Same command with differing outcomes is legitimate re-verification.
	mixed := summarizeTraceRepetitions(
		[]string{"bash:go test .", "bash:go test ."},
		[]TraceOutcome{outcomeFor("h1", "tool_error"), outcomeFor("h2", "success")},
		[]bool{false, false},
	)
	if mixed[0].Pointless || mixed[0].LegitReason == "" {
		t.Fatalf("mixed = %+v, want legitimate", mixed[0])
	}
	// A repeat after an intervening successful change is legitimate.
	withChange := summarizeTraceRepetitions(
		[]string{"bash:go test .", "bash:go test ."},
		[]TraceOutcome{outcomeFor("h1", "tool_error"), outcomeFor("h1", "tool_error")},
		[]bool{false, true},
	)
	if withChange[0].Pointless {
		t.Fatalf("withChange = %+v, want legitimate (intervening change)", withChange[0])
	}
}

func TestLongestNoProgressRun(t *testing.T) {
	keys := []string{"a", "b", "b", "b", "a", "c", "c"}
	outcomes := []TraceOutcome{
		outcomeFor("x", "success"),
		outcomeFor("y", "tool_error"), outcomeFor("y", "tool_error"), outcomeFor("y", "tool_error"),
		outcomeFor("z", "success"),
		outcomeFor("w", "guard_rejected"), outcomeFor("w", "guard_rejected"),
	}
	if got := longestNoProgressRun(keys, outcomes); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
	// Successes break runs even on identical commands.
	keys2 := []string{"a", "a"}
	outcomes2 := []TraceOutcome{outcomeFor("x", "success"), outcomeFor("x", "success")}
	if got := longestNoProgressRun(keys2, outcomes2); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestReadsWithoutProgress(t *testing.T) {
	reads := []string{"a.go", "b.go", "a.go", "x", "a.go"}
	hashes := []string{"h1", "h2", "h1", "h3", "h1"}
	writers := map[int]string{}
	// a.go read 3x with steady hash and no intervening write: 2 pointless.
	if got := readsWithoutProgress(reads, hashes, writers); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
	// An intervening write excuses the later re-read only.
	writers = map[int]string{3: "a.go"}
	if got := readsWithoutProgress(reads, hashes, writers); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
	// Changed content excuses the re-read.
	if got := readsWithoutProgress(reads, []string{"h1", "h2", "h9", "h3", "h1"}, map[int]string{}); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestIsCheckCommand(t *testing.T) {
	for _, command := range []string{"bash:./check.sh 2>&1 | head", "bash:go test ./...", "bash:go test -run T .", "bash:npm test"} {
		if !isCheckCommand(command) {
			t.Errorf("%q should be a check command", command)
		}
	}
	for _, command := range []string{"bash:ls -la", "bash:git log --oneline", "read:go.mod", "bash:go version"} {
		if isCheckCommand(command) {
			t.Errorf("%q should not be a check command", command)
		}
	}
}

func TestAttachOutcomesJoinsInOrder(t *testing.T) {
	attempts := []ToolAttempt{
		{ExecID: 1, TurnID: "t1", Tool: "bash", Arguments: map[string]any{"command": "go test ./..."}},
		{ExecID: 2, TurnID: "t1", Tool: "read", Arguments: map[string]any{"path": "a.go"}},
		{ExecID: 3, TurnID: "t1", Tool: "bash", Arguments: map[string]any{"command": "go test ."}},
	}
	outcomes := []string{
		"Command blocked by safety guard (path outside working dir)",
		"[file: a.go | total: 10 bytes]",
		"ok example.com/x 0.01s",
	}
	joined := AttachOutcomes(attempts, outcomes)
	if len(joined) != 3 {
		t.Fatalf("joined %d, want 3", len(joined))
	}
	if joined[0].Outcome.Outcome != "guard_rejected" || joined[0].Command != "bash:go test ./..." {
		t.Fatalf("first = %+v", joined[0])
	}
	if joined[1].Command != "read:a.go" {
		t.Fatalf("second command = %q", joined[1].Command)
	}
	if joined[2].Outcome.Outcome != "success" {
		t.Fatalf("third = %+v", joined[2])
	}
	// Shorter outcome side still classifies what it can.
	short := AttachOutcomes(attempts, outcomes[:1])
	if len(short) != 1 || short[0].Outcome.Outcome != "guard_rejected" {
		t.Fatalf("short join = %+v", short)
	}
}

func TestReverificationAfterRestart(t *testing.T) {
	reads := []string{"a.go", "b.go", "a.go", "c.go"}
	pre := map[string]bool{"a.go": true, "b.go": true}
	if got := ReverificationAfterRestart(reads, 2, pre); got != 1 {
		t.Fatalf("got %d, want 1 (only the post-restart a.go re-read)", got)
	}
}

func TestTimeAndTokensToFirstEdit(t *testing.T) {
	start := int64(1000000)
	mkAttempt := func(id int64, tool string, at string, req string) ToolAttempt {
		return ToolAttempt{ExecID: id, Tool: tool, StartedAt: "1970-01-01T00:00:01.000000000Z", RequestID: req}
	}
	_ = mkAttempt
	_ = start
	attempts := []ToolAttempt{
		{ExecID: 1, Tool: "read", StartedAt: "1970-01-01T00:00:01.500000000Z", RequestID: "req-001"},
		{ExecID: 2, Tool: "write", StartedAt: "1970-01-01T00:00:02.000000000Z", RequestID: "req-002"},
	}
	outcomes := []TraceOutcome{
		{Outcome: "success"}, {Outcome: "success"},
	}
	tokens := map[string]int{"req-001": 400, "req-002": 600}
	at := map[string]int64{}
	elapsed, spent := TimeAndTokensToFirstEdit(1000, attempts, outcomes, tokens, at)
	if elapsed != 1000 || spent != 600 {
		t.Fatalf("elapsed=%d spent=%d, want 1000 and 600", elapsed, spent)
	}
	if elapsed, spent := TimeAndTokensToFirstEdit(1000, attempts[:1], outcomes[:1], tokens, at); elapsed != -1 || spent != -1 {
		t.Fatalf("no-edit case = %d,%d, want -1,-1", elapsed, spent)
	}
}
