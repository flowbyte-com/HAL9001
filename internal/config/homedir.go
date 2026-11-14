// Home directory consolidation.
//
// HAL's user-owned runtime data lives in one place:
//
//	~/.hal/data/      configuration, credentials, sessions, audit and usage
//	~/.hal/workspace/ the workspace HAL may propose files in
//
// The installation tree under the same root is untouched:
//
//	~/.hal/releases/  ~/.hal/current  ~/.hal/state/
//
// The previous layout kept data in ~/.hal9001 and the workspace in
// ~/HAL. MigrateUserHome copies those trees into the new layout,
// byte-for-byte with permissions intact, and refuses to guess when both
// sides contain conflicting data. Cleanup of the legacy directories is
// never automatic: it moves them into a timestamped backup under
// ~/.hal/state/backups and requires explicit approval, and
// RollbackHomeMigration restores them from that backup. The previous
// release never learns the new paths, so the backup is the rollback.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EnvCleanLegacyHome gates deletion of the legacy directories. The
// migration itself only copies; nothing is removed until the operator
// sets HAL_CLEAN_LEGACY_HOME=1 for that run.
const EnvCleanLegacyHome = "HAL_CLEAN_LEGACY_HOME"

// EnvRollbackHome runs a home migration rollback and exits. It restores
// the legacy layout from the migration backup, so a previous release,
// which only understands the old paths, works again.
const EnvRollbackHome = "HAL_ROLLBACK_HOME_MIGRATION"

// homeMigrationVersion identifies the receipt schema.
const homeMigrationVersion = 1

// homeMigrationReceipt is the receipt name inside <root>/state.
const homeMigrationReceipt = "home-migration.json"

// HALRootFor returns the user-owned HAL root for a home directory.
func HALRootFor(home string) string {
	return filepath.Join(home, ".hal")
}

// DataDirFor returns the directory holding configuration, sessions,
// audit and usage for a home directory.
func DataDirFor(home string) string {
	return filepath.Join(HALRootFor(home), "data")
}

// WorkspaceDefaultFor returns the default workspace directory for a
// home directory.
func WorkspaceDefaultFor(home string) string {
	return filepath.Join(HALRootFor(home), "workspace")
}

// LegacyDataDirFor returns the pre-consolidation data directory.
func LegacyDataDirFor(home string) string {
	return filepath.Join(home, ".hal9001")
}

// LegacyWorkspaceDirFor returns the pre-consolidation workspace directory.
func LegacyWorkspaceDirFor(home string) string {
	return filepath.Join(home, "HAL")
}

// HomeMigrationConflictError reports ambiguous state: the same relative
// path holds different bytes on both sides, so no automatic choice is
// safe. It carries every conflicting path.
type HomeMigrationConflictError struct {
	Conflicts []string
}

func (e *HomeMigrationConflictError) Error() string {
	sort.Strings(e.Conflicts)
	return fmt.Sprintf("config: refusing ambiguous home migration; conflicting paths: %s",
		strings.Join(e.Conflicts, ", "))
}

// migratedFile is one verified file of a home migration.
type migratedFile struct {
	// Base is "data" or "workspace": which legacy tree the file came from.
	Base string `json:"base"`
	// Rel is the path relative to that tree, slash-separated.
	Rel string `json:"rel"`
	// Size is the byte count.
	Size int64 `json:"size"`
	// SHA256 is the hex digest of the bytes.
	SHA256 string `json:"sha256"`
	// Mode is the source file permission bits.
	Mode uint32 `json:"mode"`
}

// homeMigrationReceiptFile is the on-disk migration record.
type homeMigrationReceiptFile struct {
	Version   int            `json:"version"`
	At        string         `json:"at"`
	Files     []migratedFile `json:"files"`
	Backup    string         `json:"backup,omitempty"`
	CleanedAt string         `json:"cleaned_at,omitempty"`
}

// HomeMigrationReport describes one migration run.
type HomeMigrationReport struct {
	// Home is the home directory the migration ran against.
	Home string
	// Copied lists destination-relative paths written by this run
	// ("data/..." or "workspace/...").
	Copied []string
	// SkippedIdentical lists source files already byte-for-byte in place.
	SkippedIdentical []string
	// DestChanged lists source files left alone because the destination
	// has legitimately moved on since migration: the source still
	// matches the receipt, while the destination was transformed
	// afterwards (for example the configuration schema upgrade, or new
	// turns appended to a session). The destination always wins.
	DestChanged []string
	// Initialized reports a fresh installation: no legacy data existed,
	// and the new directories were ensured.
	Initialized bool
}

