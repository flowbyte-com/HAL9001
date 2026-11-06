package buildinfo

import (
	"strings"
	"testing"
)

func TestReportUsesStampedIdentity(t *testing.T) {
	oldVersion, oldCommit, oldEpoch, oldDirty := Version, Commit, BuildEpoch, Dirty
	defer func() {
		Version, Commit, BuildEpoch, Dirty = oldVersion, oldCommit, oldEpoch, oldDirty
	}()
	Version = "v1.2.3"
	Commit = "bb0afa73751d04f74f9ed7947565ceca5e997935"
	BuildEpoch = "1793636117"
	Dirty = "false"

	info := Current()
	if info.Version != Version || info.Commit != Commit || info.ShortCommit != "bb0afa73" {
		t.Fatalf("info = %#v", info)
	}
	if info.BuildEpoch != 1793636117 || info.Dirty {
		t.Fatalf("info epoch/dirty = %#v", info)
	}
	report := Report("HAL9001")
	for _, want := range []string{"HAL9001 v1.2.3", "commit: " + Commit + "\n"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report = %q, want %q", report, want)
		}
	}
	if got := ReportValue("HAL9001 updater"); !strings.Contains(got, "commit "+Commit) {
		t.Fatalf("report value = %q", got)
	}
}

func TestShort(t *testing.T) {
	if got := Short("bb0afa73751d04f74f9ed7947565ceca5e997935"); got != "bb0afa73" {
		t.Fatalf("short = %q", got)
	}
	if got := Short(""); got != "unknown" {
		t.Fatalf("short empty = %q", got)
	}
	if got := Short("abc"); got != "abc" {
		t.Fatalf("short abc = %q", got)
	}
}
