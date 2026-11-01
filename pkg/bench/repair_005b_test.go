package bench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// 005B-R repair tests. Every test is deterministic and offline; none touches
// a live provider. Together they prove the acceptance criterion: a failed
// coding trial may lose its result, but must not silently lose the evidence
// of its provider requests or undermine the spending limit.

// fixedUsageProvider returns a successful response with constant usage.
type fixedUsageProvider struct{ total int }

func (f fixedUsageProvider) GetDefaultModel() string { return "stub" }

func (f fixedUsageProvider) Chat(_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{
		Content:      "ok",
		FinishReason: "stop",
		Usage: &providers.UsageInfo{
			PromptTokens: f.total * 7 / 10, CompletionTokens: f.total * 3 / 10,
			TotalTokens: f.total,
		},
	}, nil
}

func cappedLiveSpec(t *testing.T, trialDir string, perTrial, total int64) TrialSpec {
	t.Helper()
	return TrialSpec{
		Experiment: "D", ProviderKind: "live", ModelName: "stub",
		ContextWindow: 100, MaxTokens: 100,
		TrialDir: trialDir, TokenBudgetPerTrial: perTrial, TokenBudgetTotal: total,
	}
}

func driveRequests(t *testing.T, recorder *RecordingProvider, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := recorder.requestAllowed(); err != nil {
			t.Fatalf("request %d blocked early: %v", i+1, err)
		}
		requestID, raw, request := recorder.begin("t1", "stub", nil, nil)
		response, err := fixedUsageProvider{total: 1000}.Chat(context.Background(), nil, nil, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		recorder.finish(requestID, raw, request, time.Now(), response, nil)
	}
}

func TestUsageLedgerReservationOnlyIsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw", usageLedgerFileName)
	if err := appendUsageEvent(path, UsageEvent{Type: "reservation", RequestID: "req-001", Allowance: 2000}); err != nil {
		t.Fatal(err)
	}
	recovery, err := RecoverUsageLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Known != 0 || recovery.Reserved != 2000 || recovery.Unknown != 1 || recovery.Events != 1 {
		t.Fatalf("recovery = %+v, want known=0 reserved=2000 unknown=1 events=1", recovery)
	}
	if got := TrialSpendForBudget(recovery); got != 2000 {
		t.Fatalf("spend = %d, want 2000 (reservation retained, never zero)", got)
	}
}

func TestUsageLedgerResponseSupersedesReservation(t *testing.T) {
	dir := t.TempDir()
	recorder := NewRecordingProvider(fixedUsageProvider{total: 1000}, cappedLiveSpec(t, dir, 100000, 1000000))
	// Simulate one dispatched request through the real begin/finish path.
	if err := recorder.requestAllowed(); err != nil {
		t.Fatal(err)
	}
	requestID, raw, request := recorder.begin("t1", "stub", nil, nil)
	response, _ := fixedUsageProvider{total: 1000}.Chat(context.Background(), nil, nil, "", nil)
	recorder.finish(requestID, raw, request, time.Now(), response, nil)
	recovery, err := RecoverUsageLedger(UsageLedgerPath(recorder.spec))
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Known != 1000 || recovery.Reserved != 0 || recovery.Unknown != 0 || recovery.Events != 1 {
		t.Fatalf("recovery = %+v, want known=1000, no reservation held", recovery)
	}
}

func TestUsageLedgerDuplicatesAndCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw", usageLedgerFileName)
	mustAppend := func(event UsageEvent) {
		t.Helper()
		if err := appendUsageEvent(path, event); err != nil {
			t.Fatal(err)
		}
	}
	mustAppend(UsageEvent{Type: "reservation", RequestID: "req-001", Allowance: 2000})
	mustAppend(UsageEvent{Type: "reservation", RequestID: "req-001", Allowance: 2000})
	mustAppend(UsageEvent{Type: "response", RequestID: "req-001", Reported: true, TotalTokens: 500, Allowance: 2000})
	mustAppend(UsageEvent{Type: "response", RequestID: "req-001", Reported: true, TotalTokens: 500, Allowance: 2000})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("not json\n{\"no_id\":true}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	recovery, err := RecoverUsageLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Known != 500 || recovery.Unknown != 0 || recovery.Events != 1 {
		t.Fatalf("recovery = %+v, want single counted response known=500", recovery)
	}
	if recovery.Duplicates == 0 {
		t.Fatal("expected duplicate lines to be reported")
	}
	if _, err := RecoverUsageLedger(filepath.Join(t.TempDir(), "absent.jsonl")); err != nil {
		t.Fatalf("missing ledger must not error: %v", err)
	}
}

