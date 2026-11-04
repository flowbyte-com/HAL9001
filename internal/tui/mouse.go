package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The pointer drives two things: the wheel scrolls, and a drag selects.
//
// Selection is application-managed, the way Pi does it: the terminal
// reports cells, HAL maps those cells back to the text it rendered, and
// the release copies the selection through OSC 52. The terminal's own
// selection is replaced while mouse reporting is on, so HAL draws the
// selection itself.
//
// A drag that reaches the top or the bottom of the conversation scrolls
// the history on its own, so a selection can reach text that was never
// on screen. The scroll is driven by the event loop: a tick moves the
// conversation by a row and asks for the next tick. Nothing runs in the
// background, and the chain ends with the selection.

// The auto-scroll of a drag at the edge of the conversation.
const (
	// selectionEdgeRows is how many rows at the top and at the bottom
	// of the conversation start the auto-scroll.
	selectionEdgeRows = 2
	// selectionScrollInterval is the delay between two auto-scroll
	// steps. It is slow enough to select a word at a time.
	selectionScrollInterval = 80 * time.Millisecond
	// selectionScrollRows is how many rows one auto-scroll step moves.
	selectionScrollRows = 1
)

// selectionScrollMsg is one tick of the auto-scroll.
//
// gen identifies the chain the tick belongs to. A tick of a chain that
// has already been replaced is ignored, so a scroll that has ended can
// never be revived by a late tick.
type selectionScrollMsg struct{ gen int }

// selectionScrollCmd schedules the next auto-scroll step.
func selectionScrollCmd(gen int) tea.Cmd {
	return tea.Tick(selectionScrollInterval, func(time.Time) tea.Msg {
		return selectionScrollMsg{gen: gen}
	})
}

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
	m.selPointerX, m.selPointerY, m.selPointerValid = msg.X, msg.Y, true
	m.stopSelectionScroll()
	return m, nil
}

// mouseDrag extends the selection while the pointer travels.
func (m Model) mouseDrag(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	if !m.selecting {
		return m, nil
	}
	m.selPointerX, m.selPointerY, m.selPointerValid = msg.X, msg.Y, true
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
	return m, m.updateSelectionScroll()
}

// mouseRelease ends the selection and copies what it covered.
func (m Model) mouseRelease(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	if !m.selecting {
		return m, nil
	}
	m.selecting = false
	m.stopSelectionScroll()
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
	m.selPointerValid = false
	m.stopSelectionScroll()
}

// updateSelectionScroll starts, stops or keeps the auto-scroll of a
// drag that rests at the edge of the conversation.
//
// A chain is started only when the direction changes, so one drag never
// runs two chains at once: the tick of the older chain is recognised by
// its generation and ignored.
func (m *Model) updateSelectionScroll() tea.Cmd {
	dir := m.selectionScrollDirection()
	if dir == m.selScrollDir {
		return nil
	}
	m.selScrollDir = dir
	m.selScrollGen++
	if dir == 0 {
		// The pointer left the edge. The tick that is on its way finds
		// no direction and the chain ends.
		return nil
	}
	return selectionScrollCmd(m.selScrollGen)
}

// stopSelectionScroll ends the auto-scroll. The generation is bumped so
// that a tick which is already on its way is ignored instead of
// reviving the scroll.
func (m *Model) stopSelectionScroll() {
	m.selScrollDir = 0
	m.selScrollGen++
}

// selectionScrollDirection reports the auto-scroll direction for the
// pointer: -1 when it rests at the top of the conversation, 1 at the
// bottom, 0 anywhere else.
//
// The auto-scroll only runs for a drag that has actually moved: an
// ordinary click at the edge of the viewport must copy nothing.
func (m Model) selectionScrollDirection() int {
	if !m.selecting || !m.selPointerValid || !m.selMoved {
		return 0
	}
	top := m.viewportTop()
	height := m.frame.viewport
	if height < 1 {
		return 0
	}
	y := m.selPointerY
	if y < top || y >= top+height {
		// The pointer is outside the conversation.
		return 0
	}
	edge := min(selectionEdgeRows, height/2)
	if edge < 1 {
		return 0
	}
	if y < top+edge {
		return -1
	}
	if y >= top+height-edge {
		return 1
	}
	return 0
}

// selectionScrollTick moves the conversation while the pointer rests at
// the edge of the viewport, and asks for the next tick.
//
// The chain ends as soon as the selection ends, the pointer leaves the
// edge or the history has reached its end.
func (m Model) selectionScrollTick(msg selectionScrollMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.selScrollGen {
		// A chain that has already been replaced.
		return m, nil
	}
	if !m.selecting || m.selScrollDir == 0 {
		return m, nil
	}
	if !m.scrollConversation(m.selScrollDir) {
		// The history has no more rows in that direction.
		m.stopSelectionScroll()
		return m, nil
	}
	// The conversation moved under the pointer, so the selection grows
	// with it.
	m.extendSelectionToPointer()
	return m, selectionScrollCmd(m.selScrollGen)
}

// scrollConversation moves the conversation by one auto-scroll step and
// reports whether it moved.
func (m *Model) scrollConversation(dir int) bool {
	before := m.viewport.YOffset()
	if dir < 0 {
		m.viewport.ScrollUp(selectionScrollRows)
	} else {
		m.viewport.ScrollDown(selectionScrollRows)
	}
	return m.viewport.YOffset() != before
}

// extendSelectionToPointer moves the focus of the selection to the text
// under the pointer. The pointer itself has not moved, but the
// conversation has, so the selection follows the content.
func (m *Model) extendSelectionToPointer() {
	if !m.selPointerValid {
		return
	}
	pos, ok := m.contentCell(m.selPointerX, m.selPointerY)
	if !ok {
		return
	}
	if pos != m.selFocus {
		m.selMoved = true
		m.selFocus = pos
	}
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
// header.
func (m Model) viewportTop() int {
	return 1 // the header
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
		idx, inRow, ok := m.contentRowAt(m.viewport.YOffset() + vy)
		if !ok {
			continue
		}
		if idx < a.row || idx > b.row {
			continue
		}
		row := m.conv.rows[idx]
		from := inRow * w
		to := from + w
		if idx == a.row && from < a.cell {
			from = a.cell
		}
		if idx == b.row && to > b.cell {
			to = b.cell
		}
		// The selection covers the row's text, not the decoration
		// around it: the label, the indent and the renderer's margin
		// are interface, and the padding behind the text is not part
		// of it.
		if from < row.cellStart {
			from = row.cellStart
		}
		if to > row.cellEnd {
			to = row.cellEnd
		}
		if to <= from {
			continue
		}
		out[i] = highlightCells(out[i], from-inRow*w, to-inRow*w)
	}
	return out
}
