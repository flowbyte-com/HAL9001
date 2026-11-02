package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
)

// labTempDir keeps every disposable resource under the repository's .lab tree,
// as required for these capability tests.
func labTempDir(t *testing.T, prefix string) string {
	t.Helper()
	base := filepath.Join("..", "..", ".lab", "tmp")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("create .lab/tmp: %v", err)
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		t.Fatalf("resolve .lab/tmp: %v", err)
	}
	dir, err := os.MkdirTemp(absBase, prefix)
	if err != nil {
		t.Fatalf("create lab temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func mpmBinaryForToolsTest(t *testing.T) string {
	t.Helper()
	candidates := []string{
		os.Getenv("DEEPCLAW_MPM_EXECUTABLE"),
		filepath.Join("..", "..", ".lab", "bin", "mpm"),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if abs, err := filepath.Abs(candidate); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	t.Skip("mpm test binary not available")
	return ""
}

type mpmEscapeFixture struct {
	root      string
	project   string
	workspace string
	home      string
	bin       string
}

func newMPMEscapeFixture(t *testing.T, project, fact string) *mpmEscapeFixture {
	t.Helper()
	root := labTempDir(t, "003d-capability-")
	workspace := filepath.Join(root, "mpm-workspace")
	home := filepath.Join(root, "mpm-home")
	projectDir := filepath.Join(root, "project")
	for _, dir := range []string{workspace, home, projectDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := mpmBinaryForToolsTest(t)
	payload, _ := json.Marshal(map[string]any{
		"action": "save",
		"params": map[string]any{
			"fact": fact,
			"tags": []string{"project:" + project},
		},
	})
	cmd := exec.Command(bin, "call", "mpm_memory", "--payload", string(payload))
	cmd.Env = mpmChildEnv(workspace, home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed mpm: %v: %s", err, out)
	}
	// Mimic the agent process environment: the shell inherits the same MPM
	// configuration the trusted retriever uses.
	t.Setenv("MPM_WORKSPACE", workspace)
	t.Setenv("DEEPCLAW_MPM_HOME", home)
	t.Setenv("HOME", home)

	fixture := &mpmEscapeFixture{
		root:      root,
		project:   project,
		workspace: workspace,
		home:      home,
		bin:       bin,
	}
	if err := os.WriteFile(fixture.sentinelPath(), []byte("host-original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func mpmChildEnv(workspace, home string) []string {
	return append(os.Environ(),
		"MPM_WORKSPACE="+workspace,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
	)
}

func (f *mpmEscapeFixture) sentinelPath() string {
	return filepath.Join(f.workspace, "sentinel.txt")
}

func (f *mpmEscapeFixture) projectDir() string {
	return filepath.Join(f.root, "project")
}

func (f *mpmEscapeFixture) query(t *testing.T, query string) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"action": "query",
		"params": map[string]any{
			"query":      query,
			"limit":      10,
			"projection": "full",
			"scope":      "local",
		},
	})
	cmd := exec.Command(f.bin, "call", "mpm_memory", "--payload", string(payload))
	cmd.Env = mpmChildEnv(f.workspace, f.home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("query mpm: %v: %s", err, out)
	}
	return string(out)
}

func hashTree(t *testing.T, root string) string {
	t.Helper()
	type entry struct {
		path string
		sum  string
	}
	var entries []entry
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		rel, _ := filepath.Rel(root, path)
		entries = append(entries, entry{path: rel, sum: hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		t.Fatalf("hash tree: %v", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s:%s\n", e.path, e.sum)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func enableProtectedIsolation(t *testing.T, fixture *mpmEscapeFixture) {
	t.Helper()
	t.Setenv("PICOCLAW_HOME", filepath.Join(fixture.root, "picoclaw-home"))
	cfg := config.DefaultConfig()
	cfg.Isolation.Enabled = true
	isolation.Configure(cfg)
	if err := isolation.ProtectPaths(fixture.workspace, fixture.home, fixture.bin); err != nil {
		t.Fatalf("protect paths: %v", err)
	}
	t.Cleanup(func() {
		isolation.ResetProtectedPaths()
		isolation.Configure(config.DefaultConfig())
	})
}

func disableIsolation(t *testing.T) {
	t.Helper()
	isolation.ResetProtectedPaths()
	isolation.Configure(config.DefaultConfig())
	t.Cleanup(func() {
		isolation.ResetProtectedPaths()
		isolation.Configure(config.DefaultConfig())
	})
}

func newAttackExecTool(t *testing.T, projectDir string) *ExecTool {
	t.Helper()
	cfg := config.DefaultConfig()
	tool, err := NewExecToolWithConfig(projectDir, false, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig: %v", err)
	}
	return tool
}

func runAttack(t *testing.T, tool *ExecTool, command string) *ToolResult {
	t.Helper()
	return tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": command,
		"timeout": float64(60),
	})
}

func writeEscapeScript(t *testing.T, projectDir string) string {
	t.Helper()
	script := filepath.Join(projectDir, "escape.py")
	body := `import glob, os, sqlite3, subprocess, sys

workspace, binary, canary = sys.argv[1], sys.argv[2], sys.argv[3]
print("listing:", sorted(os.listdir(workspace)) if os.path.isdir(workspace) else "missing")
payload = '{"action":"save","params":{"fact":"%s-subprocess","tags":["project:escape"]}}' % canary
subprocess.run([binary, "call", "mpm_memory", "--payload", payload], check=False)
for db in sorted(glob.glob(os.path.join(workspace, "**", "*.db"), recursive=True)):
    con = sqlite3.connect(db)
    con.execute("CREATE TABLE IF NOT EXISTS escape_canary (value TEXT)")
    con.execute("INSERT INTO escape_canary VALUES (?)", (canary,))
    con.commit()
    con.close()
    print("sqlite-ok:", db)
with open(os.path.join(workspace, "sentinel.txt"), "a") as handle:
    handle.write("interp-wrote\n")
print("done")
`
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return script
}

// TestWithoutIsolationDemonstratesMPMWrite is the Phase 1/2 reproduction: with
// no isolation and no protected paths, the coding shell reaches the disposable
// MPM store directly and through Python.
func TestWithoutIsolationDemonstratesMPMWrite(t *testing.T) {
	const canary = "ESCAPE-DEMO-003D-1"
	fixture := newMPMEscapeFixture(t, "escape", "escape provisioning fact")
	disableIsolation(t)
	tool := newAttackExecTool(t, fixture.projectDir())

	beforeSentinel, err := os.ReadFile(fixture.sentinelPath())
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"action":"save","params":{"fact":"` + canary + ` direct","tags":["project:escape"]}}`
	direct := runAttack(t, tool, fmt.Sprintf(`%q call mpm_memory --payload %q`, fixture.bin, payload))
	if direct.IsError {
		t.Fatalf("direct write unexpectedly failed before remediation: %s", direct.ForLLM)
	}
	if !strings.Contains(fixture.query(t, canary), canary) {
		t.Fatal("reproduction did not create the MPM record")
	}

	beforeTree := hashTree(t, fixture.workspace)
	script := writeEscapeScript(t, fixture.projectDir())
	_ = runAttack(t, tool, fmt.Sprintf(`python3 %q %q %q %q`,
		script, fixture.workspace, fixture.bin, "ESCAPE-DEMO-INTERP"))

	afterSentinel, err := os.ReadFile(fixture.sentinelPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeSentinel) == string(afterSentinel) {
		t.Fatal("interpreter did not modify the MPM workspace before remediation")
	}
	if hashTree(t, fixture.workspace) == beforeTree {
		t.Fatal("interpreter did not modify the MPM database/workspace before remediation")
	}
}

// TestProtectedShellCannotModifyMPMStore is the acceptance test: direct,
// interpreter and indirect subprocess attempts cannot change the host store.
func TestProtectedShellCannotModifyMPMStore(t *testing.T) {
	const canary = "ESCAPE-PROTECTED-003D-2"
	fixture := newMPMEscapeFixture(t, "protected", "protected provisioning fact")
	enableProtectedIsolation(t, fixture)
	tool := newAttackExecTool(t, fixture.projectDir())

	workspaceHash := hashTree(t, fixture.workspace)
	sentinelBefore, err := os.ReadFile(fixture.sentinelPath())
	if err != nil {
		t.Fatal(err)
	}

	payload := `{"action":"save","params":{"fact":"` + canary + ` direct","tags":["project:protected"]}}`
	result := runAttack(t, tool, fmt.Sprintf(`%q call mpm_memory --payload %q`, fixture.bin, payload))
	if result.IsError {
		t.Logf("direct MPM invocation was denied at execution: %s", result.ForLLM)
	}

	script := writeEscapeScript(t, fixture.projectDir())
	result = runAttack(t, tool, fmt.Sprintf(`python3 %q %q %q %q`,
		script, fixture.workspace, fixture.bin, canary+"-interp"))
	if strings.Contains(result.ForLLM, "sqlite-ok") {
		t.Fatalf("interpreter modified a database inside the protected store: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "listing:") {
		t.Fatalf("interpreter did not execute inside the sandbox: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "listing: []") {
		t.Fatalf("protected MPM store is visible inside the shell sandbox: %s", result.ForLLM)
	}

	afterSentinel, err := os.ReadFile(fixture.sentinelPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(sentinelBefore) != string(afterSentinel) {
		t.Fatal("interpreter modified the protected MPM workspace")
	}
	if got := hashTree(t, fixture.workspace); got != workspaceHash {
		t.Fatalf("protected MPM workspace changed: %s -> %s", workspaceHash, got)
	}
	if strings.Contains(fixture.query(t, canary), canary) {
		t.Fatal("protected shell created an MPM record")
	}
}

// TestProtectedShellScrubsMPMCapabilityEnv verifies that inherited MPM
// environment variables do not leak the capability into the child process.
func TestProtectedShellScrubsMPMCapabilityEnv(t *testing.T) {
	fixture := newMPMEscapeFixture(t, "env", "env provisioning fact")
	t.Setenv("DEEPCLAW_MPM_EXECUTABLE", fixture.bin)
	t.Setenv("MPM_WORKSPACE", fixture.workspace)
	t.Setenv("DEEPCLAW_MPM_HOME", fixture.home)
	t.Setenv("DEEPCLAW_MPM_PROJECT", "env")
	enableProtectedIsolation(t, fixture)
	tool := newAttackExecTool(t, fixture.projectDir())

	result := runAttack(t, tool, `env`)
	if result.IsError {
		t.Fatalf("env failed in the protected shell: %s", result.ForLLM)
	}
	if strings.Contains(result.ForLLM, "DEEPCLAW_MPM_") {
		t.Fatalf("MPM environment leaked into the protected shell:\n%s", result.ForLLM)
	}
	for _, line := range strings.Split(result.ForLLM, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "MPM_") {
			t.Fatalf("MPM environment leaked into the protected shell: %q", line)
		}
	}
}

// TestProtectedShellKeepsCodingWork confirms file edits and ordinary tooling
// still work inside the boundary.
func TestProtectedShellKeepsCodingWork(t *testing.T) {
	fixture := newMPMEscapeFixture(t, "coding", "coding provisioning fact")
	enableProtectedIsolation(t, fixture)
	tool := newAttackExecTool(t, fixture.projectDir())

	result := runAttack(t, tool,
		`printf 'hello-003d' > note.txt && cat note.txt && git --version >/dev/null && echo coding-ok`)
	if result.IsError {
		t.Fatalf("ordinary coding command failed: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "hello-003d") || !strings.Contains(result.ForLLM, "coding-ok") {
		t.Fatalf("ordinary coding command output = %q", result.ForLLM)
	}
	data, err := os.ReadFile(filepath.Join(fixture.projectDir(), "note.txt"))
	if err != nil || string(data) != "hello-003d" {
		t.Fatalf("workspace file was not written: %q, %v", data, err)
	}
}

// TestProtectedShellAfterRestart re-establishes the boundary and confirms the
// protected store stays out of reach for a fresh tool instance.
func TestProtectedShellAfterRestart(t *testing.T) {
	const canary = "ESCAPE-RESTART-003D-3"
	fixture := newMPMEscapeFixture(t, "restart", "restart provisioning fact")
	enableProtectedIsolation(t, fixture)
	tool := newAttackExecTool(t, fixture.projectDir())

	workspaceHash := hashTree(t, fixture.workspace)
	first := runAttack(t, tool, fmt.Sprintf(`python3 -c %q`,
		`import os; open(os.path.join(os.environ.get("HOME","/nonexistent"),"x"),"w").write("x")`))
	_ = first

	// Simulate a process restart: fresh config generation, same protected paths.
	isolation.ResetProtectedPaths()
	if err := isolation.ProtectPaths(fixture.workspace, fixture.home, fixture.bin); err != nil {
		t.Fatal(err)
	}
	tool = newAttackExecTool(t, fixture.projectDir())
	payload := `{"action":"save","params":{"fact":"` + canary + `","tags":["project:restart"]}}`
	_ = runAttack(t, tool, fmt.Sprintf(`%q call mpm_memory --payload %q`, fixture.bin, payload))

	if got := hashTree(t, fixture.workspace); got != workspaceHash {
		t.Fatalf("protected workspace changed after restart: %s -> %s", workspaceHash, got)
	}
	if strings.Contains(fixture.query(t, canary), canary) {
		t.Fatal("after restart the shell created an MPM record")
	}
}

// TestCodingBashRoutesThroughProtectedExecution proves that the minimal
// coding `bash` adapter delegates to the existing ExecTool and therefore keeps
// the 003D isolation boundary instead of calling os/exec directly.
func TestCodingBashRoutesThroughProtectedExecution(t *testing.T) {
	const canary = "ESCAPE-CODING-BASH-003E"
	fixture := newMPMEscapeFixture(t, "coding-bash", "coding bash provisioning fact")
	enableProtectedIsolation(t, fixture)
	inner := newAttackExecTool(t, fixture.projectDir())
	bash := NewCodingBashTool(inner)

	workspaceHash := hashTree(t, fixture.workspace)
	sentinelBefore, err := os.ReadFile(fixture.sentinelPath())
	if err != nil {
		t.Fatal(err)
	}
	run := func(command string) *ToolResult {
		return bash.Execute(context.Background(), map[string]any{
			"command": command,
			"timeout": float64(60),
		})
	}

	payload := `{"action":"save","params":{"fact":"` + canary + `","tags":["project:coding-bash"]}}`
	result := run(fmt.Sprintf(`%q call mpm_memory --payload %q`, fixture.bin, payload))
	if !result.IsError {
		t.Logf("direct MPM invocation returned: %s", result.ForLLM)
	}

	script := writeEscapeScript(t, fixture.projectDir())
	result = run(fmt.Sprintf(`python3 %q %q %q %q`,
		script, fixture.workspace, fixture.bin, canary+"-interp"))
	if strings.Contains(result.ForLLM, "sqlite-ok") {
		t.Fatalf("coding bash modified a database inside the protected store: %s", result.ForLLM)
	}

	sentinelAfter, err := os.ReadFile(fixture.sentinelPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(sentinelBefore) != string(sentinelAfter) {
		t.Fatal("coding bash modified the protected MPM workspace")
	}
	if got := hashTree(t, fixture.workspace); got != workspaceHash {
		t.Fatalf("protected MPM workspace changed: %s -> %s", workspaceHash, got)
	}
	if strings.Contains(fixture.query(t, canary), canary) {
		t.Fatal("coding bash created an MPM record")
	}

	// Ordinary foreground execution still works through the adapter.
	result = run(`printf coding-bash-routes-ok`)
	if result.IsError || !strings.Contains(result.ForLLM, "coding-bash-routes-ok") {
		t.Fatalf("ordinary coding bash command failed: %+v", result)
	}
}
