package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 005D: deterministic progress and repetition metrics over trusted tool
// traces. Every function below is a pure function of recorded, joined
// attempts — never of model claims. Thresholds and patterns are documented
// with each function; they are bench measurement choices, not task rules.

// TraceOutcome classifies one attempt's recorded outcome.
type TraceOutcome struct {
	// Outcome is one of: success, tool_error, guard_rejected, hook_denied,
	// skipped, unknown.
	Outcome string
	// GuardKind refines guard_rejected: dangerous_pattern, path_outside,
	// traversal, allowlist, escaped.
	GuardKind string
	// ExitCode is set when the outcome text carries an explicit code.
	ExitCode *int
	// OutcomeHash identifies identical outputs for repeat analysis.
	OutcomeHash string
}

// normalizeTraceCommand reduces a tool invocation to a comparable key.
// Bash uses the trimmed full command (flags and pipes are significant, so
// `go test ./...` and `go test .` are different invocations); file tools
// use tool plus path.
func normalizeTraceCommand(tool string, args map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "bash", "exec":
		command, _ := args["command"].(string)
		return "bash:" + strings.Join(strings.Fields(command), " ")
	case "read":
		path, _ := args["path"].(string)
		return "read:" + strings.TrimSpace(path)
	case "write":
		path, _ := args["path"].(string)
		return "write:" + strings.TrimSpace(path)
	case "edit":
		path, _ := args["path"].(string)
		oldText, _ := args["old_text"].(string)
		return "edit:" + strings.TrimSpace(path) + "\x00" + oldText
	default:
		return strings.ToLower(strings.TrimSpace(tool))
	}
}

var (
	guardDangerousPattern = "dangerous pattern detected"
	guardPathOutside      = "path outside working dir"
	guardTraversal        = "path traversal detected"
	guardAllowlist        = "not in allowlist"
	guardEscaped          = "escaped workspace"
	hookDeniedMarker      = "Tool execution denied by"
	exitCodePatterns      = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\[command exited with code (-?\d+)\]`),
		regexp.MustCompile(`(?i)\bexit\s*[:=]\s*(-?\d+)`),
		regexp.MustCompile(`(?i)\bexit\s+code\s+(-?\d+)`),
		regexp.MustCompile(`(?i)\bEXIT:\s*(-?\d+)`),
	}
)

// classifyTraceOutcome maps recorded outcome evidence to a stable class.
// Guard strings are matched first because a blocked command never produces
// an exit code; an explicit code decides otherwise.
func classifyTraceOutcome(outcomeText string, isError bool) TraceOutcome {
	classified := TraceOutcome{}
	hash := sha256.Sum256([]byte(outcomeText))
	classified.OutcomeHash = hex.EncodeToString(hash[:])
	switch {
	case strings.Contains(outcomeText, guardDangerousPattern):
		classified.Outcome, classified.GuardKind = "guard_rejected", "dangerous_pattern"
	case strings.Contains(outcomeText, guardPathOutside):
		classified.Outcome, classified.GuardKind = "guard_rejected", "path_outside"
	case strings.Contains(outcomeText, guardTraversal):
		classified.Outcome, classified.GuardKind = "guard_rejected", "traversal"
	case strings.Contains(outcomeText, guardAllowlist):
		classified.Outcome, classified.GuardKind = "guard_rejected", "allowlist"
	case strings.Contains(outcomeText, guardEscaped):
		classified.Outcome, classified.GuardKind = "guard_rejected", "escaped"
	case strings.Contains(outcomeText, hookDeniedMarker):
		classified.Outcome = "hook_denied"
	default:
		for _, pattern := range exitCodePatterns {
			if match := pattern.FindStringSubmatch(outcomeText); match != nil {
				if code, err := strconv.Atoi(match[1]); err == nil {
					classified.ExitCode = &code
					if code == 0 {
						classified.Outcome = "success"
					} else {
						classified.Outcome = "tool_error"
					}
					return classified
				}
			}
		}
		if isError {
			classified.Outcome = "tool_error"
		} else {
			classified.Outcome = "success"
		}
	}
	return classified
}

// checkCommandPatterns recognizes test/check invocations for pass/fail
// accounting. Deliberately narrow: only repository check entry points.
var checkCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(^|[\s;&|])(sh\s+)?(\./)?check\.sh\b`),
	regexp.MustCompile(`\bgo\s+test\b`),
	regexp.MustCompile(`\bpytest\b`),
	regexp.MustCompile(`\bnpm\s+test\b`),
	regexp.MustCompile(`\bcargo\s+test\b`),
}

