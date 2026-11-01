package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sipeed/picoclaw/pkg/promptwork"
)

// DraftPromptTool renders a portable, vendor-neutral task brief from
// structured fields. It is a thin adapter over promptwork.Build: no model,
// no I/O, deterministic output. Registered only when the HAL9001 persona
// is enabled.
type DraftPromptTool struct{}

func (t *DraftPromptTool) Name() string { return "draft_prompt" }

func (t *DraftPromptTool) Description() string {
	return "Draft a portable task brief (objective, background, requirements, " +
		"constraints, deliverables, acceptance criteria) for handing work to " +
		"another agent, researcher, or collaborator. Returns Markdown with no " +
		"vendor-specific assumptions. Use when the user wants instructions " +
		"drafted for someone (or something) else."
}

func (t *DraftPromptTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"objective":    map[string]any{"type": "string", "description": "What should be accomplished, in one or two sentences (required)."},
			"background":   map[string]any{"type": "string", "description": "Context the recipient needs."},
			"requirements": map[string]any{"type": "string", "description": "What is needed; one item per line."},
			"constraints":  map[string]any{"type": "string", "description": "Exclusions and limits; one item per line."},
			"evidence":     map[string]any{"type": "string", "description": "Known facts and open questions; one item per line."},
			"deliverables": map[string]any{"type": "string", "description": "What should come back; one item per line."},
			"acceptance":   map[string]any{"type": "string", "description": "How success will be recognized; one item per line."},
			"audience":     map[string]any{"type": "string", "description": "Intended recipient, e.g. \"a general-purpose coding agent\"."},
		},
		"required": []string{"objective"},
	}
}

func stringListArg(args map[string]any, key string) []string {
	raw, _ := args[key].(string)
	if strings.TrimSpace(raw) == "" {
		if list, ok := args[key].([]string); ok {
			return list
		}
		if anyList, ok := args[key].([]any); ok {
			out := make([]string, 0, len(anyList))
			for _, item := range anyList {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
		return nil
	}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			trimmed = strings.TrimPrefix(trimmed, "- ")
			trimmed = strings.TrimPrefix(trimmed, "* ")
			out = append(out, strings.TrimSpace(trimmed))
		}
	}
	return out
}

func (t *DraftPromptTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	objective, _ := args["objective"].(string)
	brief := promptwork.Brief{
		Objective:    objective,
		Background:   workshopStringArg(args, "background"),
		Requirements: stringListArg(args, "requirements"),
		Constraints:  stringListArg(args, "constraints"),
		Evidence:     stringListArg(args, "evidence"),
		Deliverables: stringListArg(args, "deliverables"),
		Acceptance:   stringListArg(args, "acceptance"),
		Audience:     workshopStringArg(args, "audience"),
	}
	out := promptwork.Build(brief)
	if notes := brief.Validate(); len(notes) > 0 {
		out += "\n---\nIncomplete brief — consider adding: " + strings.Join(notes, "; ") + ".\n"
	}
	return NewToolResult(out)
}

func workshopStringArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// ReviewPromptTool lints a prompt for ambiguity, structural gaps, and
// portability risks. Thin adapter over promptwork.Review: deterministic,
// rule-based, never a judgment of intent. Registered only under HAL9001.
type ReviewPromptTool struct{}

func (t *ReviewPromptTool) Name() string { return "review_prompt" }

func (t *ReviewPromptTool) Description() string {
	return "Review a draft prompt for ambiguity (hedge words), structural gaps " +
		"(missing objective or acceptance criteria), and portability risks " +
		"(vendor-specific references, secrets). Returns a list of findings with " +
		"fix suggestions. Use before handing a drafted brief to its recipient."
}

func (t *ReviewPromptTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string", "description": "The draft prompt text to review (required)."},
		},
		"required": []string{"text"},
	}
}

func (t *ReviewPromptTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	text, _ := args["text"].(string)
	findings := promptwork.Review(text)
	if len(findings) == 0 {
		return NewToolResult("No issues found. The prompt states an objective, defines acceptance, and avoids vendor-specific or risky language.")
	}
	var sb strings.Builder
	sb.WriteString("Findings (" + itoa(len(findings)) + "):\n")
	encoded, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		for _, f := range findings {
			sb.WriteString("- [" + string(f.Severity) + "] " + f.Section + ": " + f.Message + " Fix: " + f.Suggestion + "\n")
		}
		return NewToolResult(sb.String())
	}
	sb.WriteString(string(encoded) + "\n")
	return NewToolResult(sb.String())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
