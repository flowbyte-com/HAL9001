package workspace

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// newStore returns a store rooted in a temporary directory, plus its root.
func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "HAL")
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, s.Root()
}

// mustPrepare prepares a proposal or fails the test.
func mustPrepare(t *testing.T, s *Store, rel, content string) Proposal {
	t.Helper()
	p, err := s.Prepare(Op{Path: rel, Content: []byte(content), Session: "test-session", Turn: 3})
	if err != nil {
		t.Fatalf("Prepare(%q): %v", rel, err)
	}
	return p
}

// TestPrepareNeverWrites checks the core promise: a proposal does not
// touch the filesystem.
func TestPrepareNeverWrites(t *testing.T) {
	s, root := newStore(t)
	p := mustPrepare(t, s, "notes/research.md", "# Note\n")
	if p.Overwrite {
		t.Error("a new file must not be reported as an overwrite")
	}
	if p.Size() != len("# Note\n") {
		t.Errorf("size = %d", p.Size())
	}
	if p.Digest == "" || len(p.Digest) != 64 {
		t.Errorf("digest = %q", p.Digest)
	}
	if p.Session != "test-session" || p.Turn != 3 {
		t.Errorf("binding = %q/%d", p.Session, p.Turn)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("Prepare created the workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes")); !os.IsNotExist(err) {
		t.Error("Prepare created parent directories")
	}
}

// TestApproveWritesExactlyOnce checks the happy path and that a second
// approval of the same proposal cannot write again.
func TestApproveWritesExactlyOnce(t *testing.T) {
	s, root := newStore(t)
	p := mustPrepare(t, s, "notes/research.md", "line one\nline two\n")
	res, err := s.Approve(p)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if res.Bytes != int64(len("line one\nline two\n")) {
		t.Errorf("result = %#v", res)
	}
	got, err := os.ReadFile(filepath.Join(root, "notes", "research.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "line one\nline two\n" {
		t.Errorf("content = %q", got)
	}
	if info, err := os.Stat(filepath.Join(root, "notes", "research.md")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm()&0o111 != 0 {
		t.Errorf("saved file is executable: %v", info.Mode())
	}
	if _, err := s.Approve(p); err == nil {
		t.Fatal("a consumed proposal must not write twice")
	}
}

// TestRejectWritesNothing checks that a rejection leaves no trace.
func TestRejectWritesNothing(t *testing.T) {
	s, root := newStore(t)
	p := mustPrepare(t, s, "letter.md", "hello")
	if err := s.Reject(p, "operator declined"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "letter.md")); !os.IsNotExist(err) {
		t.Error("a rejected proposal wrote a file")
	}
}

// TestOverwriteRequiresApprovalOfTheExistingFile checks that a replacement
// is marked as one and carries the size of the file it replaces.
func TestOverwriteRequiresApprovalOfTheExistingFile(t *testing.T) {
	s, root := newStore(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "draft.md"), []byte("old draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := mustPrepare(t, s, "draft.md", "new draft")
	if !p.Overwrite || p.Existing != int64(len("old draft")) {
		t.Fatalf("proposal = %#v", p)
	}
	if _, err := s.Approve(p); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "draft.md"))
	if string(got) != "new draft" {
		t.Errorf("content = %q", got)
	}
}

// TestPathTraversalIsRefused covers every shape of escape attempt.
func TestPathTraversalIsRefused(t *testing.T) {
	s, _ := newStore(t)
	cases := []string{
		"../outside.md",
		"a/../../outside.md",
		"..",
		"./notes.md",
		"",
		"   ",
		"/etc/passwd",
		"/tmp/x",
		`C:\Windows\system.ini`,
		`C:/Windows/system.ini`,
		`..\outside.md`,
		`a\..\..\outside.md`,
		"a//b.md",
		"a/./b.md",
		"dir/",
		"a:b.md",
		"note?.md",
		"note*.md",
		"CON",
		"nul.txt",
		"COM1.md",
		"trailing.",
		"a /b.md",
		strings.Repeat("x", 201) + ".md",
		strings.Repeat("a/", maxPathDepth) + "deep.md",
		"notes\x00.md",
	}
	for _, rel := range cases {
		if _, err := s.Prepare(Op{Path: rel, Content: []byte("x")}); !errors.Is(err, ErrPath) {
			t.Errorf("Prepare(%q) error = %v, want ErrPath", rel, err)
		}
	}
}

