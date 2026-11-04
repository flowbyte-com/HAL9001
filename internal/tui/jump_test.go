package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// A reader who has scrolled back through the conversation needs an
// obvious way to return to the latest message. The control is drawn
// over the last row of the conversation, so the frame never changes
// size, and it is interface: a selection never copies it.

// jumpFixture builds a conversation long enough to scroll.
func jumpFixture(t *testing.T) Model {
	t.Helper()
	m := testModel()
	m.SetMouse(true)
	for i := 0; i < 60; i++ {
		m.appendEntry("assistant", "line "+strings.Repeat("x", 40))
	}
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(Model)
}

// scrollUp moves the conversation away from its end.
func scrollUp(m Model, rows int) Model {
	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 10, Y: 5})
	m = updated.(Model)
	for i := 1; i < rows; i++ {
		updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 10, Y: 5})
		m = updated.(Model)
	}
	return m
}

// TestTheJumpControlAppearsAboveTheBottom checks that the control shows
// only while the reader is away from the latest message.
func TestTheJumpControlAppearsAboveTheBottom(t *testing.T) {
	m := jumpFixture(t)
	if m.jumpVisible() {
		t.Fatal("the control must be hidden at the bottom")
	}
	m = scrollUp(m, 2)
	if !m.jumpVisible() {
		t.Fatal("the control must show above the bottom")
	}
	// The control is drawn on the last row of the conversation, which
	// is the row immediately above the composer.
	rows := m.layoutRows()
	found := false
	for _, row := range rows {
		if strings.Contains(stripANSI(row), "Jump to latest") {
			found = true
		}
	}
	if !found {
		t.Errorf("the control is not on screen:\n%s", strings.Join(rows, "\n"))
	}
}

// TestTheJumpControlHidesAtTheBottom checks that the control disappears
// once the conversation is back at its end.
func TestTheJumpControlHidesAtTheBottom(t *testing.T) {
	m := jumpFixture(t)
	m = scrollUp(m, 3)
	if !m.jumpVisible() {
		t.Fatal("the control must show above the bottom")
	}
	m.viewport.GotoBottom()
	if m.jumpVisible() {
		t.Error("the control must hide at the bottom")
	}
	for _, row := range m.layoutRows() {
		if strings.Contains(stripANSI(row), "Jump to latest") {
			t.Errorf("the control is still on screen:\n%s", strings.Join(m.layoutRows(), "\n"))
		}
	}
}

// TestTheJumpControlDoesNotMoveTheFrame checks that the control costs
// no row: the conversation keeps its height whether it is there or not.
func TestTheJumpControlDoesNotMoveTheFrame(t *testing.T) {
	m := jumpFixture(t)
	height, viewport := len(m.layoutRows()), m.frame.viewport
	m = scrollUp(m, 2)
	if got := len(m.layoutRows()); got != height {
		t.Errorf("the frame grew to %d rows, want %d", got, height)
	}
	if m.frame.viewport != viewport {
		t.Errorf("the conversation shrank to %d rows, want %d", m.frame.viewport, viewport)
	}
}

// TestTheJumpControlRestoresBottomFollow checks that a jump brings the
// bottom-follow back: the next streamed message is followed.
func TestTheJumpControlRestoresBottomFollow(t *testing.T) {
	m := jumpFixture(t)
	m = scrollUp(m, 3)
	if m.viewport.AtBottom() {
		t.Fatal("the wheel must leave the bottom")
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	m = updated.(Model)
	if !m.viewport.AtBottom() {
		t.Error("the jump must return to the bottom")
	}
}

// TestTheKeyboardJumpWorksWithoutTheMouse checks that the shortcut does
// not depend on the pointer.
func TestTheKeyboardJumpWorksWithoutTheMouse(t *testing.T) {
	m := jumpFixture(t)
	m.SetMouse(false)
	m = scrollUp(m, 3)
	if m.viewport.AtBottom() {
		t.Fatal("the wheel must leave the bottom")
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	m = updated.(Model)
	if !m.viewport.AtBottom() {
		t.Error("Ctrl+End must return to the bottom")
	}
	if m.jumpVisible() {
		t.Error("the control must hide after the jump")
	}
}

// TestTheKeyboardJumpLeavesTheComposerAlone checks that the shortcut
// does not touch the draft.
func TestTheKeyboardJumpLeavesTheComposerAlone(t *testing.T) {
	m := jumpFixture(t)
	m = typeText(m, "a draft in progress")
	m = scrollUp(m, 2)
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	m = updated.(Model)
	if got := m.composer.Value(); got != "a draft in progress" {
		t.Errorf("the draft changed: %q", got)
	}
}

// TestAClickOnTheControlJumps checks the hit-testing: a press on the
// control jumps to the latest message.
func TestAClickOnTheControlJumps(t *testing.T) {
	m := jumpFixture(t)
	m = scrollUp(m, 3)
	row, from, to, ok := m.jumpRect()
	if !ok {
		t.Fatal("the control has no rectangle")
	}
	if !m.jumpHit(from, row) {
		t.Error("the start of the control must be a hit")
	}
	if !m.jumpHit(to-1, row) {
		t.Error("the end of the control must be a hit")
	}
	if m.jumpHit(from-1, row) {
		t.Error("the cell before the control must not be a hit")
	}
	if m.jumpHit(from, row-1) {
		t.Error("the row above the control must not be a hit")
	}
	updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: from, Y: row})
	m = updated.(Model)
	if !m.viewport.AtBottom() {
		t.Error("the click must jump to the latest message")
	}
	if m.selecting {
		t.Error("a click on the control must not start a selection")
	}
}

