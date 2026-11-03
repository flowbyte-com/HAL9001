package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A drag that reaches the edge of the conversation scrolls the history
// on its own, so a selection can reach text that was never on screen.
//
// The scroll is driven by the event loop: a tick moves the conversation
// by a row and asks for the next tick. Nothing runs in the background,
// and the chain ends with the selection.

// scrollFixture builds a conversation of numbered user messages that is
// long enough to scroll.
func scrollFixture(t *testing.T) Model {
	t.Helper()
	m := testModel()
	m.SetMouse(true)
	for i := 0; i < 40; i++ {
		m.appendEntry("user", fmt.Sprintf("row %02d of the answer", i))
	}
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(Model)
}

// pressPointer puts the pointer down at a screen position.
func pressPointer(m Model, x, y int) Model {
	updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	return updated.(Model)
}

// dragPointer moves the pointer to a screen position with the button
// held down.
func dragPointer(m Model, x, y int) Model {
	updated, _ := m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: x, Y: y})
	return updated.(Model)
}

// releasePointer lifts the pointer at a screen position.
func releasePointer(m Model, x, y int) Model {
	updated, _ := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
	return updated.(Model)
}

// scrollTick delivers one tick of the auto-scroll.
func scrollTick(m Model) Model {
	updated, _ := m.Update(selectionScrollMsg{gen: m.selScrollGen})
	return updated.(Model)
}

// rowsText is the text of a range of conversation rows.
func rowsText(m Model, first, last int) string {
	lines := make([]string, 0, last-first+1)
	for i := first; i <= last; i++ {
		lines = append(lines, m.entries[i].text)
	}
	return strings.Join(lines, "\n")
}

// textEnd is the column just after the text of a user row.
const textEnd = entryPrefixCells + len("row 00 of the answer")

// TestSelectionAutoScrollsUp checks that a drag that reaches the top of
// the conversation scrolls the history up, one row per tick, and that
// the selection grows into the text that comes into view.
func TestSelectionAutoScrollsUp(t *testing.T) {
	m := scrollFixture(t)
	before := m.viewport.YOffset()

	press, ok := m.screenRowOf(30)
	if !ok {
		t.Fatal("the row is not on screen")
	}
	// The press is at the end of the text, the drag ends at the top
	// edge of the conversation.
	m = pressPointer(m, textEnd, press)
	m = dragPointer(m, entryPrefixCells, m.viewportTop())
	if m.selScrollDir != -1 {
		t.Fatalf("the drag must start the upward auto-scroll, direction = %d", m.selScrollDir)
	}
	focus := m.selFocus
	for i := 0; i < 3; i++ {
		m = scrollTick(m)
	}
	if got := before - m.viewport.YOffset(); got != 3 {
		t.Errorf("the conversation moved %d rows up, want 3", got)
	}
	if m.selFocus.row >= focus.row {
		t.Errorf("the selection did not grow upwards: row %d -> %d", focus.row, m.selFocus.row)
	}
	if m.selFocus.row != 21 {
		t.Errorf("the focus is on row %d, want 21", m.selFocus.row)
	}
}

// TestSelectionAutoScrollsDown checks the other edge: a drag that
// reaches the bottom of the conversation scrolls the history down.
func TestSelectionAutoScrollsDown(t *testing.T) {
	m := scrollFixture(t)
	m.viewport.GotoTop()
	m.renderConversation()
	before := m.viewport.YOffset()

	press, ok := m.screenRowOf(5)
	if !ok {
		t.Fatal("the row is not on screen")
	}
	bottom := m.viewportTop() + m.frame.viewport - 1
	m = pressPointer(m, entryPrefixCells, press)
	m = dragPointer(m, textEnd, bottom)
	if m.selScrollDir != 1 {
		t.Fatalf("the drag must start the downward auto-scroll, direction = %d", m.selScrollDir)
	}
	focus := m.selFocus
	for i := 0; i < 3; i++ {
		m = scrollTick(m)
	}
	if got := m.viewport.YOffset() - before; got != 3 {
		t.Errorf("the conversation moved %d rows down, want 3", got)
	}
	if m.selFocus.row <= focus.row {
		t.Errorf("the selection did not grow downwards: row %d -> %d", focus.row, m.selFocus.row)
	}
	if m.selFocus.row != 18 {
		t.Errorf("the focus is on row %d, want 18", m.selFocus.row)
	}
}

