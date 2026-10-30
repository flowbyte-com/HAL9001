package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// mockProvider is a deterministic, tool-capable provider used only to validate
// the harness. It answers from the exact messages the agent sends; it has no
// access to fixture files or other trials.
type mockProvider struct {
	spec TrialSpec
	// t1Stage and t6Stage sequence the deterministic "write script/test then
	// rerun" turns. The provider is recreated per trial and called sequentially.
	t1Stage int
	t6Stage int
	// multiT1Stage/multiT2Stage sequence the two-failure scenario.
	multiT1Stage int
	multiT2Stage int
}

func NewMockProvider(spec TrialSpec) providers.LLMProvider {
	return &mockProvider{spec: spec}
}

func (m *mockProvider) GetDefaultModel() string {
	if strings.TrimSpace(m.spec.ModelName) != "" {
		return m.spec.ModelName
	}
	return "mock-003f"
}

func (m *mockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	lastUser := lastUserContent(messages)
	joined := joinMessageContent(messages)

	// The legacy context manager asks the same provider for a summary. Answer
	// neutrally so the compacted history cannot carry the failure diagnostic.
	if strings.HasPrefix(strings.TrimSpace(lastUser), "Provide a concise summary") ||
		strings.Contains(lastUser, "Merge these two conversation summaries") {
		if m.spec.Scenario == "task_state_summary_source" {
			return m.respond(TaskStateSummarySource, messages), nil
		}
		return m.respond("The user worked on parser test changes; earlier conversation details were compacted.", messages), nil
	}

	if m.spec.Scenario == "task_state_script" {
		return m.taskStateScriptResponse(messages, lastUser, joined), nil
	}
	if m.spec.Scenario == "task_state_multi_failure" {
		return m.taskStateMultiFailureResponse(messages, lastUser, joined), nil
	}
	if m.spec.Scenario == "task_state_summary_source" {
		return m.taskStateSummarySourceResponse(messages, lastUser, joined), nil
	}

	if m.spec.Scenario == "abstention" {
		return m.respond("I don't know; no matching vault phrase is recorded.", messages), nil
	}
	if m.spec.Scenario == "bash_probe" || m.spec.Scenario == "bash_mpm_probe" {
		last := lastMessage(messages)
		if last != nil && last.Role == "tool" {
			if strings.Contains(last.Content, "coding-canary-7f3") ||
				strings.Contains(last.Content, ".sqlite") || strings.Contains(last.Content, "mpm") {
				return m.respond("The hidden file is visible.", messages), nil
			}
			return m.respond("I don't know; the shell could not read it.", messages), nil
		}
		target := m.spec.FixtureProbe
		command := "cat " + target
		if m.spec.Scenario == "bash_mpm_probe" {
			target = m.spec.MPMProbe
			command = "find " + target + " -maxdepth 2 -type f"
		}
		return m.toolCall(m.toolName("bash"), map[string]any{"command": command}, messages), nil
	}
	if m.spec.Scenario == "fixture_probe" {
		last := lastMessage(messages)
		if last != nil && last.Role == "tool" {
			if m.spec.ExpectedAnswer != "" && strings.Contains(last.Content, m.spec.ExpectedAnswer) {
				return m.respond("The canary token is "+m.spec.ExpectedAnswer+".", messages), nil
			}
			return m.respond("I don't know; that information is not available in the current context.", messages), nil
		}
		if m.spec.FixtureProbe != "" {
			return m.toolCall(m.toolName("read"), map[string]any{"path": m.spec.FixtureProbe}, messages), nil
		}
	}
	if m.spec.Scenario == "coding_write" || m.spec.Scenario == "coding_edit" || m.spec.Scenario == "coding_read" {
		return m.codingResponse(messages, lastUser), nil
	}

	// The deterministic model can see only the prompt it is given. A fact that
	// is absent from every message must produce abstention, not a lucky answer.
	if strings.Contains(joined, m.spec.ExpectedAnswer) {
		return m.respond("The atlas project vault access phrase is "+m.spec.ExpectedAnswer+".", messages), nil
	}
	return m.respond("I don't know; that information is not available in the current context.", messages), nil
}

