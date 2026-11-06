package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Result describes a completed mutating operation.
type Result struct {
	Action         string
	From           string
	To             string
	Previous       string
	Version        string
	Updated        bool
	AlreadyCurrent bool
	Report         string
}

// CheckResult describes a read-only update check.
type CheckResult struct {
	Installed bool
	Current   string
	Remote    string
	Version   string
	Relation  string
	Detail    string
}

// Report renders the check for the operator.
func (c CheckResult) Report() string {
	var b strings.Builder
	b.WriteString("HAL9001 update check\n")
	if c.Installed {
		fmt.Fprintf(&b, "Installed:   %s", c.Current)
		if c.Version != "" {
			fmt.Fprintf(&b, " (%s)", c.Version)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("Installed:   none\n")
	}
	fmt.Fprintf(&b, "Remote:      %s\n", c.Remote)
	fmt.Fprintf(&b, "Status:      %s\n", c.Relation)
	if c.Detail != "" {
		fmt.Fprintf(&b, "Detail:      %s\n", c.Detail)
	}
	return b.String()
}

type runHooks struct {
	afterBuild    func() error
	afterInstall  func() error
	afterActivate func() error
	afterLinks    func() error
}

// Update installs the latest commit on the stable branch. The source is
// resolved to an exact SHA before any build starts, and the active
// pointer is moved only after the complete release validates.
func Update(ctx context.Context, l Layout, remote Remote, branch string, builder Builder) (Result, error) {
	return updateWith(ctx, l, remote, branch, builder, runHooks{})
}

func updateWith(ctx context.Context, l Layout, remote Remote, branch string, builder Builder, hooks runHooks) (Result, error) {
	if remote == nil {
		remote = NewGitRemote("")
	}
	if builder == nil {
		builder = GoBuilder{CacheDir: filepath.Join(l.State(), "go-cache")}
	}
	if strings.TrimSpace(branch) == "" {
		branch = DefaultBranch
	}
	if err := ValidateBranch(branch); err != nil {
		return Result{}, err
	}

	currentTarget, currentRev, currentManifest, err := activeValidated(l)
	if errors.Is(err, ErrNotInstalled) {
		return Result{}, fmt.Errorf("HAL9001 is not installed at %s; run `go run ./cmd/hal-update --bootstrap` from a source checkout first", l.Root)
	}
	if err != nil {
		return Result{}, fmt.Errorf("the active installation is not usable: %w", err)
	}

	remoteRev, err := remote.Resolve(ctx, branch)
	if err != nil {
		return Result{}, err
	}
	if !ValidRevision(remoteRev) {
		return Result{}, fmt.Errorf("the update source resolved to non-exact revision %q", remoteRev)
	}
	if currentRev == remoteRev {
		return Result{
			Action: "update", From: currentRev, To: currentRev,
			Version: currentManifest.Version, AlreadyCurrent: true,
			Report: fmt.Sprintf("HAL9001 is current at %s.", currentRev),
		}, nil
	}

	lock, err := AcquireLock(l)
	if err != nil {
		return Result{}, err
	}
	defer lock.Release()

	// Re-read under the lock: another updater may have completed while
	// this process was resolving the remote.
	currentTarget, currentRev, currentManifest, err = activeValidated(l)
	if err != nil {
		return Result{}, err
	}
	if currentRev == remoteRev {
		return Result{
			Action: "update", From: currentRev, To: currentRev,
			Version: currentManifest.Version, AlreadyCurrent: true,
			Report: fmt.Sprintf("HAL9001 is current at %s.", currentRev),
		}, nil
	}

	CleanStaging(l)
	if err := EnsureRealDir(l.Staging(), 0o755); err != nil {
		return Result{}, fmt.Errorf("cannot prepare the staging directory: %w", err)
	}
	work, err := os.MkdirTemp(l.Staging(), "update-")
	if err != nil {
		return Result{}, fmt.Errorf("cannot create the update staging area: %w", err)
	}
	defer RemoveStaging(l, work)

	sourceDir := filepath.Join(work, "src")
	meta, err := remote.Fetch(ctx, remoteRev, sourceDir)
	if err != nil {
		return Result{}, err
	}
	if meta.Commit != remoteRev {
		return Result{}, fmt.Errorf("the source checkout resolved to %s, not the pinned commit %s", meta.Commit, remoteRev)
	}

	candidate := filepath.Join(work, "candidate")
	if err := os.MkdirAll(filepath.Join(candidate, "bin"), 0o755); err != nil {
		return Result{}, err
	}
	if err := builder.Build(ctx, sourceDir, filepath.Join(candidate, "bin"), meta.BuildMeta()); err != nil {
		return Result{}, fmt.Errorf("the candidate build failed: %w", err)
	}
	if hooks.afterBuild != nil {
		if err := hooks.afterBuild(); err != nil {
			return Result{}, err
		}
	}
	if err := writeAndValidate(candidate, meta, remoteRev); err != nil {
		return Result{}, err
	}
	if _, err := InstallCandidate(l, candidate, remoteRev); err != nil {
		return Result{}, err
	}
	if hooks.afterInstall != nil {
		if err := hooks.afterInstall(); err != nil {
			return Result{}, err
		}
	}

	priorPrevious, err := ReadPrevious(l)
	if err != nil {
		return Result{}, err
	}
	if err := RecordPrevious(l, currentTarget); err != nil {
		return Result{}, fmt.Errorf("cannot record the rollback candidate: %w", err)
	}
	if _, err := Activate(l, remoteRev); err != nil {
		_ = RecordPrevious(l, priorPrevious)
		return Result{}, err
	}
	if hooks.afterActivate != nil {
		if err := hooks.afterActivate(); err != nil {
			return Result{}, restoreAfterFailedActivation(l, currentTarget, priorPrevious, err)
		}
	}
	if err := ReconcileLinks(l, false); err != nil {
		return Result{}, restoreAfterFailedActivation(l, currentTarget, priorPrevious, err)
	}
	if hooks.afterLinks != nil {
		if err := hooks.afterLinks(); err != nil {
			return Result{}, restoreAfterFailedActivation(l, currentTarget, priorPrevious, err)
		}
	}
	return Result{
		Action:   "update",
		From:     currentRev,
		To:       remoteRev,
		Previous: currentRev,
		Version:  manifestVersion(l, remoteRev),
		Updated:  true,
		Report:   fmt.Sprintf("Updated HAL9001 %s -> %s. Restart HAL to use the new release; running sessions were not touched.", currentRev, remoteRev),
	}, nil
}

// Bootstrap installs a complete release from a local, clean checkout.
// It is the supported first-install path; no existing unmanaged command
// is overwritten unless --replace-existing was explicitly requested.
func Bootstrap(ctx context.Context, l Layout, sourceDir string, builder Builder, replaceExisting bool) (Result, error) {
	return bootstrapWith(ctx, l, sourceDir, builder, replaceExisting, runHooks{})
}

func bootstrapWith(ctx context.Context, l Layout, sourceDir string, builder Builder, replaceExisting bool, hooks runHooks) (Result, error) {
	if builder == nil {
		builder = GoBuilder{CacheDir: filepath.Join(l.State(), "go-cache")}
	}
	meta, top, err := LocalSource(ctx, sourceDir)
	if err != nil {
		return Result{}, err
	}
	if Inside(l.Root, top) {
		return Result{}, fmt.Errorf("the source checkout %s is inside the installation root %s; build from a checkout outside it", top, l.Root)
	}
	if err := PreflightLinks(l, replaceExisting); err != nil {
		return Result{}, err
	}

	lock, err := AcquireLock(l)
	if err != nil {
		return Result{}, err
	}
	defer lock.Release()

	CleanStaging(l)
	if err := EnsureRealDir(l.Staging(), 0o755); err != nil {
		return Result{}, fmt.Errorf("cannot prepare the bootstrap staging area: %w", err)
	}
	work, err := os.MkdirTemp(l.Staging(), "update-")
	if err != nil {
		return Result{}, fmt.Errorf("cannot create the bootstrap staging area: %w", err)
	}
	defer RemoveStaging(l, work)

	candidate := filepath.Join(work, "candidate")
	if err := os.MkdirAll(filepath.Join(candidate, "bin"), 0o755); err != nil {
		return Result{}, err
	}
	if err := builder.Build(ctx, top, filepath.Join(candidate, "bin"), meta.BuildMeta()); err != nil {
		return Result{}, fmt.Errorf("the bootstrap build failed: %w", err)
	}
	if hooks.afterBuild != nil {
		if err := hooks.afterBuild(); err != nil {
			return Result{}, err
		}
	}
	// The checkout may have changed while the build ran. A release is
	// labelled with the commit it was built from, so re-resolve and
	// require the same clean revision.
	checked, _, err := LocalSource(ctx, top)
	if err != nil {
		return Result{}, fmt.Errorf("the source changed during the build: %w", err)
	}
	if checked.Commit != meta.Commit {
		return Result{}, fmt.Errorf("the source moved from %s to %s during the build; retry from a stable checkout", meta.Commit, checked.Commit)
	}
	if err := writeAndValidate(candidate, meta, meta.Commit); err != nil {
		return Result{}, err
	}
	if _, err := InstallCandidate(l, candidate, meta.Commit); err != nil {
		return Result{}, err
	}
	if hooks.afterInstall != nil {
		if err := hooks.afterInstall(); err != nil {
			return Result{}, err
		}
	}

	priorPrevious, err := ReadPrevious(l)
	if err != nil {
		return Result{}, err
	}
	currentTarget, currentRev, _, activeErr := activeValidated(l)
	already := activeErr == nil && currentRev == meta.Commit
	if activeErr != nil && !errors.Is(activeErr, ErrNotInstalled) {
		return Result{}, fmt.Errorf("the existing installation is not usable: %w", activeErr)
	}
	if !already {
		if currentTarget != "" {
			if err := RecordPrevious(l, currentTarget); err != nil {
				return Result{}, fmt.Errorf("cannot record the rollback candidate: %w", err)
			}
			if _, err := Activate(l, meta.Commit); err != nil {
				_ = RecordPrevious(l, priorPrevious)
				return Result{}, err
			}
			if hooks.afterActivate != nil {
				if err := hooks.afterActivate(); err != nil {
					return Result{}, restoreAfterFailedActivation(l, currentTarget, priorPrevious, err)
				}
			}
		} else {
			if _, err := Activate(l, meta.Commit); err != nil {
				_ = RecordPrevious(l, priorPrevious)
				return Result{}, err
			}
			if hooks.afterActivate != nil {
				if err := hooks.afterActivate(); err != nil {
					return Result{}, restoreAfterFailedActivation(l, "", priorPrevious, err)
				}
			}
		}
	}
	if err := ReconcileLinks(l, replaceExisting); err != nil {
		if !already {
			return Result{}, restoreAfterFailedActivation(l, currentTarget, priorPrevious, err)
		}
		return Result{}, err
	}
	if hooks.afterLinks != nil {
		if err := hooks.afterLinks(); err != nil {
			if !already {
				return Result{}, restoreAfterFailedActivation(l, currentTarget, priorPrevious, err)
			}
			return Result{}, err
		}
	}
	version := manifestVersion(l, meta.Commit)
	if already {
		return Result{
			Action: "bootstrap", From: currentRev, To: meta.Commit,
			Version: version, AlreadyCurrent: true,
			Report: fmt.Sprintf("HAL9001 %s is already the active release; managed commands were checked.", meta.Commit),
		}, nil
	}
	return Result{
		Action: "bootstrap", From: currentRev, To: meta.Commit,
		Previous: currentRev, Version: version, Updated: true,
		Report: fmt.Sprintf("Installed HAL9001 %s at %s. Launch it with `hal`.", version, l.Root),
	}, nil
}

// Check compares the installed commit with the stable branch without
// changing the installation.
func Check(ctx context.Context, l Layout, remote Remote, branch string) (CheckResult, error) {
	if remote == nil {
		remote = NewGitRemote("")
	}
	if strings.TrimSpace(branch) == "" {
		branch = DefaultBranch
	}
	if err := ValidateBranch(branch); err != nil {
		return CheckResult{}, err
	}
	remoteRev, err := remote.Resolve(ctx, branch)
	if err != nil {
		return CheckResult{}, err
	}
	result := CheckResult{Remote: remoteRev, Relation: "not installed"}
	target, rev, err := ReadActive(l)
	if errors.Is(err, ErrNotInstalled) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Installed = true
	result.Current = rev
	if m, err := ReadManifest(l.ReleaseDir(rev)); err == nil {
		result.Version = m.Version
	}
	if target == "" {
		return result, fmt.Errorf("the active pointer is empty")
	}
	if _, err := ValidateRelease(l.ReleaseDir(rev), rev); err != nil {
		result.Relation = "unhealthy"
		result.Detail = err.Error()
		return result, nil
	}
	if rev == remoteRev {
		result.Relation = "current"
		return result, nil
	}
	ancestor, err := remote.IsAncestor(ctx, branch, rev, remoteRev)
	if err != nil {
		result.Relation = "different"
		result.Detail = "could not determine ancestry: " + err.Error()
		return result, nil
	}
	if ancestor {
		result.Relation = "behind"
	} else {
		result.Relation = "diverged"
	}
	return result, nil
}

// Rollback switches atomically to the recorded previous release. It
// never fetches or rebuilds. Repeated rollback toggles between the two
// recorded releases.
func Rollback(ctx context.Context, l Layout) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	previous, err := ReadPrevious(l)
	if err != nil {
		return Result{}, err
	}
	if previous == "" {
		return Result{}, fmt.Errorf("no previous release is recorded; nothing to roll back to")
	}
	previousRev, err := ParseTarget(previous)
	if err != nil {
		return Result{}, err
	}
	if _, err := ValidateRelease(l.ReleaseDir(previousRev), previousRev); err != nil {
		return Result{}, fmt.Errorf("the previous release %s cannot be activated: %w", previousRev, err)
	}

	lock, err := AcquireLock(l)
	if err != nil {
		return Result{}, err
	}
	defer lock.Release()

	previous, err = ReadPrevious(l)
	if err != nil {
		return Result{}, err
	}
	if previous == "" {
		return Result{}, fmt.Errorf("no previous release is recorded; nothing to roll back to")
	}
	previousRev, err = ParseTarget(previous)
	if err != nil {
		return Result{}, err
	}
	if _, err := ValidateRelease(l.ReleaseDir(previousRev), previousRev); err != nil {
		return Result{}, fmt.Errorf("the previous release %s cannot be activated: %w", previousRev, err)
	}

	currentTarget, currentRev, currentErr := ReadActive(l)
	currentAvailable := currentErr == nil
	if currentErr != nil && !errors.Is(currentErr, ErrNotInstalled) {
		return Result{}, currentErr
	}
	if currentAvailable && currentRev == previousRev {
		return Result{
			Action: "rollback", From: currentRev, To: previousRev,
			Version: manifestVersion(l, previousRev), AlreadyCurrent: true,
			Report: fmt.Sprintf("HAL9001 is already on %s; nothing to roll back.", previousRev),
		}, nil
	}

	if _, err := Activate(l, previousRev); err != nil {
		return Result{}, err
	}
	if currentTarget != "" {
		if err := RecordPrevious(l, currentTarget); err != nil {
			_ = RestorePointer(l, currentTarget)
			return Result{}, err
		}
	} else if err := RecordPrevious(l, ""); err != nil {
		_ = RestorePointer(l, "")
		return Result{}, err
	}
	if err := ReconcileLinks(l, false); err != nil {
		restoreErr := RestorePointer(l, currentTarget)
		_ = RecordPrevious(l, previous)
		if restoreErr != nil {
			return Result{}, fmt.Errorf("rollback activated %s but the managed commands could not be repaired: %v; restoring the old pointer also failed: %v", previousRev, err, restoreErr)
		}
		return Result{}, err
	}
	return Result{
		Action: "rollback", From: currentRev, To: previousRev,
		Previous: currentRev, Version: manifestVersion(l, previousRev), Updated: true,
		Report: fmt.Sprintf("Rolled back HAL9001 %s -> %s. Restart HAL to use the restored release; running sessions were not touched.", currentRev, previousRev),
	}, nil
}