// TestSelectionAutoScrollCopiesOffScreenText checks that the text the
// auto-scroll brought into view is copied exactly: the rows the
// selection now covers, in order, with no labels and no decoration.
func TestSelectionAutoScrollCopiesOffScreenText(t *testing.T) {
	m := scrollFixture(t)
	var copied string
	m.hooks.Copy = func(s string) error {
		copied = s
		return nil
	}
	press, ok := m.screenRowOf(30)
	if !ok {
		t.Fatal("the row is not on screen")
	}
	m = pressPointer(m, textEnd, press)
	m = dragPointer(m, entryPrefixCells, m.viewportTop())
	for i := 0; i < 3; i++ {
		m = scrollTick(m)
	}
	m = releasePointer(m, entryPrefixCells, m.viewportTop())

	want := rowsText(m, 21, 30)
	if copied != want {
		t.Errorf("copied:\n%q\nwant:\n%q", copied, want)
	}
	if strings.Contains(copied, "You >") || strings.Contains(copied, "HAL >") {
		t.Errorf("the copy holds an interface label: %q", copied)
	}
	if strings.Contains(copied, "\x1b") {
		t.Errorf("the copy holds escape sequences: %q", copied)
	}
	if strings.Contains(copied, "Ctrl+O") {
		t.Errorf("the copy holds the collapse hint: %q", copied)
	}
}

// TestSelectionAutoScrollCopiesOffScreenTextDown is the same check for
// the bottom edge.
func TestSelectionAutoScrollCopiesOffScreenTextDown(t *testing.T) {
	m := scrollFixture(t)
	m.viewport.GotoTop()
	m.renderConversation()
	var copied string
	m.hooks.Copy = func(s string) error {
		copied = s
		return nil
	}
	press, ok := m.screenRowOf(5)
	if !ok {
		t.Fatal("the row is not on screen")
	}
	bottom := m.viewportTop() + m.frame.viewport - 1
	m = pressPointer(m, entryPrefixCells, press)
	m = dragPointer(m, textEnd, bottom)
	for i := 0; i < 3; i++ {
		m = scrollTick(m)
	}
	m = releasePointer(m, textEnd, bottom)

	want := rowsText(m, 5, 18)
	if copied != want {
		t.Errorf("copied:\n%q\nwant:\n%q", copied, want)
	}
}

// TestSelectionAutoScrollStopsOnRelease checks that the scroll ends
// with the selection: a tick that arrives afterwards moves nothing.
func TestSelectionAutoScrollStopsOnRelease(t *testing.T) {
	m := scrollFixture(t)
	press, _ := m.screenRowOf(30)
	m = pressPointer(m, textEnd, press)
	m = dragPointer(m, entryPrefixCells, m.viewportTop())
	m = scrollTick(m)
	offset := m.viewport.YOffset()

	m = releasePointer(m, entryPrefixCells, m.viewportTop())
	if m.selecting {
		t.Error("the release must end the selection")
	}
	m = scrollTick(m)
	if got := m.viewport.YOffset(); got != offset {
		t.Errorf("the conversation moved after the release: %d -> %d", offset, got)
	}
}

// TestSelectionAutoScrollStopsOnCancel checks that Escape cancels the
// selection and its auto-scroll.
func TestSelectionAutoScrollStopsOnCancel(t *testing.T) {
	m := scrollFixture(t)
	press, _ := m.screenRowOf(30)
	m = pressPointer(m, textEnd, press)
	m = dragPointer(m, entryPrefixCells, m.viewportTop())
	m = scrollTick(m)
	offset := m.viewport.YOffset()

	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.selecting {
		t.Error("Escape must cancel the selection")
	}
	m = scrollTick(m)
	if got := m.viewport.YOffset(); got != offset {
		t.Errorf("the conversation moved after the cancel: %d -> %d", offset, got)
	}
}

// TestSelectionAutoScrollStopsOnSessionChange checks that a change of
// conversation ends the scroll with the selection.
func TestSelectionAutoScrollStopsOnSessionChange(t *testing.T) {
	m := scrollFixture(t)
	press, _ := m.screenRowOf(30)
	m = pressPointer(m, textEnd, press)
	m = dragPointer(m, entryPrefixCells, m.viewportTop())
	m = scrollTick(m)

	m.SetHistory([]SessionLine{{Role: "user", Text: "another conversation"}})
	offset := m.viewport.YOffset()
	m = scrollTick(m)
	if got := m.viewport.YOffset(); got != offset {
		t.Errorf("the conversation moved after the session change: %d -> %d", offset, got)
	}
}

// TestSelectionAutoScrollStopsWhenPointerLeavesEdge checks that the
// scroll ends as soon as the pointer leaves the edge.
func TestSelectionAutoScrollStopsWhenPointerLeavesEdge(t *testing.T) {
	m := scrollFixture(t)
	press, _ := m.screenRowOf(30)
	m = pressPointer(m, textEnd, press)
	m = dragPointer(m, entryPrefixCells, m.viewportTop())
	if m.selScrollDir != -1 {
		t.Fatal("the drag must start the upward auto-scroll")
	}
	mid := m.viewportTop() + m.frame.viewport/2
	m = dragPointer(m, entryPrefixCells, mid)
	if m.selScrollDir != 0 {
		t.Fatalf("the scroll must stop when the pointer leaves the edge, direction = %d", m.selScrollDir)
	}
	offset := m.viewport.YOffset()
	m = scrollTick(m)
	if got := m.viewport.YOffset(); got != offset {
		t.Errorf("the conversation moved after the pointer left the edge: %d -> %d", offset, got)
	}
}

