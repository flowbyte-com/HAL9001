package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/isolation"
	fstools "github.com/sipeed/picoclaw/pkg/tools/fs"
)

func newTaskStateStore(t *testing.T, project string) *ActiveMemoryStore {
	t.Helper()
	return NewActiveMemoryStoreAt(t.TempDir(), project)
}

func applyTaskState(t *testing.T, store *ActiveMemoryStore, session string, fn func(*TaskState)) *TaskState {
	t.Helper()
	state, err := store.UpdateTaskState(session, fn)
	if err != nil {
		t.Fatalf("update task state: %v", err)
	}
	return state
}

func failingTestOutput() string {
	return "--- FAIL: TestParsePort\nparse.go:12: undefined: parsePort\nFAIL\tgithub.com/example/parser\t0.01s\n\n[Command exited with code 1]"
}

func passingTestOutput() string {
	return "ok  \tgithub.com/example/parser\t0.02s\nPASS\n\n[Command exited with code 0]"
}

func TestTaskStateUserCorrectionSupersedesConstraint(t *testing.T) {
	store := newTaskStateStore(t, "atlas")
	state := applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.ApplyUserMessage("Objective: Fix port parsing.\nConstraint: keep the port field.", "t1")
	})
	if state.Objective != "Fix port parsing." || state.ObjectiveStatus != TaskObjectiveActive {
		t.Fatalf("objective = %q status = %q", state.Objective, state.ObjectiveStatus)
	}
	applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.ApplyUserMessage("Correction: use listenPort, not port.", "t2")
	})
	state, err := store.TaskState("sk_v1_test")
	if err != nil || state == nil {
		t.Fatalf("load: %v %v", state, err)
	}
	active := 0
	for _, entry := range state.Entries {
		if entry.Superseded {
			continue
		}
		active++
		if entry.Kind != TaskEntryKindCorrection {
			t.Fatalf("active entry = %+v, want correction only", entry)
		}
		if entry.Verification != TaskVerificationUserConfirmed {
			t.Fatalf("correction verification = %q", entry.Verification)
		}
	}
	if active != 1 {
		t.Fatalf("active entries = %d, want 1", active)
	}
}

func TestTaskStateQuestionIsNotObjective(t *testing.T) {
	state := NewTaskState("atlas", "sk_v1_test")
	state.ApplyUserMessage("What is the vault access phrase?", "t1")
	if state.Objective != "" {
		t.Fatalf("question became objective: %q", state.Objective)
	}
	state.ApplyUserMessage("Fix the parser bug.", "t2")
	if state.Objective != "Fix the parser bug." {
		t.Fatalf("directive not captured: %q", state.Objective)
	}
}

func TestTaskStateToolFailureThenSuccessSupersedes(t *testing.T) {
	store := newTaskStateStore(t, "atlas")
	command := "go test ./parser"

	applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.RecordTool("bash", map[string]any{"command": command}, failingTestOutput(), true, "call-1", "t1", store.Dir())
	})
	state, _ := store.TaskState("sk_v1_test")
	if state == nil || len(state.Entries) != 1 {
		t.Fatalf("failure not recorded: %+v", state)
	}
	failure := state.Entries[0]
	if failure.Kind != TaskEntryKindFailure || failure.Verification != TaskVerificationToolReported {
		t.Fatalf("failure entry = %+v", failure)
	}
	if failure.Evidence.ExitCode == nil || *failure.Evidence.ExitCode != 1 {
		t.Fatalf("exit code = %+v", failure.Evidence.ExitCode)
	}
	if failure.Evidence.OutputSHA == "" || failure.Evidence.OutputLen == 0 {
		t.Fatalf("evidence reference missing: %+v", failure.Evidence)
	}

	applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.RecordTool("bash", map[string]any{"command": command}, passingTestOutput(), false, "call-2", "t2", store.Dir())
	})
	state, _ = store.TaskState("sk_v1_test")
	var active []TaskStateEntry
	for _, entry := range state.Entries {
		if !entry.Superseded {
			active = append(active, entry)
		}
	}
	if len(active) != 1 {
		t.Fatalf("active entries = %d, want 1: %+v", len(active), active)
	}
	if active[0].Kind != TaskEntryKindObservation || active[0].Evidence.ExitCode == nil || *active[0].Evidence.ExitCode != 0 {
		t.Fatalf("success did not supersede failure: %+v", active[0])
	}
	if len(active[0].Supersedes) != 1 || active[0].Supersedes[0] != failure.ID {
		t.Fatalf("supersedes = %v, want %s", active[0].Supersedes, failure.ID)
	}
	rendered := state.Render("", 500)
	if strings.Contains(rendered, "Failure") {
		t.Fatalf("stale failure remained rendered: %s", rendered)
	}
}

