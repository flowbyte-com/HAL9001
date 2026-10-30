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
