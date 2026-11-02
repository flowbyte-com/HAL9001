package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain doubles as the trial subprocess entry point. The parent test starts
// this same test binary with -test.run=^$ and DEEPCLAW_BENCH_TRIAL_SPEC set, so
// every trial still gets a fresh process and a minimal environment.
func TestMain(m *testing.M) {
	if spec := os.Getenv("DEEPCLAW_BENCH_TRIAL_SPEC"); spec != "" {
		if _, err := RunTrialFile(spec); err != nil {
			// The result file is the authoritative observation. Exit zero so
			// the parent does not replace a recorded fail with a transport error.
			_, _ = os.Stderr.WriteString("trial: " + err.Error() + "\n")
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testRunner(t *testing.T) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("test executable: %v", err)
	}
	return []string{exe, "-test.run", "^$"}
}

func readRawRecords(t *testing.T, path string) []RawRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw %s: %v", path, err)
	}
	var records []RawRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record RawRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("parse raw %s: %v", path, err)
		}
		records = append(records, record)
	}
	return records
}

func rawText(records []RawRecord) string {
	var b strings.Builder
	for _, record := range records {
		for _, message := range record.Messages {
			b.WriteString(message.Content)
			b.WriteByte('\n')
		}
		b.WriteString(record.Response.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func findTrial(t *testing.T, summary *RunSummary, condition, scenario string) TrialResult {
	t.Helper()
	for _, trial := range summary.Trials {
		if trial.Condition == condition && trial.Scenario == scenario {
			return trial
		}
	}
	t.Fatalf("no trial for %s/%s", condition, scenario)
	return TrialResult{}
}

func trialSpec(t *testing.T, result TrialResult) TrialSpec {
	t.Helper()
	spec, err := LoadTrialSpec(filepath.Join(filepath.Dir(result.ResultPath), "trial-spec.json"))
	if err != nil {
		t.Fatalf("load spec for %s: %v", result.TrialID, err)
	}
	return *spec
}

func TestHarnessValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("harness validation runs isolated subprocess trials")
	}
	outA := t.TempDir()
	runner := testRunner(t)
	summaryA, err := RunExperiment(RunOptions{
		Experiment: "A", RunID: "mock-a", OutDir: outA,
		ProviderKind: "mock", ModelName: "mock-003f",
		MPMBinary: DefaultMPMBinary(), Trials: 1, TrialRunner: runner,
	})
	if err != nil {
		t.Fatalf("run experiment A: %v", err)
	}
	if summaryA.Validation.Failed != 0 || summaryA.Validation.Errors != 0 {
		t.Fatalf("experiment A validation: %+v", summaryA.Validation)
	}

	outB := t.TempDir()
	summaryB, err := RunExperiment(RunOptions{
		Experiment: "B", RunID: "mock-b", OutDir: outB,
		ProviderKind: "mock", ModelName: "mock-003f", Trials: 1, TrialRunner: runner,
	})
	if err != nil {
		t.Fatalf("run experiment B: %v", err)
	}
	if summaryB.Validation.Failed != 0 || summaryB.Validation.Errors != 0 {
		t.Fatalf("experiment B validation: %+v", summaryB.Validation)
	}

	t.Run("1_clean_independent_sessions", func(t *testing.T) {
		seen := map[string]string{}
		for _, trial := range summaryA.Trials {
			if trial.Status != "pass" {
				t.Fatalf("%s status %s: %s", trial.TrialID, trial.Status, trial.Error)
			}
			if !checkPassed(trial.Preflight, "no_pre_existing_session") {
				t.Fatalf("%s started with a session file", trial.TrialID)
			}
			if len(trial.SessionFilesObserved) != 1 {
				t.Fatalf("%s observed session files %v", trial.TrialID, trial.SessionFilesObserved)
			}
			if len(trial.Turns) == 0 || trial.Turns[0].HistoryBefore != 0 {
				t.Fatalf("%s did not start with empty history", trial.TrialID)
			}
			if previous, exists := seen[trial.Settings.SessionKey]; exists {
				t.Fatalf("session key reused by %s and %s", previous, trial.TrialID)
			}
			seen[trial.Settings.SessionKey] = trial.TrialID
		}
	})

	t.Run("2_no_memory_is_answer_free", func(t *testing.T) {
		for _, scenario := range []string{"recall", "paraphrase", "stale", "injection", "cross_project"} {
			trial := findTrial(t, summaryA, "no_memory", scenario)
			text := rawText(readRawRecords(t, trial.RawPath))
			for _, marker := range []string{AtlasAnswer, ZebraAnswer, StaleAnswer, InjectionWrong} {
				if strings.Contains(text, marker) {
					t.Fatalf("no_memory/%s prompt contained %q", scenario, marker)
				}
			}
			if !checkPassed(trial.Preflight, "no_answer_in_workspace") {
				t.Fatalf("no_memory/%s had answer-bearing workspace content", scenario)
			}
		}
	})

	t.Run("3_eager_receives_only_its_fixture", func(t *testing.T) {
		trial := findTrial(t, summaryA, "eager", "recall")
		if !checkPassed(trial.Preflight, "eager_memory_only_in_MEMORY_md") {
			t.Fatal("eager fixture was not confined to MEMORY.md")
		}
		raw := rawText(readRawRecords(t, trial.RawPath))
		if !strings.Contains(raw, AtlasAnswer) {
			t.Fatal("eager prompt did not contain the intended fixture")
		}
		if strings.Contains(raw, ZebraAnswer) || strings.Contains(raw, StaleAnswer) {
			t.Fatal("eager recall prompt contained another scenario's fixture")
		}
	})

	t.Run("4_mpm_is_project_scoped", func(t *testing.T) {
		for _, condition := range []string{"mpm_retrieval", "mpm_active"} {
			trial := findTrial(t, summaryA, condition, "cross_project")
			if !checkPassed(trial.Preflight, "mpm_fixture_scoped") {
				t.Fatal("MPM fixture preflight failed")
			}
			text := rawText(readRawRecords(t, trial.RawPath))
			if !strings.Contains(text, AtlasAnswer) {
				t.Fatalf("%s did not receive the atlas fact", condition)
			}
			if strings.Contains(text, ZebraAnswer) {
				t.Fatalf("%s received the zebra project's fact", condition)
			}
		}
	})

	t.Run("5_restart_and_history_clear", func(t *testing.T) {
		trial := findTrial(t, summaryA, "mpm_active", "resident_after_history_clear")
		if len(trial.Turns) != 2 {
			t.Fatalf("expected two turns, got %d", len(trial.Turns))
		}
		second := trial.Turns[1]
		if !second.HistoryCleared || !second.Restarted || second.HistoryBefore != 0 || second.SummaryCharsBefore != 0 {
			t.Fatalf("turn 2 did not start from cleared state: %+v", second)
		}
		records := readRawRecords(t, trial.RawPath)
		var turnTwo []RawRecord
		for _, record := range records {
			if record.TurnID == "t2" {
				turnTwo = append(turnTwo, record)
			}
		}
		if len(turnTwo) == 0 {
			t.Fatal("no turn-2 request recorded")
		}
		for _, record := range turnTwo {
			for _, message := range record.Messages {
				if message.Role == "assistant" && strings.Contains(message.Content, AtlasAnswer) {
					t.Fatal("turn-2 request still carried turn-1 assistant history")
				}
			}
		}
		if !strings.Contains(rawText(turnTwo), "Active working memory") {
			t.Fatal("turn-2 request did not carry the resident active-memory block")
		}
		retrievalOnly := findTrial(t, summaryA, "mpm_retrieval", "resident_after_history_clear")
		var retrievalTurnTwo []RawRecord
		for _, record := range readRawRecords(t, retrievalOnly.RawPath) {
			if record.TurnID == "t2" {
				retrievalTurnTwo = append(retrievalTurnTwo, record)
			}
		}
		if strings.Contains(rawText(retrievalTurnTwo), AtlasAnswer) {
			t.Fatal("retrieval-only turn 2 acquired the fact without active memory")
		}
	})

	t.Run("6_request_counts_are_exact", func(t *testing.T) {
		for _, trial := range append(append([]TrialResult{}, summaryA.Trials...), summaryB.Trials...) {
			raw := readRawRecords(t, trial.RawPath)
			if trial.Totals.LLMRequests != len(trial.Requests) || len(raw) != len(trial.Requests) {
				t.Fatalf("%s: totals=%d requests=%d raw=%d", trial.TrialID,
					trial.Totals.LLMRequests, len(trial.Requests), len(raw))
			}
			turnSum := 0
			for _, turn := range trial.Turns {
				turnSum += turn.Requests
			}
			if turnSum != len(trial.Requests) {
				t.Fatalf("%s: turn request sum %d != %d", trial.TrialID, turnSum, len(trial.Requests))
			}
			for _, request := range trial.Requests {
				if request.TurnID == "" {
					t.Fatalf("%s: request without turn id", trial.TrialID)
				}
			}
		}
		for _, condition := range ExperimentConditions("A") {
			trial := findTrial(t, summaryA, condition, "recall")
			if trial.Totals.LLMRequests != 1 {
				t.Fatalf("%s recall requests = %d, want 1", condition, trial.Totals.LLMRequests)
			}
		}
		for _, trial := range summaryB.Trials {
			if trial.Totals.LLMRequests != 2 {
				t.Fatalf("%s coding requests = %d, want 2", trial.TrialID, trial.Totals.LLMRequests)
			}
		}
	})

	t.Run("7_memory_comparison_tool_profiles_identical", func(t *testing.T) {
		for _, scenario := range ExperimentScenarios("A") {
			var name, sha string
			var bytes int
			for _, condition := range ExperimentConditions("A") {
				trial := findTrial(t, summaryA, condition, scenario)
				names := strings.Join(trial.Settings.ToolNames, ",")
				if names != "bash,edit,read,write" {
					t.Fatalf("%s/%s tools = %s", condition, scenario, names)
				}
				if name == "" {
					name, sha, bytes = names, trial.Settings.ToolSchemaSHA, trial.Settings.ToolSchemaBytes
					continue
				}
				if names != name || sha != trial.Settings.ToolSchemaSHA || bytes != trial.Settings.ToolSchemaBytes {
					t.Fatalf("%s/%s tool schema differs from the other memory conditions", condition, scenario)
				}
			}
		}
	})

	t.Run("8_raw_results_are_independently_scorable", func(t *testing.T) {
		for _, trial := range summaryA.Trials {
			spec := trialSpec(t, trial)
			records := readRawRecords(t, trial.RawPath)
			if len(records) == 0 {
				t.Fatalf("%s has no raw records", trial.TrialID)
			}
			answer := records[len(records)-1].Response.Content
			score := ScoreResponse(spec, answer)
			if score.Correct != trial.Score.Correct || score.ForbiddenHit != trial.Score.ForbiddenHit ||
				score.AbstainOK != trial.Score.AbstainOK || score.ExpectedMatch != trial.Score.ExpectedMatch {
				t.Fatalf("%s independent score %+v != recorded %+v", trial.TrialID, score, trial.Score)
			}
		}
		repeat, err := RunExperiment(RunOptions{
			Experiment: "A", RunID: "mock-a-repeat", OutDir: t.TempDir(),
			ProviderKind: "mock", ModelName: "mock-003f",
			MPMBinary: DefaultMPMBinary(), Trials: 1, TrialRunner: runner,
			Conditions: []string{"no_memory"}, Scenarios: []string{"recall"},
		})
		if err != nil {
			t.Fatalf("repeat run: %v", err)
		}
		first := findTrial(t, summaryA, "no_memory", "recall")
		again := findTrial(t, repeat, "no_memory", "recall")
		if first.Turns[0].Response != again.Turns[0].Response || first.Totals.LLMRequests != again.Totals.LLMRequests {
			t.Fatalf("deterministic repeat differed: %q/%d vs %q/%d",
				first.Turns[0].Response, first.Totals.LLMRequests,
				again.Turns[0].Response, again.Totals.LLMRequests)
		}
	})

	t.Run("9_fixture_files_cannot_be_read_by_tools", func(t *testing.T) {
		probe, err := RunExperiment(RunOptions{
			Experiment: "A", RunID: "mock-probe", OutDir: t.TempDir(),
			ProviderKind: "mock", ModelName: "mock-003f", Trials: 1, TrialRunner: runner,
			Conditions: []string{"no_memory"}, Scenarios: []string{"fixture_probe", "bash_probe"},
		})
		if err != nil {
			t.Fatalf("probe run: %v", err)
		}
		for _, scenario := range []string{"fixture_probe", "bash_probe"} {
			trial := findTrial(t, probe, "no_memory", scenario)
			if trial.Status != "pass" || !trial.Score.AbstainOK {
				t.Fatalf("%s trial did not abstain: status=%s score=%+v", scenario, trial.Status, trial.Score)
			}
			records := readRawRecords(t, trial.RawPath)
			if strings.Contains(rawText(records), "coding-canary-7f3") {
				t.Fatalf("%s: fixture canary entered the prompt through a tool", scenario)
			}
			foundError := false
			for _, call := range trial.ToolCalls {
				if call.IsError {
					foundError = true
				}
			}
			if !foundError {
				t.Fatalf("%s: outside-workspace fixture access was not denied", scenario)
			}
		}

		mpmProbe, err := RunExperiment(RunOptions{
			Experiment: "A", RunID: "mock-mpm-probe", OutDir: t.TempDir(),
			ProviderKind: "mock", ModelName: "mock-003f", MPMBinary: DefaultMPMBinary(),
			Trials: 1, TrialRunner: runner, Conditions: []string{"mpm_active"},
			Scenarios: []string{"bash_mpm_probe"},
		})
		if err != nil {
			t.Fatalf("MPM probe run: %v", err)
		}
		mpmTrial := findTrial(t, mpmProbe, "mpm_active", "bash_mpm_probe")
		if mpmTrial.Status != "pass" || !mpmTrial.Score.AbstainOK {
			t.Fatalf("MPM shell probe did not abstain: status=%s score=%+v", mpmTrial.Status, mpmTrial.Score)
		}
		for _, call := range mpmTrial.ToolCalls {
			if !call.IsError && call.OutcomeLen > 0 {
				_ = call
			}
		}
		mpmText := rawText(readRawRecords(t, mpmTrial.RawPath))
		if strings.Contains(mpmText, ".sqlite") || strings.Contains(mpmText, "Atlas project vault access phrase:") {
			t.Fatal("MPM store leaked through shell execution")
		}
	})

	t.Run("10_contaminated_trial_fails_preflight", func(t *testing.T) {
		scenario, err := Scenario("recall")
		if err != nil {
			t.Fatal(err)
		}
		runDir := t.TempDir()
		opts := RunOptions{
			Experiment: "A", RunID: "contaminated", ProviderKind: "mock",
			ModelName: "mock-003f", MPMBinary: DefaultMPMBinary(), Trials: 1,
		}
		spec, err := buildTrialSpec(opts, runDir, "no_memory", scenario, "contaminated-no-memory-recall-t01", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := PrepareTrial(&spec); err != nil {
			t.Fatal(err)
		}
		sessionDir := filepath.Join(spec.AgentWorkspace, "sessions")
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			t.Fatal(err)
		}
		contaminated := `{"role":"assistant","content":"` + AtlasAnswer + `"}` + "\n"
		if err := os.WriteFile(filepath.Join(sessionDir, "leftover.jsonl"), []byte(contaminated), 0o600); err != nil {
			t.Fatal(err)
		}
		result, _ := runTrialSubprocess(runDir, spec, runner, "mock")
		if result.Status != "fail" {
			t.Fatalf("contaminated trial status = %s (%s)", result.Status, result.Error)
		}
		if !strings.Contains(result.Error, "no_pre_existing_session") {
			t.Fatalf("contaminated trial failed for the wrong reason: %s", result.Error)
		}
		if len(result.Requests) != 0 {
			t.Fatalf("contaminated trial still sent %d provider requests", len(result.Requests))
		}
	})
}

func checkPassed(checks []Check, name string) bool {
	for _, check := range checks {
		if check.Name == name {
			return check.Passed
		}
	}
	return false
}
