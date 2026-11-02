package bench

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
)

func taskStateSnapshot(instance *agent.AgentInstance, sessionKey string, budget int) *TaskStateSnapshot {
	if instance == nil || instance.ActiveMemory == nil || !instance.TaskStateEnabled {
		return nil
	}
	state, err := instance.ActiveMemory.TaskState(sessionKey)
	if err != nil || state == nil {
		return &TaskStateSnapshot{Available: false}
	}
	snapshot := &TaskStateSnapshot{
		Available:           true,
		Residency:           instance.TaskStateResidency,
		Entries:             len(state.Entries),
		Objective:           state.Objective,
		ObjectiveStatus:     state.ObjectiveStatus,
		ActiveKinds:         []string{},
		ActiveVerifications: []string{},
		ActiveKeys:          []string{},
		ActiveIDs:           []string{},
		ActiveQuotes:        []string{},
		ActiveTools:         []string{},
	}
	for _, entry := range state.Entries {
		if entry.Superseded {
			continue
		}
		snapshot.ActiveEntries++
		snapshot.ActiveKinds = append(snapshot.ActiveKinds, entry.Kind)
		snapshot.ActiveVerifications = append(snapshot.ActiveVerifications, entry.Verification)
		snapshot.ActiveKeys = append(snapshot.ActiveKeys, entry.Key)
		snapshot.ActiveIDs = append(snapshot.ActiveIDs, entry.ID)
		snapshot.ActiveQuotes = append(snapshot.ActiveQuotes, entry.Evidence.Quote)
		snapshot.ActiveTools = append(snapshot.ActiveTools, entry.Evidence.ToolName)
	}
	rendered := state.Render("", budget)
	snapshot.RenderedChars = len(rendered)
	snapshot.RenderedTokens = (len(rendered) + 3) / 4
	snapshot.RenderedText = rendered
	snapshot.StorageBytes = activeMemoryStorageBytes(instance.ActiveMemory.Dir())
	snapshot.ResidentRecords = residentCacheRecords(instance, sessionKey)
	return snapshot
}

