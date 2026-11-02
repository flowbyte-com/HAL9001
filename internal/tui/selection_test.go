package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The selection map has to answer one question exactly: which characters
// of the text the user wrote are on the screen cells they dragged over.

// TestUserRowExcludesLabel checks that the "You > " label is interface
// decoration and never part of a selection.
func TestUserRowExcludesLabel(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("user", "hello world")
	m.renderConversation()
	if len(m.conv.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(m.conv.rows))
	}
	text := m.conv.extract(cellPos{row: 0, cell: 0}, cellPos{row: 0, cell: len(m.conv.rows[0].cells)})
	if text != "hello world" {
		t.Errorf("selection = %q, want %q", text, "hello world")
	}
}

// TestAssistantWrappedLineJoins checks that a wrapped paragraph is
// copied as one line: no artificial line break, and no space invented
// where the renderer broke a word.
func TestAssistantWrappedLineJoins(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	text := "The quick brown fox jumps over the lazy dog and keeps on running until the very end of the sentence."
	m.appendEntry("assistant", text)
	m.renderConversation()
	if len(m.conv.rows) < 2 {
		t.Fatalf("expected the paragraph to wrap, got %d row(s)", len(m.conv.rows))
	}
	// The first row of an assistant turn is the empty "HAL > " line, so
	// the text starts on the second row.
	got := m.conv.extract(cellPos{row: 1, cell: 8}, cellPos{row: len(m.conv.rows) - 1, cell: len(m.conv.rows[len(m.conv.rows)-1].cells)})
	if got != text {
		t.Errorf("selection = %q\nwant       %q", got, text)
	}
	if strings.Contains(got, "\n") {
		t.Error("a wrapped paragraph must not gain a line break")
	}
}

// TestPartialSelection checks that only the dragged cells are copied.
func TestPartialSelection(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("user", "abcdefghij")
	m.renderConversation()
	got := m.conv.extract(cellPos{row: 0, cell: entryPrefixCells + 2}, cellPos{row: 0, cell: entryPrefixCells + 5})
	if got != "cde" {
		t.Errorf("selection = %q, want %q", got, "cde")
	}
}

// TestSelectionAcrossMessages checks that a selection spanning two
// messages keeps the line break between them.
func TestSelectionAcrossMessages(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("user", "first")
	m.appendEntry("assistant", "second")
	m.renderConversation()
	last := m.conv.rows[len(m.conv.rows)-1]
	got := m.conv.extract(cellPos{row: 0, cell: entryPrefixCells}, cellPos{row: len(m.conv.rows) - 1, cell: len(last.cells)})
	if !strings.HasPrefix(got, "first\n") || !strings.HasSuffix(got, "second") {
		t.Errorf("selection = %q, want it to start with %q and end with %q", got, "first\n", "second")
	}
}

// TestLongWordIsNotSplit checks that a word the renderer had to break
// comes back as one word, with no space inserted.
func TestLongWordIsNotSplit(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	word := "supercalifragilisticexpialidocious"
	m.appendEntry("assistant", word)
	m.renderConversation()
	if len(m.conv.rows) < 2 {
		t.Skip("the word fits on one row at this width")
	}
	last := m.conv.rows[len(m.conv.rows)-1]
	got := m.conv.extract(cellPos{row: 1, cell: 8}, cellPos{row: len(m.conv.rows) - 1, cell: len(last.cells)})
	if got != word {
		t.Errorf("selection = %q, want %q", got, word)
	}
}

// TestUnicodeCells checks that a wide character and a combining mark
// are copied whole.
func TestUnicodeCells(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	text := "héllo wörld ✓ 日本語"
	m.appendEntry("user", text)
	m.renderConversation()
	row := m.conv.rows[0]
	got := m.conv.extract(cellPos{row: 0, cell: 0}, cellPos{row: 0, cell: len(row.cells)})
	if got != text {
		t.Errorf("selection = %q, want %q", got, text)
	}
}