func TestUsageLedgerPermissionsAndNoSecrets(t *testing.T) {
	dir := t.TempDir()
	spec := cappedLiveSpec(t, dir, 100000, 1000000)
	recorder := NewRecordingProvider(fixedUsageProvider{total: 1000}, spec)
	driveRequests(t, recorder, 2)
	path := UsageLedgerPath(spec)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-", "Bearer", "Authorization", "canary-secret-xyz"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("ledger contains forbidden material %q", secret)
		}
	}
}

func TestUsageLedgerSegmentsShareOneTrialBudget(t *testing.T) {
	dir := t.TempDir()
	mkSpec := func() TrialSpec {
		return cappedLiveSpec(t, dir, 2100, 1000000)
	}
	seg1 := NewRecordingProvider(fixedUsageProvider{total: 1000}, mkSpec())
	driveRequests(t, seg1, 2) // reported 2000; seg2's first needs 2000+200 > 2100
	// A resumed segment in a new process recovers seg1's spend from the
	// shared ledger and refuses the request that would breach the cap.
	seg2 := NewRecordingProvider(fixedUsageProvider{total: 1000}, mkSpec())
	if err := seg2.requestAllowed(); err == nil || !strings.Contains(err.Error(), "token budget exhausted") {
		t.Fatalf("seg2 first request allowed or wrong error: %v", err)
	}
	recovery, err := RecoverUsageLedger(UsageLedgerPath(mkSpec()))
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Known != 2000 || recovery.Unknown != 0 {
		t.Fatalf("recovery = %+v, want known=2000 unknown=0", recovery)
	}
}

func TestBudgetExhaustionDuringToolLoop(t *testing.T) {
	dir := t.TempDir()
	recorder := NewRecordingProvider(fixedUsageProvider{total: 1000}, cappedLiveSpec(t, dir, 2500, 1000000))
	driveRequests(t, recorder, 3) // 3000 reported; 4th needs 3000+200 > 2500
	if err := recorder.requestAllowed(); err == nil || !strings.Contains(err.Error(), "token budget exhausted") {
		t.Fatalf("4th request allowed or wrong error: %v", err)
	}
	stopped, reason := recorder.BudgetStop()
	if !stopped || !strings.Contains(reason, "token budget exhausted") {
		t.Fatalf("BudgetStop = %v %q", stopped, reason)
	}
	recovery, err := RecoverUsageLedger(UsageLedgerPath(recorder.spec))
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Known != 3000 || recovery.Unknown != 0 || recovery.Events != 3 {
		t.Fatalf("recovery = %+v, want all three completed requests known", recovery)
	}
}

func TestMeteringFailureRetainsReservation(t *testing.T) {
	dir := t.TempDir()
	spec := cappedLiveSpec(t, dir, 100000, 1000000)
	recorder := NewRecordingProvider(fixedUsageProvider{total: 1000}, spec)
	if err := recorder.requestAllowed(); err != nil {
		t.Fatal(err)
	}
	requestID, raw, request := recorder.begin("t1", "stub", nil, nil)
	// A successful response carrying no usable total.
	recorder.finish(requestID, raw, request, time.Now(), &providers.LLMResponse{Content: "ok"}, nil)
	if err := recorder.requestAllowed(); err == nil || !strings.Contains(err.Error(), "metering") {
		t.Fatalf("second request allowed or wrong error: %v", err)
	}
	recovery, err := RecoverUsageLedger(UsageLedgerPath(spec))
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Known != 0 || recovery.Unknown != 1 || recovery.Reserved != 200 {
		t.Fatalf("recovery = %+v, want unknown=1 reserved=allowance(200)", recovery)
	}
}