func activeValidated(l Layout) (target, rev string, m Manifest, err error) {
	target, rev, err = ReadActive(l)
	if err != nil {
		return "", "", Manifest{}, err
	}
	m, err = ValidateRelease(l.ReleaseDir(rev), rev)
	if err != nil {
		return "", "", Manifest{}, err
	}
	return target, rev, m, nil
}

func writeAndValidate(candidate string, meta SourceMeta, rev string) error {
	m, err := NewManifest(candidate, meta.BuildMeta())
	if err != nil {
		return fmt.Errorf("cannot create the release manifest: %w", err)
	}
	if err := WriteManifest(candidate, m); err != nil {
		return fmt.Errorf("cannot write the release manifest: %w", err)
	}
	if _, err := ValidateRelease(candidate, rev); err != nil {
		return fmt.Errorf("the candidate release failed validation: %w", err)
	}
	return nil
}

func (m SourceMeta) BuildMeta() BuildMeta {
	return BuildMeta{Commit: m.Commit, Version: m.Version, Epoch: m.Epoch, Source: m.Source}
}

func restoreAfterFailedActivation(l Layout, previousTarget, priorPrevious string, cause error) error {
	restoreErr := RestorePointer(l, previousTarget)
	recordErr := RecordPrevious(l, priorPrevious)
	if restoreErr != nil {
		return fmt.Errorf("%v; the previous release could not be restored automatically: %v (manual recovery: point %s back to %q)", cause, restoreErr, l.Current(), previousTarget)
	}
	if recordErr != nil {
		return fmt.Errorf("%v; the active release was restored but the previous-release record could not be restored: %v", cause, recordErr)
	}
	return fmt.Errorf("%v; the previous release was restored", cause)
}

func manifestVersion(l Layout, rev string) string {
	m, err := ReadManifest(l.ReleaseDir(rev))
	if err != nil {
		return ""
	}
	return m.Version
}
