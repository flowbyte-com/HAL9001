package install

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCandidateRefusesSymlinkedReleases(t *testing.T) {
	l := testLayout(t)
	rev := testRev('a')
	candidate := newCandidate(t, rev)
	outside := t.TempDir()
	if err := os.MkdirAll(l.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, l.Releases()); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCandidate(l, candidate, rev); err == nil {
		t.Fatal("a symlinked releases directory was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, rev)); !os.IsNotExist(err) {
		t.Fatalf("the release escaped into %s: %v", outside, err)
	}
}

func TestUpdateRefusesSymlinkedStaging(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	seedRelease(t, l, oldRev)
	outside := t.TempDir()
	realStaging := l.Staging() + ".real"
	if err := os.Rename(l.Staging(), realStaging); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, l.Staging()); err != nil {
		t.Fatal(err)
	}
	_, err := Update(context.Background(), l, &fakeRemote{rev: testRev('b')}, "main", &fakeBuilder{})
	if err == nil {
		t.Fatal("a symlinked staging directory was accepted")
	}
	if _, active, _ := ReadActive(l); active != oldRev {
		t.Fatalf("active = %s", active)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("update wrote into the staging symlink target: %v", entries)
	}
}

func TestUpdateRefusesCurrentPointerEscape(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	seedRelease(t, l, oldRev)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(l.Current()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, l.Current()); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(context.Background(), l, &fakeRemote{rev: testRev('b')}, "main", &fakeBuilder{}); err == nil {
		t.Fatal("an escaping current pointer was accepted")
	}
	status := Inspect(l)
	if status.Healthy {
		t.Fatal("an escaping current pointer was reported healthy")
	}
}

func TestBootstrapRefusesSourceInsideRoot(t *testing.T) {
	l := testLayout(t)
	if err := os.MkdirAll(l.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(l.Root, "checkout")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, inside, "init", "-q")
	runGit(t, inside, "config", "user.email", "test@example.invalid")
	runGit(t, inside, "config", "user.name", "HAL Test")
	if err := os.WriteFile(filepath.Join(inside, "file.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, inside, "add", "file.txt")
	runGit(t, inside, "commit", "-q", "-m", "inside")
	if _, err := Bootstrap(context.Background(), l, inside, &fakeBuilder{}, false); err == nil {
		t.Fatal("a source checkout inside the installation root was accepted")
	}
}

func TestStatusRejectsSymlinkedReleaseDirectory(t *testing.T) {
	l := testLayout(t)
	rev := testRev('a')
	seedRelease(t, l, rev)
	outside := t.TempDir()
	copyDir(t, l.ReleaseDir(rev), outside)
	if err := os.RemoveAll(l.ReleaseDir(rev)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, l.ReleaseDir(rev)); err != nil {
		t.Fatal(err)
	}
	status := Inspect(l)
	if status.Healthy {
		t.Fatal("a symlinked release directory was reported healthy")
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		s := filepath.Join(src, entry.Name())
		d := filepath.Join(dst, entry.Name())
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.IsDir() {
			copyDir(t, s, d)
			continue
		}
		body, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d, body, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
}
