package attachments

// Regression tests for the reserved sidecar name.
//
// The upload directory holds two things for every attachment: the
// copy, and the sidecar that describes it. A file called
// "attachment.json" is an ordinary file the operator may attach, and
// its copy must not take the sidecar's name; and the sidecar must
// never be readable as if it were a copy.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sidecarName is the name the upload directory reserves for the record
// that describes a copy.
//
// It is written out here on purpose: the test pins the name on disk
// rather than the constant that happens to hold it, so it states the
// contract instead of the implementation.
const sidecarName = "attachment.json"

// reservedName reports whether a name is the reserved sidecar name.
func reservedName(name string) bool {
	return strings.EqualFold(name, sidecarName)
}

// TestAFileNamedLikeTheSidecarAttaches checks the first half of the
// correction: an ordinary file called "attachment.json" attaches.
func TestAFileNamedLikeTheSidecarAttaches(t *testing.T) {
	s, root := newStore(t)
	body := `{"operator":"the content the model must discuss"}`
	path := writeSource(t, t.TempDir(), "attachment.json", body)

	meta, err := s.Attach(path)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// The operator still sees the name they chose.
	if meta.Name != "attachment.json" {
		t.Errorf("the operator-visible name = %q, want %q", meta.Name, "attachment.json")
	}

	// The copy and the sidecar are two different files.
	sidecar := filepath.Join(root, meta.ID, sidecarName)
	copyPath := filepath.Join(root, filepath.FromSlash(meta.Path))
	if copyPath == sidecar {
		t.Fatalf("the copy and the sidecar share the path %q", meta.Path)
	}
	for _, p := range []string{sidecar, copyPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is missing: %v", p, err)
		}
	}

	// The copy holds the operator's bytes, not the record.
	stored, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != body {
		t.Errorf("the copy holds %q, want the operator's content %q", stored, body)
	}
	// And the content the model would receive is the operator's bytes.
	text, _, err := s.Content(meta, 4096)
	if err != nil {
		t.Fatalf("the copy could not be read: %v", err)
	}
	if !strings.Contains(text, "the content the model must discuss") {
		t.Errorf("the content is %q", text)
	}
	if strings.Contains(text, "\"source\"") {
		t.Errorf("the content is the record, not the file: %q", text)
	}
}

// TestTheSidecarNameIsNeverTakenByACopy checks the name rule itself.
func TestTheSidecarNameIsNeverTakenByACopy(t *testing.T) {
	for _, name := range []string{
		"attachment.json",
		"ATTACHMENT.JSON",
		"Attachment.Json",
		" attachment.json ",
	} {
		safe := safeName(name)
		if reservedName(safe) {
			t.Errorf("safeName(%q) = %q, which is the reserved name", name, safe)
		}
		if safe == "" {
			t.Errorf("safeName(%q) is empty", name)
		}
	}
	// An ordinary name is untouched, so existing attachments keep the
	// path they were stored with.
	for _, name := range []string{"notes.md", "data.csv", "main.go"} {
		if got := safeName(name); got != name {
			t.Errorf("safeName(%q) = %q", name, got)
		}
	}
}

// TestAForgedReferenceToTheSidecarIsRefused checks the second half:
// the record is never readable as if it were a copy.
func TestAForgedReferenceToTheSidecarIsRefused(t *testing.T) {
	s, _ := newStore(t)
	src := writeSource(t, t.TempDir(), "notes.md", "operator bytes")
	meta, err := s.Attach(src)
	if err != nil {
		t.Fatal(err)
	}
	forged := Meta{
		ID:    meta.ID,
		Name:  sidecarName,
		Path:  meta.ID + "/" + sidecarName,
		Bytes: 10,
		Kind:  "text",
	}
	text, _, err := s.Content(forged, 4096)
	if err == nil {
		t.Errorf("the sidecar was read as a copy's content: %q", text)
	}
	if strings.Contains(text, "source") {
		t.Errorf("the record was returned: %q", text)
	}
}