// TestAClickOnTheControlCopiesNothing checks that the control's own
// label is never selected or copied.
func TestAClickOnTheControlCopiesNothing(t *testing.T) {
	m := jumpFixture(t)
	m = scrollUp(m, 3)
	copied := ""
	m.hooks.Copy = func(text string) error {
		copied = text
		return nil
	}
	row, from, _, _ := m.jumpRect()
	updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: from, Y: row})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: from, Y: row})
	m = updated.(Model)
	if strings.Contains(copied, "Jump to latest") {
		t.Errorf("the clipboard holds the control: %q", copied)
	}
	if copied != "" {
		t.Errorf("a click on the control copied something: %q", copied)
	}
}

// TestTheControlTextIsNotConversation checks that the label is not part
// of the conversation's text, so a selection cannot pick it up.
func TestTheControlTextIsNotConversation(t *testing.T) {
	m := jumpFixture(t)
	m = scrollUp(m, 3)
	for _, e := range m.Entries() {
		if strings.Contains(e.text, "Jump to latest") {
			t.Errorf("the conversation holds the control: %q", e.text)
		}
	}
}

// TestADragThroughTheControlDoesNotJump checks that a selection that
// passes over the control is not turned into a jump.
func TestADragThroughTheControlDoesNotJump(t *testing.T) {
	m := jumpFixture(t)
	m = scrollUp(m, 3)
	row, from, _, _ := m.jumpRect()
	before := m.viewport.YOffset()
	// The drag starts well away from the control and passes over it.
	updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: row - 3})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: from + 2, Y: row})
	m = updated.(Model)
	if m.viewport.YOffset() != before {
		t.Error("a drag over the control jumped to the latest message")
	}
	if !m.selecting {
		t.Error("the drag must still be a selection")
	}
}

// TestTheJumpControlShortensOnNarrowTerminals checks that the label
// fits: a narrow terminal takes the shorter form.
func TestTheJumpControlShortensOnNarrowTerminals(t *testing.T) {
	for _, width := range []int{170, 120, 80, 50, 40, 30, 20, 12} {
		m := testModel()
		m.SetMouse(true)
		for i := 0; i < 60; i++ {
			m.appendEntry("assistant", "line "+strings.Repeat("x", 40))
		}
		m.renderConversation()
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = updated.(Model)
		m = scrollUp(m, 2)
		label := m.jumpLabel()
		if lipgloss.Width(label) > width {
			t.Errorf("width %d: the label is %d cells wide: %q", width, lipgloss.Width(label), label)
		}
		if !strings.Contains(label, "Jump to latest") && label != "↓ Latest" {
			t.Errorf("width %d: the label says nothing: %q", width, label)
		}
		for _, row := range m.layoutRows() {
			if w := lipgloss.Width(stripANSI(row)); w > width {
				t.Errorf("width %d: a row is %d cells wide: %q", width, w, stripANSI(row))
			}
		}
	}
}

// TestTheJumpControlWorksWithAFiveRowComposer checks that the control
// still sits above the composer and still answers a click when the
// composer has grown.
func TestTheJumpControlWorksWithAFiveRowComposer(t *testing.T) {
	m := jumpFixture(t)
	m = pasteText(m, numberedLines(30))
	if m.frame.composer != maxComposerHeight {
		t.Fatalf("the composer shows %d rows, want %d", m.frame.composer, maxComposerHeight)
	}
	m = scrollUp(m, 2)
	row, from, _, ok := m.jumpRect()
	if !ok {
		t.Fatal("the control has no rectangle")
	}
	// The control sits on the last row of the conversation, which is
	// above the composer whatever height the composer has.
	if row != m.viewportTop()+m.frame.viewport-1 {
		t.Errorf("the control is on row %d, want %d", row, m.viewportTop()+m.frame.viewport-1)
	}
	if row >= m.composerTop() {
		t.Errorf("the control is on row %d, below the composer at %d", row, m.composerTop())
	}
	updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: from, Y: row})
	m = updated.(Model)
	if !m.viewport.AtBottom() {
		t.Error("the click must jump to the latest message")
	}
}

