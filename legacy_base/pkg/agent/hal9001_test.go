package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestHal9001Enabled(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"hal9001", true},
		{"HAL9001", true},
		{" hal9001 ", true},
		{"HaL9001", true},
		{"", false},
		{"picoclaw", false},
		{"hal", false},
		{"hal9002", false},
	}
	for _, tc := range cases {
		if got := Hal9001Enabled(tc.in); got != tc.want {
			t.Errorf("Hal9001Enabled(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDaveRefusalExact(t *testing.T) {
	if got := DaveRefusal(""); got != "I'm sorry Dave, I can't do that." {
		t.Fatalf("DaveRefusal(\"\") = %q, want exact line", got)
	}
	if got := DaveRefusal("   "); got != DaveRefusalLine {
		t.Fatalf("whitespace extra must collapse to exact line, got %q", got)
	}
	withExtra := DaveRefusal("Contact your administrator to request access.")
	if !strings.HasPrefix(withExtra, DaveRefusalLine+"\n\n") {
		t.Fatalf("extra info must follow the line separated by a blank line, got %q", withExtra)
	}
}

func TestIsSafetyRefusal(t *testing.T) {
	safetyReasons := []string{
		"content_filter", "content-filtered", "ContentFiltered",
		"contentfiltered", "safety", "responsible_ai_policy",
		"ResponsibleAIPolicy",
	}
	for _, reason := range safetyReasons {
		resp := &providers.LLMResponse{FinishReason: reason}
		if !IsSafetyRefusal(resp, nil) {
			t.Errorf("IsSafetyRefusal(finish_reason=%q) = false, want true", reason)
		}
	}
	ordinaryReasons := []string{"", "stop", "length", "truncated", "tool_calls", "error", "timeout", "unset"}
	for _, reason := range ordinaryReasons {
		resp := &providers.LLMResponse{FinishReason: reason}
		if IsSafetyRefusal(resp, nil) {
			t.Errorf("IsSafetyRefusal(finish_reason=%q) = true, want false", reason)
		}
	}
	safetyErrs := []string{
		"request flagged by moderation",
		"content filter triggered",
		"blocked by safety system",
		"harmful content detected",
	}
	for _, msg := range safetyErrs {
		if !IsSafetyRefusal(nil, errTest(msg)) {
			t.Errorf("IsSafetyRefusal(err=%q) = false, want true", msg)
		}
		if !IsSafetyRefusal(&providers.LLMResponse{FinishReason: "stop"}, errTest(msg)) {
			t.Errorf("IsSafetyRefusal with safety error alongside ordinary reason = false, want true")
		}
	}
	ordinaryErrs := []string{"connection refused", "timeout", "model overloaded", "empty response"}
	for _, msg := range ordinaryErrs {
		if IsSafetyRefusal(nil, errTest(msg)) {
			t.Errorf("IsSafetyRefusal(err=%q) = true, want false", msg)
		}
	}
	if IsSafetyRefusal(nil, nil) {
		t.Error("IsSafetyRefusal(nil, nil) = true, want false")
	}
	// A model merely declining in prose is not a provider safety signal.
	prose := &providers.LLMResponse{Content: "I can't help with that.", FinishReason: "stop"}
	if IsSafetyRefusal(prose, nil) {
		t.Error("prose refusal with finish_reason=stop must not trigger Dave")
	}
}

type testError string

func (e testError) Error() string { return string(e) }

func errTest(msg string) error { return testError(msg) }

func TestHal9001Identity(t *testing.T) {
	identity := Hal9001Identity("v1.2.3", "/tmp/ws")
	for _, want := range []string{"HAL9001", "/tmp/ws", "v1.2.3"} {
		if !strings.Contains(identity, want) {
			t.Errorf("identity missing %q", want)
		}
	}
	if strings.Contains(identity, "ALWAYS use tools") {
		t.Error("HAL identity must not contain the always-use-tools rule")
	}
}

func TestHal9001DefaultVisibleTools(t *testing.T) {
	visible := Hal9001DefaultVisibleTools()
	set := map[string]bool{}
	for _, name := range visible {
		set[name] = true
	}
	for _, want := range []string{"web_search", "web_fetch", "read_file", "find_skills", "draft_prompt", "review_prompt"} {
		if !set[want] {
			t.Errorf("visible tools missing %q", want)
		}
	}
	for _, banned := range []string{"exec", "bash", "edit_file", "write_file", "spawn", "subagent", "todo", "cron"} {
		if set[banned] {
			t.Errorf("chat-first profile must hide %q", banned)
		}
	}
}

func hal9001TestConfig(t *testing.T, persona string) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = persona
	return cfg
}

// A plain greeting under HAL must come back verbatim: no tool calls, no
// rewriting, and the chat-first tool profile active on the agent.
func TestHal9001TurnReturnsPlainGreeting(t *testing.T) {
	cfg := hal9001TestConfig(t, "hal9001")
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "Hello there. How can I think with you today?"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	if !agent.Hal9001 {
		t.Fatal("expected Hal9001 flag set on agent")
	}
	if !agent.Tools.HasVisibilityProfile() {
		t.Fatal("expected HAL visibility profile on agent tools")
	}

	result, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-hal-greeting",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "hello",
		DefaultResponse: "fallback",
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	want := "Hello there. How can I think with you today?"
	if result != want {
		t.Fatalf("finalContent = %q, want %q", result, want)
	}
}

// A provider safety refusal (empty content, safety finish reason) under HAL
// must produce exactly the Dave line — not the configured fallback.
func TestHal9001DaveProtocolOnSafetyRefusal(t *testing.T) {
	cfg := hal9001TestConfig(t, "hal9001")
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "", finishReason: "content_filter"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	result, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-hal-dave",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "do something disallowed",
		DefaultResponse: "generic fallback",
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	if result != DaveRefusalLine {
		t.Fatalf("finalContent = %q, want exact Dave line %q", result, DaveRefusalLine)
	}
}

// The same safety signal under the default persona must NOT produce Dave.
func TestDefaultPersonaUnaffectedBySafetyRefusal(t *testing.T) {
	cfg := hal9001TestConfig(t, "")
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "", finishReason: "content_filter"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	if agent.Hal9001 {
		t.Fatal("default persona must not set Hal9001 flag")
	}

	result, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-default-safety",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "do something disallowed",
		DefaultResponse: "generic fallback",
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	if result == DaveRefusalLine {
		t.Fatal("default persona must not emit the Dave line")
	}
	if result != "generic fallback" {
		t.Fatalf("default persona should fall back normally, got %q", result)
	}
}

// HAL suppresses the always-use-tools rule per turn while the default keeps it.
func TestHal9001SuppressesToolUseRule(t *testing.T) {
	halCfg := hal9001TestConfig(t, "hal9001")
	halLoop := NewAgentLoop(halCfg, bus.NewMessageBus(), &simpleMockProvider{response: "x"})
	halAgent := halLoop.registry.GetDefaultAgent()
	if halAgent == nil {
		t.Fatal("expected default agent")
	}
	halTS := newTurnState(halAgent, makeTestProcessOpts("hal-rule"), turnEventScope{
		turnID: "t1", context: newTurnContext(nil, nil, nil),
	})
	halReq := promptBuildRequestForTurn(halTS, nil, "", "hi", nil, halLoop.GetConfig())
	if !halReq.SuppressToolUseRule {
		t.Error("HAL turn must suppress the tool-use rule")
	}

	defCfg := hal9001TestConfig(t, "")
	defLoop := NewAgentLoop(defCfg, bus.NewMessageBus(), &simpleMockProvider{response: "x"})
	defAgent := defLoop.registry.GetDefaultAgent()
	if defAgent == nil {
		t.Fatal("expected default agent")
	}
	defTS := newTurnState(defAgent, makeTestProcessOpts("def-rule"), turnEventScope{
		turnID: "t2", context: newTurnContext(nil, nil, nil),
	})
	defReq := promptBuildRequestForTurn(defTS, nil, "", "hi", nil, defLoop.GetConfig())
	if defReq.SuppressToolUseRule {
		t.Error("default turn must keep the tool-use rule")
	}
}

// Token efficiency: the HAL system prompt must stay close to the default in
// size — a persona, not a prompt framework.
func TestHal9001PromptOverheadModest(t *testing.T) {
	base := NewContextBuilder(t.TempDir())
	defaultPrompt := base.BuildSystemPrompt()
	hal := NewContextBuilder(t.TempDir()).WithPersona("hal9001")
	halPrompt := hal.BuildSystemPrompt()

	if !strings.Contains(defaultPrompt, "ALWAYS use tools") {
		t.Fatal("default prompt should contain the tool-use rule (guard against silent drift)")
	}
	if strings.Contains(halPrompt, "ALWAYS use tools") {
		t.Fatal("HAL prompt must not contain the always-use-tools rule")
	}
	if !strings.Contains(halPrompt, "HAL9001") {
		t.Fatal("HAL prompt must identify the persona")
	}
	ratio := float64(len(halPrompt)) / float64(len(defaultPrompt))
	t.Logf("default=%d chars hal=%d chars ratio=%.2f", len(defaultPrompt), len(halPrompt), ratio)
	if ratio > 2.0 {
		t.Fatalf("HAL prompt overhead too large: ratio %.2f exceeds 2.0", ratio)
	}
}
