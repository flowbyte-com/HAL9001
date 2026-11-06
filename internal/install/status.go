package install

import (
	"fmt"
	"os"
	"strings"
)

// Status is the offline view of an installation. It never creates or
// changes anything.
type Status struct {
	Layout           Layout
	Installed        bool
	Healthy          bool
	Problems         []string
	Target           string
	Revision         string
	ReleaseDir       string
	Manifest         Manifest
	PreviousTarget   string
	PreviousRevision string
	PreviousValid    bool
	HalManaged       bool
	UpdaterManaged   bool
}

// Inspect reads the installation and validates the active release and
// the recorded rollback candidate. It is safe on a missing or broken
// installation.
func Inspect(l Layout) Status {
	s := Status{Layout: l, Healthy: true}
	target, rev, err := ReadActive(l)
	switch {
	case err == ErrNotInstalled:
		s.Healthy = false
		s.HalManaged = LinkManaged(l, HalName)
		s.UpdaterManaged = LinkManaged(l, UpdaterName)
		return s
	case err != nil:
		s.Healthy = false
		s.Problems = append(s.Problems, err.Error())
		s.HalManaged = LinkManaged(l, HalName)
		s.UpdaterManaged = LinkManaged(l, UpdaterName)
		return s
	}
	s.Installed = true
	s.Target = target
	s.Revision = rev
	s.ReleaseDir = l.ReleaseDir(rev)
	manifest, err := ValidateRelease(s.ReleaseDir, rev)
	if err != nil {
		s.Healthy = false
		s.Problems = append(s.Problems, err.Error())
	} else {
		s.Manifest = manifest
	}
	if previous, err := ReadPrevious(l); err != nil {
		s.Healthy = false
		s.Problems = append(s.Problems, err.Error())
	} else if previous != "" {
		s.PreviousTarget = previous
		prevRev, err := ParseTarget(previous)
		if err != nil {
			s.Healthy = false
			s.Problems = append(s.Problems, err.Error())
		} else {
			s.PreviousRevision = prevRev
			if _, err := ValidateRelease(l.ReleaseDir(prevRev), prevRev); err != nil {
				s.Problems = append(s.Problems, "previous release: "+err.Error())
			} else {
				s.PreviousValid = true
			}
		}
	}
	s.HalManaged = LinkManaged(l, HalName)
	s.UpdaterManaged = LinkManaged(l, UpdaterName)
	if !s.HalManaged {
		s.Problems = append(s.Problems, fmt.Sprintf("%s is not a managed symlink", l.LinkPath(HalName)))
	}
	if !s.UpdaterManaged {
		s.Problems = append(s.Problems, fmt.Sprintf("%s is not a managed symlink", l.LinkPath(UpdaterName)))
	}
	if len(s.Problems) > 0 {
		s.Healthy = false
	}
	return s
}

// Report renders the operator-facing status block.
func (s Status) Report() string {
	var b strings.Builder
	b.WriteString("HAL9001 update status\n")
	fmt.Fprintf(&b, "Installation root:  %s\n", s.Layout.Root)
	if !s.Installed {
		state := "not installed"
		if len(s.Problems) > 0 {
			state = "broken"
		}
		fmt.Fprintf(&b, "Installation:       %s\n", state)
		if state == "not installed" {
			b.WriteString("Run `go run ./cmd/hal-update --bootstrap` from a HAL9001 checkout to install.\n")
		}
		fmt.Fprintf(&b, "Managed hal:        %s (%s)\n", yesNo(s.HalManaged), s.Layout.LinkPath(HalName))
		fmt.Fprintf(&b, "Managed hal-update: %s (%s)\n", yesNo(s.UpdaterManaged), s.Layout.LinkPath(UpdaterName))
		for _, problem := range s.Problems {
			fmt.Fprintf(&b, "  problem: %s\n", problem)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "Version:            %s\n", versionOrUnknown(s.Manifest.Version))
	fmt.Fprintf(&b, "Commit:             %s\n", s.Revision)
	fmt.Fprintf(&b, "Active release:     %s\n", s.ReleaseDir)
	if s.PreviousRevision == "" {
		b.WriteString("Previous release:   none\n")
	} else {
		state := "missing or invalid"
		if s.PreviousValid {
			state = "valid"
		}
		fmt.Fprintf(&b, "Previous release:   %s (%s)\n", s.PreviousRevision, state)
	}
	health := "healthy"
	if !s.Healthy {
		health = "degraded"
	}
	fmt.Fprintf(&b, "Installation:       %s\n", health)
	fmt.Fprintf(&b, "Managed hal:        %s (%s)\n", yesNo(s.HalManaged), s.Layout.LinkPath(HalName))
	fmt.Fprintf(&b, "Managed hal-update: %s (%s)\n", yesNo(s.UpdaterManaged), s.Layout.LinkPath(UpdaterName))
	for _, problem := range s.Problems {
		fmt.Fprintf(&b, "  problem: %s\n", problem)
	}
	return b.String()
}

// ActiveVersion returns the installed version and commit, or empty
// strings when no active release exists.
func ActiveVersion(l Layout) (version, commit string) {
	target, rev, err := ReadActive(l)
	if err != nil || target == "" {
		return "", ""
	}
	m, err := ReadManifest(l.ReleaseDir(rev))
	if err != nil {
		return "", rev
	}
	return m.Version, m.Commit
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func versionOrUnknown(version string) string {
	if strings.TrimSpace(version) == "" {
		return "unknown"
	}
	return version
}

// ReleaseExists reports whether a release directory exists.
func ReleaseExists(l Layout, rev string) bool {
	if !ValidRevision(rev) {
		return false
	}
	info, err := os.Lstat(l.ReleaseDir(rev))
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}
