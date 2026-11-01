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

// --- Agent-report review (HAL 444: review without implementing) ---

// WorkReport is an implementing agent's self-report plus whatever
// supporting evidence is available. Evidence is optional; its absence is
// itself a finding (see Assessment.SelfReportOnly).
type WorkReport struct {
	Objectives   []string
	Acceptance   []string
	Summary      string
	TestsPassed  []string
	TestsFailed  []string
	ChangedFiles []string
}

// Assessment is the deterministic outcome of reviewing a WorkReport. It
// distinguishes what the evidence supports from what is merely claimed.
// Nothing here is an independent verification: without tests or diffs,
// the strongest statement available is "self-reported".
type Assessment struct {
	Verified       []string
	Unverified     []string
	Deviations     []string
	Risks          []string
	Actions        []string
	FollowUp       string
	SelfReportOnly bool
}

// reportMatchThreshold is the fraction of a criterion's significant
// tokens that must appear in a text for the criterion to count as
// addressed by that text.
const reportMatchThreshold = 0.5

var fileTokenPattern = regexp.MustCompile(`[A-Za-z0-9_][\w\-./]*\.\w+`)

// reportTokens normalizes text to significant stems: lowercased,
// split on non-alphanumerics and camelCase boundaries, short tokens
// dropped, and a tiny deterministic stemmer (plurals, -ed, -ing) so
// "TestCanaryDetectFiveMinutes" matches the criterion "bad canaries
// are detected within five minutes". Doubling ("run"/"running") and
// synonyms ("twice"/"two") are out of scope by design.
var reportSplitPattern = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// splitCamel cuts s at lower→Upper and acronym→Word boundaries
// (TestCanaryDetect, HTTPSProxy) without lookahead, which RE2 lacks.
func splitCamel(s string) []string {
	var parts []string
	runes := []rune(s)
	start := 0
	cut := func(end int) {
		if end > start {
			parts = append(parts, string(runes[start:end]))
		}
		start = end
	}
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		lowerToUpper := prev >= 'a' && prev <= 'z' && cur >= 'A' && cur <= 'Z'
		acronymToWord := i+1 < len(runes) && prev >= 'A' && prev <= 'Z' &&
			cur >= 'A' && cur <= 'Z' && runes[i+1] >= 'a' && runes[i+1] <= 'z'
		if lowerToUpper || acronymToWord {
			cut(i)
		}
	}
	cut(len(runes))
	return parts
}

func reportTokens(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, chunk := range reportSplitPattern.Split(text, -1) {
		for _, tok := range splitCamel(chunk) {
			lowered := strings.ToLower(tok)
			if len(lowered) < 3 {
				continue
			}
			out[stemToken(lowered)] = struct{}{}
		}
	}
	return out
}

func stemToken(tok string) string {
	if strings.HasSuffix(tok, "ies") && len(tok) > 5 {
		return tok[:len(tok)-3] + "y"
	}
	if strings.HasSuffix(tok, "s") && len(tok)-1 >= 3 && !strings.HasSuffix(tok, "ss") {
		tok = tok[:len(tok)-1]
	}
	// Strip -ing/-ed only when a usable stem remains. Consonant doubling
	// ("run"/"running") and synonyms are out of scope by design.
	for _, suffix := range []string{"ing", "ed"} {
		if strings.HasSuffix(tok, suffix) && len(tok)-len(suffix) >= 3 {
			tok = tok[:len(tok)-len(suffix)]
			break
		}
	}
	return tok
}

func reportOverlap(criterion string, textTokens map[string]struct{}) float64 {
	crit := reportTokens(criterion)
	if len(crit) == 0 {
		return 0
	}
	hit := 0
	for tok := range crit {
		if _, ok := textTokens[tok]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(crit))
}

func reportAddresses(criterion, text string) bool {
	return reportOverlap(criterion, reportTokens(text)) >= reportMatchThreshold
}

