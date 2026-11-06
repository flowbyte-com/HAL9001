// Package buildinfo reports the product version and the exact source
// revision a HAL9001 binary was built from.
//
// Installed releases are stamped at link time by the updater. A plain
// `go build` or `go run` has no such stamp; in that case the Go
// toolchain's VCS settings are used instead, so the version is never a
// hand-maintained constant that has to be edited for every commit.
package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// These variables are set with -ldflags -X by the updater and the
// Makefile. They stay empty for an ordinary `go build`.
var (
	Version    = ""
	Commit     = ""
	BuildEpoch = ""
	Dirty      = ""
)

// Info is the resolved build identity.
type Info struct {
	Version string
	Commit  string
	// ShortCommit is the first eight characters of Commit, or "" when
	// the commit is unknown.
	ShortCommit string
	BuildEpoch  int64
	Dirty       bool
}

// Current resolves the identity of the running binary. Explicit
// link-time values win; otherwise the VCS data embedded by the Go
// toolchain is used.
func Current() Info {
	info := Info{
		Version: strings.TrimSpace(Version),
		Commit:  strings.TrimSpace(Commit),
		Dirty:   strings.EqualFold(strings.TrimSpace(Dirty), "true"),
	}
	if epoch, err := strconv.ParseInt(strings.TrimSpace(BuildEpoch), 10, 64); err == nil && epoch > 0 {
		info.BuildEpoch = epoch
	}

	if info.Commit == "" || info.Version == "" || info.BuildEpoch == 0 {
		fallback, ok := readBuildInfo()
		if ok {
			if info.Commit == "" {
				info.Commit = fallback.Commit
			}
			if info.Version == "" {
				info.Version = fallback.Version
			}
			if info.BuildEpoch == 0 {
				info.BuildEpoch = fallback.BuildEpoch
			}
			if !info.Dirty {
				info.Dirty = fallback.Dirty
			}
		}
	}

	if info.Commit == "" {
		info.Commit = "unknown"
	}
	info.ShortCommit = Short(info.Commit)
	if info.Version == "" {
		if info.Commit == "unknown" {
			info.Version = "dev"
		} else {
			info.Version = "dev-" + info.ShortCommit
		}
	}
	return info
}

// Short returns the first eight characters of a commit, or "unknown".
func Short(commit string) string {
	commit = strings.TrimSpace(commit)
	if commit == "" || commit == "unknown" {
		return "unknown"
	}
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

// Report renders the operator-facing version block. The commit line is
// the contract the updater uses to reject a release that mixes
// binaries from two builds.
func Report(product string) string {
	info := Current()
	return fmt.Sprintf("%s %s\ncommit: %s\n", product, info.Version, info.Commit)
}

// ReportValue is the one-line form used by --status and diagnostics.
func ReportValue(product string) string {
	info := Current()
	return fmt.Sprintf("%s %s (commit %s)", product, info.Version, info.Commit)
}

func readBuildInfo() (Info, bool) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Info{}, false
	}
	info := Info{}
	if v := strings.TrimSpace(bi.Main.Version); v != "" && v != "(devel)" {
		info.Version = v
	}
	for _, setting := range bi.Settings {
		switch setting.Key {
		case "vcs.revision":
			info.Commit = strings.TrimSpace(setting.Value)
		case "vcs.time":
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(setting.Value)); err == nil {
				info.BuildEpoch = t.Unix()
			}
		case "vcs.modified":
			info.Dirty = strings.EqualFold(strings.TrimSpace(setting.Value), "true")
		}
	}
	return info, true
}
