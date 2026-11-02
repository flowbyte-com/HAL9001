package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestActiveMemoryCarriesAcrossTurns(t *testing.T) {
	t.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	t.Setenv("DEEPCLAW_ACTIVE_MEMORY", "1")
	bin := mpmTestBinary(t)
	tmp := t.TempDir()
	agentWs := filepath.Join(tmp, "agent")
	mpmWs := filepath.Join(tmp, "mpm")
	mpmHome := filepath.Join(tmp, "home")
	os.MkdirAll(mpmWs, 0755)
	seedMPMFixture(t, bin, mpmWs, mpmHome, "deepclaw-active", "The active memory secret code is amber-falcon-9.")

	t.Setenv("MPM_WORKSPACE", mpmWs)
	t.Setenv("DEEPCLAW_MPM_EXECUTABLE", bin)
	t.Setenv("DEEPCLAW_MPM_HOME", mpmHome)
	t.Setenv("DEEPCLAW_MPM_PROJECT", "deepclaw-active")
	t.Setenv("DEEPCLAW_MPM_BUDGET", "350")

	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: agentWs, ModelName: "test-model", MaxTokens: 4096, MaxToolIterations: 10}}}
	msgBus := bus.NewMessageBus()
	provider := &recordingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	if _, err := al.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{Channel: "pico", ChatID: "chat-1", Content: "secret code"})); err != nil {
		t.Fatal(err)
	}
	if _, err := al.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{Channel: "pico", ChatID: "chat-1", Content: "second turn"})); err != nil {
		t.Fatal(err)
	}
	last := provider.lastMessages[len(provider.lastMessages)-1].Content
	if !strings.Contains(last, "amber-falcon-9") {
		t.Fatalf("active memory did not carry across turns:\n%s", last)
	}
}

func TestActiveMemoryRestartsFromSidecar(t *testing.T) {
	t.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	t.Setenv("DEEPCLAW_ACTIVE_MEMORY", "1")
	bin := mpmTestBinary(t)
	tmp := t.TempDir()
	agentWs := filepath.Join(tmp, "agent")
	mpmWs := filepath.Join(tmp, "mpm")
	mpmHome := filepath.Join(tmp, "home")
	os.MkdirAll(mpmWs, 0755)
	seedMPMFixture(t, bin, mpmWs, mpmHome, "deepclaw-restart", "The restart secret code is copper-lynx-3.")

	t.Setenv("MPM_WORKSPACE", mpmWs)
	t.Setenv("DEEPCLAW_MPM_EXECUTABLE", bin)
	t.Setenv("DEEPCLAW_MPM_HOME", mpmHome)
	t.Setenv("DEEPCLAW_MPM_PROJECT", "deepclaw-restart")
	t.Setenv("DEEPCLAW_MPM_BUDGET", "350")

	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: agentWs, ModelName: "test-model", MaxTokens: 4096, MaxToolIterations: 10}}}
	msgBus := bus.NewMessageBus()
	provider := &recordingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)
	if _, err := al.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{Channel: "pico", ChatID: "chat-1", Content: "secret code"})); err != nil {
		t.Fatal(err)
	}

	// New agent instance, same workspace/session sidecar.
	msgBus2 := bus.NewMessageBus()
	provider2 := &recordingProvider{}
	al2 := NewAgentLoop(cfg, msgBus2, provider2)
	if _, err := al2.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{Channel: "pico", ChatID: "chat-1", Content: "secret code"})); err != nil {
		t.Fatal(err)
	}
	last := provider2.lastMessages[len(provider2.lastMessages)-1].Content
	if !strings.Contains(last, "copper-lynx-3") {
		t.Fatalf("active memory did not survive restart:\n%s", last)
	}
}

func TestActiveMemoryProjectIsolation(t *testing.T) {
	t.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	t.Setenv("DEEPCLAW_ACTIVE_MEMORY", "1")
	bin := mpmTestBinary(t)
	tmp := t.TempDir()
	// Project A
	wsA := filepath.Join(tmp, "a")
	mpmA := filepath.Join(tmp, "mpm-a")
	homeA := filepath.Join(tmp, "home-a")
	os.MkdirAll(mpmA, 0755)
	seedMPMFixture(t, bin, mpmA, homeA, "project-a", "Project A secret code is alpha-1.")
	// Project B
	wsB := filepath.Join(tmp, "b")
	mpmB := filepath.Join(tmp, "mpm-b")
	homeB := filepath.Join(tmp, "home-b")
	os.MkdirAll(mpmB, 0755)
	seedMPMFixture(t, bin, mpmB, homeB, "project-b", "Project B secret code is beta-2.")

	for _, tc := range []struct {
		ws, mpmWs, home, project, want string
	}{
		{wsA, mpmA, homeA, "project-a", "alpha-1"},
		{wsB, mpmB, homeB, "project-b", "beta-2"},
	} {
		t.Setenv("MPM_WORKSPACE", tc.mpmWs)
		t.Setenv("DEEPCLAW_MPM_EXECUTABLE", bin)
		t.Setenv("DEEPCLAW_MPM_HOME", tc.home)
		t.Setenv("DEEPCLAW_MPM_PROJECT", tc.project)
		t.Setenv("DEEPCLAW_MPM_BUDGET", "350")
		cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: tc.ws, ModelName: "test-model", MaxTokens: 4096, MaxToolIterations: 10}}}
		msgBus := bus.NewMessageBus()
		provider := &recordingProvider{}
		al := NewAgentLoop(cfg, msgBus, provider)
		if _, err := al.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{Channel: "pico", ChatID: "chat-1", Content: "secret code"})); err != nil {
			t.Fatal(err)
		}
		last := provider.lastMessages[len(provider.lastMessages)-1].Content
		if !strings.Contains(last, tc.want) {
			t.Fatalf("project %s missing own memory: %s", tc.project, last)
		}
		other := "beta-2"
		if tc.want == "beta-2" {
			other = "alpha-1"
		}
		if strings.Contains(last, other) {
			t.Fatalf("project %s leaked other project memory: %s", tc.project, last)
		}
	}
}
