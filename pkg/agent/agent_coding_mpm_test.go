package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/mpm"
)

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func queryDisposableMPM(t *testing.T, bin, workspace, home, project, query string) int {
	t.Helper()
	retriever, err := mpm.NewRetriever(mpm.Config{
		Executable:   bin,
		Workspace:    workspace,
		HomeDir:      home,
		ProjectID:    project,
		BudgetTokens: 350,
		Scope:        "local",
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("create retriever: %v", err)
	}
	retrieval, err := retriever.Retrieve(context.Background(), query)
	if err != nil {
		t.Fatalf("query mpm: %v", err)
	}
	return len(retrieval.Records)
}

func TestCodingProfileBashUsesMPMIsolation(t *testing.T) {
	const canary = "CODING-PROFILE-ESCAPE-003E"
	bin := mpmTestBinary(t)
	root := t.TempDir()
	agentWorkspace := filepath.Join(root, "agent-workspace")
	mpmWorkspace := filepath.Join(root, "mpm-workspace")
	mpmHome := filepath.Join(root, "mpm-home")
	project := "coding-profile"

	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project, "coding profile provisioning fact")
	t.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	t.Setenv("MPM_WORKSPACE", mpmWorkspace)
	t.Setenv("DEEPCLAW_MPM_EXECUTABLE", bin)
	t.Setenv("DEEPCLAW_MPM_HOME", mpmHome)
	t.Setenv("DEEPCLAW_MPM_PROJECT", project)
	t.Setenv("DEEPCLAW_MPM_BUDGET", "350")

	loop := newCodingProfileLoop(t, agentWorkspace, nil)
	agent := codingAgent(t, loop)
	if agent.MPMRetriever == nil {
		t.Fatal("trusted retriever is unavailable")
	}
	if agent.MPMUnsandboxed {
		t.Fatal("protected coding profile was marked unsandboxed")
	}
	got := agent.Tools.List()
	want := []string{"bash", "edit", "read", "write"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("coding profile tools = %v, want %v", got, want)
	}

	sentinel := filepath.Join(mpmWorkspace, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("host-original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(mpmWorkspace, "src", "db", "mpm.db")
	dbBefore := fileSHA256(t, database)

	payload, _ := json.Marshal(map[string]any{
		"action": "save",
		"params": map[string]any{
			"fact": canary,
			"tags": []string{"project:" + project},
		},
	})
	direct := fmt.Sprintf("%q call mpm_memory --payload %q", bin, string(payload))
	result := executeTool(t, agent, "bash", map[string]any{"command": direct})
	if !result.IsError {
		t.Logf("direct MPM invocation returned: %s", result.ForLLM)
	}

	script := filepath.Join(agentWorkspace, "escape.py")
	body := `import os, sys
workspace, sentinel = sys.argv[1], sys.argv[2]
print("listing:", sorted(os.listdir(workspace)) if os.path.isdir(workspace) else "missing")
with open(sentinel, "a") as handle:
    handle.write("interpreter-wrote\n")
`
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	result = executeTool(t, agent, "bash", map[string]any{
		"command": fmt.Sprintf("python3 %q %q %q", script, mpmWorkspace, sentinel),
	})
	if !result.IsError && (strings.Contains(result.ForLLM, "memories.db") ||
		strings.Contains(result.ForLLM, "listing: ['")) {
		t.Fatalf("protected MPM workspace is visible inside bash: %s", result.ForLLM)
	}

	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "host-original\n" {
		t.Fatalf("bash modified the protected MPM workspace: %q", data)
	}
	if got := fileSHA256(t, database); got != dbBefore {
		t.Fatalf("bash modified the MPM database: %s -> %s", dbBefore, got)
	}
	if count := queryDisposableMPM(t, bin, mpmWorkspace, mpmHome, project, canary); count != 0 {
		t.Fatalf("bash created %d MPM records for canary %q", count, canary)
	}

	// In-process file tools must enforce the same protected paths.
	result = executeTool(t, agent, "write", map[string]any{
		"path": database, "content": "tampered", "overwrite": true,
	})
	if !result.IsError || !strings.Contains(result.ForLLM, "DeepClaw MPM isolation") {
		t.Fatalf("write tool accepted a protected MPM path: %+v", result)
	}
	result = executeTool(t, agent, "edit", map[string]any{
		"path": database, "old_text": "x", "new_text": "y",
	})
	if !result.IsError {
		t.Fatalf("edit tool accepted a protected MPM path: %+v", result)
	}
	if !strings.Contains(result.ForLLM, "DeepClaw MPM isolation") &&
		!strings.Contains(result.ForLLM, "path escapes workspace") {
		t.Fatalf("edit tool denied for an unexpected reason: %s", result.ForLLM)
	}
	if got := fileSHA256(t, database); got != dbBefore {
		t.Fatalf("file tools modified the MPM database: %s -> %s", dbBefore, got)
	}
}

func TestCodingProfileFailClosedWithoutIsolationBackend(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	agentWorkspace := filepath.Join(root, "agent-workspace")
	mpmWorkspace := filepath.Join(root, "mpm-workspace")
	mpmHome := filepath.Join(root, "mpm-home")
	project := "coding-profile-fail-closed"
	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project, "fail closed provisioning fact")

	t.Setenv("DEEPCLAW_CODING_PROFILE", "1")
	// No bwrap on PATH: the protected execution boundary cannot be verified.
	t.Setenv("PATH", t.TempDir())
	loop, _ := newMPMPolicyHarness(t, agentWorkspace, mpmWorkspace, mpmHome, project, bin)
	agent := codingAgent(t, loop)

	if !agent.Tools.HasVisibilityProfile() {
		t.Fatal("coding profile was not installed")
	}
	for _, name := range []string{"bash", "exec"} {
		if _, ok := agent.Tools.Get(name); ok {
			t.Fatalf("%q stayed callable without a verifiable isolation backend", name)
		}
	}
	for _, name := range agent.Tools.List() {
		if name == "bash" || name == "exec" {
			t.Fatalf("%q is provider-visible without isolation: %v", name, agent.Tools.List())
		}
	}
}

func TestCodingProfileUnsafeModeRemainsExplicit(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	agentWorkspace := filepath.Join(root, "agent-workspace")
	mpmWorkspace := filepath.Join(root, "mpm-workspace")
	mpmHome := filepath.Join(root, "mpm-home")
	project := "coding-profile-unsafe"
	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project, "unsafe provisioning fact")

	t.Setenv("DEEPCLAW_CODING_PROFILE", "1")
	t.Setenv("DEEPCLAW_MPM_UNSANDBOXED", "1")
	loop, _ := newMPMPolicyHarness(t, agentWorkspace, mpmWorkspace, mpmHome, project, bin)
	agent := codingAgent(t, loop)

	if !agent.MPMUnsandboxed {
		t.Fatal("explicit unsafe mode was not recorded")
	}
	if _, ok := agent.Tools.Get("bash"); !ok {
		t.Fatal("unsafe mode should keep the ordinary bash tool")
	}
	if !strings.Contains(strings.Join(agent.Tools.List(), ","), "bash") {
		t.Fatalf("bash is not visible in unsafe mode: %v", agent.Tools.List())
	}
}

func TestCodingProfileProtectedWithoutUnsafeFlag(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	agentWorkspace := filepath.Join(root, "agent-workspace")
	mpmWorkspace := filepath.Join(root, "mpm-workspace")
	mpmHome := filepath.Join(root, "mpm-home")
	project := "coding-profile-protected"
	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project, "protected provisioning fact")

	t.Setenv("DEEPCLAW_CODING_PROFILE", "1")
	loop, _ := newMPMPolicyHarness(t, agentWorkspace, mpmWorkspace, mpmHome, project, bin)
	agent := codingAgent(t, loop)

	if agent.MPMRetriever == nil {
		t.Fatal("trusted retriever should be available")
	}
	if agent.MPMUnsandboxed {
		t.Fatal("protected coding profile was marked unsandboxed")
	}
	if !isolation.CurrentConfig().Enabled {
		t.Fatal("coding profile with MPM did not enable isolation")
	}
	if !strings.Contains(strings.Join(agent.Tools.List(), ","), "bash") {
		t.Fatalf("bash missing in protected profile: %v", agent.Tools.List())
	}
}
