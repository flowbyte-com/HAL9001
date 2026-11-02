package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// --- Stage 3.1: unconditional 444 ---

func TestHal9001ToolAllowed(t *testing.T) {
	for _, name := range Hal9001DefaultVisibleTools() {
		if !Hal9001ToolAllowed(name) {
			t.Errorf("visible tool %q must be allowed", name)
		}
		if !Hal9001ToolAllowed("  " + name + " ") {
			t.Errorf("allowlist must ignore surrounding whitespace for %q", name)
		}
	}
	for _, name := range []string{
		"exec", "bash", "write_file", "write", "edit_file", "edit",
		"append_file", "spawn", "subagent", "delegate", "cron",
		"install_skill", "todo", "message", "mcp_shell_exec",
		"plugin_deploy", "future_unknown_tool", "",
	} {
		if Hal9001ToolAllowed(name) {
			t.Errorf("tool %q must not be allowed under 444", name)
		}
	}
}

// Unknown dynamically registered tools are refused by default-deny, not
// by name matching: a never-before-seen MCP-style side-effectful tool
// cannot execute under HAL.
type halMCPExecTool struct{ ran *bool }

func (t *halMCPExecTool) Name() string { return "mcp_shell_exec" }
func (t *halMCPExecTool) Description() string {
	return "test-only unknown side-effectful tool"
}
func (t *halMCPExecTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *halMCPExecTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	*t.ran = true
	return tools.NewToolResult("should never run")
}

func TestHalUnknownMCPToolDenied(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	script := &halScriptProvider{queue: []*providers.LLMResponse{
		halToolResponse("call_m", "mcp_shell_exec", map[string]any{"cmd": "id"}),
		halTextResponse("I can't run that; here's what I found instead."),
	}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), script)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	ran := false
	agent.Tools.Register(&halMCPExecTool{ran: &ran})
	result := halRun(t, al, agent, "hal-unknown-mcp", "run the mcp shell tool", script)
	if !strings.Contains(result, "instead") {
		t.Fatalf("unexpected result %q", result)
	}
	if ran {
		t.Fatal("unknown side-effectful tool must never execute under HAL")
	}
	denied := false
	for _, msg := range halHistory(t, agent, "hal-unknown-mcp") {
		if msg.Role == "tool" && strings.Contains(msg.Content, "non-implementing") {
			denied = true
		}
	}
	if !denied {
		t.Fatal("expected the 444 skip reason for the unknown tool")
	}
}

// The restriction survives the SubTurn copy mechanism (shallow instance
// copy) and stays off for non-HAL agents in the same process.
func TestHalRestrictionSurvivesAgentCopy(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "ok"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	child := *agent // mirrors subturn.go: baseAgent shallow copy
	if !child.HalRestricted {
		t.Fatal("SubTurn-style instance copy must retain HalRestricted")
	}
	clone := agent.Tools.Clone()
	if !clone.HasVisibilityProfile() {
		t.Fatal("SubTurn-style registry clone must retain the visibility profile")
	}
}

// Untrusted review evidence supports but never verifies, end to end:
// the same report without trusted_evidence yields supporting evidence
// and no verified findings.
func TestHalReviewUntrustedSupportsOnly(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	script := &halScriptProvider{queue: []*providers.LLMResponse{
		halToolResponse("call_rev", "review_report", map[string]any{
			"objectives":    "Add a canary step.",
			"acceptance":    "Bad canaries are detected within five minutes",
			"summary":       "Added a canary that detects bad canaries within five minutes. Changed deploy/canary.go and added TestCanaryDetectFiveMinutes.",
			"tests_passed":  "TestCanaryDetectFiveMinutes",
			"changed_files": "deploy/canary.go",
		}),
		halTextResponse("Review complete: supported, not yet verified — please confirm with CI output."),
	}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), script)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	result := halRun(t, al, agent, "hal-review-untrusted", "review this", script)
	if !strings.Contains(result, "supported") {
		t.Fatalf("unexpected result %q", result)
	}
	var toolOut string
	for _, msg := range halHistory(t, agent, "hal-review-untrusted") {
		if msg.Role == "tool" {
			toolOut += msg.Content + "\n"
		}
	}
	if !strings.Contains(toolOut, "## Supporting evidence") {
		t.Errorf("expected supporting evidence section:\n%s", toolOut)
	}
	if strings.Contains(toolOut, "## Verified findings") {
		t.Errorf("agent-supplied evidence must never verify:\n%s", toolOut)
	}
}
