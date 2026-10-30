package isolation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func resetProtectedForTest(t *testing.T) {
	t.Helper()
	ResetProtectedPaths()
	t.Cleanup(ResetProtectedPaths)
}

func TestProtectPaths_RejectsRelativePath(t *testing.T) {
	resetProtectedForTest(t)
	if err := ProtectPaths("relative/path"); err == nil {
		t.Fatal("ProtectPaths accepted a relative path")
	}
	if got := ProtectedPaths(); len(got) != 0 {
		t.Fatalf("registry changed after rejected path: %v", got)
	}
}

func TestProtectPaths_DeduplicatesAndResolves(t *testing.T) {
	resetProtectedForTest(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "store")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := ProtectPaths(target, target, link, ""); err != nil {
		t.Fatalf("ProtectPaths: %v", err)
	}
	paths := ProtectedPaths()
	count := 0
	for _, path := range paths {
		if path == target || path == link {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected the directory and its symlink alias once each, got %v", paths)
	}
	if !IsProtectedPath(filepath.Join(target, "nested", "memories.db")) {
		t.Fatal("nested path below a protected directory is not protected")
	}
	if IsProtectedPath(filepath.Join(dir, "unrelated")) {
		t.Fatal("unrelated path was reported as protected")
	}
	if !IsProtectedPath(link) {
		t.Fatal("symlink alias was not reported as protected")
	}
}

func TestApplyUserEnv_ScrubsMPMCapability(t *testing.T) {
	cmd := exec.Command("true")
	cmd.Env = []string{
		"DEEPCLAW_MPM_ENABLED=1",
		"DEEPCLAW_MPM_EXECUTABLE=/opt/mpm",
		"DEEPCLAW_MPM_HOME=/home/user/.mpm",
		"MPM_WORKSPACE=/home/user/.mpm/workspace",
		"MPM_UNRELATED=kept-out",
		"ORDINARY_VARIABLE=kept",
	}
	ApplyUserEnv(cmd, t.TempDir())

	env := strings.Join(cmd.Environ(), "\n")
	for _, leaked := range []string{"DEEPCLAW_MPM_", "MPM_"} {
		if strings.Contains(env, leaked) {
			t.Fatalf("MPM configuration leaked into the isolated child environment:\n%s", env)
		}
	}
	if !strings.Contains(env, "ORDINARY_VARIABLE=kept") {
		t.Fatalf("ordinary environment variable was dropped:\n%s", env)
	}
}
