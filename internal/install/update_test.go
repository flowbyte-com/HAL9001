package install

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuccessfulUpdatePreservesOldRelease(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	newRev := testRev('b')
	seedRelease(t, l, oldRev)
	remote := &fakeRemote{rev: newRev}

	result, err := Update(context.Background(), l, remote, "main", &fakeBuilder{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.From != oldRev || result.To != newRev || result.Previous != oldRev {
		t.Fatalf("result = %#v", result)
	}
	_, active, err := ReadActive(l)
	if err != nil || active != newRev {
		t.Fatalf("active = %q, %v", active, err)
	}
	if !ReleaseExists(l, oldRev) {
		t.Fatal("the previous release directory was removed")
	}
	previous, err := ReadPrevious(l)
	if err != nil || previous != "releases/"+oldRev {
		t.Fatalf("previous = %q, %v", previous, err)
	}
	status := Inspect(l)
	if !status.Healthy || status.PreviousRevision != oldRev || !status.PreviousValid {
		t.Fatalf("status = %#v", status)
	}
	for _, name := range []string{HalName, UpdaterName} {
		if !LinkManaged(l, name) {
			t.Fatalf("%s is not managed after update", name)
		}
	}
}

func TestNoUpdateDoesNotStageOrChange(t *testing.T) {
	l := testLayout(t)
	rev := testRev('a')
	seedRelease(t, l, rev)
	builder := &fakeBuilder{}
	remote := &fakeRemote{rev: rev}

	result, err := Update(context.Background(), l, remote, "main", builder)
	if err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyCurrent || result.Updated {
		t.Fatalf("result = %#v", result)
	}
	if builder.calls != 0 {
		t.Fatalf("builder calls = %d", builder.calls)
	}
	if _, active, _ := ReadActive(l); active != rev {
		t.Fatalf("active = %s", active)
	}
	entries, err := os.ReadDir(l.Staging())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("no-update created staging material: %v", entries)
	}
}

func TestFailedBuildLeavesCurrentRelease(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	newRev := testRev('b')
	seedRelease(t, l, oldRev)
	builder := &fakeBuilder{fail: errors.New("compiler failed")}
	_, err := Update(context.Background(), l, &fakeRemote{rev: newRev}, "main", builder)
	if err == nil || !strings.Contains(err.Error(), "candidate build failed") {
		t.Fatalf("error = %v", err)
	}
	if _, active, _ := ReadActive(l); active != oldRev {
		t.Fatalf("active = %s", active)
	}
	if ReleaseExists(l, newRev) {
		t.Fatal("a failed build left a release directory")
	}
	entries, err := os.ReadDir(l.Staging())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging was not cleaned: %v", entries)
	}
}

func TestResolveFailureLeavesCurrentRelease(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	seedRelease(t, l, oldRev)
	remote := &fakeRemote{resolveErr: errors.New("offline")}
	_, err := Update(context.Background(), l, remote, "main", &fakeBuilder{})
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("error = %v", err)
	}
	if _, active, _ := ReadActive(l); active != oldRev {
		t.Fatalf("active = %s", active)
	}
}

func TestFetchFailureLeavesCurrentRelease(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	seedRelease(t, l, oldRev)
	remote := &fakeRemote{rev: testRev('b'), fetchErr: errors.New("network down")}
	_, err := Update(context.Background(), l, remote, "main", &fakeBuilder{})
	if err == nil {
		t.Fatal("a failed fetch must be reported")
	}
	if _, active, _ := ReadActive(l); active != oldRev {
		t.Fatalf("active = %s", active)
	}
}

func TestIncompleteReleaseIsRefused(t *testing.T) {
	for name, builder := range map[string]*fakeBuilder{
		"missing updater": {missingUpdater: true},
		"mixed build":     {badUpdaterCommit: true},
	} {
		t.Run(name, func(t *testing.T) {
			l := testLayout(t)
			oldRev := testRev('a')
			newRev := testRev('b')
			seedRelease(t, l, oldRev)
			_, err := Update(context.Background(), l, &fakeRemote{rev: newRev}, "main", builder)
			if err == nil {
				t.Fatal("an invalid candidate must be refused")
			}
			if _, active, _ := ReadActive(l); active != oldRev {
				t.Fatalf("active = %s", active)
			}
			if ReleaseExists(l, newRev) {
				t.Fatal("an invalid candidate became a release")
			}
		})
	}
}

