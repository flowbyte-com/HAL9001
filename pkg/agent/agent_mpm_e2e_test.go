package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func mpmTestBinary(t *testing.T) string {
	t.Helper()
	candidates := []string{
		os.Getenv("DEEPCLAW_MPM_EXECUTABLE"),
		os.Getenv("MPM_EXECUTABLE"),
		filepath.Join("..", "..", ".lab", "bin", "mpm"),
		"/home/v/projects/deepclaw/.lab/bin/mpm",
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Skip("mpm test binary not available")
	return ""
}

func seedMPMFixture(t *testing.T, bin, workspace, home, project, fact string) {
	t.Helper()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"action": "save",
		"params": map[string]any{
			"fact": fact,
			"tags": []string{"deepclaw-e2e", "project:" + project},
		},
	})
	cmd := exec.Command(bin, "call", "mpm_memory", "--payload", string(payload))
	cmd.Env = append(os.Environ(),
		"MPM_WORKSPACE="+workspace,
		"HOME="+home,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("seed mpm memory failed: %v: %s", err, string(out))
	}
}

func TestProcessMessage_DeepClawMPMRetrievalEndToEnd(t *testing.T) {
	bin := mpmTestBinary(t)
	tmpDir := t.TempDir()
	agentWorkspace := filepath.Join(tmpDir, "agent-workspace")
	mpmWorkspace := filepath.Join(tmpDir, "mpm-workspace")
	mpmHome := filepath.Join(tmpDir, "mpm-home")
	project := "deepclaw-e2e"

	t.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	t.Setenv("MPM_WORKSPACE", mpmWorkspace)
	t.Setenv("DEEPCLAW_MPM_EXECUTABLE", bin)
	t.Setenv("DEEPCLAW_MPM_HOME", mpmHome)
	t.Setenv("DEEPCLAW_MPM_PROJECT", project)
	t.Setenv("DEEPCLAW_MPM_BUDGET", "350")

	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project,
		"The DeepClaw MPM fixture secret code is blue-heron-42.")

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         agentWorkspace,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &recordingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	response, err := al.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{
		Channel: "pico",
		ChatID:  "chat-1",
		Content: "secret code",
	}))
	if err != nil {
		t.Fatalf("processMessage() error = %v", err)
	}
	if response != "Mock response" {
		t.Fatalf("processMessage() response = %q, want %q", response, "Mock response")
	}
	if len(provider.lastMessages) == 0 {
		t.Fatal("provider did not receive any messages")
	}

	systemPrompt := provider.lastMessages[0].Content
	if !strings.Contains(systemPrompt, "blue-heron-42") {
		t.Fatalf("DeepClaw prompt missing MPM fixture:\n%s", systemPrompt)
	}
	if got := strings.Count(systemPrompt, "blue-heron-42"); got != 1 {
		t.Fatalf("expected one retrieval injection, got %d", got)
	}
	if strings.Contains(systemPrompt, "MEMORY.md") {
		t.Fatalf("DeepClaw mode should not eagerly inject MEMORY.md:\n%s", systemPrompt)
	}
}

func TestProcessMessage_DeepClawMPMProjectIsolationEndToEnd(t *testing.T) {
	bin := mpmTestBinary(t)
	tmpDir := t.TempDir()
	agentWorkspace := filepath.Join(tmpDir, "agent-workspace")
	mpmWorkspace := filepath.Join(tmpDir, "mpm-workspace")
	mpmHome := filepath.Join(tmpDir, "mpm-home")
	project := "deepclaw-isolation"

	t.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	t.Setenv("MPM_WORKSPACE", mpmWorkspace)
	t.Setenv("DEEPCLAW_MPM_EXECUTABLE", bin)
	t.Setenv("DEEPCLAW_MPM_HOME", mpmHome)
	t.Setenv("DEEPCLAW_MPM_PROJECT", project)
	t.Setenv("DEEPCLAW_MPM_BUDGET", "350")

	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project,
		"Project-scoped secret code is blue-heron-42.")
	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, "other-project",
		"Other project secret code is red-sparrow-7.")

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         agentWorkspace,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &recordingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	if _, err := al.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{
		Channel: "pico",
		ChatID:  "chat-1",
		Content: "secret code",
	})); err != nil {
		t.Fatal(err)
	}
	systemPrompt := provider.lastMessages[0].Content
	if !strings.Contains(systemPrompt, "blue-heron-42") {
		t.Fatalf("expected project-scoped memory, got:\n%s", systemPrompt)
	}
	if strings.Contains(systemPrompt, "red-sparrow-7") {
		t.Fatalf("project isolation failed, other project leaked:\n%s", systemPrompt)
	}
}

