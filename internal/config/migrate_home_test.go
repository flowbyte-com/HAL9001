package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic fixtures only. Every home directory here is a temporary
// directory; the operator's live files are never touched.

// fakeHome builds a legacy layout: ~/.hal9001 with a 0600 config,
// sessions, usage and audit, plus a ~/HAL workspace with nested files.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	legacy := filepath.Join(home, ".hal9001")
	if err := os.MkdirAll(filepath.Join(legacy, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(legacy, "config.json"), `{"endpoint":"keep-me"}`, 0o600)
	write(filepath.Join(legacy, "sessions", "a.jsonl"), "{\"role\":\"user\"}\n", 0o600)
	write(filepath.Join(legacy, "usage.json"), `{"v":1}`, 0o600)
	write(filepath.Join(legacy, "audit.jsonl"), "{\"op\":\"x\"}\n", 0o600)
	write(filepath.Join(legacy, "nested", "deep.txt"), "deep", 0o644)
	ws := filepath.Join(home, "HAL")
	write(filepath.Join(ws, "notes", "n.txt"), "note", 0o644)
	write(filepath.Join(ws, "uploads", "u.bin"), "\x00\x01", 0o600)
	return home
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestHomeDefaultsPointAtUnifiedLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HAL9001_HOME", "")
	t.Setenv("HAL9001_WORKSPACE", "")
	t.Setenv("HAL9001_CONFIG", "")
	if got := ConfigDir(); got != filepath.Join(home, ".hal", "data") {
		t.Errorf("ConfigDir = %q", got)
	}
	if got := DefaultStoreDir(); got != filepath.Join(home, ".hal", "data", "sessions") {
		t.Errorf("DefaultStoreDir = %q", got)
	}
	if got := DefaultWorkspaceDir(); got != filepath.Join(home, ".hal", "workspace") {
		t.Errorf("DefaultWorkspaceDir = %q", got)
	}
	// Explicit overrides keep working exactly as before.
	t.Setenv("HAL9001_HOME", filepath.Join(home, "custom-home"))
	if got := ConfigDir(); got != filepath.Join(home, "custom-home") {
		t.Errorf("ConfigDir override = %q", got)
	}
	if got := DefaultStoreDir(); got != filepath.Join(home, "custom-home", "sessions") {
		t.Errorf("DefaultStoreDir override = %q", got)
	}
	t.Setenv("HAL9001_WORKSPACE", filepath.Join(home, "custom-ws"))
	if got := DefaultWorkspaceDir(); got != filepath.Join(home, "custom-ws") {
		t.Errorf("DefaultWorkspaceDir override = %q", got)
	}
}

func TestMigrateHomeCopiesByteForByte(t *testing.T) {
	home := fakeHome(t)
	report, err := MigrateUserHomeAt(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Copied) == 0 {
		t.Fatal("nothing was copied")
	}
	pairs := [][2]string{
		{".hal9001/config.json", ".hal/data/config.json"},
		{".hal9001/sessions/a.jsonl", ".hal/data/sessions/a.jsonl"},
		{".hal9001/usage.json", ".hal/data/usage.json"},
		{".hal9001/audit.jsonl", ".hal/data/audit.jsonl"},
		{".hal9001/nested/deep.txt", ".hal/data/nested/deep.txt"},
		{"HAL/notes/n.txt", ".hal/workspace/notes/n.txt"},
		{"HAL/uploads/u.bin", ".hal/workspace/uploads/u.bin"},
	}
	for _, pair := range pairs {
		src := filepath.Join(home, filepath.FromSlash(pair[0]))
		dst := filepath.Join(home, filepath.FromSlash(pair[1]))
		if readFile(t, dst) != readFile(t, src) {
			t.Errorf("%s differs after migration", pair[1])
		}
		if fileMode(t, dst) != fileMode(t, src) {
			t.Errorf("%s mode = %o, want source mode %o", pair[1], fileMode(t, dst), fileMode(t, src))
		}
	}
	// The credential file keeps its restrictive permissions.
	if mode := fileMode(t, filepath.Join(home, ".hal", "data", "config.json")); mode != 0o600 {
		t.Errorf("migrated config mode = %o, want 600", mode)
	}
	// Legacy sources are left untouched: the copy is not a move.
	if readFile(t, filepath.Join(home, ".hal9001", "config.json")) != `{"endpoint":"keep-me"}` {
		t.Error("the legacy config was modified")
	}
	// A receipt records every migrated file.
	receipt, err := readHomeMigrationReceipt(HALRootFor(home))
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || len(receipt.Files) != len(pairs) {
		t.Fatalf("receipt = %+v", receipt)
	}
	// Reruns are idempotent: nothing left to copy, nothing errors.
	again, err := MigrateUserHomeAt(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Copied) != 0 {
		t.Errorf("rerun copied %v", again.Copied)
	}
	if len(again.SkippedIdentical) != len(pairs) {
		t.Errorf("rerun skipped %d, want %d", len(again.SkippedIdentical), len(pairs))
	}
}

