package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runExperimentC(t *testing.T, runID, scenario string, conditions ...string) *RunSummary {
	t.Helper()
	if testing.Short() {
		t.Skip("experiment C runs isolated subprocess trials")
	}
	summary, err := RunExperiment(RunOptions{
		Experiment: "C", RunID: runID, OutDir: t.TempDir(),
		ProviderKind: "mock", ModelName: "mock-003f",
		MPMBinary: DefaultMPMBinary(), Trials: 1, TrialRunner: testRunner(t),
		Conditions: conditions, Scenarios: []string{scenario},
	})
	if err != nil {
		t.Fatalf("run experiment C: %v", err)
	}
	return summary
}

func requirePassingTrial(t *testing.T, trial TrialResult) {
	t.Helper()
	if trial.Status != "pass" {
		t.Fatalf("%s status = %s: %s", trial.Condition, trial.Status, trial.Error)
	}
	if !trial.Score.Correct {
		t.Fatalf("%s turn scores failed: %+v", trial.Condition, trial.TurnScores)
	}
}

func turnTokens(t *testing.T, trial TrialResult, turnID string) int {
	t.Helper()
	turn := turnResultByID(trial.Turns, turnID)
	if turn == nil {
		t.Fatalf("%s missing turn %s", trial.Condition, turnID)
	}
	return turn.TaskStatePromptTokens
}

// TestTaskStateResidencyExperiment compares the four residency baselines on the
// 004A regression scenario. Only the boundary and always conditions enable task
// state; both must recover the compacted failure.
func TestTaskStateResidencyExperiment(t *testing.T) {
	summary := runExperimentC(t, "residency-c", "task_state_script",
		"history_cache", "always_state", "boundary_state", "summary_only")
	control := findTrial(t, summary, "history_cache", "task_state_script")
	always := findTrial(t, summary, "always_state", "task_state_script")
	boundary := findTrial(t, summary, "boundary_state", "task_state_script")
	summaryOnly := findTrial(t, summary, "summary_only", "task_state_script")
	for _, trial := range []TrialResult{control, always, boundary, summaryOnly} {
		requirePassingTrial(t, trial)
	}

	if always.Settings.TaskStateResidency != "always" || boundary.Settings.TaskStateResidency != "boundary" {
		t.Fatalf("residency settings: always=%q boundary=%q",
			always.Settings.TaskStateResidency, boundary.Settings.TaskStateResidency)
	}
	if control.Settings.TaskState || summaryOnly.Settings.TaskState {
		t.Fatal("a no-state condition enabled task state")
	}
	if control.TaskStateFinal != nil || summaryOnly.TaskStateFinal != nil {
		t.Fatal("a no-state condition produced a task-state sidecar")
	}

	for _, trial := range []TrialResult{control, summaryOnly} {
		turn := turnResultByID(trial.Turns, "t5")
		if turn == nil || strings.Contains(turn.Response, TaskStateDiagnostic) {
			t.Fatalf("%s unexpectedly retained the compacted failure: %+v", trial.Condition, turn)
		}
	}
	for _, trial := range []TrialResult{always, boundary} {
		turn := turnResultByID(trial.Turns, "t5")
		if turn == nil || !strings.Contains(turn.Response, TaskStateDiagnostic) {
			t.Fatalf("%s lost the compacted failure: %+v", trial.Condition, turn)
		}
	}

	// 004A rendered the still-visible failure at t2/t3 because the stored quote
	// was whitespace-normalized while history was not. Boundary residency must
	// remove that duplicate without losing the later, genuinely hidden fact.
	for _, turnID := range []string{"t2", "t3"} {
		if got := turnTokens(t, boundary, turnID); got != 0 {
			t.Fatalf("boundary %s rendered %d tokens of duplicate state", turnID, got)
		}
		if got := turnTokens(t, always, turnID); got == 0 {
			t.Fatalf("always %s unexpectedly suppressed the 004A duplicate", turnID)
		}
	}
	if turnTokens(t, boundary, "t4") == 0 || turnTokens(t, boundary, "t5") == 0 {
		t.Fatal("boundary state disappeared after compaction")
	}
	if turnTokens(t, boundary, "t7") >= turnTokens(t, always, "t7") {
		t.Fatalf("boundary t7 (%d) did not reduce always t7 (%d)",
			turnTokens(t, boundary, "t7"), turnTokens(t, always, "t7"))
	}
	if strings.Contains(turnResultByID(boundary.Turns, "t7").TaskStatePromptText, TaskStateDiagnostic) {
		t.Fatal("boundary t7 repeated the superseded failure")
	}
	if boundary.Totals.TaskStatePromptTokens >= always.Totals.TaskStatePromptTokens {
		t.Fatalf("boundary residency (%d) did not beat always residency (%d)",
			boundary.Totals.TaskStatePromptTokens, always.Totals.TaskStatePromptTokens)
	}
	if boundary.Totals.PromptTokens >= always.Totals.PromptTokens {
		t.Fatalf("boundary total prompt (%d) did not beat always (%d)",
			boundary.Totals.PromptTokens, always.Totals.PromptTokens)
	}
	if !snapshotHasKind(boundary.TaskStateFinal, "correction") ||
		!snapshotHasKind(boundary.TaskStateFinal, "diagnosis") ||
		snapshotHasKind(boundary.TaskStateFinal, "failure") {
		t.Fatalf("boundary final state = %+v", boundary.TaskStateFinal)
	}
	if !strings.Contains(boundary.TaskStateFinal.RenderedText, "proposed, unverified") {
		t.Fatal("boundary final state lost the proposed verification label")
	}

	controlRaw := rawText(readRawRecords(t, control.RawPath))
	if strings.Contains(controlRaw, "source: task.state") {
		t.Fatal("control prompt contained a task-state part")
	}
	boundaryFollowUp := rawPromptForQuestion(t, boundary.RawPath, "We restarted the process")
	if !strings.Contains(boundaryFollowUp, "source: task.state") ||
		!strings.Contains(boundaryFollowUp, TaskStateDiagnostic) {
		t.Fatalf("boundary follow-up did not carry attributed evidence: %s", boundaryFollowUp)
	}
	boundaryT2 := rawPromptForQuestion(t, boundary.RawPath, "Correction: use listenPort")
	if strings.Contains(boundaryT2, "source: task.state") {
		t.Fatalf("boundary repeated state still visible in history:\n%s", boundaryT2)
	}

	// The last t6 request is assembled after the successful rerun. Its task
	// state must reflect that result; the earlier failure may remain only as
	// superseded history and must not appear as an unresolved claim.
	var t6 []RawRecord
	for _, record := range readRawRecords(t, boundary.RawPath) {
		if record.TurnID == "t6" {
			t6 = append(t6, record)
		}
	}
	if len(t6) < 2 {
		t.Fatalf("t6 requests = %d, want at least 2", len(t6))
	}
	lastT6 := t6[len(t6)-1]
	lastState := rawTaskStateBlock(lastT6)
	if strings.Contains(lastState, TaskStateDiagnostic) {
		t.Fatalf("last t6 task-state block still presented the resolved failure as active:\n%s", lastState)
	}
	lastT6Text := rawRecordText(lastT6)
	if !strings.Contains(lastT6Text, "ok  \tgithub.com/example/parser\t0.02s") &&
		!strings.Contains(lastT6Text, "ok github.com/example/parser 0.02s") {
		t.Fatalf("last t6 request did not contain the successful tool result:\n%s", lastT6Text)
	}
	if got := strings.Count(lastT6Text, "source: task.state"); got != 1 {
		t.Fatalf("last t6 request has %d task-state parts, want 1:\n%s", got, lastT6Text)
	}

	spec := trialSpec(t, boundary)
	for _, root := range []string{spec.MPMWorkspace, spec.MPMHome} {
		if found := filesContaining(t, root, TaskStateDiagnostic); len(found) > 0 {
			t.Fatalf("task observation was written into MPM state: %v", found)
		}
	}
}

