package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotInstalled means there is no active release pointer.
var ErrNotInstalled = errors.New("HAL9001 is not installed")

// ReadActive returns the current pointer target and its revision
// without validating the release contents.
func ReadActive(l Layout) (target, rev string, err error) {
	info, err := os.Lstat(l.Current())
	if os.IsNotExist(err) {
		return "", "", ErrNotInstalled
	}
	if err != nil {
		return "", "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", "", fmt.Errorf("%s exists but is not a managed symlink", l.Current())
	}
	target, err = os.Readlink(l.Current())
	if err != nil {
		return "", "", err
	}
	rev, err = ParseTarget(target)
	if err != nil {
		return "", "", err
	}
	return target, rev, nil
}

// ReadPrevious returns the recorded previous pointer target. A missing
// record is not an error; an invalid one is.
func ReadPrevious(l Layout) (string, error) {
	path := l.PreviousRelease()
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("previous-release record %s is not a regular file", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	target := strings.TrimSpace(string(raw))
	if target == "" {
		return "", nil
	}
	if _, err := ParseTarget(target); err != nil {
		return "", fmt.Errorf("previous-release record is invalid: %w", err)
	}
	return target, nil
}

// RecordPrevious atomically records the pointer target that was active
// before an activation. An empty target removes the record.
func RecordPrevious(l Layout, target string) error {
	if target == "" {
		if err := os.Remove(l.PreviousRelease()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if _, err := ParseTarget(target); err != nil {
		return err
	}
	return AtomicWriteFile(l.PreviousRelease(), []byte(target+"\n"), 0o600)
}

// InstallCandidate moves a complete, validated candidate into
// releases/<rev>. A release directory is immutable: if it already
// exists it is validated and reused, never replaced.
func InstallCandidate(l Layout, candidate, rev string) (reused bool, err error) {
	if !ValidRevision(rev) {
		return false, fmt.Errorf("invalid release revision %q", rev)
	}
	if _, err := ValidateRelease(candidate, rev); err != nil {
		return false, fmt.Errorf("candidate release failed validation: %w", err)
	}
	if err := EnsureRealDir(l.Releases(), 0o755); err != nil {
		return false, fmt.Errorf("cannot prepare releases directory: %w", err)
	}
	final := l.ReleaseDir(rev)
	info, statErr := os.Lstat(final)
	switch {
	case statErr == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("release path %s exists and is not a real directory", final)
		}
		if _, err := ValidateRelease(final, rev); err != nil {
			return false, fmt.Errorf("release %s already exists and is not valid; releases are immutable, so it will not be replaced: %w", rev, err)
		}
		return true, nil
	case !os.IsNotExist(statErr):
		return false, statErr
	}
	if err := os.Rename(candidate, final); err != nil {
		return false, fmt.Errorf("cannot install release %s: %w", rev, err)
	}
	if _, err := ValidateRelease(final, rev); err != nil {
		// The directory did not exist before this call, so it is safe
		// to remove a release that failed its own post-install check.
		_ = os.RemoveAll(final)
		return false, fmt.Errorf("installed release %s failed validation: %w", rev, err)
	}
	return false, nil
}

// Activate atomically points current at releases/<rev>. It returns the
// previous target so the caller can restore it if a later step fails.
func Activate(l Layout, rev string) (previous string, err error) {
	if !ValidRevision(rev) {
		return "", fmt.Errorf("invalid release revision %q", rev)
	}
	info, err := os.Stat(l.ReleaseDir(rev))
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("cannot activate %s: release directory does not exist", rev)
	}
	if info, err := os.Lstat(l.Current()); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("%s exists but is not a managed symlink; refusing to replace it", l.Current())
		}
		raw, err := os.Readlink(l.Current())
		if err != nil {
			return "", err
		}
		if _, err := ParseTarget(raw); err != nil {
			return "", fmt.Errorf("current pointer is invalid; refusing to overwrite it: %w", err)
		}
		previous = raw
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := ReplaceSymlink(l.Current(), l.CurrentTarget(rev)); err != nil {
		return "", fmt.Errorf("cannot switch the active release pointer: %w", err)
	}
	return previous, nil
}

