package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitRemoteResolvesFetchesAndCompares(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.invalid")
	runGit(t, repo, "config", "user.name", "HAL Test")
	runGit(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "commit", "-q", "-m", "one")
	first := testSHA(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "commit", "-q", "-am", "two")
	second := testSHA(t, repo)

	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, t.TempDir(), "clone", "-q", "--bare", repo, remoteDir)
	remote := NewGitRemote(remoteDir)

	ctx := context.Background()
	resolved, err := remote.Resolve(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != second {
		t.Fatalf("resolved = %s, want %s", resolved, second)
	}
	dest := filepath.Join(t.TempDir(), "source")
	meta, err := remote.Fetch(ctx, first, dest)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Commit != first || meta.Version == "" || meta.Epoch <= 0 || meta.Source != remoteDir {
		t.Fatalf("meta = %#v", meta)
	}
	if got := runGit(t, dest, "rev-parse", "HEAD"); got != first {
		t.Fatalf("checked out %s, want %s", got, first)
	}
	ancestor, err := remote.IsAncestor(ctx, "main", first, second)
	if err != nil {
		t.Fatal(err)
	}
	if !ancestor {
		t.Fatal("first commit must be an ancestor of the second")
	}
	ancestor, err = remote.IsAncestor(ctx, "main", second, first)
	if err != nil {
		t.Fatal(err)
	}
	if ancestor {
		t.Fatal("the later commit must not be an ancestor of the earlier one")
	}
}

func TestGitRemoteRejectsOptionLikeInput(t *testing.T) {
	remote := NewGitRemote("--upload-pack=evil")
	if _, err := remote.Resolve(context.Background(), "main"); err == nil {
		t.Fatal("an option-like remote must be refused")
	}
	if err := ValidateBranch("main;rm"); err == nil {
		t.Fatal("an invalid branch was accepted")
	}
	if err := ValidateBranch("-main"); err == nil {
		t.Fatal("an option-like branch was accepted")
	}
	if !strings.Contains(DefaultRemote, "flowbyte-com/HAL9001") {
		t.Fatalf("default remote = %s", DefaultRemote)
	}
}
