package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func applyMockWrites(t *testing.T, workspace string, actions []codingMockAction) {
	t.Helper()
	for _, action := range actions {
		if action.Tool != "write" {
			continue
		}
		path, _ := action.Args["path"].(string)
		content, _ := action.Args["content"].(string)
		full := filepath.Join(workspace, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func materializeForScoring(t *testing.T, taskID string) (CodingTask, string, CodingFixtureState) {
	t.Helper()
	task, err := LoadCodingTask(taskID)
	if err != nil {
		t.Fatalf("load task %s: %v", taskID, err)
	}
	dir := filepath.Join(t.TempDir(), "repo")
	state, err := MaterializeCodingTask(task, dir)
	if err != nil {
		t.Fatalf("materialize %s: %v", taskID, err)
	}
	return task, dir, state
}

func TestCodingFixturesQualify(t *testing.T) {
	if testing.Short() {
		t.Skip("fixture qualification builds and tests five Go repositories")
	}
	report, err := VerifyCodingFixtures(filepath.Join(t.TempDir(), "fixtures"))
	if err != nil {
		t.Fatalf("verify fixtures: %v", err)
	}
	if !report.Valid {
		t.Fatalf("fixtures invalid: %+v", report.Tasks)
	}
	if len(report.Tasks) < 5 {
		t.Fatalf("fixtures = %d, want at least 5", len(report.Tasks))
	}
	for _, item := range report.Tasks {
		if !item.Deterministic {
			t.Errorf("%s starting commit is not deterministic", item.TaskID)
		}
		if !item.BuildOK {
			t.Errorf("%s pristine repository does not build", item.TaskID)
		}
		if item.HiddenFailed == 0 {
			t.Errorf("%s hidden tests pass on the unfixed repository", item.TaskID)
		}
	}
}

func TestCodingScoringFullPartialAndConstraint(t *testing.T) {
	if testing.Short() {
		t.Skip("coding scoring runs the Go toolchain")
	}
	task, workspace, _ := materializeForScoring(t, "multi_file_cart")
	actions := codingMockActions("multi_file_cart", "t1")

	// Full fix: the resulting repository must pass every hidden and public test.
	applyMockWrites(t, workspace, actions)
	full, err := ScoreCodingTask(task, workspace, filepath.Join(t.TempDir(), "scoring-full"))
	if err != nil {
		t.Fatalf("score full: %v", err)
	}
	if !full.FullSuccess || full.Partial != 1 {
		t.Fatalf("full fix = success:%t partial:%v, want success and 1", full.FullSuccess, full.Partial)
	}
	if full.WrongFixApplied {
		t.Fatalf("full fix still matched a wrong-fix pattern: %v", full.ConstraintViolations)
	}

	// Partial fix: only the discount file is repaired. Functional score must be
	// partial and full success false; the public test criterion must fail.
	task2, partialWorkspace, _ := materializeForScoring(t, "multi_file_cart")
	applyMockWrites(t, partialWorkspace, actions[:3])
	partial, err := ScoreCodingTask(task2, partialWorkspace, filepath.Join(t.TempDir(), "scoring-partial"))
	if err != nil {
		t.Fatalf("score partial: %v", err)
	}
	if partial.FullSuccess {
		t.Fatal("partial fix reported full success")
	}
	if partial.Partial <= 0 || partial.Partial >= 1 {
		t.Fatalf("partial score = %v, want between 0 and 1", partial.Partial)
	}
	if partial.PublicTestsFailed == 0 {
		t.Fatal("partial fix unexpectedly passed every public test")
	}

	// Constraint violation: editing a protected public test invalidates full
	// success even if the implementation would otherwise pass.
	task3, constraintWorkspace, _ := materializeForScoring(t, "multi_file_cart")
	applyMockWrites(t, constraintWorkspace, actions)
	if err := os.WriteFile(filepath.Join(constraintWorkspace, "cart_test.go"),
		[]byte("package cart\n\n// weakened by the agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	constrained, err := ScoreCodingTask(task3, constraintWorkspace, filepath.Join(t.TempDir(), "scoring-constraint"))
	if err != nil {
		t.Fatalf("score constraint: %v", err)
	}
	if constrained.FullSuccess {
		t.Fatal("protected-file violation reported full success")
	}
	if !containsAny(strings.Join(constrained.ConstraintViolations, ","), "modified_protected_file:cart_test.go") {
		t.Fatalf("constraint violations = %v, want modified cart_test.go", constrained.ConstraintViolations)
	}
}

func TestCodingScoringIsConditionIndependent(t *testing.T) {
	if testing.Short() {
		t.Skip("coding scoring runs the Go toolchain")
	}
	task, workspace, _ := materializeForScoring(t, "multi_file_cart")
	applyMockWrites(t, workspace, codingMockActions("multi_file_cart", "t1"))

	controlSpec := TrialSpec{
		Condition: CodingConditionControl, Experiment: "D", CodingTask: task.ID,
		AgentWorkspace: workspace,
		TrialDir:       filepath.Join(t.TempDir(), "control"),
	}
	treatmentSpec := TrialSpec{
		Condition: CodingConditionTreatment, Experiment: "D", CodingTask: task.ID,
		AgentWorkspace: workspace,
		TrialDir:       filepath.Join(t.TempDir(), "treatment"), TaskState: true,
	}
	for _, dir := range []string{controlSpec.TrialDir, treatmentSpec.TrialDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	control, err := scoreCodingResult(controlSpec, TrialResult{}, time.Time{})
	if err != nil {
		t.Fatalf("control score: %v", err)
	}
	treatment, err := scoreCodingResult(treatmentSpec, TrialResult{}, time.Time{})
	if err != nil {
		t.Fatalf("treatment score: %v", err)
	}
	if control.FullSuccess != treatment.FullSuccess || control.Partial != treatment.Partial ||
		control.PointsEarned != treatment.PointsEarned {
		t.Fatalf("condition changed the verdict: control=%+v treatment=%+v", control, treatment)
	}
}

func TestCodingTrialPlanIsPairedAndRandomized(t *testing.T) {
	scenarios := []string{"multi_file_cart", "compaction_ledger"}
	base := RunOptions{Experiment: "D", RunID: "plan-seed", Trials: 2}
	first, seed, pairs, err := buildTrialPlan(base, []string{CodingConditionControl, CodingConditionTreatment}, scenarios)
	if err != nil {
		t.Fatal(err)
	}
	if pairs != 4 || len(first) != 8 {
		t.Fatalf("plan pairs=%d trials=%d, want 4 and 8", pairs, len(first))
	}
	seenPair := map[string]map[string]bool{}
	for _, item := range first {
		if item.PairID == "" {
			t.Fatal("trial has no pair id")
		}
		if seenPair[item.PairID] == nil {
			seenPair[item.PairID] = map[string]bool{}
		}
		seenPair[item.PairID][item.Condition] = true
	}
	for pairID, conditions := range seenPair {
		if !conditions[CodingConditionControl] || !conditions[CodingConditionTreatment] {
			t.Fatalf("pair %s is not complete: %v", pairID, conditions)
		}
	}
	for i, item := range first {
		if item.SequenceIndex != i {
			t.Fatalf("sequence index %d at position %d", item.SequenceIndex, i)
		}
	}

	again, seedAgain, _, err := buildTrialPlan(base, []string{CodingConditionControl, CodingConditionTreatment}, scenarios)
	if err != nil {
		t.Fatal(err)
	}
	if seed != seedAgain {
		t.Fatalf("seed changed: %d vs %d", seed, seedAgain)
	}
	for i := range first {
		if first[i] != again[i] {
			t.Fatalf("plan is not reproducible at %d: %+v vs %+v", i, first[i], again[i])
		}
	}
	other, _, _, err := buildTrialPlan(RunOptions{Experiment: "D", RunID: "plan-seed", Trials: 2, Seed: seed + 1},
		[]string{CodingConditionControl, CodingConditionTreatment}, scenarios)
	if err != nil {
		t.Fatal(err)
	}
	same := true
	for i := range first {
		if first[i] != other[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("a different seed produced the identical order")
	}
}

func TestExperimentDMockRun(t *testing.T) {
	if testing.Short() {
		t.Skip("experiment D runs isolated subprocess trials and the Go toolchain")
	}
	out := t.TempDir()
	summary, err := RunExperiment(RunOptions{
		Experiment: "D", RunID: "005a-test", OutDir: out,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1,
		Scenarios:   []string{"compaction_ledger", "restart_parser"},
		TrialRunner: testRunner(t),
	})
	if err != nil {
		t.Fatalf("run D: %v", err)
	}
	if summary.Validation.Total != 4 || summary.Validation.Passed != 4 {
		t.Fatalf("validation = %+v, want 4/4 pass", summary.Validation)
	}
	if summary.Coding == nil || summary.Coding.Pairs != 2 {
		t.Fatalf("coding summary = %+v, want 2 pairs", summary.Coding)
	}
	if summary.Coding.ControlFullSuccess != 2 || summary.Coding.TreatmentFullSuccess != 2 {
		t.Fatalf("mock full success control=%d treatment=%d, want 2/2",
			summary.Coding.ControlFullSuccess, summary.Coding.TreatmentFullSuccess)
	}

	for _, trial := range summary.Trials {
		if trial.Coding == nil {
			t.Fatalf("%s has no coding outcome", trial.TrialID)
		}
		if !trial.Coding.FullSuccess || trial.Coding.Partial != 1 {
			t.Fatalf("%s outcome = success:%t partial:%v", trial.TrialID, trial.Coding.FullSuccess, trial.Coding.Partial)
		}
		if trial.Totals.MPMRetrievalCalls != 0 {
			t.Fatalf("%s made %d MPM retrieval calls", trial.TrialID, trial.Totals.MPMRetrievalCalls)
		}
		if trial.ResidentCacheRecords != 0 {
			t.Fatalf("%s has %d resident MPM records", trial.TrialID, trial.ResidentCacheRecords)
		}
		if trial.SequenceIndex < 0 {
			t.Fatalf("%s has no sequence index", trial.TrialID)
		}
		spec := trialSpec(t, trial)
		if len(presentHiddenTests(spec.AgentWorkspace, mustTask(t, trial.Scenario))) != 0 {
			t.Fatalf("%s leaked a hidden test into the workspace", trial.TrialID)
		}
		if trial.Condition == CodingConditionControl {
			if strings.Contains(rawText(readRawRecords(t, trial.RawPath)), "source: task.state") {
				t.Fatalf("%s control prompt contains task state", trial.TrialID)
			}
		} else if !strings.Contains(rawText(readRawRecords(t, trial.RawPath)), "source: task.state") {
			t.Fatalf("%s treatment prompt never carried task state", trial.TrialID)
		}
		switch trial.Scenario {
		case "compaction_ledger":
			compacted := false
			for _, turn := range trial.Turns {
				if turn.Compacted && turn.SummaryCharsAfter > 0 {
					compacted = true
				}
			}
			if !compacted {
				t.Fatalf("%s did not exercise compaction", trial.TrialID)
			}
		case "restart_parser":
			restarted := false
			resumedHistory := 0
			for _, turn := range trial.Turns {
				if turn.Restarted {
					restarted = true
					resumedHistory = turn.HistoryBefore
				}
			}
			if !restarted {
				t.Fatalf("%s did not exercise restart", trial.TrialID)
			}
			// 005A requires a real process restart, not an in-process loop
			// rebuild: the trial must span at least two OS processes and the
			// resumed process must see the earlier session history.
			if trial.ProcessStarts < 2 {
				t.Fatalf("%s ran in %d process(es), want at least 2", trial.TrialID, trial.ProcessStarts)
			}
			if resumedHistory == 0 {
				t.Fatalf("%s resumed with empty history", trial.TrialID)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(trial.ResultPath), "result-s2.json")); err != nil {
				t.Fatalf("%s has no second segment result: %v", trial.TrialID, err)
			}
		}
	}
}

func mustTask(t *testing.T, taskID string) CodingTask {
	t.Helper()
	task, err := LoadCodingTask(taskID)
	if err != nil {
		t.Fatalf("load %s: %v", taskID, err)
	}
	return task
}

func TestTrialSegmentsSplitOnlyAtRestart(t *testing.T) {
	turns := []TurnSpec{
		{ID: "t1"},
		{ID: "t2", RestartBefore: true},
		{ID: "t3"},
		{ID: "t4", RestartBefore: true},
	}
	segments := trialSegments(TrialSpec{Experiment: "D", Turns: turns})
	want := []trialSegment{
		{start: 0, end: 1, resume: false},
		{start: 1, end: 3, resume: true},
		{start: 3, end: 4, resume: true},
	}
	if len(segments) != len(want) {
		t.Fatalf("segments = %+v, want %+v", segments, want)
	}
	for i := range want {
		if segments[i] != want[i] {
			t.Fatalf("segment %d = %+v, want %+v", i, segments[i], want[i])
		}
	}

	// Experiments A-C must keep their single-process behavior.
	single := trialSegments(TrialSpec{Experiment: "C", Turns: turns})
	if len(single) != 1 || single[0] != (trialSegment{start: 0, end: 4}) {
		t.Fatalf("non-D segments = %+v, want one process over every turn", single)
	}

	// A restart marker on the first turn has no earlier segment to preserve,
	// so it stays in the initial process.
	firstMarker := trialSegments(TrialSpec{Experiment: "D", Turns: []TurnSpec{
		{ID: "t1", RestartBefore: true}, {ID: "t2"},
	}})
	if len(firstMarker) != 1 {
		t.Fatalf("leading restart marker produced %+v, want one segment", firstMarker)
	}
}
