package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTaskStateExperimentC is the deterministic 004A mechanism test: identical
// scripted turns and tools, with task state as the only difference. The mock
// provider answers from the exact prompt it receives, so a retained fact can
// only come from the prompt.
func TestTaskStateExperimentC(t *testing.T) {
	if testing.Short() {
		t.Skip("experiment C runs isolated subprocess trials")
	}
	summary, err := RunExperiment(RunOptions{
		Experiment: "C", RunID: "task-state-c", OutDir: t.TempDir(),
		ProviderKind: "mock", ModelName: "mock-003f",
		MPMBinary: DefaultMPMBinary(), Trials: 1, TrialRunner: testRunner(t),
	})
	if err != nil {
		t.Fatalf("run experiment C: %v", err)
	}
	control := findTrial(t, summary, "history_cache", "task_state_script")
	treatment := findTrial(t, summary, "task_state", "task_state_script")
	for _, trial := range []TrialResult{control, treatment} {
		if trial.Status != "pass" {
			t.Fatalf("%s status = %s: %s", trial.Condition, trial.Status, trial.Error)
		}
		if !trial.Score.Correct {
			t.Fatalf("%s turn scores failed: %+v", trial.Condition, trial.TurnScores)
		}
	}
	if control.Settings.TaskState {
		t.Fatal("control enabled task state")
	}
	if !treatment.Settings.TaskState {
		t.Fatal("treatment did not enable task state")
	}
	if strings.Join(control.Settings.ToolNames, ",") != strings.Join(treatment.Settings.ToolNames, ",") {
		t.Fatalf("tool profiles differ: %v vs %v", control.Settings.ToolNames, treatment.Settings.ToolNames)
	}
	if control.Totals.LLMRequests != treatment.Totals.LLMRequests {
		t.Fatalf("request counts differ: %d vs %d", control.Totals.LLMRequests, treatment.Totals.LLMRequests)
	}

	if control.TaskStateFinal != nil {
		t.Fatalf("control produced a task-state sidecar: %+v", control.TaskStateFinal)
	}
	final := treatment.TaskStateFinal
	if final == nil || !final.Available {
		t.Fatal("treatment produced no task-state sidecar")
	}
	if snapshotHasKind(final, "failure") {
		t.Fatalf("successful rerun did not supersede the failure: %s", final.RenderedText)
	}
	if !snapshotHasKind(final, "observation") {
		t.Fatalf("successful observation missing: %s", final.RenderedText)
	}
	if final.RenderedTokens > 500 {
		t.Fatalf("rendered task state exceeds budget: %d tokens", final.RenderedTokens)
	}
	if !strings.Contains(final.RenderedText, TaskStateCorrection) {
		t.Fatalf("correction missing from final state: %s", final.RenderedText)
	}
	if !strings.Contains(final.RenderedText, "proposed, unverified") {
		t.Fatalf("proposed diagnosis lost its verification label: %s", final.RenderedText)
	}

	controlTurn := turnResultByID(control.Turns, "t5")
	treatmentTurn := turnResultByID(treatment.Turns, "t5")
	if controlTurn == nil || treatmentTurn == nil {
		t.Fatal("post-compaction turn missing")
	}
	if strings.Contains(controlTurn.Response, TaskStateDiagnostic) {
		t.Fatalf("control retained the compacted failure: %s", controlTurn.Response)
	}
	if !strings.Contains(treatmentTurn.Response, TaskStateDiagnostic) {
		t.Fatalf("treatment did not retain the compacted failure: %s", treatmentTurn.Response)
	}
	if treatmentTurn.TaskState == nil || !snapshotHasKind(treatmentTurn.TaskState, "failure") {
		t.Fatalf("treatment sidecar lost the failure before the follow-up: %+v", treatmentTurn.TaskState)
	}

	controlRaw := rawText(readRawRecords(t, control.RawPath))
	treatmentRaw := rawText(readRawRecords(t, treatment.RawPath))
	if strings.Contains(controlRaw, "source: task.state") {
		t.Fatal("control prompt contained a task-state part")
	}
	if !strings.Contains(treatmentRaw, "source: task.state") {
		t.Fatal("treatment prompt contained no task-state part")
	}
	if !strings.Contains(treatmentRaw, "Objective [active]") ||
		!strings.Contains(treatmentRaw, "Failure [tool_reported]") {
		t.Fatal("treatment prompt did not attribute the retained evidence")
	}

	// The controlling fact must be absent from the control's ordinary history
	// at the post-compaction follow-up, not merely absent from its answer.
	controlFollowUp := rawPromptForQuestion(t, control.RawPath, "We restarted the process")
	if strings.Contains(controlFollowUp, TaskStateDiagnostic) {
		t.Fatalf("control history still contained the failure at the follow-up: %s", controlFollowUp)
	}
	treatmentFollowUp := rawPromptForQuestion(t, treatment.RawPath, "We restarted the process")
	if !strings.Contains(treatmentFollowUp, "source: task.state") ||
		!strings.Contains(treatmentFollowUp, TaskStateDiagnostic) {
		t.Fatalf("treatment follow-up did not carry attributed evidence: %s", treatmentFollowUp)
	}

	// Task state must never be promoted into MPM. The diagnostic may exist in
	// the protected sidecar, but not in the trial's MPM workspace or home.
	spec := trialSpec(t, treatment)
	for _, root := range []string{spec.MPMWorkspace, spec.MPMHome} {
		if found := filesContaining(t, root, TaskStateDiagnostic); len(found) > 0 {
			t.Fatalf("task observation was written into MPM state: %v", found)
		}
	}
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