// TestDragCopiesText drives the whole pointer path: press, drag,
// release, and checks what reached the clipboard.
func TestDragCopiesText(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	var copied string
	m.hooks.Copy = func(s string) error {
		copied = s
		return nil
	}
	m.appendEntry("user", "select me please")
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	// The conversation sits just above the composer, so the message is
	// on the last row of the conversation area, not the first.
	row, ok := m.screenRowOf(0)
	if !ok {
		t.Fatal("the message is not on screen")
	}
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: entryPrefixCells, Y: row})
	m = updated.(Model)
	if !m.selecting {
		t.Fatal("the press must start a selection")
	}
	updated, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: entryPrefixCells + 6, Y: row})
	m = updated.(Model)
	if !m.selMoved {
		t.Fatal("the drag must move the selection")
	}
	updated, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: entryPrefixCells + 6, Y: row})
	m = updated.(Model)
	if copied != "select" {
		t.Errorf("copied %q, want %q", copied, "select")
	}
	if m.selecting || m.selMoved {
		t.Error("the selection must be cleared after the release")
	}
}

// TestClickWithoutDragCopiesNothing checks that an ordinary click never
// reaches the clipboard.
func TestClickWithoutDragCopiesNothing(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	copies := 0
	m.hooks.Copy = func(s string) error {
		copies++
		return nil
	}
	m.appendEntry("user", "hello")
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	row, ok := m.screenRowOf(0)
	if !ok {
		t.Fatal("the message is not on screen")
	}
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: row})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 2, Y: row})
	m = updated.(Model)
	if copies != 0 {
		t.Errorf("a plain click copied %d time(s), want 0", copies)
	}
}

// TestRightClickDoesNotSelect checks that the secondary button never
// starts a selection.
func TestRightClickDoesNotSelect(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("user", "hello")
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	row, _ := m.screenRowOf(0)
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: 8, Y: row})
	m = updated.(Model)
	if m.selecting {
		t.Error("the right button must not start a selection")
	}
}

// TestSelectionHighlight checks that the selected cells are painted.
func TestSelectionHighlight(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("user", "highlighted")
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.selAnchor = cellPos{row: 0, cell: entryPrefixCells}
	m.selFocus = cellPos{row: 0, cell: entryPrefixCells + 4}
	m.selMoved = true
	rows := m.highlightSelection(m.viewportRows(m.frame.viewport))
	found := false
	for _, row := range rows {
		if strings.Contains(row, "\x1b[7m") {
			found = true
		}
	}
	if !found {
		t.Error("the selection is not highlighted")
	}
}

// TestSelectionSurvivesScrolling checks that a selection keeps its
// anchor when the history is scrolled, so a selection can be made
// across content that was not on screen.
func TestSelectionSurvivesScrolling(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	for i := 0; i < 40; i++ {
		m.appendEntry("assistant", fmt.Sprintf("row %02d of the answer", i))
	}
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	// Read from the top of the history.
	m.viewport.GotoTop()
	row, ok := m.screenRowOf(0)
	if !ok {
		t.Fatal("the conversation is not on screen")
	}
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: row})
	m = updated.(Model)
	anchor := m.selAnchor
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 2, Y: row})
	m = updated.(Model)
	if m.selAnchor != anchor {
		t.Errorf("scrolling moved the anchor: %v -> %v", anchor, m.selAnchor)
	}
	// The drag now reaches a row that is not on screen.
	last := len(m.conv.rows) - 1
	m.selFocus = cellPos{row: last, cell: 20}
	m.selMoved = true
	text := m.conv.extract(m.selAnchor, m.selFocus)
	if !strings.Contains(text, "row 00 of the answer") {
		t.Errorf("the selection does not reach back to the anchor: %q", text[:min(60, len(text))])
	}
	if !strings.Contains(text, "row 39 of th") {
		t.Errorf("the selection does not reach the far end: %q", text[max(0, len(text)-60):])
	}
}

// TestSelectionClearedOnSessionChange checks that a selection does not
// survive a change of conversation.
func TestSelectionClearedOnSessionChange(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("user", "hello")
	m.renderConversation()
	m.selAnchor = cellPos{row: 0, cell: 1}
	m.selFocus = cellPos{row: 0, cell: 5}
	m.selMoved = true
	m.SetHistory([]SessionLine{{Role: "user", Text: "other"}})
	if m.selMoved || m.selAnchor != (cellPos{}) || m.selFocus != (cellPos{}) {
		t.Error("a session change must clear the selection")
	}
}