// AssessReport reviews an implementation report against its objectives
// and acceptance criteria. It is deliberately shallow and deterministic:
// it checks addressal, corroboration, and consistency, and never claims
// independent verification from a self-report alone.
func AssessReport(r WorkReport) Assessment {
	var a Assessment
	summary := strings.TrimSpace(r.Summary)
	if summary == "" {
		a.Deviations = append(a.Deviations, "No implementation report provided.")
		a.Actions = append(a.Actions, "Ask the implementing agent for a written report: what changed, what was tested, what remains.")
	}
	for _, objective := range nonEmpty(r.Objectives) {
		if summary != "" && !reportAddresses(objective, summary) {
			a.Deviations = append(a.Deviations, fmt.Sprintf("Objective not addressed in report: %q.", objective))
			a.Actions = append(a.Actions, fmt.Sprintf("Ask for explicit status on objective %q (done, partial, or dropped with reason).", objective))
		}
	}
	if len(nonEmpty(r.Acceptance)) == 0 {
		a.Risks = append(a.Risks, "No acceptance criteria were provided, so there is nothing to assess completion against.")
		a.Actions = append(a.Actions, "Define acceptance criteria before judging the work done.")
	}
	evidenceTokens := map[string]struct{}{}
	for _, t := range append(append([]string{}, r.TestsPassed...), r.TestsFailed...) {
		for tok := range reportTokens(t) {
			evidenceTokens[tok] = struct{}{}
		}
	}
	for _, f := range r.ChangedFiles {
		for tok := range reportTokens(f) {
			evidenceTokens[tok] = struct{}{}
		}
	}
	hasEvidence := len(nonEmpty(r.TestsPassed))+len(nonEmpty(r.TestsFailed))+len(nonEmpty(r.ChangedFiles)) > 0
	a.SelfReportOnly = summary != "" && !hasEvidence
	for _, criterion := range nonEmpty(r.Acceptance) {
		if summary == "" || !reportAddresses(criterion, summary) {
			a.Deviations = append(a.Deviations, fmt.Sprintf("Acceptance criterion not addressed in report: %q.", criterion))
			a.Actions = append(a.Actions, fmt.Sprintf("Address or explicitly defer: %q.", criterion))
			a.Risks = append(a.Risks, fmt.Sprintf("Work may ship without the agreed outcome %q.", criterion))
			continue
		}
		corroborated := ""
		for _, t := range nonEmpty(r.TestsPassed) {
			if reportAddresses(criterion, t) {
				corroborated = t
				break
			}
		}
		switch {
		case corroborated != "" && !a.SelfReportOnly:
			a.Verified = append(a.Verified, fmt.Sprintf("Acceptance %q corroborated by passing test %q.", criterion, corroborated))
		case reportOverlap(criterion, evidenceTokens) >= reportMatchThreshold && !a.SelfReportOnly:
			a.Unverified = append(a.Unverified, fmt.Sprintf("Acceptance %q is described and change-evidenced, but no passing test confirms the outcome.", criterion))
			a.Actions = append(a.Actions, fmt.Sprintf("Provide a passing test for %q; change evidence alone does not confirm behavior.", criterion))
		default:
			a.Unverified = append(a.Unverified, fmt.Sprintf("Acceptance %q is self-reported only.", criterion))
			a.Actions = append(a.Actions, fmt.Sprintf("Provide test results or diffs supporting %q.", criterion))
		}
	}
	for _, t := range nonEmpty(r.TestsFailed) {
		a.Deviations = append(a.Deviations, fmt.Sprintf("Failing test: %q.", t))
		a.Actions = append(a.Actions, fmt.Sprintf("Fix %q and re-run before accepting the work.", t))
		a.Risks = append(a.Risks, "Known test failures ship if the work is accepted as-is.")
	}
	changed := map[string]bool{}
	for _, f := range nonEmpty(r.ChangedFiles) {
		changed[strings.ToLower(strings.TrimSpace(f))] = true
	}
	for _, claimed := range fileTokenPattern.FindAllString(summary, -1) {
		if !changed[strings.ToLower(claimed)] {
			a.Deviations = append(a.Deviations, fmt.Sprintf("Report claims a change to %q with no supporting file record.", claimed))
			a.Actions = append(a.Actions, fmt.Sprintf("Reconcile the claimed change to %q with the provided diffs or file list.", claimed))
		}
	}
	if a.SelfReportOnly {
		a.Risks = append(a.Risks, "Assessment is based solely on the agent's self-report: nothing here is independently verified.")
		a.Actions = append(a.Actions, "Treat every claim above as unverified until tests or diffs arrive.")
	}
	if len(a.Deviations)+len(a.Unverified) > 0 {
		objective := "Close the review gaps in the reported work."
		if first := firstNonEmpty(r.Objectives); first != "" {
			objective = "Close the review gaps in: " + firstSentence(first)
		}
		a.FollowUp = Build(Brief{
			Objective:    objective,
			Requirements: append([]string{}, a.Actions...),
			Acceptance:   unaddressedCriteria(r, summary),
			Audience:     "a general-purpose specialist agent",
		})
	}
	return a
}

func unaddressedCriteria(r WorkReport, summary string) []string {
	var out []string
	for _, criterion := range nonEmpty(r.Acceptance) {
		if summary == "" || !reportAddresses(criterion, summary) {
			out = append(out, criterion)
		}
	}
	if len(out) == 0 {
		out = append(out, "All review deviations resolved; previously failing tests pass.")
	}
	return out
}

func firstNonEmpty(items []string) string {
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// Render formats the assessment as Markdown: verified findings first,
// then claims, deviations, risks, actions, and the suggested follow-up.
// Empty sections are omitted; the self-report disclaimer is always
// present when no evidence was available.
func (a Assessment) Render() string {
	var sb strings.Builder
	sb.WriteString("# Work review\n")
	writeAssessmentList := func(title string, items []string) {
		items = nonEmpty(items)
		if len(items) == 0 {
			return
		}
		sb.WriteString("\n## " + title + "\n\n")
		for _, item := range items {
			sb.WriteString("- " + item + "\n")
		}
	}
	writeAssessmentList("Verified findings", a.Verified)
	writeAssessmentList("Unverified claims", a.Unverified)
	writeAssessmentList("Deviations", a.Deviations)
	writeAssessmentList("Risks", a.Risks)
	writeAssessmentList("Recommended corrective actions", a.Actions)
	if strings.TrimSpace(a.FollowUp) != "" {
		sb.WriteString("\n## Suggested follow-up brief\n\n" + strings.TrimSpace(a.FollowUp) + "\n")
	}
	if a.SelfReportOnly {
		sb.WriteString("\n_Note: no tests, diffs, or file records were provided. " +
			"This assessment is based solely on the agent's self-report; nothing above is independently verified._\n")
	}
	return sb.String()
}