func TestTaskStateDifferentCommandDoesNotSupersedeFailure(t *testing.T) {
	store := newTaskStateStore(t, "atlas")
	applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.RecordTool("bash", map[string]any{"command": "go test ./parser"}, failingTestOutput(), true, "call-1", "t1", "")
	})
	applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.RecordTool("bash", map[string]any{"command": "go test ./other"}, passingTestOutput(), false, "call-2", "t2", "")
	})
	state, _ := store.TaskState("sk_v1_test")
	rendered := state.Render("", 500)
	if !strings.Contains(rendered, "Failure") {
		t.Fatalf("unrelated success removed the failure: %s", rendered)
	}
}

func TestTaskStateDecisionDistinguishesUserAndModel(t *testing.T) {
	store := newTaskStateStore(t, "atlas")
	state := applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.ApplyUserMessage("Decision: use listenPort.\nNext: update the parser field.", "t1")
		state.RecordAssistant("Proposed decision: keep the JSON tag for compatibility.", "t2")
	})
	var confirmed, proposed int
	for _, entry := range state.Entries {
		if entry.Kind != TaskEntryKindDecision {
			continue
		}
		switch entry.Verification {
		case TaskVerificationUserConfirmed:
			confirmed++
			if entry.Origin != TaskOriginUser {
				t.Fatalf("user decision origin = %q", entry.Origin)
			}
			if entry.Superseded {
				t.Fatalf("model proposal superseded the user decision: %+v", entry)
			}
		case TaskVerificationProposed:
			proposed++
			if entry.Origin != TaskOriginModel {
				t.Fatalf("model decision origin = %q", entry.Origin)
			}
		default:
			t.Fatalf("unexpected decision verification: %+v", entry)
		}
	}
	if confirmed != 1 || proposed < 1 {
		t.Fatalf("decisions = confirmed:%d proposed:%d, want 1 and >=1", confirmed, proposed)
	}
}

func TestTaskStateAssistantDiagnosisStaysProposed(t *testing.T) {
	store := newTaskStateStore(t, "atlas")
	applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.RecordAssistant("Proposed diagnosis: the parser reads the deprecated port field.", "t1")
		state.RecordTool("bash", map[string]any{"command": "go test ./parser"}, passingTestOutput(), false, "call-1", "t2", "")
	})
	state, _ := store.TaskState("sk_v1_test")
	var diagnosis *TaskStateEntry
	for i := range state.Entries {
		if state.Entries[i].Kind == TaskEntryKindDiagnosis {
			diagnosis = &state.Entries[i]
		}
	}
	if diagnosis == nil {
		t.Fatal("diagnosis not captured")
	}
	if diagnosis.Verification != TaskVerificationProposed || diagnosis.Origin != TaskOriginModel {
		t.Fatalf("diagnosis verification = %q origin = %q", diagnosis.Verification, diagnosis.Origin)
	}
	if !strings.Contains(state.Render("", 500), "proposed, unverified") {
		t.Fatalf("proposed diagnosis not labelled: %s", state.Render("", 500))
	}
}