// RestorePointer atomically moves current back to a recorded target. An
// empty target removes the pointer, which is the correct recovery for a
// failed first installation.
func RestorePointer(l Layout, target string) error {
	if target == "" {
		info, err := os.Lstat(l.Current())
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s is not a managed symlink", l.Current())
		}
		return os.Remove(l.Current())
	}
	if _, err := ParseTarget(target); err != nil {
		return err
	}
	return ReplaceSymlink(l.Current(), target)
}

// LinkManaged reports whether the command path is a symlink to this
// installation's stable target.
func LinkManaged(l Layout, name string) bool {
	path := l.LinkPath(name)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	return sameTarget(l, path, target, l.LinkTarget(name))
}

func sameTarget(l Layout, path, target, want string) bool {
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target) == filepath.Clean(want)
}

type linkPlan struct {
	name       string
	path       string
	target     string
	backup     string
	existed    bool
	managed    bool
	priorValue string
}

// planLinks checks every managed command path before anything is
// activated. An unrelated existing command is refused unless the
// operator explicitly asked bootstrap to migrate it.
func planLinks(l Layout, allowReplace bool) ([]linkPlan, error) {
	var plans []linkPlan
	backupRoot := filepath.Join(l.Backups(), "legacy-"+time.Now().UTC().Format("20060102T150405")+"-"+randomSuffix())
	for _, name := range []string{HalName, UpdaterName} {
		plan := linkPlan{name: name, path: l.LinkPath(name), target: l.LinkTarget(name)}
		info, err := os.Lstat(plan.path)
		switch {
		case os.IsNotExist(err):
			// Nothing to preserve.
		case err != nil:
			return nil, err
		case info.IsDir():
			return nil, fmt.Errorf("refusing to replace directory %s", plan.path)
		default:
			plan.existed = true
			if info.Mode()&os.ModeSymlink != 0 {
				raw, err := os.Readlink(plan.path)
				if err != nil {
					return nil, err
				}
				plan.priorValue = raw
				if sameTarget(l, plan.path, raw, plan.target) {
					plan.managed = true
				}
			}
			if !plan.managed {
				if !allowReplace {
					return nil, fmt.Errorf("refusing to replace %s: it is not managed by HAL9001. Move it aside, or bootstrap with --replace-existing to preserve it under %s", plan.path, l.Backups())
				}
				plan.backup = filepath.Join(backupRoot, name)
			}
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// applyLinks installs or repairs the stable command symlinks. Existing
// unmanaged commands are moved into state/backups first. A failure
// restores the previous links and the moved commands.
func applyLinks(l Layout, plans []linkPlan) error {
	if err := os.MkdirAll(l.BinDir, 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", l.BinDir, err)
	}
	var moved []linkPlan
	for _, plan := range plans {
		if plan.backup == "" {
			continue
		}
		if err := MoveAside(plan.path, plan.backup); err != nil {
			restoreMoved(moved)
			return fmt.Errorf("cannot preserve existing command %s: %w", plan.path, err)
		}
		moved = append(moved, plan)
	}
	var applied []linkPlan
	for _, plan := range plans {
		if err := ReplaceSymlink(plan.path, plan.target); err != nil {
			restoreLinks(applied)
			restoreMoved(moved)
			return fmt.Errorf("cannot install %s: %w", plan.path, err)
		}
		applied = append(applied, plan)
	}
	return nil
}

func restoreLinks(plans []linkPlan) {
	for i := len(plans) - 1; i >= 0; i-- {
		plan := plans[i]
		switch {
		case plan.priorValue != "":
			_ = ReplaceSymlink(plan.path, plan.priorValue)
		case !plan.existed:
			_ = os.Remove(plan.path)
		}
	}
}

func restoreMoved(plans []linkPlan) {
	for i := len(plans) - 1; i >= 0; i-- {
		plan := plans[i]
		if plan.backup == "" {
			continue
		}
		_ = os.Remove(plan.path)
		_ = MoveAside(plan.backup, plan.path)
	}
}

// ReconcileLinks validates the command paths before activation and
// installs them afterwards.
func ReconcileLinks(l Layout, allowReplace bool) error {
	plans, err := planLinks(l, allowReplace)
	if err != nil {
		return err
	}
	return applyLinks(l, plans)
}

// PreflightLinks checks command paths without modifying anything.
func PreflightLinks(l Layout, allowReplace bool) error {
	_, err := planLinks(l, allowReplace)
	return err
}
