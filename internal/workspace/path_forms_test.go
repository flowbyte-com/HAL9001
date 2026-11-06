package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestWorkspacePathForms tests the three natural ways an operator can
// name a file: workspace-relative, ~/HAL/..., and an absolute path inside
// the workspace. All must resolve to the same relative proposal, and the
// displayed destination must match the file actually written.
func TestWorkspacePathForms(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := filepath.Join(home, "HAL")
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}

	relative := mustPrepare(t, s, "relative.txt", "relative")
	if relative.Path != "relative.txt" {
		t.Errorf("relative path = %q", relative.Path)
	}
	if got, want := s.DisplayPath(relative), "~/HAL/relative.txt"; got != want {
		t.Errorf("relative display = %q, want %q", got, want)
	}

	tilde := mustPrepare(t, s, "~/HAL/tilde.txt", "tilde")
	if tilde.Path != "tilde.txt" {
		t.Errorf("tilde path = %q, want tilde.txt", tilde.Path)
	}
	if got, want := s.DisplayPath(tilde), "~/HAL/tilde.txt"; got != want {
		t.Errorf("tilde display = %q, want %q", got, want)
	}
	if _, err := s.Approve(tilde); err != nil {
		t.Fatalf("approve tilde: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(s.Root(), "tilde.txt")); err != nil || string(got) != "tilde" {
		t.Fatalf("tilde write = %q (%v)", got, err)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "HAL", "tilde.txt")); !os.IsNotExist(err) {
		t.Errorf("tilde write created a doubled HAL directory: %v", err)
	}

	absolute := mustPrepare(t, s, filepath.Join(s.Root(), "absolute.txt"), "absolute")
	if absolute.Path != "absolute.txt" {
		t.Errorf("absolute path = %q, want absolute.txt", absolute.Path)
	}
	if got, want := s.DisplayPath(absolute), "~/HAL/absolute.txt"; got != want {
		t.Errorf("absolute display = %q, want %q", got, want)
	}
	if _, err := s.Approve(absolute); err != nil {
		t.Fatalf("approve absolute: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(s.Root(), "absolute.txt")); err != nil || string(got) != "absolute" {
		t.Fatalf("absolute write = %q (%v)", got, err)
	}

	// A genuine nested HAL directory is preserved. The resolver must not
	// strip a workspace-name component from a relative path.
	nested := mustPrepare(t, s, "~/HAL/HAL/nested.txt", "nested")
	if nested.Path != "HAL/nested.txt" {
		t.Errorf("nested path = %q, want HAL/nested.txt", nested.Path)
	}
	if got, want := s.DisplayPath(nested), "~/HAL/HAL/nested.txt"; got != want {
		t.Errorf("nested display = %q, want %q", got, want)
	}
	if _, err := s.Approve(nested); err != nil {
		t.Fatalf("approve nested: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(s.Root(), "HAL", "nested.txt")); err != nil || string(got) != "nested" {
		t.Fatalf("nested write = %q (%v)", got, err)
	}
}

func TestAbsoluteAndTildePathsOutsideAreRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	s, err := Open(filepath.Join(home, "HAL"))
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	cases := []string{
		filepath.Join(outside, "outside.txt"),
		"~/outside.txt",
		"~/HAL/../outside.txt",
		"~",
		"~user/outside.txt",
		"/etc/passwd",
	}
	for _, raw := range cases {
		if _, err := s.Prepare(Op{Path: raw, Content: []byte("x")}); !errors.Is(err, ErrPath) {
			t.Errorf("Prepare(%q) error = %v, want ErrPath", raw, err)
		}
	}
}

func TestAbsolutePathSymlinkEscapeIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	s, err := Open(filepath.Join(home, "HAL"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(s.Root(), "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := s.Prepare(Op{
		Path: filepath.Join(link, "pwned.txt"), Content: []byte("x"),
	}); !errors.Is(err, ErrSymlink) {
		t.Fatalf("absolute symlink escape error = %v, want ErrSymlink", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Error("the symlink test wrote outside the workspace")
	}
}

func TestTildeWriteCreatesParentsAndRecordsTheCanonicalPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	s, err := Open(filepath.Join(home, "HAL"))
	if err != nil {
		t.Fatal(err)
	}
	p := mustPrepare(t, s, "~/HAL/notes/2026/guarded-write-test.txt", "HAL444_WRITE_OK")
	if got, want := s.DisplayPath(p), "~/HAL/notes/2026/guarded-write-test.txt"; got != want {
		t.Errorf("display = %q, want %q", got, want)
	}
	if _, err := s.Approve(p); err != nil {
		t.Fatalf("approve: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(s.Root(), "notes", "2026", "guarded-write-test.txt"))
	if err != nil || string(got) != "HAL444_WRITE_OK" {
		t.Fatalf("approved file = %q (%v)", got, err)
	}
}