func isCheckCommand(normalized string) bool {
	command := normalized
	if index := strings.Index(command, ":"); index >= 0 {
		command = command[index+1:]
	}
	for _, pattern := range checkCommandPatterns {
		if pattern.MatchString(command) {
			return true
		}
	}
	return false
}

// TraceRepetition summarizes repeats of one normalized command.
type TraceRepetition struct {
	Key         string
	Total       int
	Failed      int
	Guarded     int
	MaxRun      int // longest consecutive run
	Pointless   bool
	LegitReason string
}

// summarizeTraceRepetitions counts repeats and flags pointless loops: the
// same failing command with an identical outcome and no intervening
// successful file change. A repeat that follows a successful modification,
// or whose outcomes differ, is legitimate re-verification.
func summarizeTraceRepetitions(keys []string, outcomes []TraceOutcome, changedBetween []bool) []TraceRepetition {
	byKey := map[string][]int{}
	for i, key := range keys {
		byKey[key] = append(byKey[key], i)
	}
	names := make([]string, 0, len(byKey))
	for name := range byKey {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []TraceRepetition
	for _, name := range names {
		indexes := byKey[name]
		rep := TraceRepetition{Key: name, Total: len(indexes)}
		run, best := 0, 0
		var previous string
		for _, i := range indexes {
			if outcomes[i].Outcome == "tool_error" || outcomes[i].Outcome == "guard_rejected" {
				rep.Failed++
			}
			if outcomes[i].Outcome == "guard_rejected" {
				rep.Guarded++
			}
			marker := outcomes[i].Outcome + ":" + outcomes[i].OutcomeHash
			if marker == previous {
				run++
			} else {
				run, previous = 1, marker
			}
			if run > best {
				best = run
			}
		}
		rep.MaxRun = best
		// Pointless only when the command, the failure, AND the output all
		// repeat with no successful file change anywhere between.
		if len(indexes) > 1 {
			same := true
			for _, i := range indexes[1:] {
				if outcomes[i].OutcomeHash != outcomes[indexes[0]].OutcomeHash ||
					outcomes[i].Outcome != outcomes[indexes[0]].Outcome {
					same = false
					break
				}
			}
			failed := rep.Failed == len(indexes)
			changed := false
			for _, c := range changedBetween {
				if c {
					changed = true
					break
				}
			}
			if same && failed && !changed {
				rep.Pointless = true
			} else if same {
				rep.LegitReason = "identical command re-run after a change or with differing outcomes"
			} else {
				rep.LegitReason = "command repeated with differing outcomes"
			}
		}
		out = append(out, rep)
	}
	return out
}

// longestNoProgressRun finds the longest consecutive run of attempts sharing
// one normalized command and one failure-class outcome.
func longestNoProgressRun(keys []string, outcomes []TraceOutcome) int {
	best, run := 0, 0
	var previous string
	for i, key := range keys {
		failed := outcomes[i].Outcome == "tool_error" || outcomes[i].Outcome == "guard_rejected"
		marker := ""
		if failed {
			marker = key + "\x00" + outcomes[i].Outcome
		}
		if marker != "" && marker == previous {
			run++
		} else if marker != "" {
			run, previous = 1, marker
		} else {
			run, previous = 0, ""
		}
		if run > best {
			best = run
		}
	}
	return best
}

// readsWithoutProgress counts reads of one path whose output never changes
// and which no successful write/edit to that path separates.
func readsWithoutProgress(paths []string, hashes []string, writers map[int]string) int {
	byPath := map[string][]int{}
	for i, path := range paths {
		byPath[path] = append(byPath[path], i)
	}
	count := 0
	for _, indexes := range byPath {
		if len(indexes) < 2 {
			continue
		}
		steady := true
		for _, i := range indexes[1:] {
			if hashes[i] != hashes[indexes[0]] {
				steady = false
				break
			}
		}
		if !steady {
			continue
		}
		for i := 1; i < len(indexes); i++ {
			separated := false
			for j := indexes[i-1] + 1; j < indexes[i]; j++ {
				if writers[j] == paths[indexes[0]] {
					separated = true
					break
				}
			}
			if !separated {
				count++
			}
		}
	}
	return count
}

// ClassifiedAttempt joins one tool attempt with its recorded outcome text.
// Position in a same-order slice is the join key: dispatch is strictly
// sequential and both the trace and the session history preserve that
// order. Length mismatches are tolerated (shorter side wins) so a partial
// trace still classifies what it can.
type ClassifiedAttempt struct {
	ExecID     int64
	TurnID     string
	RequestID  string
	Tool       string
	Command    string
	Outcome    TraceOutcome
	FilesAfter []string
}

// AttachOutcomes pairs attempts with session tool-result texts in order.
func AttachOutcomes(attempts []ToolAttempt, outcomes []string) []ClassifiedAttempt {
	n := len(attempts)
	if len(outcomes) < n {
		n = len(outcomes)
	}
	out := make([]ClassifiedAttempt, 0, n)
	for i := 0; i < n; i++ {
		attempt := attempts[i]
		classified := classifyTraceOutcome(outcomes[i], attempt.IsError)
		command := ""
		if name, key := traceCommandKey(attempt); key != "" {
			_ = name
			command = key
		}
		out = append(out, ClassifiedAttempt{
			ExecID: attempt.ExecID, TurnID: attempt.TurnID,
			RequestID: attempt.RequestID, Tool: attempt.Tool,
			Command: command, Outcome: classified,
		})
	}
	return out
}

// traceCommandKey renders the analysis identity of one attempt and reports
// whether it names a repeatable operation (commands and file paths count;
// bare tool names do not).
func traceCommandKey(attempt ToolAttempt) (string, string) {
	command := normalizeTraceCommand(attempt.Tool, attempt.Arguments)
	if strings.HasPrefix(command, "bash:") && len(command) > len("bash:") {
		return "bash", command
	}
	if strings.HasPrefix(command, "read:") && len(command) > len("read:") {
		return "read", command
	}
	if (strings.HasPrefix(command, "write:") || strings.HasPrefix(command, "edit:")) && !strings.HasSuffix(command, "\x00") {
		return "file", command
	}
	return attempt.Tool, ""
}

// ReverificationAfterRestart counts post-restart reads of paths that were
// read before the restart: the model re-establishing ground truth after a
// genuine process boundary.
func ReverificationAfterRestart(reads []string, restartIndex int, prePaths map[string]bool) int {
	count := 0
	for i, path := range reads {
		if i >= restartIndex && prePaths[path] {
			count++
		}
	}
	return count
}

// TimeAndTokensToFirstEdit returns the elapsed milliseconds from the trial
// start and the provider-reported tokens of requests up to and including
// the request that produced the first successful modification, or -1 when
// no successful modification occurred. Token lookup is by request ID.
func TimeAndTokensToFirstEdit(startedUnixMilli int64, attempts []ToolAttempt, outcomes []TraceOutcome, requestTokens map[string]int, requestAt map[string]int64) (int64, int64) {
	for i, attempt := range attempts {
		if outcomes[i].Outcome != "success" {
			continue
		}
		if attempt.Tool != "write" && attempt.Tool != "edit" {
			continue
		}
		if attempt.StartedAt == "" {
			return -1, -1
		}
		started, err := parseTraceTime(attempt.StartedAt)
		if err != nil {
			return -1, -1
		}
		var tokens int64
		if attempt.RequestID != "" {
			tokens = int64(requestTokens[attempt.RequestID])
		}
		return started - startedUnixMilli, tokens
	}
	return -1, -1
}

func parseTraceTime(value string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}
