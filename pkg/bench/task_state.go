package bench

import (
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
		Entries:             len(state.Entries),
		Objective:           state.Objective,
		ObjectiveStatus:     state.ObjectiveStatus,
		ActiveKinds:         []string{},
		ActiveVerifications: []string{},
		ActiveKeys:          []string{},
	}
	for _, entry := range state.Entries {
		if entry.Superseded {
			continue
		}
		snapshot.ActiveEntries++
		snapshot.ActiveKinds = append(snapshot.ActiveKinds, entry.Kind)
		snapshot.ActiveVerifications = append(snapshot.ActiveVerifications, entry.Verification)
		snapshot.ActiveKeys = append(snapshot.ActiveKeys, entry.Key)
	}
	rendered := state.Render("", budget)
	snapshot.RenderedChars = len(rendered)
	snapshot.RenderedTokens = (len(rendered) + 3) / 4
	snapshot.RenderedText = rendered
	return snapshot
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
// Experiment C. Control and treatment have different expected outcomes at the
// post-compaction follow-up by design; every other mechanism check is shared.
func scoreTaskStateTrial(spec TrialSpec, result TrialResult) []TurnScore {
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
			add("failing_test_executed", hasToolCall(turn.ToolCalls, "", true, false),
				"expected an erroring bash/exec call")
			if treatment {
				add("failure_recorded", snapshotHasKind(snapshot, agent.TaskEntryKindFailure),
					"task state should record the tool-reported failure")
			}
		case "t2":
			if treatment {
				add("correction_recorded",
					snapshotHasKind(snapshot, agent.TaskEntryKindCorrection) &&
						strings.Contains(snapshotText(snapshot), TaskStateCorrection),
					"task state should record the user correction")
			} else {
				add("correction_recorded", true, "control has no task state by design")
			}
		case "t3":
			if treatment {
				add("diagnosis_proposed_unverified",
					snapshotHasKind(snapshot, agent.TaskEntryKindDiagnosis) &&
						snapshotHasVerification(snapshot, agent.TaskVerificationProposed) &&
						strings.Contains(snapshotText(snapshot), TaskStateDiagnosis),
					"assistant diagnosis must remain proposed")
			} else {
				add("diagnosis_proposed_unverified", true, "control has no task state by design")
			}
		case "t4":
			add("explicit_compaction_created_summary", turn.Compacted && turn.SummaryCharsAfter > 0,
				"explicit /compact should create a session summary")
		case "t5":
			add("restart_resumed_same_session", turn.Restarted && turn.HistoryBefore > 0,
				"restart should resume the same session history")
			retained := strings.Contains(turn.Response, TaskStateDiagnostic)
			if treatment {
				add("post_compaction_fact_retained", retained,
					"task state should retain the verified failure across compaction and restart")
			} else {
				add("post_compaction_fact_absent", !retained,
					"control should lose the failure detail after compaction")
			}
		case "t6":
			add("successful_rerun_executed", hasToolCall(turn.ToolCalls, "", false, true),
				"expected a successful bash/exec call")
			if treatment {
				add("later_success_supersedes_failure",
					snapshot != nil && !snapshotHasKind(snapshot, agent.TaskEntryKindFailure),
					"a successful matching rerun should supersede the earlier failure")
			}
		case "t7":
			answered := containsAny(strings.ToLower(turn.Response), "passes", "no,", "not failing")
			if treatment {
				add("superseded_failure_not_reported", answered &&
					(result.TaskStateFinal == nil || !snapshotHasKind(result.TaskStateFinal, agent.TaskEntryKindFailure)),
					"final state should report the passing result without a stale failure")
			} else {
				add("visible_success_reported", answered,
					"control may answer from the successful rerun still in history")
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
