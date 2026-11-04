package tui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// The jump-to-latest control.
//
// When the reader is scrolling back through the conversation, there has
// to be an obvious way back to the latest message. The control is drawn
// over the last row of the conversation rather than in a row of its
// own: the frame never changes size, so the reading position is exactly
// the same whether the control is there or not, and nothing jumps when
// it appears or disappears.
//
// The control is interface, not conversation. It is not part of the
// conversation's text, so a selection never copies it, and the text it
// covers is still selectable underneath it.

// keyJumpLatest is the chord that returns to the latest message.
const keyJumpLatest = "ctrl+end"

// jumpLabels are the forms of the control, longest first. A narrow
// terminal takes the next one down.
var jumpLabels = []string{
	"↓ Jump to latest message · Ctrl+End",
	"↓ Jump to latest · Ctrl+End",
	"↓ Jump to latest",
	"↓ Latest",
}

// jumpLabel is the control's text for the current width.
func (m Model) jumpLabel() string {
	width := m.viewport.Width()
	if width < 1 {
		width = m.width
	}
	for _, label := range jumpLabels {
		if lipgloss.Width(label) <= width {
			return label
		}
	}
	return jumpLabels[len(jumpLabels)-1]
}

// jumpVisible reports whether the control belongs on screen.
//
// It shows only while the reader is above the latest message, and it
// disappears the moment the conversation is back at its end.
func (m Model) jumpVisible() bool {
	if m.view != viewChat || m.frame.viewport < 1 {
		return false
	}
	return m.rowsAboveBottom() > 0
}

// rowsAboveBottom is how many rows the conversation is scrolled above
// its end.
func (m Model) rowsAboveBottom() int {
	total := m.viewport.TotalLineCount()
	height := m.viewport.Height()
	if total <= height {
		return 0
	}
	max := total - height
	offset := m.viewport.YOffset()
	if offset > max {
		offset = max
	}
	return max - offset
}

// jumpRect is the screen rectangle of the control: the row it is drawn
// on and the columns it covers.
func (m Model) jumpRect() (int, int, int, bool) {
	if !m.jumpVisible() {
		return 0, 0, 0, false
	}
	label := m.jumpLabel()
	width := lipgloss.Width(label)
	area := m.viewport.Width()
	if width <= 0 || width > area {
		return 0, 0, 0, false
	}
	from := (area - width) / 2
	row := m.viewportTop() + m.frame.viewport - 1
	return row, from, from + width, true
}

// jumpHit reports whether a pointer position is on the control.
//
// Only a press that starts on the control counts. A drag that passes
// over it does not: the control is a button, and a selection that
// happens to cross it must not be turned into a jump.
func (m Model) jumpHit(x, y int) bool {
	row, from, to, ok := m.jumpRect()
	if !ok || y != row {
		return false
	}
	return x >= from && x < to
}

// jumpToLatest returns the conversation to its end.
//
// The jump restores the bottom-follow as well: a reader who came back
// to the latest message wants to watch the next one arrive.
func (m *Model) jumpToLatest() {
	m.viewport.GotoBottom()
}

// jumpOverlay draws the control on the last row of the conversation.
//
// The row keeps its own text: the control is written over it, in the
// middle of the row, and the text underneath is still there for the
// selection to find.
func (m Model) jumpOverlay(rows []string) []string {
	if !m.jumpVisible() || len(rows) == 0 {
		return rows
	}
	label := m.jumpLabel()
	width := lipgloss.Width(label)
	area := m.viewport.Width()
	if width <= 0 || width > area {
		return rows
	}
	from := (area - width) / 2
	last := len(rows) - 1
	rows[last] = overlayText(rows[last], from, from+width, m.styledJump(label))
	return rows
}

// styledJump renders the control: the arrow in the accent colour, the
// rest in the muted one. It reads as something that can be pressed
// without competing with the conversation.
func (m Model) styledJump(label string) string {
	if !m.color {
		return label
	}
	st := m.theme.styles()
	arrow, rest := "↓", strings.TrimPrefix(label, "↓")
	return st.active.Render(arrow) + st.footer.Render(rest)
}
