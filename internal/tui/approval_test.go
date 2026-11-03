package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// proposalEngine answers one turn and hands back a prepared proposal, the
// way the real engine does after a model asks to save a file.
type proposalEngine struct {
	reply    string
	proposal *workspace.Proposal
	err      error
}

func (p *proposalEngine) RespondStream(_ context.Context, _ string, _ func(string)) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	return p.reply, nil
}

func (p *proposalEngine) RespondStreamEvents(_ context.Context, _ string, emit func(engine.Event)) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	if p.proposal != nil {
		emit(engine.Event{Proposal: p.proposal})
	}
	return p.reply, nil
}

// writeFixture builds a model with a workspace rooted in a temporary
// directory and a proposal prepared from the given content.
func writeFixture(t *testing.T, path, content string) (Model, *workspace.Store, string, workspace.Proposal) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "HAL")
	store, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Prepare(workspace.Op{
		Path:    path,
		Content: []byte(content),
		Session: "chat",
		Turn:    1,
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	eng := &proposalEngine{reply: "I've prepared the letter.", proposal: &p}
	m := NewWithHooks(eng, Hooks{})
	m.SetColor(false)
	m.SetWorkspace(store)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return updated.(Model), store, root, p
}

// requestApproval runs a turn that produces the proposal and stops while
// the approval view is open.
func requestApproval(t *testing.T, m Model) Model {
	t.Helper()
	m = typeText(m, "save this for me")
	m = enterCurrent(m)
	if m.view != viewApprove {
		t.Fatalf("the approval view did not open (view %d), notice %q", m.view, m.Notice())
	}
	return m
}

// TestDefaultIs444 checks the normal state: read-only, nothing pending.
func TestDefaultIs444(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	if m.WriteState() != writeIdle {
		t.Errorf("write state = %d", m.WriteState())
	}
	if got := footerText(m); !strings.Contains(got, "444 READ ONLY") {
		t.Errorf("footer = %q", got)
	}
	if _, open := m.PendingWrite(); open {
		t.Error("a fresh interface has a pending write")
	}
}

// TestProposalOpensTheReviewWithoutWriting checks that a proposal shows
// the exact operation and does not touch the filesystem.
func TestProposalOpensTheReviewWithoutWriting(t *testing.T) {
	content := "Dear hosting provider,\n\nPlease raise the disk quota.\n"
	m, _, root, p := writeFixture(t, "hosting-letter.md", content)
	m = requestApproval(t, m)

	if m.WriteState() != writeRequested {
		t.Errorf("write state = %d, want requested", m.WriteState())
	}
	pending, open := m.PendingWrite()
	if !open || pending.ID != p.ID || pending.Digest != p.Digest {
		t.Errorf("pending = %#v", pending)
	}
	rows := strings.Join(m.layoutRows(), "\n")
	for _, want := range []string{"666", "hosting-letter.md", "create", "sha256:", "Dear hosting provider", "approve", "reject"} {
		if !strings.Contains(rows, want) {
			t.Errorf("the review is missing %q:\n%s", want, stripANSI(rows))
		}
	}
	if strings.Contains(stripANSI(rows), "444 READ ONLY") {
		t.Error("the footer still claims 444 while a request is pending")
	}
	if !strings.Contains(stripANSI(rows), "666 REQUESTED") {
		t.Error("the footer does not show 666 REQUESTED")
	}
	if _, err := os.Stat(filepath.Join(root, "hosting-letter.md")); !os.IsNotExist(err) {
		t.Error("opening the review wrote the file")
	}
}

// TestApproveWritesExactlyOnce checks the deliberate approval and the
// return to 444.
func TestApproveWritesExactlyOnce(t *testing.T) {
	content := "the exact letter\n"
	m, _, root, _ := writeFixture(t, "hosting-letter.md", content)
	m = requestApproval(t, m)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = updated.(Model)
	if m.WriteState() != writeWriting {
		t.Errorf("write state = %d, want writing", m.WriteState())
	}
	if got := footerText(m); !strings.Contains(got, "666 WRITING") {
		t.Errorf("footer = %q", got)
	}
	// The write runs as a command: nothing exists until it is executed.
	if _, err := os.Stat(filepath.Join(root, "hosting-letter.md")); !os.IsNotExist(err) {
		t.Error("the file exists before the write command ran")
	}
	m = drain(m, cmd)
	if m.WriteState() != writeIdle {
		t.Errorf("write state = %d after the write", m.WriteState())
	}
	got, err := os.ReadFile(filepath.Join(root, "hosting-letter.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != content {
		t.Errorf("content = %q", got)
	}
	if m.view != viewChat {
		t.Errorf("view = %d after approval", m.view)
	}
	if !strings.Contains(m.Notice(), "Saved") || !strings.Contains(m.Notice(), "hosting-letter.md") {
		t.Errorf("notice = %q", m.Notice())
	}
	if got := footerText(m); !strings.Contains(got, "444 READ ONLY") {
		t.Errorf("footer after the write = %q", got)
	}
	if _, open := m.PendingWrite(); open {
		t.Error("the request is still pending")
	}
}

// TestRejectWritesNothing checks the rejection controls.
func TestRejectWritesNothing(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: 'n', Mod: tea.ModCtrl},
		{Code: tea.KeyEscape},
	} {
		m, _, root, _ := writeFixture(t, "letter.md", "hello")
		m = requestApproval(t, m)
		updated, _ := m.Update(key)
		m = updated.(Model)
		if m.WriteState() != writeIdle || m.view != viewChat {
			t.Errorf("%q: state = %d view = %d", key.String(), m.WriteState(), m.view)
		}
		if _, err := os.Stat(filepath.Join(root, "letter.md")); !os.IsNotExist(err) {
			t.Errorf("%q: a rejected write created the file", key.String())
		}
		if !strings.Contains(m.Notice(), "rejected") {
			t.Errorf("%q: notice = %q", key.String(), m.Notice())
		}
	}
}

