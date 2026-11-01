package bench

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
)

func readSessionLines(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func readJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// 005G: independent eligibility gate for working-memory evaluation.
//
// A continuation is a genuine information-loss opportunity only if all of
// the following hold:
//
//  1. boundary: a compaction or process restart actually occurred;
//  2. dependence: a prior finding F is relevant to a subsequent coded
//     decision (a hidden test requires F);
//  3. capturable: task state could legitimately capture F from existing
//     events (tool observations, marked user state, runtime events —
//     never model proposals or hidden content);
//  4. lost: F's evidence markers are absent from ordinary visible history
//     AND from the compaction summary after the boundary.
//
// Workspace recoverability does NOT disqualify: the control may reasonably
// re-derive F with ordinary tools, but that cost must be measured because
// it bounds the treatment's possible advantage. Eligibility is decided
// from the task structure and the recorded trace, never from the eventual
// treatment outcome.

// EligibilityInput is one candidate continuation.
type EligibilityInput struct {
	TaskID               string
	BoundaryKind         string   // "compaction", "restart", "none"
	EvidenceMarkers      []string // strings whose presence proves F survived
	VisibleHistory       string   // ordinary history text after the boundary
	Summary              string   // compaction summary text after the boundary
	RelevantToDecision   bool     // a hidden test requires F afterwards
	Capturable           bool     // F is capturable from existing events
	WorkspaceRecoverable bool     // F re-derivable from files/checks
	RecoveryCostEstimate int      // requests to re-derive (measured where possible)
	SummaryPresent       bool     // a non-empty summary exists (informational)
	HistoryTruncated     bool     // ordinary history was shortened (informational)
}

// EligibilityReport is the gate verdict.
type EligibilityReport struct {
	TaskID            string   `json:"task_id"`
	Eligible          bool     `json:"eligible"`
	FailedCriteria    []string `json:"failed_criteria,omitempty"`
	Notes             []string `json:"notes,omitempty"`
	SurvivingMarkers  []string `json:"surviving_markers,omitempty"`
	RecoverableNote   string   `json:"recoverable_note,omitempty"`
	RecoveryCostGuess int      `json:"recovery_cost_estimate,omitempty"`
}

// EvaluateEligibility applies the gate. All failing criteria are reported;
// eligibility requires every one to pass.
func EvaluateEligibility(in EligibilityInput) EligibilityReport {
	report := EligibilityReport{TaskID: in.TaskID}
	fail := func(criterion, note string) {
		report.FailedCriteria = append(report.FailedCriteria, criterion)
		if note != "" {
			report.Notes = append(report.Notes, note)
		}
	}
	if in.BoundaryKind != "compaction" && in.BoundaryKind != "restart" {
		fail("no-boundary", "no compaction or restart occurred")
	}
	if !in.RelevantToDecision {
		fail("no-dependence", "no subsequent coded decision requires the prior finding")
	}
	if !in.Capturable {
		fail("not-capturable", "the finding is not capturable from tool, user, or runtime events")
	}
	if len(in.EvidenceMarkers) == 0 {
		fail("markers-undefined", "no evidence markers defined; retention cannot be checked")
	} else {
		var surviving []string
		for _, marker := range in.EvidenceMarkers {
			if marker == "" {
				continue
			}
			if strings.Contains(in.VisibleHistory, marker) || strings.Contains(in.Summary, marker) {
				surviving = append(surviving, marker)
			}
		}
		if len(surviving) > 0 {
			sort.Strings(surviving)
			report.SurvivingMarkers = surviving
			fail("evidence-retained", "prior evidence survives in history or summary")
		}
	}
	if in.WorkspaceRecoverable {
		report.RecoverableNote = "finding is re-derivable from workspace files or checks"
		report.RecoveryCostGuess = in.RecoveryCostEstimate
	} else {
		report.RecoverableNote = "finding is not recoverable from workspace files alone"
	}
	report.Eligible = len(report.FailedCriteria) == 0
	return report
}

// SessionEvidence extracts post-boundary visible history and summary text
// from a trial workspace session file and its meta sidecar. History is the
// concatenation of user, assistant (text only, no tool-call arguments), and
// tool message content; the summary comes from the meta file when present.
func SessionEvidence(sessionPath, metaPath string) (history, summary string, err error) {
	data, err := readSessionLines(sessionPath)
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	for _, record := range data {
		role, _ := record["role"].(string)
		if role != "user" && role != "assistant" && role != "tool" {
			continue
		}
		content, _ := record["content"].(string)
		if content == "" {
			continue
		}
		b.WriteString(content)
		b.WriteString("\n")
	}
	if metaPath != "" {
		if meta, metaErr := readJSONFile(metaPath); metaErr == nil {
			if text, ok := meta["summary"].(string); ok {
				summary = text
			}
		}
	}
	return b.String(), summary, nil
}