// TestAnEmptyChecksumDoesNotBypassTheSidecar checks that the refusal
// does not depend on the checksum being present. A reference without
// one is exactly the shape a forged reference takes.
func TestAnEmptyChecksumDoesNotBypassTheSidecar(t *testing.T) {
	s, _ := newStore(t)
	src := writeSource(t, t.TempDir(), "notes.md", "operator bytes")
	meta, err := s.Attach(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{"", "0000000000000000000000000000000000000000000000000000000000000000"} {
		forged := Meta{
			ID:     meta.ID,
			Name:   sidecarName,
			Path:   meta.ID + "/" + sidecarName,
			Bytes:  10,
			Kind:   "text",
			SHA256: sha,
		}
		text, _, err := s.Content(forged, 4096)
		if err == nil {
			t.Errorf("the sidecar was read with checksum %q: %q", sha, text)
		}
	}
	// The same reference under another spelling of the name is refused
	// too: on a case-insensitive filesystem it names the same file.
	for _, name := range []string{sidecarName, "ATTACHMENT.JSON"} {
		forged := Meta{
			ID:    meta.ID,
			Name:  name,
			Path:  meta.ID + "/" + name,
			Bytes: 10,
			Kind:  "text",
		}
		if _, _, err := s.Content(forged, 4096); err == nil {
			t.Errorf("the sidecar was read as %q", name)
		}
	}
}

// TestTheStoredPathValidatorRefusesTheSidecar checks the validator
// that a session record passes through.
func TestTheStoredPathValidatorRefusesTheSidecar(t *testing.T) {
	if got := sanitizeRelativePath("abc123/attachment.json"); got != "" {
		t.Errorf("the validator kept the sidecar's path: %q", got)
	}
	if got := sanitizeRelativePath("abc123/ATTACHMENT.JSON"); got != "" {
		t.Errorf("the validator kept the sidecar's path: %q", got)
	}
	if got := sanitizeRelativePath("abc123/notes.md"); got != "abc123/notes.md" {
		t.Errorf("the validator refused an ordinary copy: %q", got)
	}
	// A copy of a file named like the sidecar keeps its own name and
	// is still readable.
	if got := sanitizeRelativePath("abc123/attachment.json.copy"); got != "abc123/attachment.json.copy" {
		t.Errorf("the validator refused the copy: %q", got)
	}
}

// TestAnExistingAttachmentStillLoads checks the compatibility of the
// correction: a record written before it keeps working.
func TestAnExistingAttachmentStillLoads(t *testing.T) {
	s, root := newStore(t)
	src := writeSource(t, t.TempDir(), "notes.md", "operator bytes")
	meta, err := s.Attach(src)
	if err != nil {
		t.Fatal(err)
	}
	// The record that was written when the attachment was made.
	back, err := s.Load(meta.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if back.Name != meta.Name || back.Path != meta.Path || back.ID != meta.ID {
		t.Errorf("the reference changed: %+v, want %+v", back, meta)
	}
	// And it is still readable.
	text, _, err := s.Content(back, 4096)
	if err != nil {
		t.Fatalf("the stored reference could not be read: %v", err)
	}
	if !strings.Contains(text, "operator bytes") {
		t.Errorf("the content is %q", text)
	}
	// The sidecar beside it still describes the copy.
	raw, err := os.ReadFile(filepath.Join(root, meta.ID, sidecarName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"path"`) {
		t.Errorf("the sidecar does not describe the copy: %s", raw)
	}
}

// TestDuplicateAttachmentsStaySeparate checks that two copies of the
// same file, and a copy of a file called like the sidecar, do not
// collide.
func TestDuplicateAttachmentsStaySeparate(t *testing.T) {
	s, root := newStore(t)
	dir := t.TempDir()
	body := `{"operator":"content"}`
	path := writeSource(t, dir, "attachment.json", body)

	first, err := s.Attach(path)
	if err != nil {
		t.Fatalf("the first attach failed: %v", err)
	}
	second, err := s.Attach(path)
	if err != nil {
		t.Fatalf("the second attach failed: %v", err)
	}
	if first.ID == second.ID {
		t.Errorf("the two attachments share an identity: %q", first.ID)
	}
	for _, meta := range []Meta{first, second} {
		stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(meta.Path)))
		if err != nil {
			t.Errorf("%q: the copy is missing: %v", meta.Path, err)
			continue
		}
		if string(stored) != body {
			t.Errorf("%q: the copy holds %q", meta.Path, stored)
		}
		// Each attachment keeps its own record.
		raw, err := os.ReadFile(filepath.Join(root, meta.ID, sidecarName))
		if err != nil {
			t.Errorf("%q: the record is missing: %v", meta.ID, err)
			continue
		}
		if !strings.Contains(string(raw), meta.ID) {
			t.Errorf("%q: the record describes another attachment: %s", meta.ID, raw)
		}
	}
}

// TestAFailedAttachLeavesNothingBehind checks that a refusal does not
// leave a copy or a record in the upload directory.
func TestAFailedAttachLeavesNothingBehind(t *testing.T) {
	s, root := newStore(t)
	dir := t.TempDir()
	// A directory is refused.
	if _, err := s.Attach(dir); err == nil {
		t.Fatal("a directory must be refused")
	}
	// A binary file is refused.
	binary := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(binary, []byte{0x00, 0x01, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(binary); err == nil {
		t.Fatal("a binary file must be refused")
	}
	// Nothing is left behind: the store does not even create the
	// upload directory until a file is successfully copied.
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the upload directory holds %d entries after a refusal", len(entries))
	}
}

// TestTheSourceFileIsUntouchedByTheCorrection checks that the copy is a
// copy: the operator's file is not moved, rewritten or truncated.
func TestTheSourceFileIsUntouchedByTheCorrection(t *testing.T) {
	s, _ := newStore(t)
	body := `{"operator":"content"}`
	path := writeSource(t, t.TempDir(), "attachment.json", body)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(path); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("the source was replaced")
	}
	if after.Size() != before.Size() {
		t.Errorf("the source size changed: %d -> %d", before.Size(), after.Size())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("the source content changed: %q", got)
	}
}

// TestTheUploadDirectoryStaysPrivate checks the permissions the
// correction must not disturb.
func TestTheUploadDirectoryStaysPrivate(t *testing.T) {
	s, root := newStore(t)
	src := writeSource(t, t.TempDir(), "attachment.json", `{"operator":"content"}`)
	meta, err := s.Attach(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		root,
		filepath.Join(root, meta.ID),
		filepath.Join(root, filepath.FromSlash(meta.Path)),
		filepath.Join(root, meta.ID, sidecarName),
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is readable by others: %v", p, perm)
		}
	}
}