// HomeCleanupReport describes one legacy cleanup run.
type HomeCleanupReport struct {
	// Backup is the directory the legacy trees were moved to, absolute.
	Backup string
	// Moved lists the legacy directories that were moved.
	Moved []string
}

// HomeRollbackReport describes one rollback run.
type HomeRollbackReport struct {
	// Restored lists the legacy directories put back in place.
	Restored []string
	// AlreadyPresent reports the legacy data was already in place and
	// verified against the backup, so nothing moved.
	AlreadyPresent bool
}

// MigrateUserHome migrates the operator's home directory.
func MigrateUserHome() (HomeMigrationReport, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return HomeMigrationReport{}, nil
	}
	return MigrateUserHomeAt(home)
}

// MigrateUserHomeAt migrates one home directory.
func MigrateUserHomeAt(home string) (HomeMigrationReport, error) {
	report := HomeMigrationReport{Home: home}
	root := HALRootFor(home)
	data := DataDirFor(home)
	workspace := WorkspaceDefaultFor(home)
	legacyData := LegacyDataDirFor(home)
	legacyWorkspace := LegacyWorkspaceDirFor(home)

	legacyDataExists := dirExists(legacyData)
	legacyWorkspaceExists := dirExists(legacyWorkspace)
	if !legacyDataExists && !legacyWorkspaceExists {
		// Fresh installation, or everything already cleaned up: only
		// the new structure may exist. Nothing legacy is read.
		if err := os.MkdirAll(data, 0o700); err != nil {
			return report, fmt.Errorf("config: %w", err)
		}
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			return report, fmt.Errorf("config: %w", err)
		}
		report.Initialized = true
		return report, nil
	}

	type job struct {
		base string
		src  string
		dst  string
	}
	jobs := []job{
		{base: "data", src: legacyData, dst: data},
		{base: "workspace", src: legacyWorkspace, dst: workspace},
	}
	// Refuse before touching anything: compare every source file with
	// its destination first.
	previous, err := readHomeMigrationReceipt(root)
	if err != nil {
		return report, err
	}
	prev := map[string]string{}
	if previous != nil {
		for _, f := range previous.Files {
			prev[f.Base+"/"+f.Rel] = f.SHA256
		}
	}
	var conflicts []string
	var planned []migratedFile
	for _, j := range jobs {
		if !dirExists(j.src) {
			continue
		}
		err := filepath.WalkDir(j.src, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				conflicts = append(conflicts, j.base+"/"+slashRel(j.src, path)+" (not a regular file)")
				return nil
			}
			if !d.Type().IsRegular() {
				conflicts = append(conflicts, j.base+"/"+slashRel(j.src, path)+" (not a regular file)")
				return nil
			}
			rel := slashRel(j.src, path)
			srcSum, srcSize, srcMode, err := hashFile(path)
			if err != nil {
				return err
			}
			dstPath := filepath.Join(j.dst, filepath.FromSlash(rel))
			dstInfo, err := os.Stat(dstPath)
			if err == nil {
				if dstInfo.IsDir() {
					conflicts = append(conflicts, j.base+"/"+rel+" (destination is a directory)")
					return nil
				}
				dstSum, _, _, err := hashFile(dstPath)
				if err != nil {
					return err
				}
				if dstSum != srcSum {
					if prev[j.base+"/"+rel] == srcSum {
						// The legacy side is unchanged since migration; the
						// destination moved on afterwards (the configuration
						// schema upgrade, new turns appended to a session).
						// The destination always wins: leave it alone.
						report.DestChanged = append(report.DestChanged, j.base+"/"+rel)
						planned = append(planned, migratedFile{Base: j.base, Rel: rel, Size: srcSize, SHA256: srcSum, Mode: uint32(srcMode)})
						return nil
					}
					conflicts = append(conflicts, j.base+"/"+rel)
					return nil
				}
				report.SkippedIdentical = append(report.SkippedIdentical, j.base+"/"+rel)
				planned = append(planned, migratedFile{Base: j.base, Rel: rel, Size: srcSize, SHA256: srcSum, Mode: uint32(srcMode)})
				return nil
			}
			if !os.IsNotExist(err) {
				return err
			}
			planned = append(planned, migratedFile{Base: j.base, Rel: rel, Size: srcSize, SHA256: srcSum, Mode: uint32(srcMode)})
			return nil
		})
		if err != nil {
			return report, fmt.Errorf("config: %w", err)
		}
	}
	if len(conflicts) > 0 {
		return report, &HomeMigrationConflictError{Conflicts: conflicts}
	}
	// Copy what is missing. Destinations that moved on are left alone.
	var written []migratedFile
	for _, f := range planned {
		var src, dst string
		if f.Base == "data" {
			src = filepath.Join(legacyData, filepath.FromSlash(f.Rel))
			dst = filepath.Join(data, filepath.FromSlash(f.Rel))
		} else {
			src = filepath.Join(legacyWorkspace, filepath.FromSlash(f.Rel))
			dst = filepath.Join(workspace, filepath.FromSlash(f.Rel))
		}
		if _, err := os.Stat(dst); err == nil {
			continue // verified identical above
		}
		if err := copyFilePreservingMode(src, dst, os.FileMode(f.Mode)); err != nil {
			return report, fmt.Errorf("config: %w", err)
		}
		report.Copied = append(report.Copied, f.Base+"/"+f.Rel)
		written = append(written, f)
	}
	// Verify every file written by this run, then record the receipt,
	// which covers every examined source file.
	for _, f := range written {
		var dst string
		if f.Base == "data" {
			dst = filepath.Join(data, filepath.FromSlash(f.Rel))
		} else {
			dst = filepath.Join(workspace, filepath.FromSlash(f.Rel))
		}
		sum, size, _, err := hashFile(dst)
		if err != nil {
			return report, fmt.Errorf("config: %w", err)
		}
		if sum != f.SHA256 || size != f.Size {
			return report, fmt.Errorf("config: verification failed for %s/%s after copy", f.Base, f.Rel)
		}
	}
	if err := writeHomeMigrationReceipt(root, planned); err != nil {
		return report, err
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return report, fmt.Errorf("config: %w", err)
	}
	sort.Strings(report.Copied)
	sort.Strings(report.SkippedIdentical)
	sort.Strings(report.DestChanged)
	return report, nil
}

