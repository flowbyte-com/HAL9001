// Package promptwork implements HAL9001's Prompt Workshop as deterministic,
// vendor-neutral building blocks: a portable brief renderer and a
// rule-based prompt reviewer. Both are pure functions over explicit input
// so they are fully testable without a model. They deliberately avoid
// templates that force every prompt into one shape: empty sections are
// omitted, and a four-sentence brief renders as four sentences plus
// headers, not as a padded form.
package promptwork

import (
	"fmt"
	"regexp"
	"strings"
)

// Brief is the structured content of a portable task brief. Only Objective
// is required; every other field is omitted from the output when empty.
type Brief struct {
	Objective    string
	Background   string
	Requirements []string
	Constraints  []string
	Evidence     []string
	Deliverables []string
	Acceptance   []string
	Audience     string
}

// Validate reports missing or weak parts of a brief as human-readable
// notes. An empty result means the brief is complete enough to render.
func (b Brief) Validate() []string {
	var notes []string
	if strings.TrimSpace(b.Objective) == "" {
		notes = append(notes, "missing objective: state what should be accomplished in one or two sentences")
	}
	if len(nonEmpty(b.Requirements)) == 0 && len(nonEmpty(b.Deliverables)) == 0 {
		notes = append(notes, "missing requirements and deliverables: say what is needed and what should come back")
	}
	if len(nonEmpty(b.Acceptance)) == 0 {
		notes = append(notes, "missing acceptance criteria: say how success will be recognized")
	}
	return notes
}