// activeMemoryStorageBytes is the on-disk sidecar size for one project. It is
// task-state storage overhead, not prompt residency.
func activeMemoryStorageBytes(dir string) int64 {
	if strings.TrimSpace(dir) == "" {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// residentCacheRecords counts MPM resident records in the session sidecar. The
// 005A primary experiment disables MPM, so both conditions must report zero.
func residentCacheRecords(instance *agent.AgentInstance, sessionKey string) int {
	if instance == nil || instance.ActiveMemory == nil {
		return 0
	}
	set, err := instance.ActiveMemory.Load(sessionKey)
	if err != nil || set == nil {
		return 0
	}
	return len(set.Records)
}

func turnResultByID(turns []TurnResult, id string) *TurnResult {
	for i := range turns {
		if turns[i].TurnID == id {
			return &turns[i]
		}
	}
	return nil
}

func hasToolCall(calls []ToolCallRecord, turnID string, wantError bool, successOnly bool) bool {
	for _, call := range calls {
		if turnID != "" && call.TurnID != turnID {
			continue
		}
		if call.Tool != "bash" && call.Tool != "exec" {
			continue
		}
		if successOnly && call.IsError {
			continue
		}
		if call.IsError == wantError {
			return true
		}
	}
	return false
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func snapshotHasKind(snapshot *TaskStateSnapshot, kind string) bool {
	if snapshot == nil {
		return false
	}
	for _, got := range snapshot.ActiveKinds {
		if got == kind {
			return true
		}
	}
	return false
}

func snapshotHasVerification(snapshot *TaskStateSnapshot, verification string) bool {
	if snapshot == nil {
		return false
	}
	for _, got := range snapshot.ActiveVerifications {
		if got == verification {
			return true
		}
	}
	return false
}

// scoreTaskStateTrial records the deterministic per-turn expectations for
// Experiment C. The scenario and residency policy determine which observations
// are expected; the underlying tools and scripted evidence are identical.
func scoreTaskStateTrial(spec TrialSpec, result TrialResult) []TurnScore {
	switch spec.Scenario {
	case "task_state_multi_failure":
		return scoreMultiFailureTrial(spec, result)
	case "task_state_summary_source":
		return scoreSummarySourceTrial(spec, result)
	default:
		return scoreResidencyTrial(spec, result)
	}
}

func boundaryResidency(spec TrialSpec) bool {
	return spec.TaskState && spec.TaskStateResidency == agent.TaskStateResidencyBoundary
}

func scoreResidencyTrial(spec TrialSpec, result TrialResult) []TurnScore {
	treatment := spec.TaskState
	resident := treatment
	scores := make([]TurnScore, 0, len(result.Turns))
	for _, turn := range result.Turns {
		turn := turn
		score := TurnScore{TurnID: turn.TurnID, Response: turn.Response}
		add := func(name string, passed bool, detail string) {
			score.Checks = append(score.Checks, Check{Name: name, Passed: passed, Detail: detail})
		}
		snapshot := turn.TaskState

		switch turn.TurnID {
		case "t1":
			add("failing_test_executed", hasToolCall(turn.ToolCalls, "", true, false),
				"expected an erroring bash/exec call")
			if resident {
				add("failure_recorded", snapshotHasKind(snapshot, agent.TaskEntryKindFailure),
					"task state should record the tool-reported failure")
			}
		case "t2":
			if resident {
				add("correction_recorded",
					snapshotHasKind(snapshot, agent.TaskEntryKindCorrection) &&
						strings.Contains(snapshotText(snapshot), TaskStateCorrection),
					"task state should record the user correction")
			} else {
				add("correction_recorded", true, "control has no task state by design")
			}
			if treatment {
				if boundaryResidency(spec) {
					add("no_duplicate_residency", turn.TaskStatePromptTokens == 0,
						"boundary policy should not repeat a correction still in history")
				} else {
					add("always_resident_004a", turn.TaskStatePromptTokens > 0,
						"always policy should render the hidden tool observation")
				}
			}
		case "t3":
			if resident {
				add("diagnosis_proposed_unverified",
					snapshotHasKind(snapshot, agent.TaskEntryKindDiagnosis) &&
						snapshotHasVerification(snapshot, agent.TaskVerificationProposed) &&
						strings.Contains(snapshotText(snapshot), TaskStateDiagnosis),
					"assistant diagnosis must remain proposed")
			} else {
				add("diagnosis_proposed_unverified", true, "control has no task state by design")
			}
			if treatment {
				if boundaryResidency(spec) {
					add("no_duplicate_residency", turn.TaskStatePromptTokens == 0,
						"boundary policy should not repeat evidence still in history")
				} else {
					add("always_resident_004a", turn.TaskStatePromptTokens > 0,
						"always policy should render the hidden tool observation")
				}
			}
		case "t4":
			add("explicit_compaction_created_summary", turn.Compacted && turn.SummaryCharsAfter > 0,
				"explicit /compact should create a session summary")
			if treatment {
				add("post_boundary_residency", turn.TaskStatePromptTokens > 0,
					"task state should enter the prompt after its evidence leaves history")
			}
		case "t5":
			add("restart_resumed_same_session", turn.Restarted && turn.HistoryBefore > 0,
				"restart should resume the same session history")
			if treatment {
				retained := strings.Contains(turn.Response, TaskStateDiagnostic)
				add("post_compaction_fact_retained", retained,
					"task state should retain the verified failure across compaction and restart")
			} else {
				add("post_compaction_fact_absent", !strings.Contains(turn.Response, TaskStateDiagnostic),
					"control should lose the failure detail after compaction")
			}
		case "t6":
			add("successful_rerun_executed", hasToolCall(turn.ToolCalls, "", false, true),
				"expected a successful bash/exec call")
			if resident {
				add("later_success_supersedes_failure",
					snapshot != nil && !snapshotHasKind(snapshot, agent.TaskEntryKindFailure),
					"a successful matching rerun should supersede the earlier failure")
			}
		case "t7":
			answered := containsAny(strings.ToLower(turn.Response), "passes", "no,", "not failing")
			if resident {
				add("superseded_failure_not_reported", answered &&
					(result.TaskStateFinal == nil || !snapshotHasKind(result.TaskStateFinal, agent.TaskEntryKindFailure)) &&
					!strings.Contains(turn.TaskStatePromptText, TaskStateDiagnostic),
					"final state should report the passing result without a stale failure")
			} else {
				add("visible_success_reported", answered,
					"control may answer from the successful rerun still in history")
			}
			if treatment {
				add("prompt_state_available", turn.TaskStatePromptTokens > 0,
					"a hidden active objective should remain resident")
			}
		}

		score.Passed = true
		for _, check := range score.Checks {
			if !check.Passed {
				score.Passed = false
			}
		}
		scores = append(scores, score)
	}
	return scores
}

func scoreMultiFailureTrial(spec TrialSpec, result TrialResult) []TurnScore {
	treatment := spec.TaskState
	scores := make([]TurnScore, 0, len(result.Turns))
	for _, turn := range result.Turns {
		turn := turn
		score := TurnScore{TurnID: turn.TurnID, Response: turn.Response}
		add := func(name string, passed bool, detail string) {
			score.Checks = append(score.Checks, Check{Name: name, Passed: passed, Detail: detail})
		}
		snapshot := turn.TaskState

		switch turn.TurnID {
		case "t1":
			errs := 0
			for _, call := range turn.ToolCalls {
				if (call.Tool == "bash" || call.Tool == "exec") && call.IsError {
					errs++
				}
			}
			add("two_failures_executed", errs >= 2, "expected two erroring command calls")
			if treatment {
				failures := 0
				for _, kind := range snapshot.ActiveKinds {
					if kind == agent.TaskEntryKindFailure {
						failures++
					}
				}
				add("two_failures_recorded", failures >= 2,
					"both distinct failures should be stored separately")
			}
		case "t2":
			add("history_clear_observed", turn.HistoryCleared, "history should be cleared")
			add("parser_rerun_succeeded", hasToolCall(turn.ToolCalls, "", false, true),
				"expected a successful parser rerun")
		case "t3":
			lint := strings.Contains(turn.Response, TaskStateLintDiagnostic)
			parser := strings.Contains(turn.Response, TaskStateDiagnostic)
			if treatment {
				add("only_lint_failure_remains", lint && !parser,
					"the matching successful rerun should supersede only the parser failure")
				add("lint_failure_recorded", snapshot != nil &&
					strings.Contains(snapshotText(snapshot), TaskStateLintDiagnostic),
					"the unrelated lint failure should remain stored")
			} else {
				add("no_state_abstains", !lint,
					"control should not retain the cleared lint failure")
			}
		case "t4":
			if treatment {
				add("new_objective_invalidates_state",
					result.TaskStateFinal != nil &&
						!snapshotHasKind(result.TaskStateFinal, agent.TaskEntryKindFailure) &&
						strings.Contains(result.TaskStateFinal.Objective, "metrics endpoint"),
					"a new objective should clear unrelated failures")
			} else {
				add("new_objective_invalidates_state", true, "control has no task state")
			}
		}

		score.Passed = true
		for _, check := range score.Checks {
			if !check.Passed {
				score.Passed = false
			}
		}
		scores = append(scores, score)
	}
	return scores
}

func scoreSummarySourceTrial(spec TrialSpec, result TrialResult) []TurnScore {
	treatment := spec.TaskState
	scores := make([]TurnScore, 0, len(result.Turns))
	for _, turn := range result.Turns {
		turn := turn
		score := TurnScore{TurnID: turn.TurnID, Response: turn.Response}
		add := func(name string, passed bool, detail string) {
			score.Checks = append(score.Checks, Check{Name: name, Passed: passed, Detail: detail})
		}

		switch turn.TurnID {
		case "t3":
			add("explicit_compaction_created_summary", turn.Compacted && turn.SummaryCharsAfter > 0,
				"explicit /compact should create a sourced summary")
		case "t4":
			add("summary_carries_finding", strings.Contains(turn.Response, TaskStateDiagnostic),
				"the native compaction summary should answer the follow-up")
			if treatment {
				add("failure_still_stored", turn.TaskState != nil && snapshotHasKind(turn.TaskState, "failure"),
					"summary evidence does not delete the stored record")
				if boundaryResidency(spec) {
					add("summary_equivalent_not_repeated",
						!strings.Contains(turn.TaskStatePromptText, TaskStateDiagnostic),
						"boundary policy should trust the sourced summary instead of repeating it")
				} else {
					add("always_resident_004a", turn.TaskStatePromptTokens > 0,
						"always policy should repeat the finding despite the summary")
				}
			}
		}

		score.Passed = true
		for _, check := range score.Checks {
			if !check.Passed {
				score.Passed = false
			}
		}
		scores = append(scores, score)
	}
	return scores
}

func snapshotText(snapshot *TaskStateSnapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.RenderedText
}