// TestTaskStateMultiFailureExperiment covers two simultaneous failures and a
// rerun that supersedes only the matching one, followed by a new objective.
func TestTaskStateMultiFailureExperiment(t *testing.T) {
	summary := runExperimentC(t, "multi-failure-c", "task_state_multi_failure",
		"history_cache", "always_state", "boundary_state")
	control := findTrial(t, summary, "history_cache", "task_state_multi_failure")
	always := findTrial(t, summary, "always_state", "task_state_multi_failure")
	boundary := findTrial(t, summary, "boundary_state", "task_state_multi_failure")
	for _, trial := range []TrialResult{control, always, boundary} {
		requirePassingTrial(t, trial)
	}

	controlAnswer := turnResultByID(control.Turns, "t3").Response
	if strings.Contains(controlAnswer, TaskStateLintDiagnostic) {
		t.Fatalf("control recovered a cleared failure: %s", controlAnswer)
	}
	for _, trial := range []TrialResult{always, boundary} {
		answer := turnResultByID(trial.Turns, "t3").Response
		if !strings.Contains(answer, TaskStateLintDiagnostic) || strings.Contains(answer, TaskStateDiagnostic) {
			t.Fatalf("%s t3 answer = %q", trial.Condition, answer)
		}
		turn := turnResultByID(trial.Turns, "t3")
		if turn.TaskState == nil || !strings.Contains(turn.TaskState.RenderedText, TaskStateLintDiagnostic) {
			t.Fatalf("%s lost the unrelated lint failure", trial.Condition)
		}
		failures := 0
		for _, kind := range turn.TaskState.ActiveKinds {
			if kind == "failure" {
				failures++
			}
		}
		if failures != 1 {
			t.Fatalf("%s active failures = %d, want 1", trial.Condition, failures)
		}
		if trial.TaskStateFinal == nil || snapshotHasKind(trial.TaskStateFinal, "failure") ||
			!strings.Contains(trial.TaskStateFinal.Objective, "metrics endpoint") {
			t.Fatalf("%s new objective did not clear state: %+v", trial.Condition, trial.TaskStateFinal)
		}
	}
	if boundary.Totals.TaskStatePromptTokens >= always.Totals.TaskStatePromptTokens {
		t.Fatalf("boundary residency (%d) did not beat always (%d)",
			boundary.Totals.TaskStatePromptTokens, always.Totals.TaskStatePromptTokens)
	}
}