// TestSymlinkEscapeIsRefused checks that a link inside the workspace
// cannot be used to write outside it, neither as a parent nor as the
// destination itself.
func TestSymlinkEscapeIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	s, root := newStore(t)
	outside := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := s.Prepare(Op{Path: "escape/pwned.md", Content: []byte("x")}); !errors.Is(err, ErrSymlink) {
		t.Errorf("parent link error = %v, want ErrSymlink", err)
	}
	target := filepath.Join(outside, "target.md")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "leaf.md")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := s.Prepare(Op{Path: "leaf.md", Content: []byte("x")}); !errors.Is(err, ErrSymlink) {
		t.Errorf("leaf link error = %v, want ErrSymlink", err)
	}
	// The outside file is untouched.
	if got, _ := os.ReadFile(target); string(got) != "original" {
		t.Errorf("outside file changed: %q", got)
	}
}

// TestSymlinkAppearingBeforeApprovalIsRefused checks that the checks run
// again at execution time: a link created after preparation fails closed.
func TestSymlinkAppearingBeforeApprovalIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	s, root := newStore(t)
	p := mustPrepare(t, s, "dir/note.md", "content")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := s.Approve(p); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Approve error = %v, want ErrSymlink", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "note.md")); !os.IsNotExist(err) {
		t.Error("the write escaped through the link")
	}
}

