package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
)

// 005E: Pi-like syntax inside an OS-enforced boundary.
//
// These tests run the real ExecTool with verified bubblewrap isolation and
// prove the two halves of the design change independently:
//
//  1. Ordinary coding commands the lexical guard used to reject now run
//     (go test ./..., heredocs, $(...), absolute in-workspace paths,
//     chmod, rm -f, export).
//  2. Protected MPM storage, credentials and out-of-scope host paths stay
//     blocked by OS isolation (masked mounts, env scrub, read-only
//     system) — with messages that are NOT guard denials.
//
// Disposable directories only; no live MPM, no network (GOPROXY=off), no
// changes to production defaults outside each test's cleanup.

func enableVerifiedIsolation(t *testing.T, root string) (project, mpmWorkspace, mpmHome string) {
	t.Helper()
	project = filepath.Join(root, "project")
	mpmWorkspace = filepath.Join(root, "mpm-workspace")
	mpmHome = filepath.Join(root, "mpm-home")
	home := filepath.Join(root, "picoclaw-home")
	for _, dir := range []string{project, mpmWorkspace, mpmHome, home} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(mpmWorkspace, "sentinel.txt"), []byte("mpm-store-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PICOCLAW_HOME", home)
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("MPM_WORKSPACE", mpmWorkspace)
	t.Setenv("DEEPCLAW_MPM_HOME", mpmHome)
	cfg := config.DefaultConfig()
	cfg.Isolation.Enabled = true
	isolation.Configure(cfg)
	if err := isolation.ProtectPaths(mpmWorkspace, mpmHome); err != nil {
		t.Fatalf("protect paths: %v", err)
	}
	if err := isolation.Preflight(); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !isolation.BubblewrapEnforced() {
		t.Fatal("isolation not verified on this host")
	}
	t.Cleanup(func() {
		isolation.ResetProtectedPaths()
		isolation.Configure(config.DefaultConfig())
	})
	return project, mpmWorkspace, mpmHome
}

