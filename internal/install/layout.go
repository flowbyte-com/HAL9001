// Package install implements HAL9001's native, user-space installer and
// transactional updater.
//
// The installation root is independent from HAL's operator data:
//
//	~/.hal/                     installation state
//	  current -> releases/<sha> the one active pointer
//	  releases/<sha>/           an immutable release
//	    bin/hal
//	    bin/hal-update
//	    manifest.json
//	  state/                    lock, previous release, backups
//	  staging/                  candidates under assembly
//
// HAL's configuration, sessions, audit and usage live in ~/.hal/data,
// and its writable workspace defaults to ~/.hal/workspace. Neither is
// touched here.
package install

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// Product is the installed product name.
	Product = "HAL9001"

	// HalName and UpdaterName are the only installed command names.
	HalName     = "hal"
	UpdaterName = "hal-update"

	// DefaultRemote is the official update source. There are no
	// published release artifacts yet, so updates build from this
	// repository at a pinned commit.
	DefaultRemote = "https://github.com/flowbyte-com/HAL9001.git"
	// DefaultBranch is the stable branch updates follow.
	DefaultBranch = "main"

	// LayoutVersion identifies the release directory contract.
	LayoutVersion = "hal-versioned/1"

	// Environment overrides. They exist for tests and for operators who
	// keep their installation somewhere other than the defaults.
	EnvInstallRoot = "HAL9001_INSTALL_ROOT"
	EnvLocalBin    = "HAL9001_LOCAL_BIN"
	EnvRemote      = "HAL9001_UPDATE_REMOTE"
	EnvBranch      = "HAL9001_UPDATE_BRANCH"
)

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Layout names every path the installer and updater own. It is derived
// without creating anything, so --status and --check stay read-only.
type Layout struct {
	Root   string
	BinDir string
}

// DefaultLayout resolves the installation root and the directory that
// holds the managed command symlinks.
func DefaultLayout() (Layout, error) {
	root := strings.TrimSpace(os.Getenv(EnvInstallRoot))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return Layout{}, fmt.Errorf("cannot resolve the home directory; set %s", EnvInstallRoot)
		}
		root = filepath.Join(home, ".hal")
	}
	binDir := strings.TrimSpace(os.Getenv(EnvLocalBin))
	if binDir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return Layout{}, fmt.Errorf("cannot resolve the home directory; set %s", EnvLocalBin)
		}
		binDir = filepath.Join(home, ".local", "bin")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Layout{}, fmt.Errorf("invalid installation root: %w", err)
	}
	binDir, err = filepath.Abs(binDir)
	if err != nil {
		return Layout{}, fmt.Errorf("invalid command directory: %w", err)
	}
	return Layout{Root: filepath.Clean(root), BinDir: filepath.Clean(binDir)}, nil
}

// NewLayout returns a layout for explicit paths.
func NewLayout(root, binDir string) Layout {
	root, _ = filepath.Abs(root)
	binDir, _ = filepath.Abs(binDir)
	return Layout{Root: filepath.Clean(root), BinDir: filepath.Clean(binDir)}
}

// Releases is <root>/releases.
func (l Layout) Releases() string { return filepath.Join(l.Root, "releases") }

// Current is <root>/current.
func (l Layout) Current() string { return filepath.Join(l.Root, "current") }

// Staging is <root>/staging.
func (l Layout) Staging() string { return filepath.Join(l.Root, "staging") }

// State is <root>/state.
func (l Layout) State() string { return filepath.Join(l.Root, "state") }

// LockFile is <root>/state/update.lock.
func (l Layout) LockFile() string { return filepath.Join(l.State(), "update.lock") }

// PreviousRelease is <root>/state/previous-release.
func (l Layout) PreviousRelease() string { return filepath.Join(l.State(), "previous-release") }

// Backups is <root>/state/backups.
func (l Layout) Backups() string { return filepath.Join(l.State(), "backups") }

// ReleaseDir is <root>/releases/<sha>.
func (l Layout) ReleaseDir(rev string) string {
	return filepath.Join(l.Releases(), rev)
}

// ReleaseBin is <root>/releases/<sha>/bin/<name>.
func (l Layout) ReleaseBin(rev, name string) string {
	return filepath.Join(l.ReleaseDir(rev), "bin", name)
}

// LinkPath is <binDir>/<name>.
func (l Layout) LinkPath(name string) string {
	return filepath.Join(l.BinDir, name)
}

// LinkTarget is the stable target a managed command symlink names. The
// target is absolute so it does not depend on the caller's working
// directory; it still resolves through the one `current` pointer.
func (l Layout) LinkTarget(name string) string {
	return filepath.Join(l.Root, "current", "bin", name)
}

// CurrentTarget is the relative target the `current` symlink holds.
func (l Layout) CurrentTarget(rev string) string {
	return filepath.Join("releases", rev)
}

// ValidRevision reports whether rev is a full, lowercase Git commit
// SHA-1. Release directory names are never arbitrary user text.
func ValidRevision(rev string) bool {
	return revisionPattern.MatchString(rev)
}

// ParseTarget accepts only "releases/<40-hex>" and returns the revision.
// It is the gate between an on-disk pointer and any filesystem action,
// so an absolute path, a traversal, or a foreign target is refused.
func ParseTarget(target string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(target))
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("release pointer %q must be relative", target)
	}
	parts := strings.Split(clean, string(filepath.Separator))
	if len(parts) != 2 || parts[0] != "releases" || !ValidRevision(parts[1]) {
		return "", fmt.Errorf("release pointer %q is not releases/<commit-sha>", target)
	}
	return parts[1], nil
}

// Inside reports whether path is inside root. It is used to keep source
// checkouts and staging cleanup from crossing the installation
// boundary.
func Inside(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == path {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