// TestDestinationChangesAreRefused covers both races: a create whose
// destination appeared, and a replace whose destination was swapped.
func TestDestinationChangesAreRefused(t *testing.T) {
	s, root := newStore(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// A create whose destination appeared after preparation.
	create := mustPrepare(t, s, "new.md", "proposed")
	if err := os.WriteFile(filepath.Join(root, "new.md"), []byte("someone else"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(create); !errors.Is(err, ErrDestinationChanged) {
		t.Errorf("create race error = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "new.md")); string(got) != "someone else" {
		t.Errorf("the racing file was overwritten: %q", got)
	}

	// A replace whose destination was swapped for a different file.
	path := filepath.Join(root, "draft.md")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	replace := mustPrepare(t, s, "draft.md", "second")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("swapped"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(replace); !errors.Is(err, ErrDestinationChanged) {
		t.Errorf("replace race error = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "swapped" {
		t.Errorf("the swapped file was overwritten: %q", got)
	}
}

// TestContentMutationIsRefused checks the digest binding: changing the
// reviewed bytes after preparation invalidates the approval.
func TestContentMutationIsRefused(t *testing.T) {
	s, root := newStore(t)
	p := mustPrepare(t, s, "note.md", "reviewed")
	p.Content = []byte("something else")
	if _, err := s.Approve(p); !errors.Is(err, ErrDestinationChanged) {
		t.Fatalf("Approve error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "note.md")); !os.IsNotExist(err) {
		t.Error("mutated content was written")
	}
}

// TestContentLimits checks the text and size rules.
func TestContentLimits(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Prepare(Op{Path: "big.md", Content: make([]byte, MaxBytes+1)}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized content error = %v", err)
	}
	if _, err := s.Prepare(Op{Path: "bin.md", Content: []byte{0xff, 0xfe, 0x00}}); !errors.Is(err, ErrNotText) {
		t.Errorf("binary content error = %v", err)
	}
	// The boundary itself is allowed, and so is an empty file.
	if _, err := s.Prepare(Op{Path: "edge.md", Content: bytes.Repeat([]byte("x"), MaxBytes)}); err != nil {
		t.Errorf("MaxBytes content refused: %v", err)
	}
	if _, err := s.Prepare(Op{Path: "empty.md", Content: nil}); err != nil {
		t.Errorf("empty content refused: %v", err)
	}
}

// TestParentDirectoriesAreCreated checks that the approved creation makes
// the directories it needs, inside the workspace.
func TestParentDirectoriesAreCreated(t *testing.T) {
	s, root := newStore(t)
	p := mustPrepare(t, s, "research/2026/notes/acc-fuel.md", "note")
	if _, err := s.Approve(p); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "research", "2026", "notes", "acc-fuel.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "note" {
		t.Errorf("content = %q", got)
	}
}

// TestProtectedDirectoryIsRefused checks the defence in depth around the
// application's own configuration directory.
func TestProtectedDirectoryIsRefused(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Protect(filepath.Join(root, ".hal9001"))
	if _, err := s.Prepare(Op{Path: ".hal9001/config.json", Content: []byte("{}")}); !errors.Is(err, ErrProtected) {
		t.Fatalf("error = %v, want ErrProtected", err)
	}
}

// TestRootSymlinkIsResolved checks that a workspace that is itself a link
// is treated as the directory it points at.
func TestRootSymlinkIsResolved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	real := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	s, err := Open(link)
	if err != nil {
		t.Fatal(err)
	}
	if s.Root() != real {
		t.Errorf("root = %q, want %q", s.Root(), real)
	}
	p := mustPrepare(t, s, "note.md", "x")
	if _, err := s.Approve(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(real, "note.md")); err != nil {
		t.Error("the write did not land in the resolved root")
	}
}

// TestConcurrentProposals checks that independent paths do not interfere
// and that two proposals for one new path cannot both succeed.
func TestConcurrentProposals(t *testing.T) {
	s, root := newStore(t)
	a := mustPrepare(t, s, "a.md", "a")
	b := mustPrepare(t, s, "b.md", "b")
	dup1 := mustPrepare(t, s, "dup.md", "first")
	dup2 := mustPrepare(t, s, "dup.md", "second")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, p := range []Proposal{a, b} {
		wg.Add(1)
		go func(i int, p Proposal) {
			defer wg.Done()
			_, errs[i] = s.Approve(p)
		}(i, p)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("independent proposal %d: %v", i, err)
		}
	}

	var dupErrs [2]error
	for i, p := range []Proposal{dup1, dup2} {
		wg.Add(1)
		go func(i int, p Proposal) {
			defer wg.Done()
			_, dupErrs[i] = s.Approve(p)
		}(i, p)
	}
	wg.Wait()
	if (dupErrs[0] == nil) == (dupErrs[1] == nil) {
		t.Fatalf("duplicate proposals: both succeeded or both failed: %v %v", dupErrs[0], dupErrs[1])
	}
	got, err := os.ReadFile(filepath.Join(root, "dup.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" && string(got) != "second" {
		t.Errorf("content = %q", got)
	}
}

// TestNilStoreIsSafe checks that a missing workspace cannot write.
func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	if _, err := s.Prepare(Op{Path: "x.md", Content: []byte("x")}); !errors.Is(err, ErrClosed) {
		t.Errorf("Prepare error = %v", err)
	}
	if _, err := s.Approve(Proposal{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Approve error = %v", err)
	}
	if err := s.Reject(Proposal{}, ""); !errors.Is(err, ErrClosed) {
		t.Errorf("Reject error = %v", err)
	}
	if s.Root() != "" {
		t.Errorf("Root = %q", s.Root())
	}
}

// TestDisplayPath checks the review's destination rendering.
func TestDisplayPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	s, err := Open(filepath.Join(home, "HAL"))
	if err != nil {
		t.Fatal(err)
	}
	p := mustPrepare(t, s, "notes/acc-fuel.md", "x")
	if got, want := s.DisplayPath(p), "~/HAL/notes/acc-fuel.md"; got != want {
		t.Errorf("DisplayPath = %q, want %q", got, want)
	}
}

// TestAuditTrail checks that decisions are recorded, contents are not, and
// the trail is bounded.
func TestAuditTrail(t *testing.T) {
	dir := t.TempDir()
	audit, err := OpenAudit(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s, root := newStore(t)
	s.WithAudit(audit).Protect(filepath.Join(root, "..", ".hal9001"))

	written := mustPrepare(t, s, "written.md", "secret contents")
	if _, err := s.Approve(written); err != nil {
		t.Fatal(err)
	}
	rejected := mustPrepare(t, s, "rejected.md", "also secret")
	if err := s.Reject(rejected, "operator declined"); err != nil {
		t.Fatal(err)
	}
	failed := mustPrepare(t, s, "failed.md", "third")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "failed.md"), []byte("raced"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(failed); err == nil {
		t.Fatal("expected the raced write to fail")
	}

	records, err := audit.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	if records[0].Decision != "approved" || records[0].Outcome != "written" {
		t.Errorf("write record = %#v", records[0])
	}
	if records[1].Decision != "rejected" || records[1].Outcome != "not-run" || records[1].Reason != "operator declined" {
		t.Errorf("reject record = %#v", records[1])
	}
	if records[2].Outcome != "failed" || records[2].Reason == "" {
		t.Errorf("failure record = %#v", records[2])
	}
	for _, r := range records {
		if r.Path == "" || r.Digest == "" || r.Operation != "write" || r.Time.IsZero() {
			t.Errorf("incomplete record = %#v", r)
		}
	}
	raw, err := os.ReadFile(audit.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret contents", "also secret", "third"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the audit trail contains document content: %q", secret)
		}
	}
}

// TestAuditIsBounded checks the six-day retention.
func TestAuditIsBounded(t *testing.T) {
	audit, err := OpenAudit(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	audit.now = func() time.Time { return now }
	old := Record{Time: now.Add(-7 * 24 * time.Hour), Operation: "write", Path: "old.md", Decision: "approved", Outcome: "written"}
	fresh := Record{Time: now.Add(-time.Hour), Operation: "write", Path: "fresh.md", Decision: "approved", Outcome: "written"}
	for _, r := range []Record{old, fresh} {
		if err := audit.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := audit.Prune(); err != nil {
		t.Fatal(err)
	}
	records, err := audit.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Path != "fresh.md" {
		t.Fatalf("records = %#v", records)
	}
}

// TestListAndReadStayInsideTheWorkspace checks the read side: it lists and
// reads real files, refuses links and stays inside the root.
func TestListAndReadStayInsideTheWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	s, root := newStore(t)
	p := mustPrepare(t, s, "notes/acc-fuel.md", "the note")
	if _, err := s.Approve(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plain.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 || entries[0].Path != "notes" || entries[1].Path != "plain.txt" {
		t.Fatalf("entries = %#v", entries)
	}
	if !entries[0].Dir || entries[1].Bytes != 5 {
		t.Errorf("entries = %#v", entries)
	}
	inside, err := s.List("notes")
	if err != nil || len(inside) != 1 || inside[0].Path != "notes/acc-fuel.md" {
		t.Fatalf("inside = %#v (%v)", inside, err)
	}
	data, err := s.ReadFile("notes/acc-fuel.md")
	if err != nil || string(data) != "the note" {
		t.Fatalf("read = %q (%v)", data, err)
	}
	if _, err := s.ReadFile("../etc/passwd"); !errors.Is(err, ErrPath) {
		t.Errorf("read outside error = %v", err)
	}
	if _, err := s.List("../"); !errors.Is(err, ErrPath) {
		t.Errorf("list outside error = %v", err)
	}
	// A link inside the workspace is never followed by a read either.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := s.ReadFile("link.txt"); !errors.Is(err, ErrSymlink) {
		t.Errorf("read through a link error = %v", err)
	}
}