// TestTypedApprovalDoesNotApprove checks that conversation text can never
// satisfy the gate: the review is a separate view that only its own keys
// decide.
func TestTypedApprovalDoesNotApprove(t *testing.T) {
	m, _, root, _ := writeFixture(t, "letter.md", "hello")
	m = requestApproval(t, m)
	for _, text := range []string{"yes", "approve", "y", "a", "r"} {
		for _, r := range text {
			updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			m = updated.(Model)
		}
	}
	if m.view != viewApprove || m.WriteState() != writeRequested {
		t.Errorf("typing left the review: view %d state %d", m.view, m.WriteState())
	}
	if _, err := os.Stat(filepath.Join(root, "letter.md")); !os.IsNotExist(err) {
		t.Error("typed text wrote the file")
	}
	// A paste in the review is ignored too.
	updated, _ := m.Update(tea.PasteMsg{Content: "yes"})
	m = updated.(Model)
	if m.writeOpen != true || m.WriteState() != writeRequested {
		t.Error("a paste decided the request")
	}
}

// TestReviewScrollsToTheWholeContent checks full-content inspection.
func TestReviewScrollsToTheWholeContent(t *testing.T) {
	var lines []string
	for i := 1; i <= 60; i++ {
		lines = append(lines, fmt.Sprintf("row %02d marker", i))
	}
	m, _, _, _ := writeFixture(t, "long.md", strings.Join(lines, "\n"))
	m = requestApproval(t, m)
	top := stripANSI(strings.Join(m.approvalRows(), "\n"))
	if !strings.Contains(top, "row 01 marker") {
		t.Errorf("the first line is missing:\n%s", top)
	}
	if strings.Contains(top, "row 60 marker") {
		// Fine: a short panel may already show it. The scroll check
		// below is the one that matters.
		t.Log("the panel already shows the end")
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	m = updated.(Model)
	panel := stripANSI(strings.Join(m.panelRows(m.approvalRows(), contentHeight(m.height)), "\n"))
	if !strings.Contains(panel, "row 60 marker") {
		t.Errorf("the end of the content is not reachable:\n%s", panel)
	}
	if m.WriteState() != writeRequested {
		t.Error("scrolling decided the request")
	}
}

// TestSecondProposalIsRefused checks that only one request can be pending.
func TestSecondProposalIsRefused(t *testing.T) {
	m, store, root, _ := writeFixture(t, "first.md", "one")
	m = requestApproval(t, m)
	second, err := store.Prepare(workspace.Op{Path: "second.md", Content: []byte("two")})
	if err != nil {
		t.Fatal(err)
	}
	// The overlay owns the interface, so a second request can only be
	// presented to the model itself: it must be refused, not queued.
	m.requestWrite(second)
	pending, _ := m.PendingWrite()
	if pending.Path != "first.md" {
		t.Errorf("pending = %q, want the first request", pending.Path)
	}
	if !strings.Contains(m.Notice(), "already pending") {
		t.Errorf("notice = %q", m.Notice())
	}
	if _, err := os.Stat(filepath.Join(root, "second.md")); !os.IsNotExist(err) {
		t.Error("the superseded proposal wrote a file")
	}
}

// TestSessionChangeRejectsTheRequest checks the session-switch path.
func TestSessionChangeRejectsTheRequest(t *testing.T) {
	m, _, root, _ := writeFixture(t, "letter.md", "hello")
	m = requestApproval(t, m)
	m.SetHistory([]SessionLine{{Role: "user", Text: "another conversation"}})
	if m.writeOpen || m.WriteState() != writeIdle {
		t.Errorf("the request survived the session change: %v %d", m.writeOpen, m.WriteState())
	}
	if m.view != viewChat {
		t.Errorf("view = %d", m.view)
	}
	if _, err := os.Stat(filepath.Join(root, "letter.md")); !os.IsNotExist(err) {
		t.Error("the session change wrote a file")
	}
}

// TestQuitRejectsTheRequest checks the application-exit path.
func TestQuitRejectsTheRequest(t *testing.T) {
	m, _, root, _ := writeFixture(t, "letter.md", "hello")
	m = requestApproval(t, m)
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.writeOpen {
		t.Fatal("Esc left the request pending")
	}
	// Quitting from the review also rejects.
	m2, _, root2, _ := writeFixture(t, "second.md", "hello")
	m2 = requestApproval(t, m2)
	updated, quit := m2.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m2 = updated.(Model)
	if m2.writeOpen {
		t.Error("quitting left the request pending")
	}
	if quit == nil || cmd == nil {
		t.Error("expected commands from the key presses")
	}
	if _, err := os.Stat(filepath.Join(root, "letter.md")); !os.IsNotExist(err) {
		t.Error("a rejected request wrote a file")
	}
	if _, err := os.Stat(filepath.Join(root2, "second.md")); !os.IsNotExist(err) {
		t.Error("quitting wrote a file")
	}
}

// TestFailedWriteDoesNotClaimSuccess checks the failure path: the notice
// says nothing was saved, and the interface is back at 444.
func TestFailedWriteDoesNotClaimSuccess(t *testing.T) {
	m, store, root, p := writeFixture(t, "letter.md", "hello")
	m = requestApproval(t, m)
	// Move the destination out from under the approval.
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "letter.md"), []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = updated.(Model)
	m = drain(m, cmd)
	if m.WriteState() != writeIdle {
		t.Errorf("state = %d", m.WriteState())
	}
	if strings.Contains(m.Notice(), "Saved") {
		t.Errorf("a failed write claimed success: %q", m.Notice())
	}
	if !strings.Contains(m.Notice(), "failed") {
		t.Errorf("notice = %q", m.Notice())
	}
	got, _ := os.ReadFile(filepath.Join(root, "letter.md"))
	if string(got) != "other" {
		t.Errorf("the racing file was replaced: %q", got)
	}
	// The audit recorded the failure, and the store can still be used.
	if _, err := store.Approve(p); err == nil {
		t.Error("the failed proposal was reused")
	}
}