func nonEmpty(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// Build renders a portable Markdown brief. The output names no vendors,
// runtimes, or proprietary tools; it describes the work so any capable
// recipient — model, agent, researcher, or human — can carry it out.
func Build(b Brief) string {
	var sb strings.Builder
	objective := strings.TrimSpace(b.Objective)
	if objective == "" {
		objective = "Untitled task"
	}
	sb.WriteString("# Task brief: " + firstSentence(objective) + "\n")
	if audience := strings.TrimSpace(b.Audience); audience != "" {
		sb.WriteString("\n_Intended recipient: " + audience + "_\n")
	}
	sb.WriteString("\n## Objective\n\n" + objective + "\n")
	if background := strings.TrimSpace(b.Background); background != "" {
		sb.WriteString("\n## Background\n\n" + background + "\n")
	}
	writeList := func(title string, items []string) {
		items = nonEmpty(items)
		if len(items) == 0 {
			return
		}
		sb.WriteString("\n## " + title + "\n\n")
		for _, item := range items {
			sb.WriteString("- " + item + "\n")
		}
	}
	writeList("Requirements", b.Requirements)
	writeList("Constraints and exclusions", b.Constraints)
	writeList("Known evidence and open questions", b.Evidence)
	writeList("Expected deliverables", b.Deliverables)
	writeList("Acceptance criteria", b.Acceptance)
	return strings.TrimRight(sb.String(), "\n") + "\n"
}

func firstSentence(text string) string {
	if index := strings.IndexAny(text, ".!?\n"); index >= 0 {
		if sentence := strings.TrimSpace(text[:index]); sentence != "" {
			return sentence
		}
	}
	if len([]rune(text)) > 120 {
		return string([]rune(text)[:117]) + "..."
	}
	return text
}

// Severity ranks a review finding.
type Severity string

const (
	// SeverityGap marks structural problems: missing sections, unclear
	// ownership, untestable outcomes.
	SeverityGap Severity = "gap"
	// SeverityAmbiguity marks wording likely to be interpreted in more
	// than one way.
	SeverityAmbiguity Severity = "ambiguity"
	// SeverityRisk marks content that could leak secrets, grant unclear
	// authority, or otherwise cause harm if followed literally.
	SeverityRisk Severity = "risk"
)

// Finding is one review observation with a concrete fix suggestion.
type Finding struct {
	Severity   Severity `json:"severity"`
	Section    string   `json:"section"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion"`
}

var hedgePatterns = []struct {
	pattern *regexp.Regexp
	example string
}{
	{regexp.MustCompile(`(?i)\bas appropriate\b`), "as appropriate"},
	{regexp.MustCompile(`(?i)\betc\b`), "etc."},
	{regexp.MustCompile(`(?i)\bif+ needed\b`), "if needed"},
	{regexp.MustCompile(`(?i)\bwhere (?:applicable|appropriate|necessary)\b`), "where applicable"},
	{regexp.MustCompile(`(?i)\btry to\b`), "try to"},
	{regexp.MustCompile(`(?i)\bas soon as possible\b`), "as soon as possible"},
	{regexp.MustCompile(`(?i)\bwhatever (?:you|it) (?:thinks|seems)\b`), "whatever seems best"},
}

// vendorTokens flags product-specific language that harms portability.
// Matching is case-insensitive word matching to avoid false hits inside
// ordinary words.
var vendorTokens = []string{
	"claude", "anthropic", "openai", "chatgpt", "gpt-4", "gpt-4o", "gpt-3",
	"gemini", "copilot", "cursor", "windsurf", "codex", "deepseek",
	"qwen", "llama", "mistral", "grok", "kimi", "glm",
	"picoclaw", "deepclaw", "hal9001",
}

// Review lints a prompt for ambiguity, structural gaps, and risks. It is
// deterministic and deliberately shallow: it flags what can be checked
// mechanically and never claims to judge intent or quality.
func Review(text string) []Finding {
	var findings []Finding
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return []Finding{{
			Severity:   SeverityGap,
			Section:    "whole prompt",
			Message:    "The prompt is empty.",
			Suggestion: "Write down the objective first, even as a single sentence.",
		}}
	}
	lowered := strings.ToLower(trimmed)
	seenHedge := map[string]bool{}
	for _, hedge := range hedgePatterns {
		if hedge.pattern.MatchString(trimmed) && !seenHedge[hedge.example] {
			seenHedge[hedge.example] = true
			findings = append(findings, Finding{
				Severity:   SeverityAmbiguity,
				Section:    "wording",
				Message:    fmt.Sprintf("Hedge phrase %q can be read in more than one way.", hedge.example),
				Suggestion: "Replace it with the concrete condition or delete it.",
			})
		}
	}
	if !hasHeading(trimmed, []string{"objective", "goal", "task", "purpose"}) {
		findings = append(findings, Finding{
			Severity:   SeverityGap,
			Section:    "structure",
			Message:    "No objective section found.",
			Suggestion: "Add an Objective section stating what should be accomplished in one or two sentences.",
		})
	}
	if !hasHeading(trimmed, []string{"accept", "done", "success", "verif", "criteria"}) {
		findings = append(findings, Finding{
			Severity:   SeverityGap,
			Section:    "structure",
			Message:    "No acceptance criteria found.",
			Suggestion: "Add an Acceptance criteria section so the recipient knows when the work is done.",
		})
	}
	for _, token := range vendorTokens {
		if wordBoundaryContains(lowered, strings.ToLower(token)) {
			findings = append(findings, Finding{
				Severity:   SeverityRisk,
				Section:    "portability",
				Message:    fmt.Sprintf("Vendor-specific reference %q limits where this prompt can be used.", token),
				Suggestion: "Describe the capability generically (e.g. \"a general-purpose assistant\") instead of naming a product.",
			})
			break
		}
	}
	if strings.Contains(lowered, "password") || strings.Contains(lowered, "api_key") || strings.Contains(lowered, "api key") || strings.Contains(lowered, "secret") {
		findings = append(findings, Finding{
			Severity:   SeverityRisk,
			Section:    "safety",
			Message:    "The prompt mentions credentials or secrets.",
			Suggestion: "Reference secret names or placeholders instead of values, and state that secrets must never be pasted into prompts.",
		})
	}
	return findings
}

func hasHeading(text string, keywords []string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		lowered := strings.ToLower(trimmed)
		for _, keyword := range keywords {
			if strings.Contains(lowered, keyword) {
				return true
			}
		}
	}
	return false
}

func wordBoundaryContains(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	// Walk occurrences one at a time. strings.Index returns -1 on a miss,
	// which must end the scan: reusing it arithmetically would recompute
	// the same index and loop forever.
	start := 0
	for start <= len(haystack) {
		rel := strings.Index(haystack[start:], needle)
		if rel < 0 {
			return false
		}
		i := start + rel
		beforeOK := i == 0 || !isWordChar(haystack[i-1])
		after := i + len(needle)
		afterOK := after >= len(haystack) || !isWordChar(haystack[after])
		if beforeOK && afterOK {
			return true
		}
		start = i + 1
		if start >= len(haystack) {
			return false
		}
	}
	return false
}

func isWordChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