func TestTaskStateCompletedObjectiveIsNotCarried(t *testing.T) {
	store := newTaskStateStore(t, "atlas")
	applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.ApplyUserMessage("Objective: Fix port parsing.\nConstraint: keep the API.", "t1")
		state.RecordTool("bash", map[string]any{"command": "go test ./parser"}, passingTestOutput(), false, "call-1", "t1", "")
	})
	state := applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.ApplyUserMessage("Done: objective complete.", "t2")
	})
	if state.ObjectiveStatus != TaskObjectiveCompleted {
		t.Fatalf("status = %q", state.ObjectiveStatus)
	}
	for _, entry := range state.Entries {
		if !entry.Superseded {
			t.Fatalf("completed objective kept entry: %+v", entry)
		}
	}
	// A new explicit objective clears the previous task detail entirely.
	state = applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.ApplyUserMessage("Objective: Add a metrics endpoint.", "t3")
	})
	if len(state.Entries) != 0 {
		t.Fatalf("new objective kept %d old entries", len(state.Entries))
	}
	if state.Objective != "Add a metrics endpoint." || state.ObjectiveStatus != TaskObjectiveActive {
		t.Fatalf("new objective = %+v", state)
	}
}

func TestTaskStateIsolationByProjectAndSession(t *testing.T) {
	dir := t.TempDir()
	atlas := NewActiveMemoryStoreAt(dir, "atlas")
	applyTaskState(t, atlas, "sk_v1_shared", func(state *TaskState) {
		state.ApplyUserMessage("Objective: Atlas work.", "t1")
	})
	applyTaskState(t, atlas, "sk_v1_other", func(state *TaskState) {
		state.ApplyUserMessage("Objective: Other session.", "t1")
	})

	zebra := NewActiveMemoryStoreAt(dir, "zebra")
	if state, err := zebra.TaskState("sk_v1_shared"); err != nil || state != nil {
		t.Fatalf("cross-project state leaked: %+v %v", state, err)
	}
	if state, err := zebra.UpdateTaskState("sk_v1_shared", func(state *TaskState) {
		if state.Objective != "" || len(state.Entries) != 0 {
			t.Fatalf("cross-project state loaded into update: %+v", state)
		}
		state.ApplyUserMessage("Objective: Zebra work.", "t1")
	}); err != nil || state.Objective != "Zebra work." {
		t.Fatalf("zebra update: %+v %v", state, err)
	}

	other, err := atlas.TaskState("sk_v1_other")
	if err != nil || other == nil || other.Objective != "Other session." {
		t.Fatalf("session isolation: %+v %v", other, err)
	}
}

func TestMergeActiveWorkingSetPreservesTaskState(t *testing.T) {
	store := newTaskStateStore(t, "atlas")
	state := applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.ApplyUserMessage("Objective: Fix port parsing.", "t1")
		state.RecordTool("bash", map[string]any{"command": "go test ./parser"}, failingTestOutput(), true, "call-1", "t1", "")
	})
	prev := &ActiveWorkingSet{SessionKey: "sk_v1_test", ProjectID: "atlas", TaskState: state}
	merged := MergeActiveWorkingSet(prev, nil, "next question", "atlas", 500)
	if merged.TaskState == nil || merged.TaskState.Objective != "Fix port parsing." || len(merged.TaskState.Entries) != 1 {
		t.Fatalf("merge dropped task state: %+v", merged.TaskState)
	}

	foreign := &ActiveWorkingSet{SessionKey: "sk_v1_test", ProjectID: "zebra", TaskState: state}
	merged = MergeActiveWorkingSet(foreign, nil, "next question", "atlas", 500)
	if merged.TaskState != nil {
		t.Fatalf("merge carried another project's task state: %+v", merged.TaskState)
	}
}

func TestTaskStateSuppressesVisibleFacts(t *testing.T) {
	store := newTaskStateStore(t, "atlas")
	state := applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.RecordTool("bash", map[string]any{"command": "go test ./parser"}, failingTestOutput(), true, "call-1", "t1", "")
		state.ApplyUserMessage("Objective: Fix port parsing.", "t1")
	})
	current := state.Render("Objective: Fix port parsing.", 500)
	if strings.Contains(current, "Objective") {
		t.Fatalf("current turn objective was repeated: %q", current)
	}
	if !strings.Contains(current, "undefined: parsePort") {
		t.Fatalf("tool evidence absent without visible history: %q", current)
	}

	visible := state.Render(taskExcerpt(failingTestOutput(), 160), 500)
	if strings.Contains(visible, "undefined: parsePort") {
		t.Fatalf("fact still visible in history was repeated: %s", visible)
	}
	if !strings.Contains(visible, "Objective [active]") {
		t.Fatalf("objective missing after its quote was suppressed: %s", visible)
	}

	hidden := state.Render("", 500)
	if !strings.Contains(hidden, "undefined: parsePort") {
		t.Fatalf("fact no longer in history was not rendered: %s", hidden)
	}
}