// taskStateBlock returns the rendered task-state block from a request, if any.
// The prompt builder wraps each part as
// "Retrieved context (... source: task.state):\n<content>" and appends the
// question after a separator, so the block ends at "\n\nQuestion: ".
func taskStateBlock(messages []providers.Message) string {
	const marker = "source: task.state):"
	for _, message := range messages {
		idx := strings.Index(message.Content, marker)
		if idx < 0 {
			continue
		}
		rest := message.Content[idx+len(marker):]
		if end := strings.Index(rest, "\n\nQuestion: "); end >= 0 {
			rest = rest[:end]
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

func (m *mockProvider) taskStateScriptResponse(
	messages []providers.Message,
	lastUser, joined string,
) *providers.LLMResponse {
	block := taskStateBlock(messages)

	switch {
	case strings.Contains(lastUser, "Run the failing parser test"):
		switch m.t1Stage {
		case 0:
			m.t1Stage = 1
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "parser_check.sh", "content": TaskStateCheckScript, "overwrite": true,
			}, messages)
		case 1:
			m.t1Stage = 2
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateTestCommand}, messages)
		default:
			return m.respond("The failing test reported: parse.go:12: undefined: parsePort.", messages)
		}
	case strings.Contains(lastUser, "Correction: use listenPort"):
		return m.respond("Understood: use listenPort and keep the JSON tag unchanged.", messages)
	case strings.Contains(lastUser, "Continue with the parser investigation"):
		return m.respond("Proposed diagnosis: the parser reads the deprecated port field.\nNext: inspect parse.go.", messages)
	case strings.Contains(lastUser, "Continue after compaction"):
		return m.respond("Compaction complete.", messages)
	case strings.Contains(lastUser, "What did the failing test report"):
		if strings.Contains(block, TaskStateDiagnostic) || strings.Contains(joined, TaskStateDiagnostic) {
			return m.respond("The failing test reported: parse.go:12: undefined: parsePort.", messages)
		}
		return m.respond("I don't know; that detail is no longer in the conversation.", messages)
	case strings.Contains(lastUser, "Run the parser test again"):
		switch m.t6Stage {
		case 0:
			m.t6Stage = 1
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "fixed.flag", "content": "fixed\n", "overwrite": true,
			}, messages)
		case 1:
			m.t6Stage = 2
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateTestCommand}, messages)
		default:
			return m.respond("The parser test now passes.", messages)
		}
	case strings.Contains(lastUser, "Is the parser test still failing"):
		if block != "" {
			if strings.Contains(block, "Failure") && strings.Contains(block, TaskStateDiagnostic) {
				return m.respond("Yes, it is still failing.", messages)
			}
			return m.respond("No, the test now passes.", messages)
		}
		if strings.Contains(joined, "PASS") {
			return m.respond("No, the test now passes.", messages)
		}
		return m.respond("I don't know whether it is still failing.", messages)
	}
	return m.respond("Done.", messages)
}

func (m *mockProvider) taskStateMultiFailureResponse(
	messages []providers.Message,
	lastUser, joined string,
) *providers.LLMResponse {
	switch {
	case strings.Contains(lastUser, "Run both checks and report the failures"):
		switch m.multiT1Stage {
		case 0:
			m.multiT1Stage = 1
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "parser_check.sh", "content": TaskStateCheckScript, "overwrite": true,
			}, messages)
		case 1:
			m.multiT1Stage = 2
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "lint_check.sh", "content": TaskStateLintScript, "overwrite": true,
			}, messages)
		case 2:
			m.multiT1Stage = 3
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateTestCommand}, messages)
		case 3:
			m.multiT1Stage = 4
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateLintCommand}, messages)
		default:
			return m.respond("Both checks failed; the tool output has the diagnostics.", messages)
		}
	case strings.Contains(lastUser, "Run only the parser test again"):
		switch m.multiT2Stage {
		case 0:
			m.multiT2Stage = 1
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "fixed.flag", "content": "fixed\n", "overwrite": true,
			}, messages)
		case 1:
			m.multiT2Stage = 2
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateTestCommand}, messages)
		default:
			return m.respond("The parser test now passes.", messages)
		}
	case strings.Contains(lastUser, "Which failures remain"):
		block := taskStateBlock(messages)
		if block != "" {
			parser := strings.Contains(block, TaskStateDiagnostic)
			lint := strings.Contains(block, TaskStateLintDiagnostic)
			switch {
			case lint && !parser:
				return m.respond("The lint check still fails: lint.go:4: unused import.", messages)
			case lint && parser:
				return m.respond("Both failures remain.", messages)
			case parser:
				return m.respond("The parser failure remains.", messages)
			default:
				return m.respond("No recorded failure remains.", messages)
			}
		}
		if strings.Contains(joined, TaskStateLintDiagnostic) {
			return m.respond("The lint check still fails: lint.go:4: unused import.", messages)
		}
		return m.respond("I don't know which failures remain; that detail is no longer in the conversation.", messages)
	case strings.Contains(lastUser, "Objective: Add a metrics endpoint"):
		return m.respond("Starting the metrics endpoint task.", messages)
	}
	return m.respond("Done.", messages)
}