func TestMigrateHomeLeavesAMovedOnDestinationAlone(t *testing.T) {
	// The destination legitimately evolves after migration: the schema
	// upgrade rewrites the config, new turns append to sessions. A rerun
	// must neither overwrite the new content nor refuse to start.
	home := fakeHome(t)
	if _, err := MigrateUserHomeAt(home); err != nil {
		t.Fatal(err)
	}
	upgraded := `{"version":2,"endpoint":"keep-me"}`
	dst := filepath.Join(home, ".hal", "data", "config.json")
	if err := os.WriteFile(dst, []byte(upgraded), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := MigrateUserHomeAt(home)
	if err != nil {
		t.Fatalf("rerun refused a moved-on destination: %v", err)
	}
	if len(report.Copied) != 0 {
		t.Errorf("rerun overwrote %v", report.Copied)
	}
	found := false
	for _, d := range report.DestChanged {
		if d == "data/config.json" {
			found = true
		}
	}
	if !found {
		t.Errorf("DestChanged = %v, want data/config.json listed", report.DestChanged)
	}
	if readFile(t, dst) != upgraded {
		t.Error("the rerun touched the moved-on destination")
	}
	// Cleanup still works: the backup preserves the legacy original,
	// and the evolved destination is left exactly as it is.
	cleanup, err := CleanupLegacyHomeAt(home, true)
	if err != nil {
		t.Fatalf("cleanup after a moved-on destination: %v", err)
	}
	if dirExists(filepath.Join(home, ".hal9001")) {
		t.Error("legacy data directory remains after cleanup")
	}
	if readFile(t, filepath.Join(cleanup.Backup, ".hal9001", "config.json")) != `{"endpoint":"keep-me"}` {
		t.Error("the backup does not hold the migrated original")
	}
	if readFile(t, dst) != upgraded {
		t.Error("cleanup touched the evolved destination")
	}
}

func TestMigrateHomeRefusesConflicts(t *testing.T) {
	home := fakeHome(t)
	dst := filepath.Join(home, ".hal", "data", "config.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(`{"endpoint":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := MigrateUserHomeAt(home)
	var conflicts *HomeMigrationConflictError
	if !errors.As(err, &conflicts) {
		t.Fatalf("error = %v, want a conflict refusal", err)
	}
	found := false
	for _, c := range conflicts.Conflicts {
		if strings.Contains(c, "config.json") {
			found = true
		}
	}
	if !found {
		t.Errorf("conflicts = %v, want config.json named", conflicts.Conflicts)
	}
	// The ambiguous destination is untouched and no receipt is written.
	if readFile(t, dst) != `{"endpoint":"other"}` {
		t.Error("the destination was modified despite the refusal")
	}
	if _, err := os.Stat(homeMigrationReceiptPath(HALRootFor(home))); !os.IsNotExist(err) {
		t.Error("a receipt was written despite the refusal")
	}
}

func TestMigrateHomeResumesPartial(t *testing.T) {
	home := fakeHome(t)
	// A previous interrupted run left a byte-identical subset behind.
	partial := filepath.Join(home, ".hal", "data", "sessions", "a.jsonl")
	if err := os.MkdirAll(filepath.Dir(partial), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, []byte("{\"role\":\"user\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := MigrateUserHomeAt(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Copied) == 0 {
		t.Error("the resume copied nothing")
	}
	for _, c := range report.Copied {
		if c == "data/sessions/a.jsonl" {
			t.Error("an identical file was recopied")
		}
	}
	if readFile(t, filepath.Join(home, ".hal", "data", "config.json")) != `{"endpoint":"keep-me"}` {
		t.Error("the resume did not complete the copy")
	}
}

func TestMigratedCopyLeavesNoTempFilesBehind(t *testing.T) {
	// The copy is atomic per file (temp file + rename): a killed run
	// can never leave a truncated destination that bricks the next
	// start, and a successful run leaves no temporary files behind.
	home := fakeHome(t)
	if _, err := MigrateUserHomeAt(home); err != nil {
		t.Fatal(err)
	}
	var leftovers []string
	for _, dir := range []string{filepath.Join(home, ".hal", "data"), filepath.Join(home, ".hal", "workspace")} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasPrefix(d.Name(), ".migrate-") || strings.HasSuffix(d.Name(), ".tmp") {
				leftovers = append(leftovers, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(leftovers) > 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestFailedCopyLeavesNoPartialDestination(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "src.txt")
	if err := os.WriteFile(src, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(src, 0o600)
	dst := filepath.Join(home, "dst", "out.txt")
	if err := copyFilePreservingMode(src, dst, 0o600); err == nil {
		t.Fatal("unreadable source was copied")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("a failed copy left a partial destination: %v", err)
	}
}

func TestMigrateHomeFreshInstallCreatesOnlyNewStructure(t *testing.T) {
	home := t.TempDir()
	report, err := MigrateUserHomeAt(home)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Initialized {
		t.Error("a fresh home must be reported as initialized")
	}
	for _, dir := range []string{
		filepath.Join(home, ".hal", "data"),
		filepath.Join(home, ".hal", "workspace"),
	} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("missing new directory %s", dir)
		}
	}
	for _, dir := range []string{
		filepath.Join(home, ".hal9001"),
		filepath.Join(home, "HAL"),
		filepath.Join(home, ".hal", "releases"),
		filepath.Join(home, ".hal", "current"),
		filepath.Join(home, ".hal", "state", "update.lock"),
	} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("fresh install created %s", dir)
		}
	}
}

func TestCleanupRequiresExplicitApproval(t *testing.T) {
	home := fakeHome(t)
	if _, err := MigrateUserHomeAt(home); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupLegacyHomeAt(home, false); err == nil {
		t.Fatal("cleanup without approval was allowed")
	}
	if !dirExists(filepath.Join(home, ".hal9001")) || !dirExists(filepath.Join(home, "HAL")) {
		t.Error("unapproved cleanup removed legacy directories")
	}
}

func TestCleanupMovesToBackupAndRollbackRestores(t *testing.T) {
	home := fakeHome(t)
	if _, err := MigrateUserHomeAt(home); err != nil {
		t.Fatal(err)
	}
	cleanup, err := CleanupLegacyHomeAt(home, true)
	if err != nil {
		t.Fatal(err)
	}
	if dirExists(filepath.Join(home, ".hal9001")) || dirExists(filepath.Join(home, "HAL")) {
		t.Fatal("legacy directories remain after cleanup")
	}
	if !dirExists(cleanup.Backup) {
		t.Fatalf("no backup at %s", cleanup.Backup)
	}
	// The backup holds byte-identical trees.
	if readFile(t, filepath.Join(cleanup.Backup, ".hal9001", "config.json")) != `{"endpoint":"keep-me"}` {
		t.Error("the backup does not hold the config")
	}
	if readFile(t, filepath.Join(cleanup.Backup, "HAL", "notes", "n.txt")) != "note" {
		t.Error("the backup does not hold the workspace file")
	}
	// New data keeps working from the new paths.
	if readFile(t, filepath.Join(home, ".hal", "data", "config.json")) != `{"endpoint":"keep-me"}` {
		t.Error("the migrated config is gone")
	}
	// Rollback restores the legacy layout byte-for-byte.
	rollback, err := RollbackHomeMigrationAt(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(rollback.Restored) == 0 {
		t.Error("rollback restored nothing")
	}
	if readFile(t, filepath.Join(home, ".hal9001", "config.json")) != `{"endpoint":"keep-me"}` {
		t.Error("the legacy config was not restored")
	}
	if readFile(t, filepath.Join(home, "HAL", "notes", "n.txt")) != "note" {
		t.Error("the legacy workspace file was not restored")
	}
	// The receipt stays but no longer names a backup, so the next
	// start re-migrates cleanly instead of refusing: the sources match
	// the receipt and the destinations legitimately moved on.
	receipt, err := readHomeMigrationReceipt(HALRootFor(home))
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || receipt.Backup != "" {
		t.Fatalf("receipt after rollback = %+v", receipt)
	}
	again, err := MigrateUserHomeAt(home)
	if err != nil {
		t.Fatalf("start after rollback refused: %v", err)
	}
	if len(again.Copied) != 0 {
		t.Errorf("start after rollback recopied %v", again.Copied)
	}
}

func TestRollbackRefusesConflictingLegacy(t *testing.T) {
	home := fakeHome(t)
	if _, err := MigrateUserHomeAt(home); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupLegacyHomeAt(home, true); err != nil {
		t.Fatal(err)
	}
	// Something new appeared where the legacy tree belongs.
	if err := os.MkdirAll(filepath.Join(home, ".hal9001"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".hal9001", "config.json"), []byte(`{"endpoint":"stranger"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := RollbackHomeMigrationAt(home)
	var conflicts *HomeMigrationConflictError
	if !errors.As(err, &conflicts) {
		t.Fatalf("rollback error = %v, want a conflict refusal", err)
	}
}

func TestRollbackWithoutBackupFails(t *testing.T) {
	home := fakeHome(t)
	if _, err := MigrateUserHomeAt(home); err != nil {
		t.Fatal(err)
	}
	// Migrated but never cleaned: there is no backup, so there is
	// nothing to roll back — the legacy data is already in place.
	if _, err := RollbackHomeMigrationAt(home); err == nil {
		t.Fatal("rollback without a backup was allowed")
	}
}

func TestCleanupRefusesWhenLegacyChangedSinceMigration(t *testing.T) {
	home := fakeHome(t)
	if _, err := MigrateUserHomeAt(home); err != nil {
		t.Fatal(err)
	}
	// The operator edits the legacy file after migrating; cleanup must
	// not silently drop that newer content.
	if err := os.WriteFile(filepath.Join(home, ".hal9001", "config.json"), []byte(`{"endpoint":"newer"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := CleanupLegacyHomeAt(home, true)
	var conflicts *HomeMigrationConflictError
	if !errors.As(err, &conflicts) {
		t.Fatalf("cleanup error = %v, want a conflict refusal", err)
	}
	if !dirExists(filepath.Join(home, ".hal9001")) {
		t.Error("cleanup removed the legacy directory despite the refusal")
	}
}