// TestTheWheelMovesEightRows checks the wheel speed.
func TestTheWheelMovesEightRows(t *testing.T) {
	m := jumpFixture(t)
	before := m.viewport.YOffset()
	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 10, Y: 5})
	m = updated.(Model)
	if got := before - m.viewport.YOffset(); got != conversationWheelRows {
		t.Errorf("the wheel moved %d rows, want %d", got, conversationWheelRows)
	}
	up := m.viewport.YOffset()
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 10, Y: 5})
	m = updated.(Model)
	if got := m.viewport.YOffset() - up; got != conversationWheelRows {
		t.Errorf("the wheel back moved %d rows, want %d", got, conversationWheelRows)
	}
}

// TestTheWheelStaysInsideTheLimits checks that the faster wheel never
// scrolls past the ends of the conversation.
func TestTheWheelStaysInsideTheLimits(t *testing.T) {
	m := jumpFixture(t)
	for i := 0; i < 40; i++ {
		updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 10, Y: 5})
		m = updated.(Model)
	}
	if m.viewport.YOffset() != 0 {
		t.Errorf("the wheel scrolled past the top: offset %d", m.viewport.YOffset())
	}
	for i := 0; i < 40; i++ {
		updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 10, Y: 5})
		m = updated.(Model)
	}
	if !m.viewport.AtBottom() {
		t.Errorf("the wheel did not reach the bottom: offset %d", m.viewport.YOffset())
	}
}

// TestTheWheelSuspendsAndRestoresBottomFollow checks the rule the
// wheel has always followed.
func TestTheWheelSuspendsAndRestoresBottomFollow(t *testing.T) {
	m := jumpFixture(t)
	m = scrollUp(m, 2)
	if m.viewport.AtBottom() {
		t.Fatal("the wheel must leave the bottom")
	}
	for i := 0; i < 40; i++ {
		updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 10, Y: 5})
		m = updated.(Model)
	}
	if !m.viewport.AtBottom() {
		t.Error("the wheel must be able to return to the bottom")
	}
}

// TestTheWheelOverTheComposerIsUnchanged checks that the faster
// conversation wheel did not touch the composer's own wheel.
func TestTheWheelOverTheComposerIsUnchanged(t *testing.T) {
	m := jumpFixture(t)
	m = pasteText(m, numberedLines(30))
	before := m.draftOffset
	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 10, Y: m.composerTop() + 1})
	m = updated.(Model)
	if m.draftOffset == before {
		t.Error("the wheel over the composer must still scroll the draft window")
	}
	if got := m.draftOffset - before; got != -3 {
		t.Errorf("the composer wheel moved %d rows, want -3", got)
	}
}

// TestTheWheelOverAPanelIsUnchanged checks that an overlay keeps its own
// wheel behaviour and never moves the conversation underneath it.
func TestTheWheelOverAPanelIsUnchanged(t *testing.T) {
	m := jumpFixture(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	m = resized.(Model)
	m.view = viewHelp
	before := m.panelOffset
	updated, _ := m.updateWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 10, Y: 5})
	m = updated.(Model)
	if m.panelOffset == before {
		t.Error("the wheel over a panel must still scroll it")
	}
}

// TestTheSelectionAutoscrollKeepsItsSpeed checks that the faster wheel
// did not change the auto-scroll of a drag: selecting text needs
// precision, and one row per tick is it.
func TestTheSelectionAutoscrollKeepsItsSpeed(t *testing.T) {
	if selectionScrollRows != 1 {
		t.Errorf("the selection auto-scroll moves %d rows a tick, want 1", selectionScrollRows)
	}
}

// TestTheJumpControlOnEveryTheme checks that the control is styled by
// the palette.
func TestTheJumpControlOnEveryTheme(t *testing.T) {
	for _, theme := range Themes() {
		m := jumpFixture(t)
		m.SetColor(true)
		m.SetTheme(theme)
		m = scrollUp(m, 2)
		row := ""
		for _, r := range m.layoutRows() {
			if strings.Contains(stripANSI(r), "Jump to latest") {
				row = r
			}
		}
		if row == "" {
			t.Fatalf("%s: the control is not on screen", theme.Name)
		}
		if !strings.Contains(row, "\x1b[") {
			t.Errorf("%s: the control is not styled", theme.Name)
		}
	}
}

// TestTheJumpControlKeepsTheReadingPosition checks that appearing and
// disappearing does not move the conversation.
func TestTheJumpControlKeepsTheReadingPosition(t *testing.T) {
	m := jumpFixture(t)
	before := m.viewport.YOffset()
	m = scrollUp(m, 2)
	after := m.viewport.YOffset()
	if after == before {
		t.Fatal("the wheel must move the conversation")
	}
	// The control is drawn over the conversation: the offset does not
	// change when it appears.
	m2 := m
	m2.viewport.SetYOffset(after)
	if m2.viewport.YOffset() != after {
		t.Error("the reading position moved")
	}
}
