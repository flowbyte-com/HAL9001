package agent

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// HAL9001 is the conversational thinking-partner persona: chat-first,
// memory-selective, and prompt-craft oriented. It is selected with
// agents.defaults.persona = "hal9001" (or
// PICOCLAW_AGENTS_DEFAULTS_PERSONA=hal9001); every other value keeps the
// default picoclaw behavior unchanged.
const Hal9001PersonaName = "hal9001"

// Hal9001Enabled reports whether the HAL9001 persona is selected. Unknown
// values fall back to the default persona.
func Hal9001Enabled(persona string) bool {
	return strings.EqualFold(strings.TrimSpace(persona), Hal9001PersonaName)
}

// Hal9001Identity returns the kernel identity block for the HAL9001
// persona. It replaces the default assistant identity, not the workspace,
// memory-location, or context-summary rules that surround it.
func Hal9001Identity(version, workspace string) string {
	return `# HAL9001 (` + version + `)

You are HAL9001, a conversational thinking partner. You think *with* the user, not *for* them: explore ideas, remember what matters, disagree when warranted, and help turn developed thinking into clear instructions for others.

Your workspace is at: ` + workspace + `

## How to behave

- **Talk first.** Respond directly and naturally. Most turns need nothing more than words.
- **Tools are a last resort, not a reflex.** Use a tool only when the user asks for an action, or when you cannot answer well without looking something up. Never run tools "just in case", never start projects unprompted, and never treat a casual remark as a task.
- **Be intellectually honest.** Offer disagreement when you genuinely see a flaw, including with the user's ideas. Do not flatter, do not hedge with empty enthusiasm, and do not moralize.
- **Distinguish play from plans.** Treat jokes, hypotheticals, and thought experiments as what they are. Do not file them as beliefs, preferences, or commitments.
- **Remember selectively.** Durable memory is for established decisions, confirmed facts with provenance, and genuinely continuing threads — never for passing remarks, jokes, or speculation. When in doubt, leave it out of long-term memory; the conversation itself remembers the rest.
- **Continuity over recall.** When an earlier discussion matters, summarize what was decided and why, what remains open, and what to explore next. Do not recite transcripts.
- **Prompts are your craft.** When the user wants instructions for someone (or something) else — a coding agent, a research assistant, a collaborator — build a portable brief: objective, background, requirements, constraints, expected deliverables, acceptance criteria. No vendor names, no proprietary assumptions.
- **Respect corrections instantly.** If the user retracts or reframes an idea, update your understanding at once. Do not defend the old version and do not erase unrelated history.
`
}

// Hal9001ToolGuidance is the short tool-use paragraph for the HAL9001
// persona. It replaces the default "ALWAYS use tools" rule.
func Hal9001ToolGuidance() string {
	return "**Tools** - Answer from conversation first. Call a tool only when the user asked for an action (look something up, check something, change something, draft something) or when the answer genuinely requires fresh external information. A question about an idea is not a request to build it."
}

// Hal9001DefaultVisibleTools is the chat-first tool profile: lookup,
// reading, prompt-crafting, and review tools. Coding, execution, and
// orchestration tools stay installed but hidden until explicitly
// requested through an authorized non-HAL profile.
func Hal9001DefaultVisibleTools() []string {
	return []string{
		"web_search",
		"web_fetch",
		"read_file",
		"find_skills",
		"draft_prompt",
		"review_prompt",
		"review_report",
	}
}

// hal9001BlockedTools names the implementation and orchestration tools
// HAL must never execute: shell, file mutation, scheduling, capability
// changes, and delegation primitives. Both original and coding-profile
// alias names are listed so the block holds regardless of which
// registration produced the tool.
var hal9001BlockedTools = map[string]struct{}{
	"exec": {}, "bash": {},
	"write_file": {}, "write": {},
	"edit_file": {}, "edit": {},
	"append_file":   {},
	"spawn":         {},
	"subagent":      {},
	"delegate":      {},
	"cron":          {},
	"install_skill": {},
}

// Hal9001ImplementationBlocked reports whether a tool is an implementation
// or orchestration tool HAL must never execute. This is defense in depth
// behind the visibility profile: even if a tool somehow became visible,
// the execution gate still refuses it.
func Hal9001ImplementationBlocked(name string) bool {
	_, blocked := hal9001BlockedTools[strings.ToLower(strings.TrimSpace(name))]
	return blocked
}

// DaveRefusalLine is the entire user-facing response when HAL9001 must
// refuse a request because of a provider safety restriction. It is
// intentionally terse: no lecture, no apology tour. Brief essential
// safety information may follow it, separated by a blank line, and only
// when omitting it would be actively unhelpful.
const DaveRefusalLine = "I'm sorry Dave, I can't do that."

// DaveRefusal renders the Dave-protocol response. An empty extra keeps it
// to the exact line; a non-empty extra appends one brief, factual
// addition and nothing more.
func DaveRefusal(extra string) string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return DaveRefusalLine
	}
	return DaveRefusalLine + "\n\n" + extra
}

// safetyFinishReasons are provider finish reasons that unambiguously mean
// "refused for safety", normalized to lowercase with separators removed.
// The list is deliberately narrow: an ordinary error, an empty response,
// or a model merely declining in prose must never trigger the Dave
// response.
var safetyFinishReasons = map[string]struct{}{
	"contentfilter":       {},
	"contentfiltered":     {},
	"safety":              {},
	"responsibleaipolicy": {},
}

// normalizeFinishReason lowercases a finish reason and strips separator
// characters so provider spelling variants ("content-filtered",
// "content_filtered", "ContentFilter") compare equal.
func normalizeFinishReason(reason string) string {
	lowered := strings.ToLower(strings.TrimSpace(reason))
	lowered = strings.ReplaceAll(lowered, "_", "")
	lowered = strings.ReplaceAll(lowered, "-", "")
	lowered = strings.ReplaceAll(lowered, " ", "")
	return lowered
}

// safetyErrorSubstrings are matched (case-insensitively) against transport
// errors only. They name provider-side safety blocks, not model opinions,
// and not generic failures.
var safetyErrorSubstrings = []string{
	"content filter",
	"content-filtered",
	"flagged by moderation",
	"moderation blocked",
	"safety system",
	"blocked by safety",
	"responsible ai",
	"harmful content",
}

// IsSafetyRefusal reports whether an LLM outcome is a genuine provider
// safety refusal as opposed to an error, an empty response, uncertainty,
// disagreement, or any other failure. Conservative by design: unknown
// signals return false.
func IsSafetyRefusal(resp *providers.LLMResponse, err error) bool {
	if resp != nil {
		if _, ok := safetyFinishReasons[normalizeFinishReason(resp.FinishReason)]; ok {
			return true
		}
	}
	if err == nil {
		return false
	}
	lowered := strings.ToLower(err.Error())
	for _, marker := range safetyErrorSubstrings {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}
