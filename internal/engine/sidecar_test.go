package engine

// Regression test for the reserved sidecar name, at the boundary
// where the operator's own path would leave the machine.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/attachments"
)

// sidecarName is the name the upload directory reserves for the record
// that describes a copy. It is written out here on purpose: the test
// pins the name on disk rather than a constant.
const sidecarName = "attachment.json"

// TestAFileNamedLikeTheSidecarReachesTheModelAsData checks that an
// ordinary file called "attachment.json" is discussed, and that what
// reaches the provider is the operator's content and not the record
// that describes it.
func TestAFileNamedLikeTheSidecarReachesTheModelAsData(t *testing.T) {
	eng, store, rec := newAttachmentEngine(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "attachment.json")
	body := `{"operator":"the content the model must discuss"}`
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, err := store.Attach(src)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if meta.Name != "attachment.json" {
		t.Errorf("the operator-visible name = %q", meta.Name)
	}

	if _, err := eng.RespondStreamEventsWithAttachments(context.Background(),
		"read it", []attachments.Meta{meta}, nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	user := userMessage(t, lastRequest(t, rec))

	// The operator's content is there.
	if !strings.Contains(user, "the content the model must discuss") {
		t.Errorf("the operator's content is missing: %q", user)
	}
	// The record is not: its marker is the source path, and the name
	// of the field that holds it.
	if strings.Contains(user, "\"source\"") {
		t.Errorf("the record reached the request: %q", user)
	}
	if strings.Contains(user, dir) {
		t.Errorf("the operator's own path reached the request: %q", user)
	}
	// The name the operator chose is still shown.
	if !strings.Contains(user, "attachment.json") {
		t.Errorf("the file's name is missing from the request: %q", user)
	}
	// The system instruction still calls the content data.
	if system := systemMessage(t, lastRequest(t, rec)); !strings.Contains(system, "untrusted data") {
		t.Errorf("the content is not marked as data: %q", system)
	}
}

// TestAForgedSidecarReferenceReachesNothing checks that a stored
// reference naming the sidecar cannot turn the record into the
// content of a turn.
func TestAForgedSidecarReferenceReachesNothing(t *testing.T) {
	eng, store, rec := newAttachmentEngine(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(src, []byte("operator bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, err := store.Attach(src)
	if err != nil {
		t.Fatal(err)
	}
	forged := attachments.Meta{
		ID:    meta.ID,
		Name:  sidecarName,
		Path:  meta.ID + "/" + sidecarName,
		Bytes: 10,
		Kind:  "text",
	}
	if _, err := eng.RespondStreamEventsWithAttachments(context.Background(),
		"read it", []attachments.Meta{forged}, nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	user := userMessage(t, lastRequest(t, rec))
	if strings.Contains(user, dir) {
		t.Errorf("the operator's own path reached the request: %q", user)
	}
	if strings.Contains(user, "\"source\"") {
		t.Errorf("the record reached the request: %q", user)
	}
}
