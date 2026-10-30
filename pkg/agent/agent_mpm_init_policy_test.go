package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
)

const eagerMemorySentinel = "EAGER-MEMORY-SENTINEL-003D"

func resetAgentIsolationState(t *testing.T) {
	t.Helper()
	isolation.ResetProtectedPaths()
	isolation.Configure(config.DefaultConfig())
	t.Cleanup(func() {
		isolation.ResetProtectedPaths()
		isolation.Configure(config.DefaultConfig())
	})
}

// newMPMPolicyHarness creates an agent loop with explicit MPM environment and a
// workspace whose MEMORY.md is a sentinel for the eager-memory fallback.
func newMPMPolicyHarness(
	t *testing.T,
	workspace, mpmWorkspace, mpmHome, project, executable string,
) (*AgentLoop, *recordingProvider) {
	t.Helper()
	resetAgentIsolationState(t)
	if mpmWorkspace != "" {
		if err := os.MkdirAll(mpmWorkspace, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if mpmHome != "" {
		if err := os.MkdirAll(mpmHome, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "memory", "MEMORY.md"),
		[]byte(eagerMemorySentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PICOCLAW_HOME", filepath.Join(workspace, "..", "picoclaw-home"))
	t.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	t.Setenv("DEEPCLAW_ACTIVE_MEMORY", "0")
	t.Setenv("MPM_WORKSPACE", mpmWorkspace)
	t.Setenv("DEEPCLAW_MPM_HOME", mpmHome)
	t.Setenv("DEEPCLAW_MPM_PROJECT", project)
	t.Setenv("DEEPCLAW_MPM_BUDGET", "350")
	t.Setenv("DEEPCLAW_MPM_EXECUTABLE", executable)

	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace:         workspace,
		ModelName:         "test-model",
		MaxTokens:         4096,
		MaxToolIterations: 10,
	}}}
	cfg.Tools.Exec.Enabled = true
	cfg.Tools.Exec.AllowRemote = true
	cfg.Tools.Exec.EnableDenyPatterns = true
	provider := &recordingProvider{}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(loop.Close)
	return loop, provider
}

func sendPolicyTurn(t *testing.T, loop *AgentLoop, provider *recordingProvider, content string) string {
	t.Helper()
	if _, err := loop.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{
		Channel: "pico",
		ChatID:  "chat-1",
		Content: content,
	})); err != nil {
		t.Fatalf("processMessage(%q): %v", content, err)
	}
	if len(provider.lastMessages) == 0 {
		t.Fatal("provider captured no messages")
	}
	return joinPrompt(provider.lastMessages)
}

func TestMPMInitFailureDisablesEagerMemoryAndRecordsDegradedState(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	mpmWorkspace := filepath.Join(root, "mpm-workspace")
	mpmHome := filepath.Join(root, "mpm-home")

	loop, provider := newMPMPolicyHarness(t, workspace, mpmWorkspace, mpmHome,
		"init-failure", filepath.Join(root, "missing-mpm-binary"))
	prompt := sendPolicyTurn(t, loop, provider, "hello")

	if strings.Contains(prompt, eagerMemorySentinel) {
		t.Fatalf("MPM initialization failure fell back to eager MEMORY.md:\n%s", prompt)
	}
	agent := loop.registry.GetDefaultAgent()
	if agent.MPMRetriever != nil {
		t.Fatal("retriever should be nil after an initialization failure")
	}
	if strings.TrimSpace(agent.MPMDegradedReason) == "" {
		t.Fatal("degraded MPM state was not recorded")
	}
	if agent.MPMUnsandboxed {
		t.Fatal("a missing executable must not enable the unsafe unsandboxed mode")
	}
	// The configured store may still exist, so the shell must be protected or
	// removed. Never both absent.
	if !isolation.CurrentConfig().Enabled && agent.Tools.HasRegistered("exec") {
		t.Fatal("shell execution is available without isolation despite a requested MPM mode")
	}
}

func TestMPMInvalidWorkspaceDisablesEagerMemory(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	loop, provider := newMPMPolicyHarness(t, workspace,
		filepath.Join(root, "does-not-exist"), filepath.Join(root, "mpm-home"),
		"invalid-workspace", filepath.Join(root, "mpm"))

	prompt := sendPolicyTurn(t, loop, provider, "hello")
	if strings.Contains(prompt, eagerMemorySentinel) {
		t.Fatalf("invalid MPM workspace fell back to eager MEMORY.md:\n%s", prompt)
	}
	agent := loop.registry.GetDefaultAgent()
	if agent.MPMRetriever != nil || strings.TrimSpace(agent.MPMDegradedReason) == "" {
		t.Fatalf("expected degraded MPM state, retriever=%v reason=%q",
			agent.MPMRetriever, agent.MPMDegradedReason)
	}
}

func TestMPMDisabledStillAllowsEagerWorkspaceMemory(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "memory", "MEMORY.md"),
		[]byte(eagerMemorySentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	resetAgentIsolationState(t)
	t.Setenv("DEEPCLAW_MPM_ENABLED", "0")

	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace:         workspace,
		ModelName:         "test-model",
		MaxTokens:         4096,
		MaxToolIterations: 10,
	}}}
	provider := &recordingProvider{}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(loop.Close)

	prompt := sendPolicyTurn(t, loop, provider, "hello")
	if !strings.Contains(prompt, eagerMemorySentinel) {
		t.Fatalf("outside DeepClaw mode the eager MEMORY.md behavior changed:\n%s", prompt)
	}
}