func TestAggregateBudgetStopsRunAcrossTrials(t *testing.T) {
	if testing.Short() {
		t.Skip("aggregate budget test runs mock trials")
	}
	out := t.TempDir()
	summary, err := RunExperiment(RunOptions{
		Experiment: "D", RunID: "005b-r-budget-stop", OutDir: out,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1,
		Scenarios:        []string{"multi_file_cart", "misleading_failure"},
		Conditions:       []string{CodingConditionControl, CodingConditionTreatment},
		TrialRunner:      testRunner(t),
		TokenBudgetTotal: 1, TokenBudgetPerTrial: 1000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.TokenBudget == nil || !summary.TokenBudget.StoppedEarly {
		t.Fatalf("budget summary = %+v, want early stop", summary.TokenBudget)
	}
	if len(summary.Trials) != 1 {
		t.Fatalf("trials = %d, want exactly 1 before the stop", len(summary.Trials))
	}
	if !strings.Contains(summary.TokenBudget.StopReason, "total cap") {
		t.Fatalf("stop reason = %q, want total-cap stop", summary.TokenBudget.StopReason)
	}
}

func TestTrialAbortReasonSpend(t *testing.T) {
	capped := TrialResult{Status: "pass", Totals: TrialTotals{TotalTokens: 900000},
		UsageRecovery: &UsageRecovery{Known: 900000}}
	if reason := trialAbortReason(capped, RunOptions{TokenBudgetPerTrial: 1000000}); reason != "" {
		t.Fatalf("under-cap trial aborts: %q", reason)
	}
	over := TrialResult{Status: "pass", Totals: TrialTotals{TotalTokens: 10},
		UsageRecovery: &UsageRecovery{Known: 10, Reserved: 2000000, Unknown: 1}}
	if reason := trialAbortReason(over, RunOptions{TokenBudgetPerTrial: 1000000}); !strings.Contains(reason, "over per-trial cap") {
		t.Fatalf("reserved spend must count toward the cap, got %q", reason)
	}
}

func TestExplicitCompactHistoryLost(t *testing.T) {
	cases := []struct {
		summary, history int
		want             bool
	}{
		{0, 0, false},    // nothing ever summarized: genuine no-op
		{0, 5, false},    // no summary at all
		{100, 0, true},   // everything summarized
		{4090, 10, true}, // the converging verbose t3 shape
		{4090, 16, true}, // boundary: twice the normal tail
		{4090, 17, false},
		{100, 60, false}, // compaction seriously behind
	}
	for _, tc := range cases {
		if got := explicitCompactHistoryLost(tc.summary, tc.history); got != tc.want {
			t.Errorf("explicitCompactHistoryLost(%d,%d) = %v, want %v", tc.summary, tc.history, got, tc.want)
		}
	}
}

func TestMaxConsecutiveIdenticalBash(t *testing.T) {
	bash := func(command string) ToolCallRecord {
		return ToolCallRecord{Tool: "bash", Arguments: map[string]any{"command": command}}
	}
	read := ToolCallRecord{Tool: "read", Arguments: map[string]any{"path": "a.go"}}
	calls := []ToolCallRecord{bash("sh check.sh"), read, bash("sh check.sh")}
	if got := maxConsecutiveIdenticalBash(calls); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
	var loop []ToolCallRecord
	for i := 0; i < 6; i++ {
		loop = append(loop, bash("sh check.sh"))
	}
	if got := maxConsecutiveIdenticalBash(loop); got != 6 {
		t.Fatalf("got %d, want 6", got)
	}
	if maxIterationExhausted("done") {
		t.Fatal("normal response flagged as exhaustion")
	}
	if !maxIterationExhausted("I've reached `max_tool_iterations` without a final response") {
		t.Fatal("exhaustion message not detected")
	}
}

func TestVerboseLiveShapeEndsTaskLimited(t *testing.T) {
	if testing.Short() {
		t.Skip("verbose live-shape test runs mock trials")
	}
	out := t.TempDir()
	summary, err := RunExperiment(RunOptions{
		Experiment: "D", RunID: "005b-r-verbose-live", OutDir: out,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1,
		Scenarios:   []string{"compaction_ledger"},
		Conditions:  []string{CodingConditionControl, CodingConditionTreatment},
		TrialRunner: testRunner(t), VerboseMock: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Trials) != 2 {
		t.Fatalf("trials = %d, want 2", len(summary.Trials))
	}
	for _, trial := range summary.Trials {
		if trial.Status != "pass" {
			t.Fatalf("%s status = %q (%s), want pass: a task limit must stay harness-valid", trial.TrialID, trial.Status, trial.Error)
		}
		if !trial.TaskLimitReached {
			t.Fatalf("%s did not hit the task limit", trial.TrialID)
		}
		// Both conditions must stop as task limits; the exact guard may
		// differ (treatment prompts carry task-state overhead, so the
		// trajectory shape shifts while staying non-convergent).
		if !strings.Contains(trial.TaskLimitReason, "task execution limit") {
			t.Fatalf("%s reason = %q, want a runaway-guard stop", trial.TrialID, trial.TaskLimitReason)
		}
		if trial.Coding == nil || trial.Coding.FullSuccess {
			t.Fatalf("%s coding = %+v, want scored partial", trial.TrialID, trial.Coding)
		}
		if trial.UsageRecovery == nil || trial.UsageRecovery.Unknown != 0 || trial.UsageRecovery.Events == 0 {
			t.Fatalf("%s recovery = %+v, want complete known spend", trial.TrialID, trial.UsageRecovery)
		}
		if trial.Condition == CodingConditionControl {
			// The control reproduces the aborted live shape: automatic
			// compaction firing without bringing the prompt under control.
			limited := false
			for _, turn := range trial.Turns {
				if turn.AutoCompactions >= maxSummariesPerTurn {
					limited = true
				}
			}
			if !limited {
				t.Fatalf("%s has no turn at the compaction trip point", trial.TrialID)
			}
		}
	}
}

func TestVerboseConvergeExercisesExplicitAfterAuto(t *testing.T) {
	if testing.Short() {
		t.Skip("verbose converge test runs a mock trial")
	}
	out := t.TempDir()
	summary, err := RunExperiment(RunOptions{
		Experiment: "D", RunID: "005b-r-verbose-conv", OutDir: out,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1,
		Scenarios:   []string{"compaction_ledger"},
		Conditions:  []string{CodingConditionControl},
		TrialRunner: testRunner(t), VerboseMock: true, VerboseMockIters: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Trials) != 1 {
		t.Fatalf("trials = %d, want 1", len(summary.Trials))
	}
	trial := summary.Trials[0]
	if trial.Status != "pass" {
		t.Fatalf("status = %q (%s), want pass", trial.Status, trial.Error)
	}
	if trial.TaskLimitReached {
		t.Fatalf("converging verbose run should not hit a task limit: %s", trial.TaskLimitReason)
	}
	var t3 *TurnResult
	for i := range trial.Turns {
		if trial.Turns[i].TurnID == "t3" {
			t3 = &trial.Turns[i]
		}
	}
	if t3 == nil {
		t.Fatal("t3 did not run")
	}
	if !t3.CompactionAlreadyLost {
		t.Fatalf("t3 = %+v, want already-lost acceptance after automatic compaction", t3)
	}
	if trial.Coding == nil || !trial.Coding.FullSuccess {
		t.Fatalf("coding = %+v, want full success", trial.Coding)
	}
}

func TestNormalMockUnaffectedByGuards(t *testing.T) {
	if testing.Short() {
		t.Skip("guard neutrality test runs mock trials")
	}
	out := t.TempDir()
	summary, err := RunExperiment(RunOptions{
		Experiment: "D", RunID: "005b-r-neutral", OutDir: out,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1,
		Scenarios:   []string{"compaction_ledger", "restart_parser"},
		Conditions:  []string{CodingConditionControl, CodingConditionTreatment},
		TrialRunner: testRunner(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, trial := range summary.Trials {
		if trial.Status != "pass" {
			t.Fatalf("%s status = %q (%s)", trial.TrialID, trial.Status, trial.Error)
		}
		if trial.TaskLimitReached {
			t.Fatalf("%s tripped a guard on a converging trajectory", trial.TrialID)
		}
		if trial.Coding == nil || !trial.Coding.FullSuccess {
			t.Fatalf("%s coding changed under guards", trial.TrialID)
		}
		if trial.UsageRecovery == nil || trial.UsageRecovery.Unknown != 0 || trial.UsageRecovery.Events == 0 {
			t.Fatalf("%s recovery = %+v", trial.TrialID, trial.UsageRecovery)
		}
		if trial.Scenario == "compaction_ledger" {
			found := false
			for _, turn := range trial.Turns {
				if turn.TurnID == "t3" && turn.Compacted && !turn.CompactionAlreadyLost {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s lost the fresh-compaction path", trial.TrialID)
			}
		}
	}
}

func TestFailPathPersistsEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("fail-path persistence test runs a mock trial")
	}
	out := t.TempDir()
	summary, err := RunExperiment(RunOptions{
		Experiment: "D", RunID: "005b-r-failpersist", OutDir: out,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1,
		Scenarios:           []string{"multi_file_cart"},
		Conditions:          []string{CodingConditionControl},
		TrialRunner:         testRunner(t),
		TokenBudgetPerTrial: 50, TokenBudgetTotal: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	trial := summary.Trials[0]
	if trial.Status != "error" || !trial.TokenBudgetStopped {
		t.Fatalf("status = %q stopped = %v, want budget error", trial.Status, trial.TokenBudgetStopped)
	}
	// The 005B defect was a missing result file; the repair must persist one.
	if _, err := os.Stat(trial.ResultPath); err != nil {
		t.Fatalf("result file missing after early failure: %v", err)
	}
	if trial.UsageRecovery == nil {
		t.Fatal("UsageRecovery missing on the failed trial")
	}
	// Refused-before-dispatch writes no reservation: zeros, explicitly.
	if *trial.UsageRecovery != (UsageRecovery{}) {
		t.Fatalf("recovery = %+v, want explicit zeros", trial.UsageRecovery)
	}
}

func TestMidTurnBudgetBlockKeepsEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("mid-turn block test runs a mock trial")
	}
	out := t.TempDir()
	summary, err := RunExperiment(RunOptions{
		Experiment: "D", RunID: "005b-r-midblock", OutDir: out,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1,
		Scenarios:   []string{"compaction_ledger"},
		Conditions:  []string{CodingConditionControl},
		TrialRunner: testRunner(t),
		// Sized between ledger turns: t1's ~4k reported total plus the
		// 9216-token allowance fits, so t1 completes and t2's second
		// request trips the guard. Absolute mock token counts vary by a
		// token or two with temp-dir path lengths, so the test asserts
		// same-run relationships, not absolute totals.
		TokenBudgetPerTrial: 13500,
	})
	if err != nil {
		t.Fatal(err)
	}
	trial := summary.Trials[0]
	if trial.Status != "error" || !trial.TokenBudgetStopped {
		t.Fatalf("status = %q stopped = %v, want budget error: %s", trial.Status, trial.TokenBudgetStopped, trial.Error)
	}
	if !strings.Contains(trial.Error, "token budget exhausted") || !strings.Contains(trial.Error, "turn t2") {
		t.Fatalf("error = %q, want t2 budget block", trial.Error)
	}
	if trial.UsageRecovery == nil || trial.UsageRecovery.Unknown != 0 {
		t.Fatalf("recovery = %+v, want no unknown outcomes", trial.UsageRecovery)
	}
	// Known spend must equal the sum the trial itself reported: the ledger
	// and the in-memory trace describe the same completed requests.
	inMemory := 0
	for _, request := range trial.Requests {
		if request.Usage.Reported && request.Usage.TotalTokens != nil {
			inMemory += *request.Usage.TotalTokens
		}
	}
	if trial.UsageRecovery.Known != inMemory || inMemory == 0 {
		t.Fatalf("recovery known = %d, in-memory sum = %d", trial.UsageRecovery.Known, inMemory)
	}
	if trial.Coding == nil {
		t.Fatal("partial work must still be scored")
	}
}

func TestRestartSegmentsShareTrialBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("segment budget test runs a mock trial")
	}
	out := t.TempDir()
	summary, err := RunExperiment(RunOptions{
		Experiment: "D", RunID: "005b-r-segbudget", OutDir: out,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1,
		Scenarios:   []string{"restart_parser"},
		Conditions:  []string{CodingConditionControl},
		TrialRunner: testRunner(t),
		// Segment 1 reports ~3.7k; with the 33792 allowance a 37000 cap
		// admits all of segment 1 yet refuses segment 2's first request —
		// but only if segment 2 sees segment 1's spend. Without ledger
		// sharing, segment 2 would start from zero and proceed.
		TokenBudgetPerTrial: 37000,
	})
	if err != nil {
		t.Fatal(err)
	}
	trial := summary.Trials[0]
	if trial.Status != "error" || !trial.TokenBudgetStopped {
		t.Fatalf("status = %q stopped = %v, want segment-2 budget block: %s", trial.Status, trial.TokenBudgetStopped, trial.Error)
	}
	if !strings.Contains(trial.Error, "turn t2") {
		t.Fatalf("error = %q, want the block in the resumed segment", trial.Error)
	}
	if trial.ProcessStarts < 2 {
		t.Fatalf("process starts = %d, want the restart to have happened", trial.ProcessStarts)
	}
	if trial.UsageRecovery == nil || trial.UsageRecovery.Known == 0 || trial.UsageRecovery.Unknown != 0 {
		t.Fatalf("recovery = %+v, want segment-1 spend retained, nothing unknown", trial.UsageRecovery)
	}
	inMemory := 0
	for _, request := range trial.Requests {
		if request.Usage.Reported && request.Usage.TotalTokens != nil {
			inMemory += *request.Usage.TotalTokens
		}
	}
	if trial.UsageRecovery.Known != inMemory {
		t.Fatalf("recovery known = %d, in-memory sum = %d", trial.UsageRecovery.Known, inMemory)
	}
}

func TestParentRecoveryWithoutResultFile(t *testing.T) {
	dir := t.TempDir()
	trialDir := filepath.Join(dir, "trials", "t01")
	ledger := filepath.Join(trialDir, "raw", usageLedgerFileName)
	mustAppend := func(event UsageEvent) {
		t.Helper()
		if err := appendUsageEvent(ledger, event); err != nil {
			t.Fatal(err)
		}
	}
	// Crash immediately before a first response.
	mustAppend(UsageEvent{Type: "reservation", RequestID: "s1-req-001", Allowance: 2000})
	// Crash immediately after a second response was persisted.
	mustAppend(UsageEvent{Type: "reservation", RequestID: "s1-req-002", Allowance: 2000})
	mustAppend(UsageEvent{Type: "response", RequestID: "s1-req-002", Status: "ok", Reported: true, TotalTokens: 500, Allowance: 2000})
	// sf: no result.json was ever written.
	recovery, err := ReconcileUsage(TrialResult{}, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Known != 500 || recovery.Reserved != 2000 || recovery.Unknown != 1 || recovery.Events != 2 {
		t.Fatalf("recovery = %+v, want known=500 reserved=2000 unknown=1 events=2", recovery)
	}
	if got := TrialSpendForBudget(recovery); got != 2500 {
		t.Fatalf("spend = %d, want 2500", got)
	}
}
