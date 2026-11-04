package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// These tests cover the race the executor must win: a directory on the
// way to the destination is replaced by a symbolic link between the
// moment the operator approved the write and the moment it is carried
// out.
//
// The exchange is made deterministic with the store's beforeWrite hook,
// which runs after every check and immediately before the write itself.
// That is the only window in which a check-based executor is exposed, so
// a test that swaps the tree there proves the containment comes from the
// root handle and not from the checks.

// swapParentForLink replaces the workspace-relative directory dir with a
// symbolic link to target. The real directory is moved aside rather than
// deleted, so the test can still show that its contents are untouched.
func swapParentForLink(t *testing.T, root, dir, target string) {
	t.Helper()
	live := filepath.Join(root, dir)
	aside := filepath.Join(root, dir+"-moved")
	if err := os.RemoveAll(aside); err != nil {
		t.Errorf("clearing the moved directory: %v", err)
		return
	}
	if err := os.Rename(live, aside); err != nil {
		t.Errorf("moving the directory aside: %v", err)
		return
	}
	if err := os.Symlink(target, live); err != nil {
		t.Errorf("installing the link: %v", err)
	}
}

// assertEmpty fails when dir holds anything.
func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the write escaped the workspace: %s holds %v", dir, names)
	}
}

// TestParentReplacedBySymlinkCannotRedirectTheWrite is the central
// demonstration: an approved write whose parent directory is replaced by
// a link must not reach outside the workspace.
func TestParentReplacedBySymlinkCannotRedirectTheWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	s, root := newStore(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := mustPrepare(t, s, "sub/note.md", "the approved contents")
	s.beforeWrite = func() {
		swapParentForLink(t, root, "sub", outside)
	}

	_, err := s.Approve(p)
	if err == nil {
		t.Fatal("the write was redirected and reported success")
	}
	if !errors.Is(err, ErrSymlink) {
		t.Errorf("error = %v, want ErrSymlink", err)
	}
	// The decisive assertion: nothing at all reached the outside
	// directory the link points at.
	assertEmpty(t, outside)
	// And the workspace itself holds no stray file either.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "note.md" {
			t.Error("the write landed in the workspace root")
		}
	}
}

// TestParentReplacedBySymlinkCannotRedirectAnOverwrite covers the
// replacement path, which writes a temporary file and renames it into
// place. Neither step may cross the workspace boundary.
func TestParentReplacedBySymlinkCannotRedirectAnOverwrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	s, root := newStore(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "sub", "note.md")
	if err := os.WriteFile(original, []byte("the reviewed contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := mustPrepare(t, s, "sub/note.md", "the replacement contents")
	s.beforeWrite = func() {
		swapParentForLink(t, root, "sub", outside)
	}

	_, err := s.Approve(p)
	if err == nil {
		t.Fatal("the replacement was redirected and reported success")
	}
	assertEmpty(t, outside)
	// The file the operator reviewed is intact: it was moved aside, not
	// written through.
	moved := filepath.Join(root, "sub-moved", "note.md")
	got, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("reading the moved file: %v", err)
	}
	if string(got) != "the reviewed contents" {
		t.Errorf("the reviewed file changed: %q", got)
	}
}

// TestDestinationReplacedBySymlinkCannotRedirectTheWrite covers the
// destination itself: a link planted where the file should go must not
// become the target of the write.
func TestDestinationReplacedBySymlinkCannotRedirectTheWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	s, root := newStore(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "target.md")
	if err := os.WriteFile(target, []byte("the original"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := mustPrepare(t, s, "note.md", "the approved contents")
	s.beforeWrite = func() {
		if err := os.Symlink(target, filepath.Join(root, "note.md")); err != nil {
			t.Errorf("planting the link: %v", err)
		}
	}

	_, err := s.Approve(p)
	if err == nil {
		t.Fatal("the write followed a planted link")
	}
	// The file the link points at is untouched: the write neither
	// replaced nor appended to it.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the original" {
		t.Errorf("the linked-to file changed: %q", got)
	}
}