func TestPostInstallFailureRecoversOnRetry(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	newRev := testRev('b')
	seedRelease(t, l, oldRev)
	remote := &fakeRemote{rev: newRev}
	hook := runHooks{afterInstall: func() error { return errors.New("interrupted after install") }}
	if _, err := updateWith(context.Background(), l, remote, "main", &fakeBuilder{}, hook); err == nil {
		t.Fatal("the injected failure was not reported")
	}
	if _, active, _ := ReadActive(l); active != oldRev {
		t.Fatalf("active after interrupted install = %s", active)
	}
	if !ReleaseExists(l, newRev) {
		t.Fatal("the complete release should remain for a retry")
	}
	if _, err := Update(context.Background(), l, remote, "main", &fakeBuilder{}); err != nil {
		t.Fatal(err)
	}
	if _, active, _ := ReadActive(l); active != newRev {
		t.Fatalf("active after retry = %s", active)
	}
}

func TestPostActivateFailureRestoresPreviousPointer(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	newRev := testRev('b')
	seedRelease(t, l, oldRev)
	hook := runHooks{afterActivate: func() error { return errors.New("interrupted after activation") }}
	_, err := updateWith(context.Background(), l, &fakeRemote{rev: newRev}, "main", &fakeBuilder{}, hook)
	if err == nil {
		t.Fatal("the injected failure was not reported")
	}
	if _, active, _ := ReadActive(l); active != oldRev {
		t.Fatalf("active after failed activation = %s", active)
	}
	previous, _ := ReadPrevious(l)
	if previous != "" {
		t.Fatalf("previous record was not restored: %q", previous)
	}
	if !ReleaseExists(l, newRev) {
		t.Fatal("the installed release should remain")
	}
}

func TestConcurrentUpdaterIsRefused(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	seedRelease(t, l, oldRev)
	lock, err := AcquireLock(l)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	_, err = Update(context.Background(), l, &fakeRemote{rev: testRev('b')}, "main", &fakeBuilder{})
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("error = %v, want LockedError", err)
	}
	if _, active, _ := ReadActive(l); active != oldRev {
		t.Fatalf("active = %s", active)
	}
	if ReleaseExists(l, testRev('b')) {
		t.Fatal("a refused concurrent update changed the installation")
	}
}