// TestOverwriteReviewChecksTheReplacementIsShown checks that a proposal
// that would replace a file says so.
func TestOverwriteReviewChecksTheReplacementIsShown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "HAL")
	store, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "draft.md"), []byte("old draft text"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := store.Prepare(workspace.Op{Path: "draft.md", Content: []byte("new")})
	if err != nil {
		t.Fatal(err)
	}
	eng := &proposalEngine{reply: "Prepared.", proposal: &p}
	m := NewWithHooks(eng, Hooks{})
	m.SetColor(false)
	m.SetWorkspace(store)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = requestApproval(t, updated.(Model))
	rows := stripANSI(strings.Join(m.approvalRows(), "\n"))
	if !strings.Contains(rows, "OVERWRITE") {
		t.Errorf("the review does not name the overwrite:\n%s", rows)
	}
	if !strings.Contains(rows, "14 bytes") {
		t.Errorf("the review does not show the existing size:\n%s", rows)
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = updated.(Model)
	m = drain(m, cmd)
	got, err := os.ReadFile(filepath.Join(root, "draft.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q", got)
	}
	if !strings.Contains(m.Notice(), "Replaced") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// TestNoWorkspaceMeansNoApproval checks that an interface without a
// workspace cannot write even if a proposal arrives.
func TestNoWorkspaceMeansNoApproval(t *testing.T) {
	p := workspace.Proposal{ID: "x", Path: "x.md", Content: []byte("x"), Digest: "d"}
	eng := &proposalEngine{reply: "Prepared.", proposal: &p}
	m := NewWithHooks(eng, Hooks{})
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "save it")
	m = enterCurrent(m)
	// Without a store the request opens, but approval cannot write.
	if m.view != viewApprove {
		t.Fatalf("view = %d", m.view)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = updated.(Model)
	if m.WriteState() != writeIdle || !strings.Contains(m.Notice(), "unavailable") {
		t.Errorf("state = %d notice = %q", m.WriteState(), m.Notice())
	}
}

// TestWriteCancelledByStreamError checks that a failed turn leaves no
// review open.
func TestWriteCancelledByStreamError(t *testing.T) {
	m, _, root, _ := writeFixture(t, "letter.md", "hello")
	m.engine = &proposalEngine{err: errors.New("provider failed")}
	m = typeText(m, "save it")
	m = enterCurrent(m)
	if m.view == viewApprove || m.writeOpen {
		t.Error("a failed turn opened the review")
	}
	if m.WriteState() != writeIdle {
		t.Errorf("state = %d", m.WriteState())
	}
	if _, err := os.Stat(filepath.Join(root, "letter.md")); !os.IsNotExist(err) {
		t.Error("a failed turn wrote a file")
	}
}