// TestWorkspaceRootReplacedBySymlinkIsRefused checks the outermost
// boundary: the workspace directory itself must still be the plain
// directory the operator pointed at.
func TestWorkspaceRootReplacedBySymlinkIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	s, root := newStore(t)
	outside := t.TempDir()
	// The workspace exists: a proposal never creates it.
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	p := mustPrepare(t, s, "note.md", "the approved contents")
	aside := root + "-moved"
	if err := os.Rename(root, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(p); err == nil {
		t.Fatal("the write followed a link in place of the workspace")
	}
	assertEmpty(t, outside)
}

// TestConcurrentParentSwapNeverEscapesTheWorkspace runs the exchange
// continuously while approvals happen, without any hook. Whatever the
// interleaving, nothing may ever be written outside the workspace.
func TestConcurrentParentSwapNeverEscapesTheWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	s, root := newStore(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		live := filepath.Join(root, "sub")
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Replace the parent with a link, then put a real
			// directory back. The removal never follows the link.
			_ = os.Remove(live)
			_ = os.Symlink(outside, live)
			_ = os.Remove(live)
			_ = os.MkdirAll(live, 0o755)
		}
	}()

	for i := 0; i < 300; i++ {
		p, err := s.Prepare(Op{Path: "sub/note.md", Content: []byte("payload")})
		if err != nil {
			// The tree is being moved under us: the proposal is
			// refused, which is a safety outcome, not a failure
			// of this test.
			continue
		}
		// The write may succeed or be refused; either is fine.
		_, _ = s.Approve(p)
		assertEmpty(t, outside)
	}

	close(stop)
	wg.Wait()
	assertEmpty(t, outside)
}

// TestTheFixtureCanRedirectAnUnconfinedWrite guards the tests above
// against passing for the wrong reason: the very same exchange,
// performed with an absolute path the way a name resolved in user space
// would be, really does redirect the write outside the workspace.
//
// If this ever stops being true, the containment tests would be proving
// nothing.
func TestTheFixtureCanRedirectAnUnconfinedWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := filepath.Join(t.TempDir(), "HAL")
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	swapParentForLink(t, root, "sub", outside)
	// The unconfined write: an absolute path, resolved by the kernel.
	f, err := os.OpenFile(filepath.Join(root, "sub", "note.md"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		t.Skipf("the platform refuses the unconfined write: %v", err)
	}
	f.Close()
	if _, err := os.Lstat(filepath.Join(outside, "note.md")); err != nil {
		t.Fatalf("the fixture no longer redirects a write: %v", err)
	}
}

// TestWorkspaceUnderASymlinkedParentStillWorks covers a portability
// detail: on some systems a parent of the workspace is itself a link
// (/var is on macOS). The root must resolve to the real directory and
// stay usable, and the write must land there.
func TestWorkspaceUnderASymlinkedParentStillWorks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(link, "HAL"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(real, "HAL"); s.Root() != want {
		t.Errorf("root = %q, want %q", s.Root(), want)
	}
	p := mustPrepare(t, s, "note.md", "the approved contents")
	if _, err := s.Approve(p); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(real, "HAL", "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the approved contents" {
		t.Errorf("content = %q", got)
	}
}

// TestRootHandleKeepsTheWriteInside checks the mechanism itself: the
// root handle resolves names inside the workspace and refuses the ones
// that leave it.
func TestRootHandleKeepsTheWriteInside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	s, root := newStore(t)
	outside := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	handle, err := s.rootHandle()
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.OpenFile("escape/pwned.md", os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm); err == nil {
		t.Fatal("the handle wrote through a link")
	}
	if err := handle.MkdirAll("escape/deep", dirPerm); err == nil {
		t.Fatal("the handle created a directory through a link")
	}
	assertEmpty(t, outside)
}
