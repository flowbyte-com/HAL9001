package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitialBootstrapInstallsBothCommands(t *testing.T) {
	l := testLayout(t)
	source := newSourceRepo(t)
	rev := testSHA(t, source)

	result, err := Bootstrap(context.Background(), l, source, &fakeBuilder{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.Action != "bootstrap" || result.To != rev {
		t.Fatalf("result = %#v", result)
	}
	target, active, err := ReadActive(l)
	if err != nil {
		t.Fatal(err)
	}
	if active != rev || target != "releases/"+rev {
		t.Fatalf("active = %q / %q", target, active)
	}
	if _, err := ValidateRelease(l.ReleaseDir(rev), rev); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{HalName, UpdaterName} {
		path := l.ReleaseBin(rev, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s is not executable", name)
		}
		if !LinkManaged(l, name) {
			t.Fatalf("%s is not a managed command link", name)
		}
	}
	status := Inspect(l)
	if !status.Healthy || !status.Installed {
		t.Fatalf("status = %#v", status)
	}
	if status.Revision != rev || status.Manifest.Version == "" {
		t.Fatalf("status identity = %#v", status)
	}
	if !strings.Contains(status.Report(), "Installation:       healthy") {
		t.Fatalf("status report = %s", status.Report())
	}
	entries, err := os.ReadDir(l.Staging())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging was not cleaned: %v", entries)
	}
}

func TestBootstrapRefusesDirtySource(t *testing.T) {
	l := testLayout(t)
	source := newSourceRepo(t)
	if err := os.WriteFile(filepath.Join(source, "untracked.txt"), []byte("no\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Bootstrap(context.Background(), l, source, &fakeBuilder{}, false)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error = %v", err)
	}
	if _, _, err := ReadActive(l); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("active after refusal = %v", err)
	}
}

func TestBootstrapPreservesUnrelatedHal(t *testing.T) {
	l := testLayout(t)
	source := newSourceRepo(t)
	if err := os.MkdirAll(l.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(l.BinDir, HalName)
	if err := os.WriteFile(legacy, []byte("legacy hal\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Bootstrap(context.Background(), l, source, &fakeBuilder{}, false)
	if err == nil || !strings.Contains(err.Error(), "not managed by HAL9001") {
		t.Fatalf("error = %v", err)
	}
	body, err := os.ReadFile(legacy)
	if err != nil || string(body) != "legacy hal\n" {
		t.Fatalf("legacy hal changed: %q, %v", body, err)
	}

	if _, err := Bootstrap(context.Background(), l, source, &fakeBuilder{}, true); err != nil {
		t.Fatal(err)
	}
	if !LinkManaged(l, HalName) || !LinkManaged(l, UpdaterName) {
		t.Fatal("managed links were not installed")
	}
	var backups []string
	err = filepath.WalkDir(l.Backups(), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == HalName {
			backups = append(backups, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	body, err = os.ReadFile(backups[0])
	if err != nil || string(body) != "legacy hal\n" {
		t.Fatalf("backup content = %q, %v", body, err)
	}
}

func TestBootstrapFailureDoesNotTouchExistingCommand(t *testing.T) {
	l := testLayout(t)
	source := newSourceRepo(t)
	if err := os.MkdirAll(l.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(l.BinDir, HalName)
	if err := os.WriteFile(legacy, []byte("legacy hal\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	builder := &fakeBuilder{fail: errors.New("build exploded")}
	if _, err := Bootstrap(context.Background(), l, source, builder, true); err == nil {
		t.Fatal("bootstrap must fail when the build fails")
	}
	body, err := os.ReadFile(legacy)
	if err != nil || string(body) != "legacy hal\n" {
		t.Fatalf("legacy hal changed: %q, %v", body, err)
	}
	if _, err := os.Stat(l.Backups()); !os.IsNotExist(err) {
		t.Fatalf("a failed build must not move the existing command: %v", err)
	}
	if _, _, err := ReadActive(l); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("active after failed bootstrap = %v", err)
	}
}

func TestBootstrapPreservesOperatorData(t *testing.T) {
	l := testLayout(t)
	source := newSourceRepo(t)
	home := t.TempDir()
	configDir := filepath.Join(home, ".hal9001")
	workspace := filepath.Join(home, "HAL")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "config.json")
	note := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(config, []byte(`{"endpoint":"keep-me"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(note, []byte("operator note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Bootstrap(context.Background(), l, source, &fakeBuilder{}, false); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		config: `{"endpoint":"keep-me"}`,
		note:   "operator note\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v", path, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(l.Root, "HAL")); !os.IsNotExist(err) {
		t.Fatalf("installation root must not contain the operator workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(l.Root, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("installation root must not contain sessions: %v", err)
	}
}

func TestBootstrapSecondRunIsIdempotent(t *testing.T) {
	l := testLayout(t)
	source := newSourceRepo(t)
	first, err := Bootstrap(context.Background(), l, source, &fakeBuilder{}, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Bootstrap(context.Background(), l, source, &fakeBuilder{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyCurrent || second.Updated {
		t.Fatalf("second bootstrap = %#v", second)
	}
	if first.To != second.To {
		t.Fatalf("revision changed: %s -> %s", first.To, second.To)
	}
	if !LinkManaged(l, HalName) || !LinkManaged(l, UpdaterName) {
		t.Fatal("links are not managed after a repeated bootstrap")
	}
}

func TestStatusIsReadOnlyWhenNotInstalled(t *testing.T) {
	l := testLayout(t)
	status := Inspect(l)
	if status.Installed || status.Healthy {
		t.Fatalf("status = %#v", status)
	}
	if !strings.Contains(status.Report(), "not installed") {
		t.Fatalf("report = %s", status.Report())
	}
	if _, err := os.Stat(l.Root); !os.IsNotExist(err) {
		t.Fatalf("status created the installation root: %v", err)
	}
}
