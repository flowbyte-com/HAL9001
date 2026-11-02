package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The composer shows a window on the draft. The wheel moves that window;
// editing pulls it back to the cursor.

// longDraft fills the composer with more lines than the window shows.
func longDraft(m Model, lines int) Model {
	for i := 0; i < lines; i++ {
		m = typeText(m, "draft line\n")
	}
	return m
}

// TestComposerWindowIsCapped checks that the composer never shows more
// than five rows of the draft.
func TestComposerWindowIsCapped(t *testing.T) {
	m := longDraft(testModel(), 20)
	if h := m.ComposerRows(); h != maxComposerHeight {
		t.Errorf("composer rows = %d, want %d", h, maxComposerHeight)
	}
	if maxComposerHeight != 5 {
		t.Errorf("maxComposerHeight = %d, want 5", maxComposerHeight)
	}
}

// TestWheelScrollsComposerDraft checks that the wheel scrolls the draft
// window without touching the text or the cursor.
func TestWheelScrollsComposerDraft(t *testing.T) {
	m := longDraft(testModel(), 20)
	draft := m.Composer().Value()
	line := m.Composer().Line()
	column := m.Composer().Column()
	if m.draftOffset == 0 {
		t.Fatal("precondition: the window starts at the top of the draft")
	}
	// The wheel over the composer scrolls up.
	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 10, Y: m.composerTop() + 1})
	m = updated.(Model)
	if m.draftOffset >= 20 {
		t.Errorf("the wheel did not scroll the draft window: offset %d", m.draftOffset)
	}
	if got := m.Composer().Value(); got != draft {
		t.Error("the wheel changed the draft")
	}
	if m.Composer().Line() != line || m.Composer().Column() != column {
		t.Error("the wheel moved the cursor")
	}
}

// TestWheelOverConversationDoesNotScrollDraft checks that the wheel
// over the conversation leaves the composer alone.
func TestWheelOverConversationDoesNotScrollDraft(t *testing.T) {
	m := longDraft(testModel(), 20)
	offset := m.draftOffset
	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 10, Y: m.viewportTop() + 1})
	m = updated.(Model)
	if m.draftOffset != offset {
		t.Errorf("the conversation wheel moved the composer window: %d -> %d", offset, m.draftOffset)
	}
}

// TestEditingFollowsTheCursor checks that typing pulls the composer
// window back to the caret.
func TestEditingFollowsTheCursor(t *testing.T) {
	m := longDraft(testModel(), 20)
	// Scroll the window away from the cursor.
	m.scrollDraft(-5)
	if m.draftOffset == 0 {
		t.Fatal("precondition: the window moved")
	}
	// Typing is cursor-aware: the caret comes back into view.
	m = typeText(m, "x")
	if m.draftOffset == 0 {
		t.Error("editing did not bring the cursor back into view")
	}
	cur := m.cursorDraftRow()
	if cur < m.draftOffset || cur >= m.draftOffset+m.ComposerRows() {
		t.Errorf("the cursor is outside the window: row %d, window %d..%d", cur, m.draftOffset, m.draftOffset+m.ComposerRows())
	}
}

// TestComposerWindowShowsTheScrolledRows checks that the window really
// shows different rows when it is scrolled.
func TestComposerWindowShowsTheScrolledRows(t *testing.T) {
	m := longDraft(testModel(), 20)
	top := m.composerWindow(m.ComposerRows())
	if !strings.Contains(strings.Join(top, "\n"), "draft line") {
		t.Errorf("the composer does not show the draft: %q", top)
	}
	m.scrollDraft(-5)
	scrolled := m.composerWindow(m.ComposerRows())
	if strings.Join(top, "\n") == strings.Join(scrolled, "\n") {
		t.Error("scrolling the draft did not change the composer")
	}
}

// TestDraftRowsCountsWrappedLines checks that the height of the draft
// counts soft-wrapped rows, not logical lines.
func TestDraftRowsCountsWrappedLines(t *testing.T) {
	m := testModel()
	m = typeText(m, strings.Repeat("word ", 60))
	if rows := m.draftRows(); rows < 2 {
		t.Errorf("a long single line should need several rows, got %d", rows)
	}
	if h := m.ComposerRows(); h != m.draftRows() {
		t.Errorf("composer rows = %d, want %d", h, m.draftRows())
	}
}