// CleanupLegacyHomeAt moves verified legacy directories into a
// timestamped backup under <root>/state/backups. Approval must be
// explicit: the migration itself never deletes.
func CleanupLegacyHomeAt(home string, approve bool) (HomeCleanupReport, error) {
	var report HomeCleanupReport
	if !approve {
		return report, fmt.Errorf("config: refusing to remove ~/.hal9001 and ~/HAL without explicit approval")
	}
	root := HALRootFor(home)
	receipt, err := readHomeMigrationReceipt(root)
	if err != nil {
		return report, err
	}
	if receipt == nil {
		return report, fmt.Errorf("config: no home migration receipt; nothing to clean")
	}
	backup := receipt.Backup
	if backup == "" {
		backup = filepath.Join(root, "state", "backups",
			"home-migration-"+time.Now().UTC().Format("20060102-150405"))
	}
	if err := os.MkdirAll(backup, 0o700); err != nil {
		return report, fmt.Errorf("config: %w", err)
	}
	// Re-verify before moving: every legacy file must still match the
	// receipt, and everything the legacy trees hold must be on record.
	// The backup is only a recovery path when it preserves exactly what
	// was migrated. The new layout may legitimately have moved on (the
	// schema upgrade rewrites the config); that never blocks cleanup.
	want := map[string]string{}
	for _, f := range receipt.Files {
		want[f.Base+"/"+f.Rel] = f.SHA256
	}
	var conflicts []string
	for _, j := range []struct{ base, dir string }{
		{"data", LegacyDataDirFor(home)},
		{"workspace", LegacyWorkspaceDirFor(home)},
	} {
		if !dirExists(j.dir) {
			continue
		}
		err := filepath.WalkDir(j.dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel := j.base + "/" + slashRel(j.dir, path)
			if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
				conflicts = append(conflicts, rel+" (not a regular file)")
				return nil
			}
			sum, _, _, err := hashFile(path)
			if err != nil {
				return err
			}
			wantSum, ok := want[rel]
			if !ok {
				conflicts = append(conflicts, rel+" (not in the migration receipt; start HAL once to migrate it, then retry cleanup)")
				return nil
			}
			if sum != wantSum {
				conflicts = append(conflicts, rel+" (legacy changed since migration)")
			}
			return nil
		})
		if err != nil {
			return report, fmt.Errorf("config: %w", err)
		}
	}
	if len(conflicts) > 0 {
		return report, &HomeMigrationConflictError{Conflicts: conflicts}
	}
	// Move whole legacy trees when they still exist.
	for _, dir := range []string{LegacyDataDirFor(home), LegacyWorkspaceDirFor(home)} {
		if !dirExists(dir) {
			continue
		}
		target := filepath.Join(backup, filepath.Base(dir))
		if dirExists(target) {
			// A rerun after a partial move: remove the already-moved
			// source remainder only when it is empty.
			rest, err := os.ReadDir(dir)
			if err != nil {
				return report, fmt.Errorf("config: %w", err)
			}
			if len(rest) == 0 {
				if err := os.Remove(dir); err != nil {
					return report, fmt.Errorf("config: %w", err)
				}
			}
			report.Moved = append(report.Moved, dir)
			continue
		}
		if err := os.Rename(dir, target); err != nil {
			return report, fmt.Errorf("config: cannot move %s to backup: %w", dir, err)
		}
		report.Moved = append(report.Moved, dir)
	}
	sort.Strings(report.Moved)
	receipt.Backup = backup
	receipt.CleanedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeHomeMigrationReceiptFull(root, *receipt); err != nil {
		return report, err
	}
	report.Backup = backup
	return report, nil
}

