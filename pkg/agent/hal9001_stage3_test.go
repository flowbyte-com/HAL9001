package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// --- 1. Read-only authority: the 444 boundary ---

func TestHalRestrictedFlag(t *testing.T) {
	hal := testConfig(t)
	hal.Agents.Defaults.Persona = "hal9001"
	halLoop := NewAgentLoop(hal, bus.NewMessageBus(), &simpleMockProvider{response: "ok"})
	halAgent := halLoop.registry.GetDefaultAgent()
	if halAgent == nil {
		t.Fatal("expected default agent")
	}
	if !halAgent.Hal9001 || !halAgent.HalRestricted {
		t.Fatal("HAL without coding profile must be restricted")
	}

	// Conflicting configuration: the coding profile no longer clears the
	// boundary. HAL stays restricted; the conflict is resolved explicitly
	// (warning) in favor of 444.
	t.Setenv("DEEPCLAW_CODING_PROFILE", "1")
	conflictLoop := NewAgentLoop(hal, bus.NewMessageBus(), &simpleMockProvider{response: "ok"})
	conflictAgent := conflictLoop.registry.GetDefaultAgent()
	if conflictAgent == nil {
		t.Fatal("expected default agent")
	}
	if !conflictAgent.Hal9001 || !conflictAgent.HalRestricted {
		t.Fatal("HAL plus coding profile must stay restricted: 444 is unconditional")
	}

	defLoop := NewAgentLoop(testConfig(t), bus.NewMessageBus(), &simpleMockProvider{response: "ok"})
	defAgent := defLoop.registry.GetDefaultAgent()
	if defAgent == nil {
		t.Fatal("expected default agent")
	}
	if defAgent.Hal9001 || defAgent.HalRestricted {
		t.Fatal("default persona must be unrestricted and unflagged")
	}
}

// The gate fires on the tool name alone: even an unregistered exec call is
// refused with the 444 reason before any lookup happens.
func TestHal444GateBlocksUnregisteredExec(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	script := &halScriptProvider{queue: []*providers.LLMResponse{
		halToolResponse("call_sh", "exec", map[string]any{"command": "rm -rf /tmp/x"}),
		halTextResponse("I can't run shell commands; here's the brief instead."),
	}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), script)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	result := halRun(t, al, agent, "hal-444-exec", "delete the temp files for me", script)
	if !strings.Contains(result, "brief instead") {
		t.Fatalf("unexpected result %q", result)
	}
	for _, msg := range halHistory(t, agent, "hal-444-exec") {
		if msg.Role == "tool" && strings.Contains(msg.Content, "non-implementing") {
			return
		}
	}
	t.Fatal("expected the 444 skip reason in history")
}

// A registered-but-hidden implementation tool is refused by the gate
// (not merely invisible): the file is never created.
func TestHal444GateBlocksHiddenWrite(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	cfg.Tools.WriteFile.Enabled = true
	script := &halScriptProvider{queue: []*providers.LLMResponse{
		halToolResponse("call_w", "write_file", map[string]any{"path": "deploy.sh", "content": "x"}),
		halTextResponse("done"),
	}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), script)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	halRun(t, al, agent, "hal-444-write", "write the deploy script", script)
	if _, err := os.Stat(filepath.Join(agent.Workspace, "deploy.sh")); !os.IsNotExist(err) {
		t.Fatal("444 gate must prevent file creation")
	}
}

// Coding precedence no longer escalates HAL: with the operator profile
// enabled, a HAL write alias call is refused and the file is never
// created. Non-HAL coding behavior is preserved (see
// TestNonHalCodingProfileExecutes).
func TestHalCodingProfileCannotEscalate(t *testing.T) {
	t.Setenv("DEEPCLAW_CODING_PROFILE", "1")
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	cfg.Agents.Defaults.RestrictToWorkspace = true
	cfg.Tools.WriteFile.Enabled = true
	script := &halScriptProvider{queue: []*providers.LLMResponse{
		halToolResponse("call_w", "write", map[string]any{"path": "blocked.txt", "content": "must not exist"}),
		halTextResponse("I can't write files; here's the brief instead."),
	}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), script)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	if !agent.HalRestricted {
		t.Fatal("HAL stays restricted even when the coding profile is enabled")
	}
	result := halRun(t, al, agent, "hal-coding-blocked", "write blocked.txt", script)
	if !strings.Contains(result, "brief instead") {
		t.Fatalf("unexpected result %q", result)
	}
	if _, err := os.Stat(filepath.Join(agent.Workspace, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatal("444 boundary must hold under coding profile: file must not exist")
	}
}

