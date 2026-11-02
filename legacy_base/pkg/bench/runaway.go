package bench

import (
	"strings"
)

// 005B-R: runaway-trajectory safeguards.
//
// The aborted 005B pilot trial showed that a live model can loop far beyond
// anything the deterministic mock produces: 40 requests and 73 tool calls in
// two turns, 25 threshold compactions that never brought the prompt below
// threshold, and two `max_tool_iterations` exhaustions. None of the existing
// bounds stopped it, because each bound (per-turn iterations, token budget
// allowance) was respected while the trajectory as a whole made no
// progress. These guards detect that condition symmetrically — identical
// code, thresholds and consequences in control and treatment — and end the
// trial as a *task execution limit* (harness-valid, repository scored
// as-is) instead of letting it decay into a harness infrastructure error
// with lost evidence.
//
// Thresholds are predetermined below and validated against the mock
// trajectories, whose maxima are far below every trip point:
//   - mock peak requests per trial: 13 (ledger);
//   - mock peak tool calls per trial: 9;
//   - mock threshold compactions per trial: 1 (explicit only);
//   - mock identical consecutive bash commands per turn: 1;
//   - mock iteration exhaustions: 0.

const (
	// maxCompactedHistoryMessages bounds the "already lost" tail accepted
	// for a no-op explicit compaction: ordinary history down to a small
	// tail (production keeps ~4 messages plus the current turn) with a
	// non-empty summary carrying the earlier content.
	maxCompactedHistoryMessages = 8
	// maxSummariesPerTurn ends a turn whose automatic compactions keep
	// firing without bringing the prompt under control.
	maxSummariesPerTurn = 6
	// maxSummariesPerTrial is the backstop across the whole trial.
	maxSummariesPerTrial = 12
	// maxConsecutiveIdenticalBashPerTurn ends a turn looping one command.
	maxConsecutiveIdenticalBashPerTurn = 6
	// maxIterationExhaustionsPerTrial ends a trial that exhausts the
	// per-turn tool budget more than once without ever responding.
	maxIterationExhaustionsPerTrial = 2
	// maxRequestsPerTrial and maxToolCallsPerTrial are last-resort
	// backstops, far above mock-scale traffic.
	maxRequestsPerTrial  = 100
	maxToolCallsPerTrial = 200
)

// maxIterationExhausted reports whether the turn ended because the agent
// hit the per-turn tool-iteration budget without a final response.
func maxIterationExhausted(response string) bool {
	return strings.Contains(response, "max_tool_iterations")
}

func normalizeBashCommand(arguments map[string]any) (string, bool) {
	raw, _ := arguments["command"].(string)
	command := strings.TrimSpace(raw)
	if command == "" {
		return "", false
	}
	return command, true
}

// maxConsecutiveIdenticalBash counts the longest run of one identical bash
// command among the turn's attempted provider tool calls.
func maxConsecutiveIdenticalBash(calls []ToolCallRecord) int {
	best, run := 0, 0
	var previous string
	for _, call := range calls {
		if normalizeCodingTool(call.Tool) != "bash" {
			run, previous = 0, ""
			continue
		}
		command, ok := normalizeBashCommand(call.Arguments)
		if !ok {
			run, previous = 0, ""
			continue
		}
		if command == previous {
			run++
		} else {
			run, previous = 1, command
		}
		if run > best {
			best = run
		}
	}
	return best
}

// turnRunawayReason inspects one completed turn for no-progress loops. It
// returns "" when the turn looks healthy. Identical thresholds apply in
// both conditions; the analysis sees only the recorded trace.
func turnRunawayReason(turnRequests []RequestRecord, turnSummaries int) string {
	if turnSummaries >= maxSummariesPerTurn {
		return "task execution limit: automatic compaction fired without bringing the prompt under control"
	}
	var attempted []ToolCallRecord
	for _, request := range turnRequests {
		attempted = append(attempted, request.ToolCalls...)
	}
	if run := maxConsecutiveIdenticalBash(attempted); run >= maxConsecutiveIdenticalBashPerTurn {
		return "task execution limit: same shell command repeated without progress"
	}
	return ""
}

// explicitCompactHistoryLost reports whether a no-op explicit compaction
// may be accepted: the trial must already carry a non-empty summary (fresh
// sessions start empty, so a summary proves some compaction already ran)
// and the remaining ordinary history must fit within twice the normal
// protected tail. Anything larger means compaction is behind and the no-op
// is genuine.
func explicitCompactHistoryLost(summaryChars, historyLen int) bool {
	return summaryChars > 0 && historyLen <= 2*maxCompactedHistoryMessages
}