func TestProcessMessage_DeepClawMPMRetrievalFailureEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	agentWorkspace := filepath.Join(tmpDir, "agent-workspace")
	mpmWorkspace := filepath.Join(tmpDir, "mpm-workspace")
	mpmHome := filepath.Join(tmpDir, "mpm-home")

	if err := os.MkdirAll(mpmWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(tmpDir, "failing-mpm")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nprintf '%s' '{\"success\":false,\"error\":\"boom\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	t.Setenv("MPM_WORKSPACE", mpmWorkspace)
	t.Setenv("DEEPCLAW_MPM_EXECUTABLE", fakeBin)
	t.Setenv("DEEPCLAW_MPM_HOME", mpmHome)
	t.Setenv("DEEPCLAW_MPM_PROJECT", "deepclaw-failure")
	t.Setenv("DEEPCLAW_MPM_BUDGET", "350")

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         agentWorkspace,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &recordingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	_, err := al.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{
		Channel: "pico",
		ChatID:  "chat-1",
		Content: "secret code",
	}))
	if err == nil {
		t.Fatal("expected DeepClaw retrieval failure, got nil")
	}
	if !strings.Contains(err.Error(), "mpm") {
		t.Fatalf("expected mpm-related error, got %v", err)
	}
}

func TestProcessMessage_DeepClawMPMBudgetTruncationEndToEnd(t *testing.T) {
	bin := mpmTestBinary(t)
	tmpDir := t.TempDir()
	agentWorkspace := filepath.Join(tmpDir, "agent-workspace")
	mpmWorkspace := filepath.Join(tmpDir, "mpm-workspace")
	mpmHome := filepath.Join(tmpDir, "mpm-home")
	project := "deepclaw-budget"

	t.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	t.Setenv("MPM_WORKSPACE", mpmWorkspace)
	t.Setenv("DEEPCLAW_MPM_EXECUTABLE", bin)
	t.Setenv("DEEPCLAW_MPM_HOME", mpmHome)
	t.Setenv("DEEPCLAW_MPM_PROJECT", project)
	t.Setenv("DEEPCLAW_MPM_BUDGET", "40")

	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project,
		"This memory is intentionally long enough to exceed a twenty token budget and should be truncated by the adapter.")

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         agentWorkspace,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &recordingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	if _, err := al.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{
		Channel: "pico",
		ChatID:  "chat-1",
		Content: "memory",
	})); err != nil {
		t.Fatal(err)
	}
	systemPrompt := provider.lastMessages[0].Content
	if !strings.Contains(systemPrompt, "truncated to fit memory budget") {
		t.Fatalf("expected explicit truncation marker, got:\n%s", systemPrompt)
	}
}

func TestProcessMessage_DeepClawMPMRetrievalOncePerTurn(t *testing.T) {
	bin := mpmTestBinary(t)
	tmpDir := t.TempDir()
	agentWorkspace := filepath.Join(tmpDir, "agent-workspace")
	mpmWorkspace := filepath.Join(tmpDir, "mpm-workspace")
	mpmHome := filepath.Join(tmpDir, "mpm-home")
	project := "deepclaw-once"

	t.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	t.Setenv("MPM_WORKSPACE", mpmWorkspace)
	t.Setenv("DEEPCLAW_MPM_EXECUTABLE", bin)
	t.Setenv("DEEPCLAW_MPM_HOME", mpmHome)
	t.Setenv("DEEPCLAW_MPM_PROJECT", project)
	t.Setenv("DEEPCLAW_MPM_BUDGET", "350")

	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project,
		"The DeepClaw MPM fixture secret code is blue-heron-42.")

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         agentWorkspace,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &recordingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	for i := 0; i < 2; i++ {
		if _, err := al.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{
			Channel: "pico",
			ChatID:  "chat-1",
			Content: "secret code",
		})); err != nil {
			t.Fatal(err)
		}
		systemPrompt := provider.lastMessages[0].Content
		if got := strings.Count(systemPrompt, "blue-heron-42"); got != 1 {
			t.Fatalf("turn %d: expected one injection, got %d", i, got)
		}
	}
}
