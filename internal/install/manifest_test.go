package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultLayoutUsesRequirementPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvInstallRoot, "")
	t.Setenv(EnvLocalBin, "")
	l, err := DefaultLayout()
	if err != nil {
		t.Fatal(err)
	}
	if l.Root != filepath.Join(home, ".hal") {
		t.Fatalf("root = %s", l.Root)
	}
	if l.BinDir != filepath.Join(home, ".local", "bin") {
		t.Fatalf("bin dir = %s", l.BinDir)
	}
}

func TestLayoutOverrides(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "install")
	bin := filepath.Join(base, "commands")
	t.Setenv(EnvInstallRoot, root)
	t.Setenv(EnvLocalBin, bin)
	l, err := DefaultLayout()
	if err != nil {
		t.Fatal(err)
	}
	if l.Root != root || l.BinDir != bin {
		t.Fatalf("layout = %#v", l)
	}
	if l.Current() != filepath.Join(root, "current") ||
		l.ReleaseBin("a", "hal") != filepath.Join(root, "releases", "a", "bin", "hal") {
		t.Fatalf("layout paths = %#v", l)
	}
}

func TestParseTargetRejectsEscapes(t *testing.T) {
	rev := testRev('a')
	if got, err := ParseTarget("releases/" + rev); err != nil || got != rev {
		t.Fatalf("valid target = %q, %v", got, err)
	}
	for _, target := range []string{
		"releases/../../etc",
		"/releases/" + rev,
		"releases/" + strings.ToUpper(rev),
		"releases/" + rev[:7],
		"other/" + rev,
		"releases/" + rev + "/extra",
	} {
		if _, err := ParseTarget(target); err == nil {
			t.Errorf("ParseTarget(%q) accepted an escape", target)
		}
	}
}

func TestInside(t *testing.T) {
	root := t.TempDir()
	if !Inside(root, filepath.Join(root, "releases", "x")) {
		t.Fatal("a child must be inside")
	}
	if Inside(root, filepath.Join(root, "..", "outside")) {
		t.Fatal("a sibling must not be inside")
	}
}

func newCandidate(t *testing.T, rev string) string {
	t.Helper()
	candidate := filepath.Join(t.TempDir(), "candidate")
	if err := os.MkdirAll(filepath.Join(candidate, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := testMeta(rev)
	if err := (&fakeBuilder{}).Build(context.Background(), "", filepath.Join(candidate, "bin"), meta.BuildMeta()); err != nil {
		t.Fatal(err)
	}
	m, err := NewManifest(candidate, meta.BuildMeta())
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(candidate, m); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func TestManifestRoundTripAndValidation(t *testing.T) {
	rev := testRev('a')
	candidate := newCandidate(t, rev)
	m, err := ReadManifest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema != ManifestSchema || m.Product != Product || m.Layout != LayoutVersion {
		t.Fatalf("manifest header = %#v", m)
	}
	if m.Commit != rev || m.Revision != rev || m.ShortCommit != rev[:8] {
		t.Fatalf("manifest identity = %#v", m)
	}
	if len(m.Binaries) != 2 || m.Binaries[0].Name != HalName || m.Binaries[1].Name != UpdaterName {
		t.Fatalf("manifest binaries = %#v", m.Binaries)
	}
	if _, err := ValidateRelease(candidate, rev); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRelease(candidate, testRev('b')); err == nil {
		t.Fatal("a manifest for another revision must be refused")
	}
}

func TestManifestRejectsCorruptJSONAndUnknownFields(t *testing.T) {
	rev := testRev('a')
	candidate := newCandidate(t, rev)
	manifest := filepath.Join(candidate, "manifest.json")
	if err := os.WriteFile(manifest, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(candidate); err == nil {
		t.Fatal("corrupt JSON was accepted")
	}
	if err := os.WriteFile(manifest, []byte(`{"schema":"hal-release/1","unknown":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(candidate); err == nil {
		t.Fatal("an unknown manifest field was accepted")
	}
}

func TestReleaseRejectsChangedBinary(t *testing.T) {
	rev := testRev('a')
	candidate := newCandidate(t, rev)
	hal := filepath.Join(candidate, "bin", HalName)
	if err := os.WriteFile(hal, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRelease(candidate, rev); err == nil {
		t.Fatal("a changed binary was accepted")
	}
}

func TestReleaseRejectsMissingBinary(t *testing.T) {
	rev := testRev('a')
	candidate := newCandidate(t, rev)
	if err := os.Remove(filepath.Join(candidate, "bin", UpdaterName)); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRelease(candidate, rev); err == nil {
		t.Fatal("a missing updater was accepted")
	}
}

func TestReleaseRejectsMixedBuild(t *testing.T) {
	rev := testRev('a')
	candidate := filepath.Join(t.TempDir(), "candidate")
	if err := os.MkdirAll(filepath.Join(candidate, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := testMeta(rev)
	if err := (&fakeBuilder{badUpdaterCommit: true}).Build(context.Background(), "", filepath.Join(candidate, "bin"), meta.BuildMeta()); err != nil {
		t.Fatal(err)
	}
	m, err := NewManifest(candidate, meta.BuildMeta())
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(candidate, m); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRelease(candidate, rev); err == nil || !strings.Contains(err.Error(), "mixed build") {
		t.Fatalf("error = %v", err)
	}
}

func TestReleaseRejectsUnexpectedEntriesAndGit(t *testing.T) {
	rev := testRev('a')
	candidate := newCandidate(t, rev)
	if err := os.WriteFile(filepath.Join(candidate, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRelease(candidate, rev); err == nil {
		t.Fatal("an unexpected release entry was accepted")
	}
	if err := os.Remove(filepath.Join(candidate, "README")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(candidate, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRelease(candidate, rev); err == nil {
		t.Fatal("Git metadata was accepted")
	}
}

func TestReleaseRejectsSymlinkedBinary(t *testing.T) {
	rev := testRev('a')
	candidate := newCandidate(t, rev)
	hal := filepath.Join(candidate, "bin", HalName)
	backup := filepath.Join(candidate, "bin", "hal-real")
	if err := os.Rename(hal, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, hal); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRelease(candidate, rev); err == nil {
		t.Fatal("a symlinked binary was accepted")
	}
}

func TestLockIsExclusiveAndReusable(t *testing.T) {
	l := testLayout(t)
	first, err := AcquireLock(l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(l); err == nil {
		t.Fatal("a second lock was granted")
	}
	first.Release()
	second, err := AcquireLock(l)
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
}

func TestCleanStagingRemovesOnlyOwnEntries(t *testing.T) {
	l := testLayout(t)
	if err := os.MkdirAll(l.Staging(), 0o755); err != nil {
		t.Fatal(err)
	}
	own := []string{"update-old", "candidate-old"}
	keep := []string{"keep-me", "operator-note"}
	for _, name := range append(append([]string{}, own...), keep...) {
		if err := os.MkdirAll(filepath.Join(l.Staging(), name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	CleanStaging(l)
	for _, name := range own {
		if _, err := os.Stat(filepath.Join(l.Staging(), name)); !os.IsNotExist(err) {
			t.Errorf("%s was not removed", name)
		}
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(l.Staging(), name)); err != nil {
			t.Errorf("%s was removed", name)
		}
	}
}

func TestRemoveStagingRefusesOutsidePath(t *testing.T) {
	l := testLayout(t)
	outside := t.TempDir()
	RemoveStaging(l, outside)
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside path was removed: %v", err)
	}
}
