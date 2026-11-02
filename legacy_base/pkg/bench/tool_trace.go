package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 005D: crash-resilient tool-execution traces (bench-only).
//
// Provider request records show intent, but the live MiniMax adapter records
// tool calls as bare `{"id": ...}` objects: no names, no arguments. The
// trusted runtime events do better — Start carries the final validated tool
// name and arguments, End carries duration, outcome length and error status
// — but they lived only in memory and the End payload has no error text or
// exit status. This file persists one event per tool lifecycle transition,
// synchronously, so every attempt survives trial failure and abnormal child
// termination exactly like the 005B-R usage ledger.
//
// Privacy design: arguments are sanitized at record time (per-string
// truncation + secret-pattern redaction). Outcome text is never stored in
// full — only a bounded prefix for classification and a content hash for
// equality checks. No credentials, headers, or hidden-test content can
// appear: hidden tests never enter tool arguments or tool results, and the
// trace file lives gitignored beside the other redacted artifacts.
//
// Join model: provider requests (intent) and trusted executions
// (observation) are separate series joined by order within a turn.
// Dispatch is strictly sequential (no goroutines in the tool loop) and the
// subscriptions below use locked delivery, so the n-th Start of a turn
// corresponds to the n-th attempted provider call of that turn. Hook-respond
// and hook-skipped paths emit their own ordered events, preserving the
// correspondence.

const toolTraceFileName = "tool-trace.jsonl"

// maxTraceStringRunes bounds any single recorded string value.
const maxTraceStringRunes = 500

// ToolAttempt is one joined tool lifecycle: a Start line, then an End (or
// Skipped) line with the same ExecID. Recovery merges the two.
type ToolAttempt struct {
	ExecID    int64          `json:"exec_id"`
	Event     string         `json:"event"` // start|end|skipped
	TurnID    string         `json:"turn_id,omitempty"`
	AgentTurn string         `json:"agent_turn,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
	StartedAt string         `json:"started_at,omitempty"`
	EndedAt   string         `json:"ended_at,omitempty"`
	// Completion fields (end/skipped lines).
	DurationMS int64  `json:"duration_ms,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	Async      bool   `json:"async,omitempty"`
	OutcomeLen int    `json:"outcome_len,omitempty"`
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// ToolTraceSummary counts one trial's joined attempts.
type ToolTraceSummary struct {
	Attempts      int `json:"attempts"`
	Executed      int `json:"executed"`
	GuardRejected int `json:"guard_rejected"`
	Failed        int `json:"failed"`
	Skipped       int `json:"skipped"`
	Unknown       int `json:"unknown_outcome"`
	Events        int `json:"events_reconciled"`
	Duplicates    int `json:"duplicate_lines_ignored"`
}

// ToolTracePath returns the trial-level trace file shared by every segment
// of the trial. Empty when the spec has no trial directory, in which case
// tracing is disabled.
func ToolTracePath(spec TrialSpec) string {
	if strings.TrimSpace(spec.TrialDir) == "" {
		return ""
	}
	return filepath.Join(spec.TrialDir, "raw", toolTraceFileName)
}

func sanitizeTraceValue(value any) any {
	switch typed := value.(type) {
	case string:
		runes := []rune(typed)
		if len(runes) > maxTraceStringRunes {
			runes = runes[:maxTraceStringRunes]
		}
		return RedactSecrets(string(runes))
	case float64, bool, int, int64:
		return value
	case nil:
		return nil
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprintf("<%T>", value)
		}
		text := RedactSecrets(string(data))
		if len([]rune(text)) > maxTraceStringRunes {
			text = string([]rune(text)[:maxTraceStringRunes])
		}
		return text
	}
}

// sanitizeTraceArgs bounds and redacts one argument map. Command strings and
// paths (the analysis targets) are preserved; bulk content is truncated.
func sanitizeTraceArgs(arguments map[string]any) map[string]any {
	if len(arguments) == 0 {
		return nil
	}
	out := make(map[string]any, len(arguments))
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out[key] = sanitizeTraceValue(arguments[key])
	}
	return out
}

func outcomeHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// toolTraceCollector joins Start/End/Skipped runtime events into attempts.
// The trial loop sets the current bench turn and a request-ID provider; the
// collector never touches prompts or schemas.
type toolTraceCollector struct {
	mu       sync.Mutex
	path     string
	nextID   int64
	turnID   string
	request  func() string
	open     map[int64]int // execID index into attempts, while unjoined
	attempts []ToolAttempt
	wrote    bool
}

func newToolTraceCollector(path string) *toolTraceCollector {
	return &toolTraceCollector{path: path, open: map[int64]int{}}
}

func (c *toolTraceCollector) setContext(turnID string, request func() string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turnID = turnID
	c.request = request
}

// reseed continues ExecIDs past whatever a previous segment (previous OS
// process sharing this file) already recorded, so IDs stay unique per trial.
func (c *toolTraceCollector) reseed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		return
	}
	recovery, err := RecoverToolTrace(c.path)
	if err != nil {
		return
	}
	if recovery.MaxExecID >= c.nextID {
		c.nextID = recovery.MaxExecID + 1
	}
}

func (c *toolTraceCollector) currentRequest() string {
	if c.request == nil {
		return ""
	}
	return c.request()
}

func (c *toolTraceCollector) appendLocked(attempt ToolAttempt) {
	c.attempts = append(c.attempts, attempt)
	c.wrote = true
	if c.path != "" {
		_ = appendTraceEvent(c.path, attempt)
	}
}

// recordStart persists a dispatch. It runs synchronously in the tool loop,
// so a crash before completion still leaves the attempt on disk.
func (c *toolTraceCollector) recordStart(agentTurn string, tool string, args map[string]any) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	attempt := ToolAttempt{
		ExecID: id, Event: "start", TurnID: c.turnID, AgentTurn: agentTurn,
		RequestID: c.currentRequest(), Tool: tool,
		Arguments: sanitizeTraceArgs(args),
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	c.open[id] = len(c.attempts)
	c.appendLocked(attempt)
	return id
}

// recordEnd joins the completion to the earliest open attempt for the tool.
// Dispatch is strictly sequential, so FIFO order is exact; a tool-name
// check guards against cross-talk if delivery ever reorders.
func (c *toolTraceCollector) recordEnd(agentTurn, tool string, duration time.Duration, isError, async bool, outcomeLen int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := int64(-1)
	for execID := range c.open {
		if id < 0 || execID < id {
			if c.attempts[c.open[execID]].Tool == tool {
				id = execID
			}
		}
	}
	if id < 0 {
		// End without a recorded Start (older code path or missed
		// subscription): persist an end-only line rather than dropping it.
		id = c.nextID
		c.nextID++
		c.appendLocked(ToolAttempt{
			ExecID: id, Event: "end", TurnID: c.turnID, AgentTurn: agentTurn,
			RequestID: c.currentRequest(), Tool: tool,
			StartedAt:  time.Now().UTC().Format(time.RFC3339Nano),
			EndedAt:    time.Now().UTC().Format(time.RFC3339Nano),
			DurationMS: duration.Milliseconds(), IsError: isError, Async: async,
			OutcomeLen: outcomeLen,
		})
		return
	}
	index := c.open[id]
	delete(c.open, id)
	completed := c.attempts[index]
	completed.Event = "end"
	completed.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	completed.DurationMS = duration.Milliseconds()
	completed.IsError = isError
	completed.Async = async
	completed.OutcomeLen = outcomeLen
	c.appendLocked(completed)
}

func (c *toolTraceCollector) recordSkipped(agentTurn, tool, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	c.appendLocked(ToolAttempt{
		ExecID: id, Event: "skipped", TurnID: c.turnID, AgentTurn: agentTurn,
		RequestID: c.currentRequest(), Tool: tool,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		EndedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Skipped:   true, SkipReason: reason,
	})
}

func appendTraceEvent(path string, attempt ToolAttempt) error {
	return appendLedgerEvent(path, attempt)
}

