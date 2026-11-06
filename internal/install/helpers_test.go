package install

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRemote struct {
	rev         string
	meta        SourceMeta
	resolveErr  error
	fetchErr    error
	ancestor    bool
	ancestorErr error
	fetched     string
}

func (r *fakeRemote) Resolve(context.Context, string) (string, error) {
	if r.resolveErr != nil {
		return "", r.resolveErr
	}
	return r.rev, nil
}

func (r *fakeRemote) Fetch(_ context.Context, commit, dest string) (SourceMeta, error) {
	if r.fetchErr != nil {
		return SourceMeta{}, r.fetchErr
	}
	r.fetched = commit
	meta := r.meta
	if meta.Commit == "" {
		meta.Commit = commit
	}
	if meta.Version == "" {
		meta.Version = "test-" + commit[:8]
	}
	if meta.Epoch == 0 {
		meta.Epoch = 1_700_000_000
	}
	if meta.Source == "" {
		meta.Source = "test://remote"
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return SourceMeta{}, err
	}
	return meta, nil
}

func (r *fakeRemote) IsAncestor(context.Context, string, string, string) (bool, error) {
	return r.ancestor, r.ancestorErr
}

type fakeBuilder struct {
	fail             error
	badUpdaterCommit bool
	missingUpdater   bool
	calls            int
}

func (b *fakeBuilder) Build(_ context.Context, _ string, binDir string, meta BuildMeta) error {
	b.calls++
	if b.fail != nil {
		return b.fail
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	updaterCommit := meta.Commit
	if b.badUpdaterCommit {
		updaterCommit = strings.Repeat("f", 40)
	}
	if err := writeScript(filepath.Join(binDir, HalName), "HAL9001", meta, meta.Commit); err != nil {
		return err
	}
	if b.missingUpdater {
		return nil
	}
	return writeScript(filepath.Join(binDir, UpdaterName), "HAL9001 updater", meta, updaterCommit)
}

func writeScript(path, product string, meta BuildMeta, commit string) error {
	body := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\n  echo \"%s %s\"\n  echo \"commit: %s\"\n  exit 0\nfi\nexit 0\n", product, meta.Version, commit)
	return os.WriteFile(path, []byte(body), 0o755)
}

func testLayout(t *testing.T) Layout {
	t.Helper()
	base := t.TempDir()
	return NewLayout(filepath.Join(base, "root"), filepath.Join(base, "bin"))
}

func testRev(ch byte) string {
	return strings.Repeat(string(ch), 40)
}

func testMeta(rev string) SourceMeta {
	return SourceMeta{
		Commit:  rev,
		Version: "v-test-" + rev[:8],
		Epoch:   1_700_000_000,
		Source:  "test://seed",
	}
}

// seedRelease installs one complete release without going through
// bootstrap or Git, so update/rollback tests start from a known state.
func seedRelease(t *testing.T, l Layout, rev string) {
	t.Helper()
	if err := os.MkdirAll(l.Staging(), 0o755); err != nil {
		t.Fatal(err)
	}
	candidate, err := os.MkdirTemp(l.Staging(), "candidate-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(candidate, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := testMeta(rev)
	builder := &fakeBuilder{}
	if err := builder.Build(context.Background(), "", filepath.Join(candidate, "bin"), meta.BuildMeta()); err != nil {
		t.Fatal(err)
	}
	m, err := NewManifest(candidate, meta.BuildMeta())
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(candidate, m); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCandidate(l, candidate, rev); err != nil {
		t.Fatal(err)
	}
	if _, err := Activate(l, rev); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileLinks(l, true); err != nil {
		t.Fatal(err)
	}
}

func newSourceRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for bootstrap tests")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.invalid")
	runGit(t, dir, "config", "user.name", "HAL Test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test checkout\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func testSHA(t *testing.T, dir string) string {
	t.Helper()
	return runGit(t, dir, "rev-parse", "HEAD")
}
