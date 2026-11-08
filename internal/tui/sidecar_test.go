package tui

// Regression test for the reserved sidecar name, at the boundary the
// operator actually sees.

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// errRawSystem is the shape of the failure the correction removes: a
// raw system error carrying HAL's own machinery.
var errRawSystem = errors.New("attachments: openat 3abb9bd1d984/attachment.json: file exists")

// TestAttachingAFileNamedLikeTheSidecarWorks checks the operator path:
// the file attaches, the pending row names it, and the name shown is
// the one the operator chose.
func TestAttachingAFileNamedLikeTheSidecarWorks(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	path := writeSource(t, src, "attachment.json", `{"operator":"content"}`)

	m = runSlash(m, "/attach "+path)

	if len(m.pendingFiles) != 1 {
		t.Fatalf("the file did not attach: %d pending files (notice: %q)",
			len(m.pendingFiles), m.notice)
	}
	if m.pendingFiles[0].Name != "attachment.json" {
		t.Errorf("the pending name = %q, want %q", m.pendingFiles[0].Name, "attachment.json")
	}
	if !strings.Contains(m.notice, "attachment.json") {
		t.Errorf("the notice does not name the file: %q", m.notice)
	}
	// The notice carries no system error and no internal identifier.
	for _, leak := range []string{"openat", "attachments:", "0x"} {
		if strings.Contains(m.notice, leak) {
			t.Errorf("the notice leaks %q: %q", leak, m.notice)
		}
	}
}

// TestAFailedAttachLeavesNoPendingReference checks that a refusal does
// not leave the operator believing a file is attached.
func TestAFailedAttachLeavesNoPendingReference(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	// A directory is refused.
	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)
	if len(m.pendingFiles) != 1 {
		t.Fatalf("the file did not attach: %d pending files", len(m.pendingFiles))
	}

	// A missing file is refused and leaves the earlier one alone. The
	// path is kept short so the notice is not cut off at the terminal
	// width before it reaches the name.
	short, err := os.MkdirTemp("", "att")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	m = runSlash(m, "/attach "+short+"/missing.txt")
	if len(m.pendingFiles) != 1 {
		t.Errorf("a refused file changed the pending list: %d", len(m.pendingFiles))
	}
	if !strings.Contains(m.notice, "missing.txt") {
		t.Errorf("the notice does not name the refused file: %q", m.notice)
	}
	for _, leak := range []string{"openat", "attachments:", "0x"} {
		if strings.Contains(m.notice, leak) {
			t.Errorf("the notice leaks %q: %q", leak, m.notice)
		}
	}
}

// TestTheAttachFailureMessagesAreOperatorWords checks every message
// the operator can see: each names a condition, and none of them
// carries a raw system error or an internal identifier.
func TestTheAttachFailureMessagesAreOperatorWords(t *testing.T) {
	for _, path := range []string{"~/notes.md", "/tmp/notes.md"} {
		message := attachFailure(errRawSystem, path)
		if strings.Contains(message, "openat") {
			t.Errorf("the message carries a system error: %q", message)
		}
		if strings.Contains(message, "attachments:") {
			t.Errorf("the message carries an internal prefix: %q", message)
		}
		if !strings.Contains(message, path) {
			t.Errorf("the message does not name the file: %q", message)
		}
	}
}
