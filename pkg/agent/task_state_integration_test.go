package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/session"
)

func newTaskStateTestLoop(t *testing.T, home, workspace, project string) (*recordingProvider, *AgentLoop) {
	t.Helper()
	t.Setenv(TaskStateEnabledEnv, "1")
	t.Setenv(TaskStateBudgetEnv, "500")
	t.Setenv("PICOCLAW_HOME", home)
	t.Setenv("DEEPCLAW_MPM_PROJECT", project)
	t.Setenv("DEEPCLAW_MPM_ENABLED", "")
	t.Setenv("DEEPCLAW_ACTIVE_MEMORY", "")

	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: workspace, ModelName: "test-model", MaxTokens: 4096, MaxToolIterations: 4,
	}}}
	provider := &recordingProvider{}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(loop.Close)
	return provider, loop
}

func taskStateTurn(t *testing.T, loop *AgentLoop, sessionKey, content string) {
	t.Helper()
	message := testInboundMessage(bus.InboundMessage{
		Channel: "pico", ChatID: "chat-1", Content: content, SessionKey: sessionKey,
	})
	if _, err := loop.processMessage(context.Background(), message); err != nil {
		t.Fatal(err)
	}
}

func TestTaskStateSurvivesHistoryClearWithoutMPM(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(t.TempDir(), "picoclaw-home")
	sessionKey := session.BuildOpaqueSessionKey("deepclaw-task-state-test:survive")
	provider, loop := newTaskStateTestLoop(t, home, workspace, "atlas")

	taskStateTurn(t, loop, sessionKey, "Objective: Fix port parsing.\nConstraint: keep the public API.")
	instance := loop.GetRegistry().GetDefaultAgent()
	if instance == nil || instance.ActiveMemory == nil || !instance.TaskStateEnabled {
		t.Fatal("task state was not enabled")
	}
	state, err := instance.ActiveMemory.TaskState(sessionKey)
	if err != nil || state == nil || state.Objective != "Fix port parsing." {
		t.Fatalf("objective not persisted: %+v %v", state, err)
	}

	// Simulate compaction/restart: the user turn that carried the objective is
	// gone from ordinary history, but the sidecar remains.
	ctx := context.Background()
	if err := instance.Sessions.SetHistory(ctx, sessionKey, nil); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sessions.SetSummary(ctx, sessionKey, ""); err != nil {
		t.Fatal(err)
	}

	taskStateTurn(t, loop, sessionKey, "Continue.")
	last := provider.lastMessages
	if len(last) == 0 {
		t.Fatal("no request captured")
	}
	var joined strings.Builder
	for _, message := range last {
		joined.WriteString(message.Content)
		joined.WriteString("\n")
	}
	prompt := joined.String()
	if !strings.Contains(prompt, "source: task.state") {
		t.Fatalf("task state prompt part missing:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Fix port parsing.") || !strings.Contains(prompt, "keep the public API.") {
		t.Fatalf("task state did not survive history clear:\n%s", prompt)
	}
	if strings.Contains(prompt, "user: Objective: Fix port parsing.") {
		t.Fatalf("task state was presented as user-authored content:\n%s", prompt)
	}
}

func TestTaskStateProjectSwitchDoesNotLeak(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(t.TempDir(), "picoclaw-home")
	sessionKey := session.BuildOpaqueSessionKey("deepclaw-task-state-test:switch")
	providerA, loopA := newTaskStateTestLoop(t, home, workspace, "atlas")
	taskStateTurn(t, loopA, sessionKey, "Objective: Atlas parser work.")
	if instanceA := loopA.GetRegistry().GetDefaultAgent(); instanceA == nil || instanceA.ActiveMemory == nil {
		t.Fatal("no task state store for project A")
	}

	// A second loop under a different project and workspace shares the
	// PicoClaw home but must not read project A's task state.
	providerB, loopB := newTaskStateTestLoop(t, home, t.TempDir(), "zebra")
	taskStateTurn(t, loopB, sessionKey, "Continue.")
	var joined strings.Builder
	for _, message := range providerB.lastMessages {
		joined.WriteString(message.Content)
		joined.WriteString("\n")
	}
	if strings.Contains(joined.String(), "Atlas parser work.") {
		t.Fatalf("project B saw project A task state:\n%s", joined.String())
	}
	_ = providerA
}