func TestRollbackTogglesAndUsesNoBuild(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	newRev := testRev('b')
	seedRelease(t, l, oldRev)
	if _, err := Update(context.Background(), l, &fakeRemote{rev: newRev}, "main", &fakeBuilder{}); err != nil {
		t.Fatal(err)
	}
	result, err := Rollback(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.From != newRev || result.To != oldRev {
		t.Fatalf("rollback result = %#v", result)
	}
	if _, active, _ := ReadActive(l); active != oldRev {
		t.Fatalf("active after rollback = %s", active)
	}
	previous, err := ReadPrevious(l)
	if err != nil || previous != "releases/"+newRev {
		t.Fatalf("previous after rollback = %q, %v", previous, err)
	}
	if !LinkManaged(l, HalName) || !LinkManaged(l, UpdaterName) {
		t.Fatal("rollback broke the managed command links")
	}

	second, err := Rollback(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Updated || second.To != newRev {
		t.Fatalf("second rollback = %#v", second)
	}
	if _, active, _ := ReadActive(l); active != newRev {
		t.Fatalf("active after second rollback = %s", active)
	}
}

func TestRollbackRespectsUpdaterLock(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	newRev := testRev('b')
	seedRelease(t, l, oldRev)
	if _, err := Update(context.Background(), l, &fakeRemote{rev: newRev}, "main", &fakeBuilder{}); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireLock(l)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	_, err = Rollback(context.Background(), l)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("error = %v, want LockedError", err)
	}
	if _, active, _ := ReadActive(l); active != newRev {
		t.Fatalf("active = %s", active)
	}
}

func TestRollbackWithoutPreviousIsRefused(t *testing.T) {
	l := testLayout(t)
	rev := testRev('a')
	seedRelease(t, l, rev)
	if _, err := Rollback(context.Background(), l); err == nil {
		t.Fatal("rollback without a previous release must fail")
	}
	if _, active, _ := ReadActive(l); active != rev {
		t.Fatalf("active = %s", active)
	}
}

func TestRollbackRejectsPointerEscape(t *testing.T) {
	l := testLayout(t)
	rev := testRev('a')
	seedRelease(t, l, rev)
	if err := os.MkdirAll(l.State(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.PreviousRelease(), []byte("../../etc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(context.Background(), l); err == nil {
		t.Fatal("a traversal in previous-release must be refused")
	}
	if _, active, _ := ReadActive(l); active != rev {
		t.Fatalf("active = %s", active)
	}
}

func TestRollbackValidatesTargetBeforeActivation(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	newRev := testRev('b')
	seedRelease(t, l, oldRev)
	if _, err := Update(context.Background(), l, &fakeRemote{rev: newRev}, "main", &fakeBuilder{}); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(l.ReleaseDir(oldRev), "manifest.json")
	if err := os.WriteFile(manifest, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(context.Background(), l); err == nil {
		t.Fatal("rollback to an invalid release must be refused")
	}
	if _, active, _ := ReadActive(l); active != newRev {
		t.Fatalf("active = %s", active)
	}
}

func TestMissingActiveManifestBlocksUpdate(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	seedRelease(t, l, oldRev)
	if err := os.Remove(filepath.Join(l.ReleaseDir(oldRev), "manifest.json")); err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{rev: testRev('b')}
	if _, err := Update(context.Background(), l, remote, "main", &fakeBuilder{}); err == nil {
		t.Fatal("an unhealthy active release must block update")
	}
	if _, active, _ := ReadActive(l); active != oldRev {
		t.Fatalf("active = %s", active)
	}
}

func TestOldReleaseStillRunsAfterPointerSwitch(t *testing.T) {
	l := testLayout(t)
	oldRev := testRev('a')
	newRev := testRev('b')
	seedRelease(t, l, oldRev)
	oldBinary := l.ReleaseBin(oldRev, HalName)
	openFile, err := os.Open(oldBinary)
	if err != nil {
		t.Fatal(err)
	}
	defer openFile.Close()
	body, err := os.ReadFile(oldBinary)
	if err != nil || !strings.Contains(string(body), oldRev) {
		t.Fatalf("old binary before update = %q, %v", body, err)
	}

	if _, err := Update(context.Background(), l, &fakeRemote{rev: newRev}, "main", &fakeBuilder{}); err != nil {
		t.Fatal(err)
	}
	// The file opened before the switch still describes the old release,
	// and the old path still executes: a running HAL is not disturbed.
	opened := make([]byte, 4096)
	n, _ := openFile.Read(opened)
	if !strings.Contains(string(opened[:n]), oldRev) {
		t.Fatalf("open old binary changed: %q", opened[:n])
	}
	cmd := exec.Command(oldBinary, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("old release no longer runs: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "commit: "+oldRev) {
		t.Fatalf("old release version = %s", out)
	}
}

func TestAtomicPointerLeavesNoTemporary(t *testing.T) {
	l := testLayout(t)
	rev := testRev('a')
	seedRelease(t, l, rev)
	previous := testRev('b')
	if _, err := Activate(l, previous); err == nil {
		// The release does not exist; activation must be refused and
		// must not leave a temporary link.
		t.Fatal("activation of a missing release succeeded")
	}
	if matches, _ := filepath.Glob(filepath.Join(l.Root, ".current.swap.*")); len(matches) != 0 {
		t.Fatalf("pointer temporaries remain: %v", matches)
	}
	if _, active, _ := ReadActive(l); active != rev {
		t.Fatalf("active = %s", active)
	}
	if _, err := Activate(l, rev); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(l.Root, ".current.swap.*")); len(matches) != 0 {
		t.Fatalf("pointer temporaries remain: %v", matches)
	}
}

func TestCheckReportsCurrentBehindAndDiverged(t *testing.T) {
	l := testLayout(t)
	rev := testRev('a')
	seedRelease(t, l, rev)
	current, err := Check(context.Background(), l, &fakeRemote{rev: rev}, "main")
	if err != nil || current.Relation != "current" {
		t.Fatalf("current check = %#v, %v", current, err)
	}
	behind, err := Check(context.Background(), l, &fakeRemote{rev: testRev('b'), ancestor: true}, "main")
	if err != nil || behind.Relation != "behind" {
		t.Fatalf("behind check = %#v, %v", behind, err)
	}
	diverged, err := Check(context.Background(), l, &fakeRemote{rev: testRev('b'), ancestor: false}, "main")
	if err != nil || diverged.Relation != "diverged" {
		t.Fatalf("diverged check = %#v, %v", diverged, err)
	}
	if _, active, _ := ReadActive(l); active != rev {
		t.Fatalf("check changed the active release: %s", active)
	}
}

func TestCheckNetworkFailureIsReported(t *testing.T) {
	l := testLayout(t)
	_, err := Check(context.Background(), l, &fakeRemote{resolveErr: errors.New("offline")}, "main")
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckOnUninstalledRoot(t *testing.T) {
	l := testLayout(t)
	result, err := Check(context.Background(), l, &fakeRemote{rev: testRev('a')}, "main")
	if err != nil {
		t.Fatal(err)
	}
	if result.Installed || result.Relation != "not installed" {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(l.Root); !os.IsNotExist(err) {
		t.Fatalf("check created the installation root: %v", err)
	}
}