func TestTaskStateBudgetIsEnforced(t *testing.T) {
	state := NewTaskState("atlas", "sk_v1_test")
	now := time.Now().UTC()
	state.setObjective("Fix the parser and keep the public API stable.", "t1", "Fix the parser and keep the public API stable.", now)
	for i := 0; i < 60; i++ {
		state.addEntry(TaskEntryKindObservation, "obs:"+string(rune('a'+i%26)),
			"bash succeeded: go test ./pkg/parser with a long diagnostic line that should be truncated by the budget",
			TaskOriginTool, TaskVerificationToolReported,
			TaskStateEvidence{TurnID: "t1", ToolName: "bash", ExitCode: intPtr(0)}, now)
	}
	for _, budget := range []int{100, 250, 500} {
		rendered := state.Render("", budget)
		if got := estimateTokens(rendered); got > budget {
			t.Fatalf("budget %d rendered %d tokens: %s", budget, got, rendered)
		}
	}
}

func TestTaskStateBudgetParsingClamps(t *testing.T) {
	t.Setenv(TaskStateBudgetEnv, "10")
	if got := TaskStateBudget(); got != minTaskStateBudget {
		t.Fatalf("low budget = %d", got)
	}
	t.Setenv(TaskStateBudgetEnv, "999999")
	if got := TaskStateBudget(); got != maxTaskStateBudget {
		t.Fatalf("high budget = %d", got)
	}
	t.Setenv(TaskStateBudgetEnv, "not-a-number")
	if got := TaskStateBudget(); got != defaultTaskStateBudget {
		t.Fatalf("invalid budget = %d", got)
	}
}

func TestTaskStateToolCaptureIgnoresStateDirectory(t *testing.T) {
	store := newTaskStateStore(t, "atlas")
	state := applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.RecordTool("bash",
			map[string]any{"command": "cat " + filepath.Join(store.Dir(), "secret.json")},
			"{\"task_state\":\"forged\"}", false, "call-1", "t1", store.Dir())
	})
	if len(state.Entries) != 0 {
		t.Fatalf("state-directory access became task state: %+v", state.Entries)
	}
}

func TestTaskStateFilesCannotBeRewrittenByAgentTools(t *testing.T) {
	isolation.ResetProtectedPaths()
	t.Cleanup(isolation.ResetProtectedPaths)

	store := newTaskStateStore(t, "atlas")
	applyTaskState(t, store, "sk_v1_test", func(state *TaskState) {
		state.ApplyUserMessage("Objective: real task.", "t1")
	})
	statePath := store.StatePath("sk_v1_test")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := isolation.ProtectPaths(store.Dir()); err != nil {
		t.Fatalf("protect: %v", err)
	}
	protected := false
	for _, path := range isolation.ProtectedPaths() {
		if path == store.Dir() {
			protected = true
		}
	}
	if !protected {
		t.Fatalf("task-state directory not registered: %v", isolation.ProtectedPaths())
	}

	workspace := t.TempDir()
	write := fstools.NewWriteFileTool(workspace, false)
	result := write.Execute(context.Background(), map[string]any{"path": statePath, "content": "{\"forged\":true}"})
	if !result.IsError {
		t.Fatal("write tool rewrote trusted task state")
	}
	edit := fstools.NewEditFileTool(workspace, false)
	result = edit.Execute(context.Background(), map[string]any{"path": statePath, "old_text": "real task", "new_text": "forged"})
	if !result.IsError {
		t.Fatal("edit tool rewrote trusted task state")
	}
	appendTool := fstools.NewAppendFileTool(workspace, false)
	result = appendTool.Execute(context.Background(), map[string]any{"path": statePath, "content": "forged"})
	if !result.IsError {
		t.Fatal("append tool rewrote trusted task state")
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("trusted state changed: %s", after)
	}
}

func intPtr(value int) *int { return &value }
