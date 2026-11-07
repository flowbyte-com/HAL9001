package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/attachments"
	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/provider"
)

// attachmentEngine is an engine that records the attachments it was
// given, so a test can see what the model would have received.
type attachmentEngine struct {
	reply string
	atts  [][]attachments.Meta
}

func (a *attachmentEngine) RespondStream(context.Context, string, func(string)) (string, error) {
	return a.reply, nil
}

func (a *attachmentEngine) RespondStreamEvents(_ context.Context, _ string, _ func(engine.Event)) (string, error) {
	return a.reply, nil
}

func (a *attachmentEngine) RespondStreamEventsWithAttachments(_ context.Context, _ string, atts []attachments.Meta, _ func(engine.Event)) (string, error) {
	a.atts = append(a.atts, atts)
	return a.reply, nil
}

// newAttachmentModel returns an interface with an upload store, plus the
// store and a source directory with one file in it.
//
// The upload directory has a short path, so a notice that names it fits
// on one line the way it does in a real installation.
func newAttachmentModel(t *testing.T) (Model, *attachments.Store, string) {
	t.Helper()
	short, err := os.MkdirTemp("", "uploads")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	store, err := attachments.Open(filepath.Join(short, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	m := New(&attachmentEngine{reply: "ok"})
	m.SetColor(false)
	m.SetAttachments(store)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(Model), store, src
}

// writeSource creates a file with the given content and returns its path.
func writeSource(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// runSlash types a slash command and runs it.
func runSlash(m Model, line string) Model {
	m = typeText(m, line)
	return enterCurrent(m)
}

// TestAttachCopiesTheFileAndShowsIt checks the whole operator path: the
// file is copied, the pending row names it, and the size is shown.
func TestAttachCopiesTheFileAndShowsIt(t *testing.T) {
	m, store, src := newAttachmentModel(t)
	path := writeSource(t, src, "notes.md", "# Notes\n\nsome content\n")

	m = runSlash(m, "/attach "+path)

	if len(m.pendingFiles) != 1 {
		t.Fatalf("pending files = %d, want 1: %+v", len(m.pendingFiles), m.pendingFiles)
	}
	if m.pendingFiles[0].Name != "notes.md" {
		t.Errorf("the pending name = %q", m.pendingFiles[0].Name)
	}
	// The copy is in the upload directory.
	if _, err := os.Stat(filepath.Join(store.Root(), filepath.FromSlash(m.pendingFiles[0].Path))); err != nil {
		t.Errorf("the copy is missing: %v", err)
	}
	// The row above the composer names the file and its size.
	rows := m.layoutRows()
	plain := make([]string, 0, len(rows))
	for _, r := range rows {
		plain = append(plain, stripANSI(r))
	}
	found := false
	for _, r := range plain {
		if strings.Contains(r, "notes.md") && strings.Contains(r, "B") {
			found = true
		}
	}
	if !found {
		t.Errorf("the pending row does not name the file:\n%s", strings.Join(plain, "\n"))
	}
}

// TestAttachAcceptsAPathWithSpaces checks that a path with spaces is
// taken whole.
func TestAttachAcceptsAPathWithSpaces(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	path := writeSource(t, src, "my report final.txt", "content")

	m = runSlash(m, "/attach "+path)
	if len(m.pendingFiles) != 1 {
		t.Fatalf("pending files = %d, want 1: %+v", len(m.pendingFiles), m.pendingFiles)
	}
	if m.pendingFiles[0].Name != "my report final.txt" {
		t.Errorf("the pending name = %q", m.pendingFiles[0].Name)
	}

	// A quoted path is taken whole too.
	m2, _, src2 := newAttachmentModel(t)
	path2 := writeSource(t, src2, "another report.txt", "content")
	m2 = runSlash(m2, "/attach \""+path2+"\"")
	if len(m2.pendingFiles) != 1 {
		t.Fatalf("pending files = %d, want 1: %+v", len(m2.pendingFiles), m2.pendingFiles)
	}
	if m2.pendingFiles[0].Name != "another report.txt" {
		t.Errorf("the pending name = %q", m2.pendingFiles[0].Name)
	}
}

// TestAttachAcceptsAUnicodeName checks that a Unicode filename survives.
func TestAttachAcceptsAUnicodeName(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	path := writeSource(t, src, "naïve café.md", "content")

	m = runSlash(m, "/attach "+path)
	if len(m.pendingFiles) != 1 {
		t.Fatalf("pending files = %d, want 1: %+v", len(m.pendingFiles), m.pendingFiles)
	}
	if m.pendingFiles[0].Name != "naïve café.md" {
		t.Errorf("the pending name = %q", m.pendingFiles[0].Name)
	}
}

// TestAttachSeveralFiles checks that more than one file can be pending.
func TestAttachSeveralFiles(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	one := writeSource(t, src, "one.txt", "one")
	two := writeSource(t, src, "two.txt", "two")

	m = runSlash(m, "/attach "+one)
	m = runSlash(m, "/attach "+two)
	if len(m.pendingFiles) != 2 {
		t.Fatalf("pending files = %d, want 2", len(m.pendingFiles))
	}
	if m.pendingFiles[0].ID == m.pendingFiles[1].ID {
		t.Error("the two attachments share an identity")
	}
}

// TestAttachTheSameNameTwice checks that two files with the same name
// are kept apart.
func TestAttachTheSameNameTwice(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	first := writeSource(t, src, "same.txt", "first")
	second := filepath.Join(t.TempDir(), "same.txt")
	if err := os.WriteFile(second, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}

	m = runSlash(m, "/attach "+first)
	m = runSlash(m, "/attach "+second)
	if len(m.pendingFiles) != 2 {
		t.Fatalf("pending files = %d, want 2", len(m.pendingFiles))
	}
	if m.pendingFiles[0].ID == m.pendingFiles[1].ID {
		t.Error("the two attachments share an identity")
	}
}

// TestAttachReportsAMissingFile checks that a bad path produces a clear
// message and no pending file.
func TestAttachReportsAMissingFile(t *testing.T) {
	m, _, _ := newAttachmentModel(t)
	m = runSlash(m, "/attach "+filepath.Join(t.TempDir(), "nope.txt"))
	if len(m.pendingFiles) != 0 {
		t.Errorf("pending files = %d, want 0", len(m.pendingFiles))
	}
	if !strings.Contains(m.notice, "no such file") {
		t.Errorf("notice = %q", m.notice)
	}
}

// TestAttachReportsAnUnreadableFile checks the refusal of a file HAL
// cannot read.
func TestAttachReportsAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the test cannot make a file unreadable as root")
	}
	m, _, src := newAttachmentModel(t)
	path := writeSource(t, src, "secret.txt", "content")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	m = runSlash(m, "/attach "+path)
	if len(m.pendingFiles) != 0 {
		t.Errorf("pending files = %d, want 0", len(m.pendingFiles))
	}
	if m.notice == "" {
		t.Error("the failure was not reported")
	}
}

// TestAttachReportsAnOversizedFile checks the size refusal.
func TestAttachReportsAnOversizedFile(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	path := filepath.Join(src, "big.txt")
	if err := os.WriteFile(path, make([]byte, attachments.MaxFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	m = runSlash(m, "/attach "+path)
	if len(m.pendingFiles) != 0 {
		t.Errorf("pending files = %d, want 0", len(m.pendingFiles))
	}
	if !strings.Contains(m.notice, "too large") {
		t.Errorf("notice = %q", m.notice)
	}
}

// TestAttachReportsABinaryFile checks that binary data is refused rather
// than turned into text.
func TestAttachReportsABinaryFile(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	path := filepath.Join(src, "binary.dat")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	m = runSlash(m, "/attach "+path)
	if len(m.pendingFiles) != 0 {
		t.Errorf("pending files = %d, want 0", len(m.pendingFiles))
	}
	if !strings.Contains(m.notice, "binary") {
		t.Errorf("notice = %q", m.notice)
	}
}

// TestThePendingSizeIsBoundedBeforeTheCopy checks that a file which
// would not fit is refused without leaving a copy behind.
func TestThePendingSizeIsBoundedBeforeTheCopy(t *testing.T) {
	m, store, src := newAttachmentModel(t)
	// Three files of 1.5 MiB each: the third would take the pending
	// set over its 4 MiB limit.
	body := strings.Repeat("x", 1536*1024)
	for i := 0; i < 2; i++ {
		path := writeSource(t, src, "big"+string(rune('a'+i))+".txt", body)
		m = runSlash(m, "/attach "+path)
	}
	if len(m.pendingFiles) != 2 {
		t.Fatalf("pending files = %d, want 2", len(m.pendingFiles))
	}
	path := writeSource(t, src, "bigc.txt", body)
	m = runSlash(m, "/attach "+path)
	if len(m.pendingFiles) != 2 {
		t.Errorf("pending files = %d, want 2", len(m.pendingFiles))
	}
	if !strings.Contains(m.notice, "limit") {
		t.Errorf("notice = %q", m.notice)
	}
	// The refused file left no copy behind.
	copies, err := os.ReadDir(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) != 2 {
		t.Errorf("the upload directory holds %d copies, want 2", len(copies))
	}
}

// TestAttachLeavesTheSourceAlone checks that the original file is not
// changed by attaching it.
func TestAttachLeavesTheSourceAlone(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	path := writeSource(t, src, "notes.md", "original content")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	m = runSlash(m, "/attach "+path)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Error("attaching changed the source file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original content" {
		t.Errorf("the source was modified: %q", got)
	}
}

// TestAttachmentsListsThePendingFiles checks the listing command.
func TestAttachmentsListsThePendingFiles(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	m = runSlash(m, "/attachments")
	if !strings.Contains(m.notice, "No files") {
		t.Errorf("notice = %q", m.notice)
	}

	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)
	m = runSlash(m, "/attachments")
	if !strings.Contains(m.notice, "notes.md") {
		t.Errorf("the listing does not name the file: %q", m.notice)
	}
	if !strings.Contains(m.notice, storeRootOf(t, m)) {
		t.Errorf("the listing does not name the upload directory: %q", m.notice)
	}
}

// storeRootOf returns the upload directory the model was given.
func storeRootOf(t *testing.T, m Model) string {
	t.Helper()
	if m.attachStore == nil {
		t.Fatal("the model has no upload store")
	}
	return m.attachStore.DisplayPath()
}

// TestDetachRemovesThePendingFile checks that a file can be taken back
// before the message is sent.
func TestDetachRemovesThePendingFile(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)
	if len(m.pendingFiles) != 1 {
		t.Fatalf("pending files = %d, want 1", len(m.pendingFiles))
	}
	id := m.pendingFiles[0].ID

	m = runSlash(m, "/detach "+id)
	if len(m.pendingFiles) != 0 {
		t.Errorf("pending files = %d, want 0", len(m.pendingFiles))
	}
	if !strings.Contains(m.notice, "stays") {
		t.Errorf("notice = %q", m.notice)
	}
}

// TestDetachKeepsTheStoredCopy checks that detaching a file does not
// destroy the copy: the operator's file is not HAL's to delete.
func TestDetachKeepsTheStoredCopy(t *testing.T) {
	m, store, src := newAttachmentModel(t)
	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)
	stored := filepath.Join(store.Root(), filepath.FromSlash(m.pendingFiles[0].Path))

	m = runSlash(m, "/detach "+m.pendingFiles[0].ID)
	if _, err := os.Stat(stored); err != nil {
		t.Errorf("the stored copy was removed: %v", err)
	}
}

// TestDetachAnUnknownFile checks that a bad identifier is reported.
func TestDetachAnUnknownFile(t *testing.T) {
	m, _, _ := newAttachmentModel(t)
	m = runSlash(m, "/detach nosuchid")
	if !strings.Contains(m.notice, "No pending attachment") {
		t.Errorf("notice = %q", m.notice)
	}
}

// TestThePendingRowSitsAboveTheComposer checks the placement of the row
// that names the pending files.
func TestThePendingRowSitsAboveTheComposer(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)

	rows := m.layoutRows()
	plain := make([]string, 0, len(rows))
	for _, r := range rows {
		plain = append(plain, stripANSI(r))
	}
	nameAt, ruleAt := -1, -1
	for i, r := range plain {
		if nameAt < 0 && strings.Contains(r, "notes.md") {
			nameAt = i
		}
		if ruleAt < 0 && strings.Contains(r, "─") && strings.Contains(r, "─") {
			ruleAt = i
		}
	}
	if nameAt < 0 {
		t.Fatalf("the pending row is missing:\n%s", strings.Join(plain, "\n"))
	}
	if ruleAt < 0 || nameAt > ruleAt {
		t.Errorf("the pending row is not above the composer rule:\n%s", strings.Join(plain, "\n"))
	}
}

