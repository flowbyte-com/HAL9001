package install

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func randomSuffix() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", os.Getpid())
	}
	return hex.EncodeToString(buf[:])
}

// ReplaceSymlink atomically replaces path with a symlink to target. The
// temporary link is created in the same directory, so the final step is
// a rename(2) and a reader sees either the old link or the new one.
func ReplaceSymlink(path, target string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".swap."+randomSuffix())
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// AtomicWriteFile replaces path with data via a same-directory rename.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := EnsureRealDir(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// MoveAside preserves an existing unmanaged command before the new
// managed symlink replaces it. The backup path is inside the
// installation's state directory.
func MoveAside(src, dst string) error {
	if err := EnsureRealDir(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("refusing to move a directory aside: %s", src)
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// A home directory split across filesystems makes rename fail. Copy
	// the link or regular file, then remove the original only after the
	// copy is durable enough to restore.
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, dst); err != nil {
			return err
		}
	} else if info.Mode().IsRegular() {
		if err := copyRegular(src, dst, info.Mode().Perm()); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("cannot preserve special file %s", src)
	}
	return os.Remove(src)
}

func copyRegular(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// CleanStaging removes incomplete candidates left by an earlier run.
// It is called only while the update lock is held, and only removes
// children of the installation's own staging directory whose names this
// package created.
func CleanStaging(l Layout) {
	if info, err := os.Lstat(l.Staging()); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	entries, err := os.ReadDir(l.Staging())
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "update-") && !strings.HasPrefix(name, "candidate-") {
			continue
		}
		path := filepath.Join(l.Staging(), name)
		if !Inside(l.Staging(), path) || path == l.Staging() {
			continue
		}
		_ = os.RemoveAll(path)
	}
}

// RemoveStaging removes one candidate directory, but only when it is
// provably inside the installation's staging area.
func RemoveStaging(l Layout, path string) {
	if path == "" || !Inside(l.Staging(), path) || filepath.Clean(path) == filepath.Clean(l.Staging()) {
		return
	}
	_ = os.RemoveAll(path)
}

// EnsureStateDir creates the state directory with private permissions.
func EnsureStateDir(l Layout) error {
	return EnsureRealDir(l.State(), 0o700)
}

// EnsureRealDir creates path when absent and refuses a symlink or a
// non-directory. The updater must never follow a planted symlink into a
// filesystem area it does not own.
func EnsureRealDir(path string, perm os.FileMode) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, perm); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to use symlinked directory %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing to use non-directory %s", path)
	}
	return nil
}
