package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The pointer drives two things: the wheel scrolls, and a drag selects.
//
// Selection is application-managed, the way Pi does it: the terminal
// reports cells, HAL maps those cells back to the text it rendered, and
// the release copies the selection through OSC 52. The terminal's own
// selection is replaced while mouse reporting is on, so HAL draws the
// selection itself.

// mousePress starts a selection when the pointer goes down over the
// conversation.
func (m Model) mousePress(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.view != viewChat {
		return m, nil
	}
	pos, ok := m.contentCell(msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	m.selecting = true
	m.selMoved = false
	m.selAnchor = pos
	m.selFocus = pos
	return m, nil
}

// mouseDrag extends the selection while the pointer travels.
func (m Model) mouseDrag(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	if !m.selecting {
		return m, nil
	}
	pos, ok := m.contentCell(msg.X, msg.Y)
	if !ok {
		// The pointer left the conversation. The selection keeps the
		// position it had, so dragging back continues it.
		return m, nil
	}
	if pos != m.selFocus {
		m.selMoved = true
		m.selFocus = pos
	}
	return m, nil
}

// mouseRelease ends the selection and copies what it covered.
func (m Model) mouseRelease(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	if !m.selecting {
		return m, nil
	}
	m.selecting = false
	if pos, ok := m.contentCell(msg.X, msg.Y); ok && pos != m.selAnchor {
		m.selMoved = true
		m.selFocus = pos
	}
	if !m.selMoved {
		// An ordinary click selected nothing, so nothing is copied.
		m.clearSelection()
		return m, nil
	}
	text := m.conv.extract(m.selAnchor, m.selFocus)
	m.clearSelection()
	if strings.TrimSpace(text) == "" {
		m.notice = "Nothing selected."
		return m, nil
	}
	return m.copySelection(text)
}

// clearSelection drops the selection state.
func (m *Model) clearSelection() {
	m.selecting = false
	m.selMoved = false
	m.selAnchor = cellPos{}
	m.selFocus = cellPos{}
}

// copySelection copies selected text through the configured clipboard.
// The clipboard is a local feature: the model never sees it.
func (m Model) copySelection(text string) (tea.Model, tea.Cmd) {
	if m.hooks.Copy == nil {
		m.notice = "Clipboard unavailable: no copy handler (use terminal-native copy as a fallback)."
		return m, nil
	}
	copyText := m.hooks.Copy
	if m.sender == nil {
		if err := copyText(text); err != nil {
			m.notice = "Copy failed: " + err.Error()
			return m, nil
		}
		m.notice = "Copied selection."
		return m, nil
	}
	return m, func() tea.Msg {
		go func() {
			if err := copyText(text); err != nil {
				m.sender(copyResultMsg{err: err})
				return
			}
			m.sender(copyResultMsg{chars: len(text), label: "selection"})
		}()
		return nil
	}
}

// viewportTop is the screen row where the conversation starts: below the
// header and the rule.
func (m Model) viewportTop() int {
	top := 1 // the header
	if m.frame.rule {
		top++
	}
	return top
}

// conversationPad is the number of blank rows above a short
// conversation, which sits just above the composer.
func (m Model) conversationPad() int {
	n := m.frame.viewport
	if n <= 0 {
		return 0
	}
	if !m.viewport.AtTop() || !m.viewport.AtBottom() {
		return 0
	}
	pad := n - m.contentRows()
	if pad <= 0 || pad >= n {
		return 0
	}
	return pad
}

// wrappedRows is how many screen rows one conversation row occupies at
// the current width.
func (m Model) wrappedRows(row convRow) int {
	w := m.viewport.Width()
	if w < 1 {
		return 1
	}
	n := (row.width + w - 1) / w
	if n < 1 {
		n = 1
	}
	return n
}

// contentRowAt maps a screen row of the conversation (counting from the
// top of the whole conversation, soft wraps included) to the content row
// it shows and the wrapped row inside it.
func (m Model) contentRowAt(visual int) (int, int, bool) {
	acc := 0
	for r := range m.conv.rows {
		h := m.wrappedRows(m.conv.rows[r])
		if visual < acc+h {
			return r, visual - acc, true
		}
		acc += h
	}
	return 0, 0, false
}

// screenRowOf returns the screen row that shows a content row, when that
// row is on screen.
func (m Model) screenRowOf(contentRow int) (int, bool) {
	if contentRow < 0 || contentRow >= len(m.conv.rows) {
		return 0, false
	}
	visual := 0
	for r := 0; r < contentRow; r++ {
		visual += m.wrappedRows(m.conv.rows[r])
	}
	offset := m.viewport.YOffset()
	if visual < offset {
		return 0, false
	}
	return m.viewportTop() + m.conversationPad() + (visual - offset), true
}

// contentCell maps a screen position to a cell in the conversation.
func (m Model) contentCell(x, y int) (cellPos, bool) {
	top := m.viewportTop()
	if y < top || y >= top+m.frame.viewport {
		return cellPos{}, false
	}
	vy := y - top - m.conversationPad()
	if vy < 0 {
		return cellPos{}, false
	}
	visual := m.viewport.YOffset() + vy
	row, inRow, ok := m.contentRowAt(visual)
	if !ok {
		return cellPos{}, false
	}
	w := m.viewport.Width()
	if w < 1 {
		w = 1
	}
	cell := inRow*w + x
	if cell < 0 {
		cell = 0
	}
	return cellPos{row: row, cell: cell}, true
}

// highlightSelection paints the selected cells on the visible
// conversation rows, so the selection is visible while it is dragged.
func (m Model) highlightSelection(rows []string) []string {
	a, b, ok := m.selectedRange()
	if !ok {
		return rows
	}
	w := m.viewport.Width()
	if w < 1 {
		return rows
	}
	pad := m.conversationPad()
	out := make([]string, len(rows))
	copy(out, rows)
	for i := range rows {
		vy := i - pad
		if vy < 0 {
			continue
		}
		row, inRow, ok := m.contentRowAt(m.viewport.YOffset() + vy)
		if !ok {
			continue
		}
		if row < a.row || row > b.row {
			continue
		}
		from := inRow * w
		to := from + w
		if row == a.row && from < a.cell {
			from = a.cell
		}
		if row == b.row && to > b.cell {
			to = b.cell
		}
		if to <= from {
			continue
		}
		out[i] = highlightCells(out[i], from-inRow*w, to-inRow*w)
	}
	return out
}