// TestSelectionAutoScrollClampsAtTop checks that the scroll stops at
// the beginning of the history.
func TestSelectionAutoScrollClampsAtTop(t *testing.T) {
	m := scrollFixture(t)
	m.viewport.GotoTop()
	m.renderConversation()
	press, _ := m.screenRowOf(5)
	m = pressPointer(m, textEnd, press)
	m = dragPointer(m, entryPrefixCells, m.viewportTop())
	m = scrollTick(m)
	if got := m.viewport.YOffset(); got != 0 {
		t.Errorf("the conversation scrolled past the beginning: offset %d", got)
	}
	if m.selScrollDir != 0 {
		t.Errorf("the scroll must stop at the beginning, direction = %d", m.selScrollDir)
	}
}

// TestSelectionAutoScrollClampsAtBottom checks that the scroll stops at
// the end of the history.
func TestSelectionAutoScrollClampsAtBottom(t *testing.T) {
	m := scrollFixture(t)
	press, _ := m.screenRowOf(30)
	m = pressPointer(m, entryPrefixCells, press)
	bottom := m.viewportTop() + m.frame.viewport - 1
	m = dragPointer(m, textEnd, bottom)
	m = scrollTick(m)
	if !m.viewport.AtBottom() {
		t.Errorf("the conversation scrolled past the end: offset %d", m.viewport.YOffset())
	}
	if m.selScrollDir != 0 {
		t.Errorf("the scroll must stop at the end, direction = %d", m.selScrollDir)
	}
}

// TestSelectionAutoScrollNeedsAMovedDrag checks that an ordinary click
// at the edge of the conversation neither scrolls nor copies: the
// auto-scroll belongs to a drag, not to a press.
func TestSelectionAutoScrollNeedsAMovedDrag(t *testing.T) {
	m := scrollFixture(t)
	copies := 0
	m.hooks.Copy = func(string) error {
		copies++
		return nil
	}
	before := m.viewport.YOffset()
	m = pressPointer(m, entryPrefixCells, m.viewportTop())
	if m.selScrollDir != 0 {
		t.Fatalf("a click must not start the auto-scroll, direction = %d", m.selScrollDir)
	}
	m = scrollTick(m)
	if got := m.viewport.YOffset(); got != before {
		t.Errorf("a click scrolled the conversation: %d -> %d", before, got)
	}
	m = releasePointer(m, entryPrefixCells, m.viewportTop())
	if copies != 0 {
		t.Errorf("a plain click copied %d time(s), want 0", copies)
	}
}

// TestSelectionAutoScrollDoesNotFightTheWheel checks that the wheel
// keeps working during a drag: it takes over from the auto-scroll
// instead of fighting it.
func TestSelectionAutoScrollDoesNotFightTheWheel(t *testing.T) {
	m := scrollFixture(t)
	press, _ := m.screenRowOf(30)
	m = pressPointer(m, textEnd, press)
	m = dragPointer(m, entryPrefixCells, m.viewportTop())
	if m.selScrollDir != -1 {
		t.Fatal("the drag must start the upward auto-scroll")
	}
	before := m.viewport.YOffset()
	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: entryPrefixCells, Y: m.viewportTop()})
	m = updated.(Model)
	if m.selScrollDir != 0 {
		t.Errorf("the wheel must stop the auto-scroll, direction = %d", m.selScrollDir)
	}
	if m.viewport.YOffset() >= before {
		t.Errorf("the wheel must scroll the conversation: %d -> %d", before, m.viewport.YOffset())
	}
	offset := m.viewport.YOffset()
	m = scrollTick(m)
	if got := m.viewport.YOffset(); got != offset {
		t.Errorf("the auto-scroll moved the conversation after the wheel: %d -> %d", offset, got)
	}
}

// TestSelectionAutoScrollSpeed checks the speed of the auto-scroll: one
// row per tick, so a text can be selected precisely.
func TestSelectionAutoScrollSpeed(t *testing.T) {
	if selectionScrollRows != 1 {
		t.Errorf("the auto-scroll moves %d rows per tick, want 1", selectionScrollRows)
	}
	if selectionScrollInterval <= 0 {
		t.Errorf("the auto-scroll interval is %v, want a positive delay", selectionScrollInterval)
	}
	m := scrollFixture(t)
	press, _ := m.screenRowOf(30)
	m = pressPointer(m, textEnd, press)
	m = dragPointer(m, entryPrefixCells, m.viewportTop())
	before := m.viewport.YOffset()
	m = scrollTick(m)
	if got := before - m.viewport.YOffset(); got != selectionScrollRows {
		t.Errorf("one tick moved %d rows, want %d", got, selectionScrollRows)
	}
}