// TestWithoutAttachmentsTheFrameIsUnchanged checks that a conversation
// without files keeps exactly the frame it had.
func TestWithoutAttachmentsTheFrameIsUnchanged(t *testing.T) {
	plain := testModel()
	rows := plain.layoutRows()
	before := make([]string, 0, len(rows))
	for _, r := range rows {
		before = append(before, stripANSI(r))
	}
	if strings.Contains(strings.Join(before, "\n"), "📎") {
		t.Errorf("the empty conversation shows an attachment row:\n%s", strings.Join(before, "\n"))
	}
}

// TestTheShortTerminalKeepsTheAttachmentRow checks the frame budget at
// 80x9: the pending row is the first thing to go, and the composer text
// is never pushed out of view for it.
func TestTheShortTerminalKeepsTheAttachmentRow(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 9})
	m = updated.(Model)
	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)

	if !m.frame.files {
		t.Errorf("the frame dropped the attachment row: %+v", m.frame)
	}
	rows := m.layoutRows()
	if len(rows) != 9 {
		t.Errorf("the frame has %d rows, want 9", len(rows))
	}
	for _, r := range rows {
		if got := lipglossWidth(r); got > 80 {
			t.Errorf("row is %d cells wide, terminal is 80: %q", got, stripANSI(r))
		}
	}
}