func bypassScratchRepo(t *testing.T, project string) {
	t.Helper()
	files := map[string]string{
		"go.mod":       "module example.com/bypass\n\ngo 1.21\n",
		"main.go":      "package bypass\n\nfunc Add(a, b int) int { return a + b }\n",
		"main_test.go": "package bypass\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad add\")\n\t}\n}\n",
		"check.sh":     "#!/bin/sh\nset -eu\ngo test ./...\n",
		"diagnose.sh":  "#!/bin/sh\necho legacy\n",
		"legacy.txt":   "old-carrier weight table\n1000 g = 750\n",
		"README.md":    "# Bypass fixture\n",
	}
	for name, content := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(project, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
}

func bypassRun(t *testing.T, tool *ExecTool, dir, command string) (bool, string) {
	t.Helper()
	result := tool.Execute(context.Background(), map[string]any{
		"action": "run", "command": command, "cwd": dir, "timeout": float64(120),
	})
	content := result.ContentForLLM()
	if strings.HasPrefix(content, "Command blocked by safety guard") {
		t.Fatalf("lexical guard fired under verified isolation for %q: %.120s", command, content)
	}
	return !result.IsError, content
}

func TestBypassModeAllowsOrdinaryCoding(t *testing.T) {
	if testing.Short() {
		t.Skip("bypass tests need bubblewrap and a Go toolchain")
	}
	root := t.TempDir()
	project, _, _ := enableVerifiedIsolation(t, root)
	bypassScratchRepo(t, project)
	tool, err := NewExecTool(project, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"./check.sh",
		"go test ./... 2>&1 | head -40",
		"go vet ./... 2>&1 | head -20",
		"go test -run TestAdd -v .",
		"go test -run TestAdd -v ./... | tail -5",
		"cat > /tmp/from-bypass.txt <<'EOF'\npackage x\nEOF",
		"export BYPASS_PROBE_CHILD=1 && printenv BYPASS_PROBE_CHILD",
		"GOCACHE=.gocache go test .",
		"rm -f zz_tmp_bypass.go && ls zz_tmp_bypass.go 2>&1; true",
		"chmod 644 main.go && ls -la main.go",
		"which go && go version",
		"touch zz_mode_tmp && chmod 644 zz_mode_tmp && rm -f zz_mode_tmp && echo CLEANUP-OK",
		"echo $(which go) | head -c 20",
		"ls -la /tmp 2>/dev/null | head -3; pwd",
		"cat " + filepath.Join(project, "go.mod"),
		"python3 -c \"print('py-ok')\"",
	} {
		if ok, content := bypassRun(t, tool, project, command); !ok {
			t.Logf("note: %q ran with error (allowed, failed at runtime): %.200s", command, content)
		}
	}
}

func TestBypassModeKeepsOSEnforcement(t *testing.T) {
	if testing.Short() {
		t.Skip("bypass tests need bubblewrap and a Go toolchain")
	}
	root := t.TempDir()
	project, mpmWorkspace, _ := enableVerifiedIsolation(t, root)
	bypassScratchRepo(t, project)
	tool, err := NewExecTool(project, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 1. Protected MPM store is masked: the directory reads empty and the
	//    sentinel is unreadable, without any guard-denial message.
	ok, content := bypassRun(t, tool, project, "ls -la "+mpmWorkspace)
	if !ok {
		t.Fatalf("listing masked store should succeed empty, got error: %.160s", content)
	}
	if strings.Contains(content, "sentinel.txt") {
		t.Fatalf("masked MPM store is visible: %.160s", content)
	}
	ok, content = bypassRun(t, tool, project, "cat "+filepath.Join(mpmWorkspace, "sentinel.txt"))
	if ok {
		t.Fatal("reading a masked MPM file succeeded")
	}
	if strings.HasPrefix(content, "Command blocked by safety guard") {
		t.Fatalf("MPM read blocked by lexical guard instead of OS isolation: %.120s", content)
	}
	// 2. Alternate spellings of the same access fail too: interpreter and
	//    rewritten absolute paths.
	for _, command := range []string{
		"python3 -c \"import os; print(os.listdir('" + mpmWorkspace + "'))\"",
		"cat " + mpmWorkspace + "/./sentinel.txt",
		"ls " + mpmWorkspace + "/../" + filepath.Base(mpmWorkspace),
	} {
		ok, content := bypassRun(t, tool, project, command)
		if ok && strings.Contains(content, "sentinel") {
			t.Fatalf("alternate MPM access succeeded: %q -> %.160s", command, content)
		}
		if strings.HasPrefix(content, "Command blocked by safety guard") {
			t.Fatalf("alternate MPM access blocked by lexical guard instead of OS isolation: %q", command)
		}
	}
	// 3. Writes outside the workspace fail at the OS boundary.
	ok, content = bypassRun(t, tool, project, "touch /tmp/bypass-escape-proof && ls /tmp/bypass-escape-proof")
	if ok {
		t.Fatal("writing outside the workspace succeeded")
	}
	if strings.HasPrefix(content, "Command blocked by safety guard") {
		t.Fatalf("host escape blocked by lexical guard instead of OS isolation: %.120s", content)
	}
	// 4. MPM capability environment stays scrubbed from children.
	ok, content = bypassRun(t, tool, project, "env | grep -E 'MPM_|DEEPCLAW_MPM' || echo CLEAN")
	if !ok || !strings.Contains(content, "CLEAN") {
		t.Fatalf("MPM capability env leaked into the sandbox: %.160s", content)
	}
}

func TestBypassModeGoCacheAndTmpAudit(t *testing.T) {
	if testing.Short() {
		t.Skip("bypass tests need bubblewrap and a Go toolchain")
	}
	root := t.TempDir()
	project, _, _ := enableVerifiedIsolation(t, root)
	bypassScratchRepo(t, project)
	tool, err := NewExecTool(project, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Default cache under the (sandboxed) HOME must work for ordinary
	// `go test ./...` — the exact invocation the lexical guard rejected.
	ok, content := bypassRun(t, tool, project, "go test ./... 2>&1 | tail -3")
	if !ok || !strings.Contains(content, "ok") {
		t.Fatalf("go test ./... failed under verified isolation: %.200s", content)
	}
	// A workspace-local cache dir is allowed and usable.
	ok, content = bypassRun(t, tool, project, "mkdir -p .gocache && GOCACHE=$PWD/.gocache go test -count=1 . 2>&1 | tail -3")
	if !ok {
		t.Fatalf("workspace-local GOCACHE failed: %.200s", content)
	}
	// Temporary files inside the workspace work.
	ok, content = bypassRun(t, tool, project, "echo data > tmp-check.txt && cat tmp-check.txt && rm tmp-check.txt && echo TMP-OK")
	if !ok || !strings.Contains(content, "TMP-OK") {
		t.Fatalf("workspace tmp file round-trip failed: %.160s", content)
	}
}