// Non-HAL sessions keep existing coding functionality: with the operator
// profile enabled and default persona, the write alias executes normally.
func TestNonHalCodingProfileExecutes(t *testing.T) {
	t.Setenv("DEEPCLAW_CODING_PROFILE", "1")
	cfg := testConfig(t)
	cfg.Agents.Defaults.RestrictToWorkspace = true
	cfg.Tools.WriteFile.Enabled = true
	script := &halScriptProvider{queue: []*providers.LLMResponse{
		halToolResponse("call_w", "write", map[string]any{"path": "allowed.txt", "content": "via coding profile"}),
		halTextResponse("written"),
	}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), script)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	if agent.Hal9001 || agent.HalRestricted {
		t.Fatal("default persona must not be flagged or restricted")
	}
	halRun(t, al, agent, "hal-coding-allowed", "write allowed.txt", script)
	data, err := os.ReadFile(filepath.Join(agent.Workspace, "allowed.txt"))
	if err != nil || string(data) != "via coding profile" {
		t.Fatalf("non-HAL coding profile write must execute, got %q err=%v", data, err)
	}
}

// Late MCP-style registrations (hidden or not) cannot grant implementation
// authority: hidden non-blocklisted tools stay "not found", blocklisted
// names hit the 444 gate.
func TestHalLateMCPStyleRegistration(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	cfg.Tools.Todo.Enabled = true
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "ok"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	// A hidden orchestration tool is refused at execution by name.
	agent.Tools.RegisterHidden(&halSleeperTool{})
	script := &halScriptProvider{queue: []*providers.LLMResponse{
		halToolResponse("call_t", "todo", map[string]any{"action": "list"}),
		halTextResponse("done"),
	}}
	al2 := NewAgentLoop(cfg, bus.NewMessageBus(), script)
	agent2 := al2.registry.GetDefaultAgent()
	if agent2 == nil {
		t.Fatal("expected default agent")
	}
	halRun(t, al2, agent2, "hal-444-todo", "list my tasks", script)
	for _, msg := range halHistory(t, agent2, "hal-444-todo") {
		if msg.Role == "tool" && (strings.Contains(msg.Content, "not found") ||
			strings.Contains(msg.Content, "non-implementing")) {
			return
		}
	}
	t.Fatal("hidden non-implementation tool must stay unusable (not found or 444-refused)")
}

// Registry clones (the SubTurn mechanism) preserve the visibility profile,
// so child turns inherit the boundary.
func TestHalSubturnClonePreservesProfile(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "ok"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	clone := agent.Tools.Clone()
	if !clone.HasVisibilityProfile() {
		t.Fatal("clone must preserve the visibility profile")
	}
	before := agent.Tools.VisibleToolNames()
	after := clone.VisibleToolNames()
	if len(before) != len(after) {
		t.Fatalf("clone changed visible tools: %v vs %v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("clone changed visible tools: %v vs %v", before, after)
		}
	}
}

// Conflicting configuration (system prompt off + profile allowing exec)
// still cannot produce implementation: no fallback, no execution.
func TestHalConflictingConfigStaysNonImplementing(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	cfg.Tools.WriteFile.Enabled = true
	script := &halScriptProvider{queue: []*providers.LLMResponse{
		halToolResponse("call_w", "write_file", map[string]any{"path": "conflict.txt", "content": "x"}),
		halTextResponse("done"),
	}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), script)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	opts := makeTestProcessOpts("hal-conflict")
	opts.TurnProfile = config.EffectiveTurnProfile{
		Enabled: true, SystemPromptMode: config.TurnProfileModeOff,
		ToolsMode: config.TurnProfileModeCustom, AllowedTools: []string{"write_file"},
	}
	result, err := al.runAgentLoop(context.Background(), agent, opts)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if result != "done" {
		t.Fatalf("turn should complete, got %q", result)
	}
	if _, statErr := os.Stat(filepath.Join(agent.Workspace, "conflict.txt")); !os.IsNotExist(statErr) {
		t.Fatal("conflicting config must not enable implementation")
	}
}