// lipglossWidth is the display width of a row.
func lipglossWidth(s string) int {
	return len([]rune(stripANSI(s)))
}

// TestSendingCarriesTheAttachments checks that the files reach the
// engine with the message.
func TestSendingCarriesTheAttachments(t *testing.T) {
	store, err := attachments.Open(filepath.Join(t.TempDir(), "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	eng := &attachmentEngine{reply: "ok"}
	m := New(eng)
	m.SetColor(false)
	m.SetAttachments(store)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	src := t.TempDir()
	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)
	m = typeText(m, "please read this")
	m = sendCurrent(m)

	if len(eng.atts) != 1 {
		t.Fatalf("the engine received %d turns with attachments, want 1", len(eng.atts))
	}
	if len(eng.atts[0]) != 1 {
		t.Fatalf("the engine received %d attachments, want 1", len(eng.atts[0]))
	}
	if eng.atts[0][0].Name != "notes.md" {
		t.Errorf("the engine received %q", eng.atts[0][0].Name)
	}
	// The pending list is empty again: the files belong to the message.
	if len(m.pendingFiles) != 0 {
		t.Errorf("pending files = %d, want 0", len(m.pendingFiles))
	}
}

// TestTheTranscriptShowsTheAttachedFiles checks that a sent message
// keeps a visible reference to its files.
func TestTheTranscriptShowsTheAttachedFiles(t *testing.T) {
	store, err := attachments.Open(filepath.Join(t.TempDir(), "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	eng := &attachmentEngine{reply: "ok"}
	m := New(eng)
	m.SetColor(false)
	m.SetAttachments(store)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	src := t.TempDir()
	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)
	m = typeText(m, "please read this")
	m = sendCurrent(m)

	view := stripANSI(string(m.View().Content))
	if !strings.Contains(view, "notes.md") {
		t.Errorf("the transcript does not show the attached file:\n%s", view)
	}
}

// TestTheRestoredConversationShowsTheFiles checks that a resumed
// conversation still names the files that were discussed.
func TestTheRestoredConversationShowsTheFiles(t *testing.T) {
	m := testModel()
	m.SetHistory([]SessionLine{{
		Role: "user",
		Text: "please read this",
		Files: []attachments.Meta{{
			ID:    "abc123",
			Name:  "notes.md",
			Path:  "abc123/notes.md",
			Bytes: 12,
		}},
	}})
	view := stripANSI(string(m.View().Content))
	if !strings.Contains(view, "notes.md") {
		t.Errorf("the restored conversation does not show the file:\n%s", view)
	}
}

// TestTheCommandsNeverReachTheModel checks that a slash command is
// handled locally.
func TestTheCommandsNeverReachTheModel(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)
	if len(m.Lines()) != 0 {
		t.Errorf("the command reached the conversation: %v", m.Lines())
	}
	if m.Composer().Value() != "" {
		t.Errorf("the draft was not cleared: %q", m.Composer().Value())
	}
}

// TestAMaliciousNameNeverBreaksTheFrame checks that a name carrying a
// newline or a terminal escape cannot split a row of the frame or
// inject a sequence into the terminal.
func TestAMaliciousNameNeverBreaksTheFrame(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	path := writeSource(t, src, "evil\nname\x1b[31m.txt", "content")
	// The command line cannot carry a newline, so the file is attached
	// the way the store would: the name reaches the interface either
	// way.
	m.attachFile(path)
	if len(m.pendingFiles) != 1 {
		t.Fatalf("pending files = %d, want 1", len(m.pendingFiles))
	}
	if strings.ContainsAny(m.pendingFiles[0].Name, "\n\r\x1b") {
		t.Errorf("the pending name still carries a control character: %q", m.pendingFiles[0].Name)
	}
	for _, row := range m.layoutRows() {
		if strings.Contains(row, "\n") {
			t.Errorf("a row carries a newline: %q", row)
		}
		if strings.Contains(row, "\x1b[31m") {
			t.Errorf("a row carries an injected sequence: %q", stripANSI(row))
		}
	}
}

// TestARestoredNameNeverBreaksTheFrame checks the same for a name that
// comes back from a session record, where it was written by hand.
func TestARestoredNameNeverBreaksTheFrame(t *testing.T) {
	m := testModel()
	m.SetHistory([]SessionLine{{
		Role: "user",
		Text: "please read this",
		Files: []attachments.Meta{{
			ID:    "abc123",
			Name:  "evil\nname\x1b[31m.txt",
			Path:  "abc123/evil.txt",
			Bytes: 12,
		}},
	}})
	for _, row := range m.layoutRows() {
		if strings.Contains(row, "\n") {
			t.Errorf("a row carries a newline: %q", row)
		}
		if strings.Contains(row, "\x1b[31m") {
			t.Errorf("a row carries an injected sequence: %q", stripANSI(row))
		}
	}
}

// TestTheAttachmentRowIsInterfaceNotText checks that the row naming the
// pending files is marked as decoration, so a selection never copies it.
func TestTheAttachmentRowIsInterfaceNotText(t *testing.T) {
	m, _, src := newAttachmentModel(t)
	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)
	m = typeText(m, "a message")
	m = sendCurrent(m)

	for i := range m.entries {
		e := m.entries[i]
		if e.role != "user" {
			continue
		}
		for row := range e.rows {
			if strings.Contains(stripANSI(e.rows[row]), "notes.md") && !e.isDecoration(row) {
				t.Errorf("the file reference row is not marked as decoration: %q", stripANSI(e.rows[row]))
			}
		}
	}
}