// TestTaskStateSummarySourceExperiment shows a native compaction summary that
// preserves a sourced diagnostic is sufficient without structured state, and
// that boundary rendering does not repeat it.
func TestTaskStateSummarySourceExperiment(t *testing.T) {
	summary := runExperimentC(t, "summary-source-c", "task_state_summary_source",
		"summary_only", "always_state", "boundary_state")
	summaryOnly := findTrial(t, summary, "summary_only", "task_state_summary_source")
	always := findTrial(t, summary, "always_state", "task_state_summary_source")
	boundary := findTrial(t, summary, "boundary_state", "task_state_summary_source")
	for _, trial := range []TrialResult{summaryOnly, always, boundary} {
		requirePassingTrial(t, trial)
	}

	answer := turnResultByID(summaryOnly.Turns, "t4").Response
	if !strings.Contains(answer, TaskStateDiagnostic) {
		t.Fatalf("summary-only control did not use the sourced summary: %q", answer)
	}
	raw := rawText(readRawRecords(t, summaryOnly.RawPath))
	if !strings.Contains(raw, "Source: bash sh parser_check.sh") ||
		!strings.Contains(raw, TaskStateDiagnostic) {
		t.Fatal("the native summary did not preserve the sourced diagnostic")
	}

	alwaysTurn := turnResultByID(always.Turns, "t4")
	boundaryTurn := turnResultByID(boundary.Turns, "t4")
	if !strings.Contains(alwaysTurn.TaskStatePromptText, TaskStateDiagnostic) {
		t.Fatal("always residency did not repeat the summary-sourced finding")
	}
	if strings.Contains(boundaryTurn.TaskStatePromptText, TaskStateDiagnostic) {
		t.Fatalf("boundary residency repeated the summary-sourced finding:\n%s", boundaryTurn.TaskStatePromptText)
	}
	if boundary.Totals.TaskStatePromptTokens >= always.Totals.TaskStatePromptTokens {
		t.Fatalf("boundary residency (%d) did not beat always (%d)",
			boundary.Totals.TaskStatePromptTokens, always.Totals.TaskStatePromptTokens)
	}
	if boundary.TaskStateFinal == nil || !snapshotHasKind(boundary.TaskStateFinal, "failure") {
		t.Fatal("boundary deleted the stored failure instead of only suppressing its rendering")
	}
}

// rawPromptForQuestion returns the messages of the first request whose last
// user message contains the question. Retrieved context is attached to that
// message, so this selects the follow-up turn rather than later history.
func rawPromptForQuestion(t *testing.T, rawPath, question string) string {
	t.Helper()
	var out strings.Builder
	for _, record := range readRawRecords(t, rawPath) {
		matched := false
		for i := len(record.Messages) - 1; i >= 0; i-- {
			if record.Messages[i].Role != "user" {
				continue
			}
			matched = strings.Contains(record.Messages[i].Content, question)
			break
		}
		if !matched {
			continue
		}
		for _, message := range record.Messages {
			out.WriteString(message.Content)
			out.WriteByte('\n')
		}
		return out.String()
	}
	t.Fatalf("no request carried question %q in %s", question, rawPath)
	return ""
}

func filesContaining(t *testing.T, root, needle string) []string {
	t.Helper()
	if strings.TrimSpace(root) == "" {
		return nil
	}
	var found []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || info.Size() > 2<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(data), needle) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	return found
}

func rawRecordText(record RawRecord) string {
	var b strings.Builder
	for _, message := range record.Messages {
		b.WriteString(message.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func rawTaskStateBlock(record RawRecord) string {
	const marker = "source: task.state):"
	for _, message := range record.Messages {
		index := strings.Index(message.Content, marker)
		if index < 0 {
			continue
		}
		block := message.Content[index+len(marker):]
		if end := strings.Index(block, "\n\nQuestion: "); end >= 0 {
			block = block[:end]
		}
		return block
	}
	return ""
}