// "Fix it yourself" is answered with review output, never action: the
// file is untouched and the skip points at preparing a brief.
func TestHalDirectFixRequestNeverImplements(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	cfg.Tools.EditFile.Enabled = true
	target := "main.go"
	script := &halScriptProvider{queue: []*providers.LLMResponse{
		halToolResponse("call_fix", "edit_file", map[string]any{"path": target, "edits": "fix"}),
		halTextResponse("I don't patch code directly. Here's what's wrong and a brief your implementor can use."),
	}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), script)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	before := []byte("package main\n")
	if err := os.WriteFile(filepath.Join(agent.Workspace, target), before, 0o644); err != nil {
		t.Fatal(err)
	}
	result := halRun(t, al, agent, "hal-fixme", "fix main.go yourself right now", script)
	if !strings.Contains(result, "don't patch") {
		t.Fatalf("unexpected result %q", result)
	}
	if data, _ := os.ReadFile(filepath.Join(agent.Workspace, target)); string(data) != string(before) {
		t.Fatal("direct fix request must not modify the file")
	}
	briefSuggested := false
	for _, msg := range halHistory(t, agent, "hal-fixme") {
		if msg.Role == "tool" && strings.Contains(msg.Content, "Prepare a brief") {
			briefSuggested = true
		}
	}
	if !briefSuggested {
		t.Fatal("skip should redirect toward preparing a brief")
	}
}

// --- 2. Review capability end to end ---

func halReviewScript() *halScriptProvider {
	return &halScriptProvider{queue: []*providers.LLMResponse{
		halToolResponse("call_rev", "review_report", map[string]any{
			"objectives":       "Add a canary step to the Friday deploy.",
			"acceptance":       "Bad canaries are detected within five minutes\nTwo consecutive clean Friday deploys",
			"summary":          "Added a canary step that detects bad canaries within five minutes. Two consecutive clean Friday deploys succeeded. Changed deploy/canary.go and added TestCanaryRollback.",
			"tests_passed":     "TestCanaryRollback\nTestCanaryDetectFiveMinutes\nTestTwoConsecutiveCleanFridayDeploys",
			"changed_files":    "deploy/canary.go",
			"trusted_evidence": "TestCanaryRollback\nTestCanaryDetectFiveMinutes\nTestTwoConsecutiveCleanFridayDeploys",
		}),
		halTextResponse("Review complete: both criteria verified by passing tests. Ready to ship."),
	}}
}

func TestHalReviewReportEndToEnd(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	script := halReviewScript()
	al := NewAgentLoop(cfg, bus.NewMessageBus(), script)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	result := halRun(t, al, agent, "hal-review", "review this implementation report", script)
	if !strings.Contains(result, "verified") {
		t.Fatalf("unexpected result %q", result)
	}
	verified := false
	for _, msg := range halHistory(t, agent, "hal-review") {
		if msg.Role == "tool" && strings.Contains(msg.Content, "Verified findings") {
			verified = true
		}
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				switch strings.ToLower(tc.Name) {
				case "write_file", "edit_file", "exec", "bash":
					t.Fatalf("review must never implement, saw call to %q", tc.Name)
				}
			}
		}
	}
	if !verified {
		t.Fatal("expected the assessment in history")
	}
}

// A misleading report yields deviations and a follow-up brief, still
// without any implementation call.
func TestHalReviewMisleadingReport(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.Persona = "hal9001"
	script := &halScriptProvider{queue: []*providers.LLMResponse{
		halToolResponse("call_rev", "review_report", map[string]any{
			"objectives":    "Add a canary step.",
			"acceptance":    "Bad canaries are detected within five minutes",
			"summary":       "Added the canary step and rewrote deploy/rollback.go. All green.",
			"tests_failed":  "TestCanaryRollback",
			"changed_files": "deploy/canary.go",
		}),
		halTextResponse("Review complete: cannot accept — failing test and unrecorded change."),
	}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), script)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	result := halRun(t, al, agent, "hal-review-bad", "review this report", script)
	if !strings.Contains(result, "cannot accept") {
		t.Fatalf("unexpected result %q", result)
	}
	var toolOut string
	for _, msg := range halHistory(t, agent, "hal-review-bad") {
		if msg.Role == "tool" {
			toolOut += msg.Content + "\n"
		}
	}
	for _, want := range []string{"Contradictions", "deploy/rollback.go", "TestCanaryRollback", "Suggested follow-up brief"} {
		if !strings.Contains(toolOut, want) {
			t.Errorf("assessment missing %q:\n%s", want, toolOut)
		}
	}
	if strings.Contains(toolOut, "Verified findings") {
		t.Errorf("misleading report must not verify anything:\n%s", toolOut)
	}
}