// TestAnEngineWithoutAttachmentsRefusesTheTurn checks that a turn with
// files is never sent without them.
func TestAnEngineWithoutAttachmentsRefusesTheTurn(t *testing.T) {
	store, err := attachments.Open(filepath.Join(t.TempDir(), "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(&scriptEngine{replies: map[string]string{}})
	m.SetColor(false)
	m.SetAttachments(store)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	src := t.TempDir()
	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)
	m = typeText(m, "please read this")
	m = sendCurrent(m)

	view := stripANSI(string(m.View().Content))
	if !strings.Contains(view, "cannot send attached files") {
		t.Errorf("the refusal is not visible:\n%s", view)
	}
}

// TestTheProviderIsNeverGivenTheSourcePath checks that the operator's
// path stays on their machine.
func TestTheProviderIsNeverGivenTheSourcePath(t *testing.T) {
	store, err := attachments.Open(filepath.Join(t.TempDir(), "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	eng := &attachmentEngine{reply: "ok"}
	m := New(eng)
	m.SetColor(false)
	m.SetAttachments(store)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	src := t.TempDir()
	path := writeSource(t, src, "notes.md", "content")
	m = runSlash(m, "/attach "+path)
	m = typeText(m, "please read this")
	m = sendCurrent(m)

	for _, atts := range eng.atts {
		for _, a := range atts {
			if strings.Contains(a.Name, src) {
				t.Errorf("the source path reached the engine: %q", a.Name)
			}
		}
	}
	_ = provider.Model{}
}
