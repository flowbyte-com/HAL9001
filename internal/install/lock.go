package install

import (
	"fmt"
	"os"
	"syscall"
)

// Lock serialises every mutating updater operation. The kernel releases
// it when the process exits, so a crash never leaves a stale lock that
// has to be cleaned up by hand.
type Lock struct {
	f *os.File
}

// LockedError means another updater holds the installation lock.
type LockedError struct {
	Path string
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("another HAL9001 update holds the lock %s; refusing to run concurrently", e.Path)
}

// AcquireLock creates the installation state and takes the single
// updater lock without blocking.
func AcquireLock(l Layout) (*Lock, error) {
	if err := EnsureRealDir(l.Root, 0o700); err != nil {
		return nil, fmt.Errorf("cannot prepare installation root %s: %w", l.Root, err)
	}
	if err := EnsureStateDir(l); err != nil {
		return nil, fmt.Errorf("cannot create state directory %s: %w", l.State(), err)
	}
	f, err := os.OpenFile(l.LockFile(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open update lock %s: %w", l.LockFile(), err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, &LockedError{Path: l.LockFile()}
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. A failure to unlock is not turned into a
// command failure: the work has already completed.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