// snapshot summarizes the in-process trace state, joining start/end
// lines by ExecID exactly like file recovery. Call it at trial end and on
// failure paths so partial evidence is preserved even without recovery.
func (c *toolTraceCollector) snapshot() ToolTraceSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	joined := map[int64]ToolAttempt{}
	for _, attempt := range c.attempts {
		previous, seen := joined[attempt.ExecID]
		if !seen || (previous.Event == "start" && attempt.Event != "start") {
			joined[attempt.ExecID] = attempt
		}
	}
	var summary ToolTraceSummary
	for _, attempt := range joined {
		summary.Events++
		switch attempt.Event {
		case "skipped":
			summary.Attempts++
			summary.Skipped++
		case "end":
			summary.Attempts++
			summary.Executed++
			if attempt.IsError {
				summary.Failed++
			}
		default:
			summary.Attempts++
			summary.Unknown++
		}
	}
	return summary
}

// SummarizeRecovery converts a file recovery into the result-level summary,
// mapping unknown outcomes through.
func SummarizeRecovery(recovery ToolTraceRecovery) ToolTraceSummary {
	return ToolTraceSummary{
		Attempts: recovery.Attempts, Executed: recovery.Executed,
		Failed: recovery.Failed, Skipped: recovery.Skipped,
		Unknown: recovery.Unknown, Events: recovery.Events,
		Duplicates: recovery.Duplicates,
	}
}

// ToolTraceRecovery is the parent-side reconstruction of one trial's tool
// attempts. Unknown counts attempts with a Start but no End or Skipped line.
type ToolTraceRecovery struct {
	Attempts   int   `json:"attempts"`
	Executed   int   `json:"executed"`
	GuardFlags int   `json:"-"`
	Failed     int   `json:"failed"`
	Skipped    int   `json:"skipped"`
	Unknown    int   `json:"unknown_outcome"`
	Events     int   `json:"events_reconciled"`
	Duplicates int   `json:"duplicate_lines_ignored"`
	MaxExecID  int64 `json:"-"`
}

// RecoverToolTrace rebuilds attempts from a trace file. End and skipped
// lines supersede their Start line by ExecID; duplicates collapse.
func RecoverToolTrace(path string) (ToolTraceRecovery, error) {
	var recovery ToolTraceRecovery
	joined, dups, err := readJoinedTraceEvents(path)
	if err != nil {
		return recovery, err
	}
	recovery.Duplicates = dups
	for _, attempt := range joined {
		if attempt.ExecID > recovery.MaxExecID {
			recovery.MaxExecID = attempt.ExecID
		}
		recovery.Events++
		switch attempt.Event {
		case "skipped":
			recovery.Attempts++
			recovery.Skipped++
		case "end":
			recovery.Attempts++
			recovery.Executed++
			if attempt.IsError {
				recovery.Failed++
			}
		default:
			recovery.Attempts++
			recovery.Unknown++
		}
	}
	return recovery, nil
}

func readJoinedTraceEvents(path string) ([]ToolAttempt, int, error) {
	lines, err := readTraceLines(path)
	if err != nil {
		return nil, 0, err
	}
	byID := map[int64]ToolAttempt{}
	seenKind := map[string]bool{}
	order := []int64{}
	dups := 0
	for _, line := range lines {
		var attempt ToolAttempt
		if json.Unmarshal([]byte(line), &attempt) != nil {
			continue
		}
		if _, seen := byID[attempt.ExecID]; !seen {
			order = append(order, attempt.ExecID)
		}
		// Only a repeated line of the same kind is a true duplicate. A
		// response superseding its reservation is the expected completion,
		// not a duplicate.
		kind := attempt.Event
		key := strconv.FormatInt(attempt.ExecID, 10) + ":" + kind
		if seenKind[key] {
			dups++
		}
		seenKind[key] = true
		previous, seen := byID[attempt.ExecID]
		if !seen || (previous.Event == "start" && attempt.Event != "start") {
			byID[attempt.ExecID] = attempt
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]ToolAttempt, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, dups, nil
}
