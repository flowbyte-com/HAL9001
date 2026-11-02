package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 005B-R: crash-resilient usage accounting.
//
// The 005B pilot proved that in-memory request records can vanish: the failed
// compaction_ledger trial made ~40 provider calls whose usage died with the
// child process, because raw traces and results were written only on clean
// completion. This ledger persists one small event per provider request,
// synchronously, before the next request is dispatched, so the parent can
// always reconstruct what was spent — even after a crash, a kill, or an
// early-return failure path that never writes result.json.
//
// The ledger never contains prompt text, response content, credentials, or
// headers: only request IDs, token counts, status, and latency. It is
// append-only with one fsync per event; volume is tens of events per trial,
// so the cost is negligible next to an LLM round trip.
//
// Three spend classes are kept separate:
//   - known: provider-reported total_tokens for completed responses;
//   - reserved: the conservative allowance held for requests whose outcome is
//     still unknown (dispatched but no response event yet);
//   - unknown: the count of such unresolved requests.
// A missing outcome is never counted as zero: its reservation stays on the
// books until reconciled.

const usageLedgerFileName = "usage-ledger.jsonl"

// UsageEvent is one line of the ledger. Type is "reservation" (written
// before dispatch) or "response" (written when the response settles).
type UsageEvent struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	TurnID    string `json:"turn_id,omitempty"`
	Segment   int    `json:"segment,omitempty"`
	Model     string `json:"model,omitempty"`
	// Allowance is the conservative pre-dispatch reservation in tokens
	// (context window + max completion). Present on reservations.
	Allowance int `json:"allowance,omitempty"`
	// Status is "ok" or "error". Present on responses.
	Status string `json:"status,omitempty"`
	// Reported is true only when the provider adapter returned a usage
	// object with a usable total. Present on responses.
	Reported         bool   `json:"reported,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	TotalTokens      int    `json:"total_tokens,omitempty"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	LatencyMS        int64  `json:"latency_ms,omitempty"`
	Error            string `json:"error,omitempty"`
	At               string `json:"at"`
}

// UsageRecovery is the parent-side reconstruction of one trial's spend.
type UsageRecovery struct {
	// Known is the sum of provider-reported totals for resolved responses.
	Known int `json:"known_reported_total"`
	// Reserved is the sum of allowances held for unresolved requests.
	Reserved int `json:"reserved_allowance_total"`
	// Unknown is the number of requests with no recorded outcome.
	Unknown int `json:"unknown_requests"`
	// Events is the number of distinct requests reconciled.
	Events int `json:"events_reconciled"`
	// Duplicates is the number of repeated lines collapsed by request ID.
	Duplicates int `json:"duplicate_lines_ignored"`
}

// UsageLedgerPath returns the trial-level ledger path shared by every
// segment of the trial. Empty when the spec has no trial directory (older
// unit tests), in which case the ledger is disabled.
func UsageLedgerPath(spec TrialSpec) string {
	if strings.TrimSpace(spec.TrialDir) == "" {
		return ""
	}
	return filepath.Join(spec.TrialDir, "raw", usageLedgerFileName)
}

var usageLedgerMu sync.Mutex

func appendUsageEvent(path string, event UsageEvent) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	event.At = time.Now().UTC().Format(time.RFC3339Nano)
	return appendJSONLLine(path, event)
}

// appendJSONLLine appends one JSON object plus newline with an fsync, mode
// 0600. Shared by the usage ledger and the tool trace so both survive
// abnormal child termination with the same durability.
func appendJSONLLine(path string, value any) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	usageLedgerMu.Lock()
	defer usageLedgerMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// readJSONLLines returns the non-empty lines of a JSONL file. A missing file
// is not an error: it means nothing was recorded yet.
func readJSONLLines(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// RecoverUsageLedger rebuilds spend from a ledger file. Response events
// supersede reservations by request ID, so re-appended or duplicated lines
// cannot double-count; segments sharing one file are naturally merged
// because request IDs carry the segment prefix (s1-req-001, ...). A missing
// file is not an error: it means no metered request was dispatched.
func RecoverUsageLedger(path string) (UsageRecovery, error) {
	var recovery UsageRecovery
	if strings.TrimSpace(path) == "" {
		return recovery, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return recovery, nil
		}
		return recovery, err
	}
	_ = data
	byID := map[string]UsageEvent{}
	order := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event UsageEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if strings.TrimSpace(event.RequestID) == "" {
			continue
		}
		if _, seen := byID[event.RequestID]; !seen {
			order = append(order, event.RequestID)
		} else {
			recovery.Duplicates++
		}
		// A response supersedes its reservation; a later duplicate of the
		// same kind keeps the first copy. Either way each ID counts once.
		previous, seen := byID[event.RequestID]
		if !seen || (previous.Type == "reservation" && event.Type == "response") {
			byID[event.RequestID] = event
		}
	}
	sort.Strings(order)
	for _, id := range order {
		event := byID[id]
		recovery.Events++
		switch event.Type {
		case "response":
			if event.Reported {
				recovery.Known += event.TotalTokens
			} else {
				recovery.Unknown++
				// The outcome is unknown, but the pre-dispatch reservation
				// for this request stays on the books. The reservation line
				// was superseded above, so re-add its allowance: it is the
				// same allowance recorded before dispatch.
				recovery.Reserved += event.Allowance
			}
		case "reservation":
			recovery.Unknown++
			recovery.Reserved += event.Allowance
		}
	}
	return recovery, nil
}

// ReconcileUsage merges the in-memory result with the ledger. In-memory
// reported totals and ledger-known totals describe the same completed
// responses, so known spend is their maximum, not their sum; reservations
// exist only in the ledger. Unknown counts add: ledger-unresolved requests
// plus in-memory requests with missing usage.
func ReconcileUsage(result TrialResult, ledgerPath string) (UsageRecovery, error) {
	recovery, err := RecoverUsageLedger(ledgerPath)
	if err != nil {
		return recovery, err
	}
	inMemoryKnown := result.Totals.TotalTokens
	inMemoryUnknown := result.Totals.UsageMissingRequests
	if inMemoryKnown > recovery.Known {
		recovery.Known = inMemoryKnown
	}
	recovery.Unknown += inMemoryUnknown
	return recovery, nil
}

// TrialSpendForBudget is the conservative spend charged against caps: known
// reported tokens plus reservations held for unknown outcomes.
func TrialSpendForBudget(recovery UsageRecovery) int64 {
	return int64(recovery.Known + recovery.Reserved)
}

func ledgerAllowance(spec TrialSpec) int {
	window := spec.ContextWindow
	if window <= 0 {
		window = 32768
	}
	maxTokens := spec.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	return window + maxTokens
}

// appendLedgerEvent persists one tool-trace event. It shares the
// fsync-per-append durability of the usage ledger.
func appendLedgerEvent(path string, attempt ToolAttempt) error {
	return appendJSONLLine(path, attempt)
}

// readTraceLines returns the non-empty lines of a tool-trace file.
func readTraceLines(path string) ([]string, error) {
	return readJSONLLines(path)
}