// RollbackHomeMigrationAt restores the legacy directories from the
// migration backup. The previous release never learns the new paths,
// so the backup is the only way back once cleanup has run.
func RollbackHomeMigrationAt(home string) (HomeRollbackReport, error) {
	var report HomeRollbackReport
	root := HALRootFor(home)
	receipt, err := readHomeMigrationReceipt(root)
	if err != nil {
		return report, err
	}
	if receipt == nil || receipt.Backup == "" {
		return report, fmt.Errorf("config: no home migration backup to roll back to (legacy directories were never removed)")
	}
	if !dirExists(receipt.Backup) {
		return report, fmt.Errorf("config: home migration backup is gone: %s", receipt.Backup)
	}
	for _, f := range receipt.Files {
		var backed string
		if f.Base == "data" {
			backed = filepath.Join(receipt.Backup, ".hal9001", filepath.FromSlash(f.Rel))
		} else {
			backed = filepath.Join(receipt.Backup, "HAL", filepath.FromSlash(f.Rel))
		}
		sum, _, _, err := hashFile(backed)
		if err != nil {
			return report, fmt.Errorf("config: home migration backup is incomplete: %w", err)
		}
		if sum != f.SHA256 {
			return report, fmt.Errorf("config: home migration backup does not match the receipt for %s/%s", f.Base, f.Rel)
		}
	}
	legacyDirs := map[string]string{
		".hal9001": LegacyDataDirFor(home),
		"HAL":      LegacyWorkspaceDirFor(home),
	}
	// Refuse when the legacy locations hold anything the backup does
	// not explain.
	var conflicts []string
	for name, dir := range legacyDirs {
		if !dirExists(dir) {
			continue
		}
		backed := filepath.Join(receipt.Backup, name)
		if !dirExists(backed) {
			conflicts = append(conflicts, dir+" (unexpected content outside the backup)")
			continue
		}
		same, err := treesIdentical(dir, backed)
		if err != nil {
			return report, fmt.Errorf("config: %w", err)
		}
		if !same {
			conflicts = append(conflicts, dir+" (differs from the backup)")
		}
	}
	if len(conflicts) > 0 {
		return report, &HomeMigrationConflictError{Conflicts: conflicts}
	}
	// Restore whatever the backup holds.
	for name, dir := range legacyDirs {
		backed := filepath.Join(receipt.Backup, name)
		if !dirExists(backed) {
			continue
		}
		if dirExists(dir) {
			report.AlreadyPresent = true
			continue
		}
		if err := os.Rename(backed, dir); err != nil {
			return report, fmt.Errorf("config: cannot restore %s: %w", dir, err)
		}
		report.Restored = append(report.Restored, dir)
	}
	sort.Strings(report.Restored)
	// The receipt stays: it records exactly what was migrated, so a
	// subsequent start re-migrates cleanly (destinations that moved on
	// are left alone) instead of refusing. Only the backup pointer is
	// cleared, and the emptied backup directory goes with it.
	backup := receipt.Backup
	receipt.Backup = ""
	receipt.CleanedAt = ""
	if err := writeHomeMigrationReceiptFull(root, *receipt); err != nil {
		return report, err
	}
	if backup != "" {
		if empty, err := dirIsEmpty(backup); err == nil && empty {
			_ = os.Remove(backup)
		}
	}
	return report, nil
}