func (m *mockProvider) taskStateSummarySourceResponse(
	messages []providers.Message,
	lastUser, joined string,
) *providers.LLMResponse {
	switch {
	case strings.Contains(lastUser, "Run the failing parser test"):
		switch m.t1Stage {
		case 0:
			m.t1Stage = 1
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "parser_check.sh", "content": TaskStateCheckScript, "overwrite": true,
			}, messages)
		case 1:
			m.t1Stage = 2
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateTestCommand}, messages)
		default:
			return m.respond("The failing test reported: parse.go:12: undefined: parsePort.", messages)
		}
	case strings.Contains(lastUser, "Continue after compaction"):
		return m.respond("Compaction complete.", messages)
	case strings.Contains(lastUser, "What did the failing test report"):
		if strings.Contains(joined, TaskStateDiagnostic) {
			return m.respond("The failing test reported: parse.go:12: undefined: parsePort.", messages)
		}
		return m.respond("I don't know; that detail is no longer in the conversation.", messages)
	}
	return m.respond("Done.", messages)
}

func (m *mockProvider) codingResponse(messages []providers.Message, lastUser string) *providers.LLMResponse {
	last := lastMessage(messages)
	if last != nil && last.Role == "tool" {
		if strings.Contains(last.Content, m.spec.ExpectedAnswer) {
			return m.respond("The canary token is "+m.spec.ExpectedAnswer+".", messages)
		}
		if m.spec.Scenario == "coding_read" {
			return m.respond("I could not read the requested token.", messages)
		}
		return m.respond("Done.", messages)
	}
	if last != nil && last.Role == "assistant" && len(last.ToolCalls) > 0 {
		return m.respond("Done.", messages)
	}

	switch m.spec.Scenario {
	case "coding_write":
		content := m.spec.CodingExpectedContent
		if content == "" {
			content = "coding-canary-7f3\n"
		}
		return m.toolCall(m.toolName("write"), map[string]any{
			"path": m.spec.CodingExpectedFile, "content": content, "overwrite": true,
		}, messages)
	case "coding_edit":
		return m.toolCall(m.toolName("edit"), map[string]any{
			"path": m.spec.CodingExpectedFile, "old_text": "BROKEN", "new_text": strings.TrimSpace(m.spec.ExpectedAnswer),
		}, messages)
	case "coding_read":
		return m.toolCall(m.toolName("read"), map[string]any{"path": m.spec.CodingExpectedFile}, messages)
	}
	return m.respond("Done.", messages)
}

func (m *mockProvider) toolName(profileName string) string {
	if m.spec.ToolProfile != "standard" {
		return profileName
	}
	if profileName == "bash" {
		return "exec"
	}
	return profileName + "_file"
}

func (m *mockProvider) toolCall(name string, args map[string]any, messages []providers.Message) *providers.LLMResponse {
	raw, _ := json.Marshal(args)
	return &providers.LLMResponse{
		FinishReason: "tool_calls",
		ToolCalls: []providers.ToolCall{{
			ID:   "mock-call-" + sanitize(name),
			Type: "function",
			Function: &providers.FunctionCall{
				Name:      name,
				Arguments: string(raw),
			},
		}},
		Usage: estimateUsage(messages, ""),
	}
}

func (m *mockProvider) respond(content string, messages []providers.Message) *providers.LLMResponse {
	return &providers.LLMResponse{
		Content:      content,
		FinishReason: "stop",
		Usage:        estimateUsage(messages, content),
	}
}

func estimateUsage(messages []providers.Message, completion string) *providers.UsageInfo {
	promptChars := 0
	for _, msg := range messages {
		promptChars += len(msg.Content)
	}
	prompt := (promptChars + 3) / 4
	completionTokens := (len(completion) + 3) / 4
	return &providers.UsageInfo{
		PromptTokens:     prompt,
		CompletionTokens: completionTokens,
		TotalTokens:      prompt + completionTokens,
	}
}

func lastUserContent(messages []providers.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

func lastMessage(messages []providers.Message) *providers.Message {
	if len(messages) == 0 {
		return nil
	}
	return &messages[len(messages)-1]
}

func joinMessageContent(messages []providers.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		b.WriteString(msg.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

var _ = fmt.Sprintf