func TestMPMMalformedResponseDegradesTurnWithoutAborting(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	mpmWorkspace := filepath.Join(root, "mpm-workspace")
	fakeBin := filepath.Join(root, "malformed-mpm")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nprintf 'not-json'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	loop, provider := newMPMPolicyHarness(t, workspace, mpmWorkspace,
		filepath.Join(root, "mpm-home"), "malformed", fakeBin)
	prompt := sendPolicyTurn(t, loop, provider, "secret code")

	if strings.Contains(prompt, "Retrieved context") || strings.Contains(prompt, "retrieval:mpm") {
		t.Fatalf("malformed retrieval produced a memory block:\n%s", prompt)
	}
	if strings.Contains(prompt, eagerMemorySentinel) {
		t.Fatalf("malformed retrieval fell back to eager MEMORY.md:\n%s", prompt)
	}
	if loop.registry.GetDefaultAgent().MPMRetriever == nil {
		t.Fatal("a malformed response is a runtime failure, not an init failure")
	}
}

func TestMPMTransientRetrievalErrorDegradesThenRecovers(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	mpmWorkspace := filepath.Join(root, "mpm-workspace")
	counter := filepath.Join(root, "counter")
	fakeBin := filepath.Join(root, "transient-mpm")
	body := "#!/bin/sh\n" +
		"if [ ! -f \"$FAKE_COUNTER\" ]; then\n" +
		"  : > \"$FAKE_COUNTER\"\n" +
		"  exit 1\n" +
		"fi\n" +
		"printf '%s' '{\"success\":true,\"memories\":[{\"id\":\"m1\",\"content\":\"TRANSIENT-CANARY-003D\",\"tags\":[\"project:transient\"],\"combined_score\":1}]}'\n"
	if err := os.WriteFile(fakeBin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_COUNTER", counter)

	loop, provider := newMPMPolicyHarness(t, workspace, mpmWorkspace,
		filepath.Join(root, "mpm-home"), "transient", fakeBin)

	first := sendPolicyTurn(t, loop, provider, "first retrieval query")
	if strings.Contains(first, "TRANSIENT-CANARY-003D") {
		t.Fatalf("the failing retrieval still produced memory:\n%s", first)
	}
	second := sendPolicyTurn(t, loop, provider, "second retrieval query")
	if !strings.Contains(second, "TRANSIENT-CANARY-003D") {
		t.Fatalf("recovered retrieval did not inject memory:\n%s", second)
	}
}

func TestDeepClawMPMForcesIsolationAndProtectsStore(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	mpmWorkspace := filepath.Join(root, "mpm-workspace")
	mpmHome := filepath.Join(root, "mpm-home")
	project := "forced-isolation"
	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project, "forced isolation provisioning fact")

	loop, _ := newMPMPolicyHarness(t, workspace, mpmWorkspace, mpmHome, project, bin)

	if !isolation.CurrentConfig().Enabled {
		t.Fatal("DeepClaw MPM mode did not enable subprocess isolation")
	}
	for _, path := range []string{mpmWorkspace, mpmHome, bin} {
		if !isolation.IsProtectedPath(path) {
			t.Fatalf("MPM path %q is not protected; protected=%v", path, isolation.ProtectedPaths())
		}
	}
	agent := loop.registry.GetDefaultAgent()
	if agent.MPMRetriever == nil {
		t.Fatal("trusted retriever should remain available")
	}
	if agent.MPMUnsandboxed {
		t.Fatal("default DeepClaw mode must not be unsandboxed")
	}
}

func TestDeepClawIsolationUnavailableFailsClosed(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	mpmWorkspace := filepath.Join(root, "mpm-workspace")
	mpmHome := filepath.Join(root, "mpm-home")
	project := "fail-closed"
	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project, "fail closed provisioning fact")

	t.Setenv("DEEPCLAW_CODING_PROFILE", "1")
	// No bwrap on PATH: the protection backend cannot be verified.
	t.Setenv("PATH", t.TempDir())
	loop, _ := newMPMPolicyHarness(t, workspace, mpmWorkspace, mpmHome, project, bin)

	agent := loop.registry.GetDefaultAgent()
	if agent.MPMRetriever == nil {
		t.Fatal("retriever initialization itself succeeded and should remain available")
	}
	if agent.Tools.HasRegistered("exec") || agent.Tools.HasRegistered("bash") {
		t.Fatal("shell execution stayed available without a verifiable isolation backend")
	}
}

func TestDeepClawUnsandboxedModeIsExplicit(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	mpmWorkspace := filepath.Join(root, "mpm-workspace")
	mpmHome := filepath.Join(root, "mpm-home")
	project := "unsafe-mode"
	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project, "unsafe mode provisioning fact")

	t.Setenv("DEEPCLAW_MPM_UNSANDBOXED", "1")
	loop, _ := newMPMPolicyHarness(t, workspace, mpmWorkspace, mpmHome, project, bin)

	agent := loop.registry.GetDefaultAgent()
	if !agent.MPMUnsandboxed {
		t.Fatal("explicit unsandboxed mode was not recorded")
	}
	if isolation.CurrentConfig().Enabled {
		t.Fatal("unsandboxed mode must not force isolation and pretend to be protected")
	}
	if !agent.Tools.HasRegistered("exec") {
		t.Fatal("unsandboxed mode should retain the ordinary shell")
	}
}