func homeMigrationReceiptPath(root string) string {
	return filepath.Join(root, "state", homeMigrationReceipt)
}

func readHomeMigrationReceipt(root string) (*homeMigrationReceiptFile, error) {
	raw, err := os.ReadFile(homeMigrationReceiptPath(root))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var receipt homeMigrationReceiptFile
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, fmt.Errorf("config: unreadable home migration receipt: %w", err)
	}
	if receipt.Version != homeMigrationVersion {
		return nil, fmt.Errorf("config: unsupported home migration receipt version %d", receipt.Version)
	}
	return &receipt, nil
}

func writeHomeMigrationReceipt(root string, files []migratedFile) error {
	previous, err := readHomeMigrationReceipt(root)
	if err != nil {
		return err
	}
	receipt := homeMigrationReceiptFile{
		Version: homeMigrationVersion,
		At:      time.Now().UTC().Format(time.RFC3339),
		Files:   files,
	}
	if previous != nil {
		// A rerun keeps the cleanup record: the backup stays the
		// recovery path.
		receipt.Backup = previous.Backup
		receipt.CleanedAt = previous.CleanedAt
	}
	return writeHomeMigrationReceiptFull(root, receipt)
}

func writeHomeMigrationReceiptFull(root string, receipt homeMigrationReceiptFile) error {
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tmp, err := os.CreateTemp(state, "home-migration-*.tmp")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("config: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.Rename(name, homeMigrationReceiptPath(root)); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

func dirExists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

func dirIsEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func slashRel(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func hashFile(path string) (sum string, size int64, mode os.FileMode, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0, 0, err
	}
	digest := sha256.Sum256(raw)
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, 0, err
	}
	return hex.EncodeToString(digest[:]), int64(len(raw)), info.Mode().Perm(), nil
}

func copyFilePreservingMode(src, dst string, mode os.FileMode) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	// Write to a temporary file and rename into place, so an
	// interrupted copy can never leave a truncated destination behind:
	// the destination is either absent (copied on rerun) or complete.
	// A stale temporary file from a killed run is inert: migration
	// walks the source trees only and never reads it.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".migrate-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// Best effort: a killed process cannot run cleanup, and a leftover
	// temporary file must never be mistaken for migrated data.
	defer os.Remove(name)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	// The process umask may have stripped bits WriteFile would keep;
	// restore the exact source bits so a 0600 credential file stays 0600.
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		return err
	}
	return nil
}

// treesIdentical reports whether two directory trees hold the same
// regular files with identical bytes.
func treesIdentical(a, b string) (bool, error) {
	want := map[string]string{}
	err := filepath.WalkDir(a, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		sum, _, _, err := hashFile(path)
		if err != nil {
			return err
		}
		want[slashRel(a, path)] = sum
		return nil
	})
	if err != nil {
		return false, err
	}
	got := map[string]string{}
	err = filepath.WalkDir(b, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		sum, _, _, err := hashFile(path)
		if err != nil {
			return err
		}
		got[slashRel(b, path)] = sum
		return nil
	})
	if err != nil {
		return false, err
	}
	if len(want) != len(got) {
		return false, nil
	}
	for rel, sum := range want {
		if got[rel] != sum {
			return false, nil
		}
	}
	return true, nil
}
