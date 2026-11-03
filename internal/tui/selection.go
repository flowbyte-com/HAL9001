package tui

import "strings"

// entryPrefixCells is the width of the "You > " and "HAL > " labels HAL
// puts in front of every conversation row. The labels are interface
// decoration: they are never part of what a selection copies.
const entryPrefixCells = len("You > ")

// decorationRow marks a row that is interface rather than message text:
// a collapse hint, an answer's metadata line. Such a row keeps its
// place in the row index, so the screen and the map still agree about
// which row is which, but it never becomes part of a selection.
const decorationRow = -1

// cellPos is a position in the conversation: a content row and a cell
// index inside that row.
type cellPos struct {
	row  int
	cell int
}

// convRow is one rendered conversation row together with the mapping
// from its display cells back to the text behind them.
type convRow struct {
	cells      []cellSpan
	width      int // the display width of the row, in cells
	textStart  int // byte offset in the row's text where the mapped text starts
	textLen    int // length of the mapped text in bytes
	cellStart  int // the first column of the row's visible text
	cellEnd    int // the column after the row's visible text
	logical    int // index of the logical line the row belongs to
	logicalOff int // byte offset of the mapped text inside that logical line
}

// convMap maps the conversation's display cells to the text behind them.
//
// The conversation is rendered as wrapped rows, but a selection has to
// copy the text as it was written: a wrapped line must not become two
// lines, and a word broken across two rows must not gain a space. The
// map therefore keeps the logical lines of every entry — the unwrapped
// text — and records which part of them each row displays.
type convMap struct {
	rows    []convRow
	logical []string
}

// buildConvMap maps the rendered conversation to the text behind it.
func buildConvMap(entries []entry) convMap {
	cm := convMap{}
	for _, e := range entries {
		cm.addEntry(e)
	}
	return cm
}

// addEntry maps one conversation entry: its rendered rows and the
// logical lines they were wrapped from.
func (cm *convMap) addEntry(e entry) {
	logical := e.logical
	base := len(cm.logical)
	cm.logical = append(cm.logical, logical...)
	li := 0
	searchFrom := 0
	for i, row := range e.rows {
		cells := scanRow(row)
		contentStart := len(cells.text)
		if entryPrefixCells < len(cells.cells) {
			contentStart = cells.cells[entryPrefixCells].start
		}
		if e.isDecoration(i) {
			// A decoration row is on screen but not in the message: it
			// keeps its place in the row index and contributes no text.
			cm.rows = append(cm.rows, convRow{
				cells:     cells.cells,
				width:     rowWidth(cells),
				textStart: contentStart,
				textLen:   0,
				cellStart: 0,
				cellEnd:   0,
				logical:   decorationRow,
			})
			continue
		}
		// mapped builds the row's entry: the cells of the row, the
		// text it shows and the logical line behind it.
		mapped := func(textStart, textLen, logical, logicalOff int) convRow {
			cs, ce := textColumns(cells.cells, cells.text, textStart, textStart+textLen)
			return convRow{
				cells:      cells.cells,
				width:      rowWidth(cells),
				textStart:  textStart,
				textLen:    textLen,
				cellStart:  cs,
				cellEnd:    ce,
				logical:    logical,
				logicalOff: logicalOff,
			}
		}
		content := cells.text[contentStart:]
		trimmed := strings.TrimRight(content, " \t")
		lead := len(content) - len(strings.TrimLeft(content, " \t"))
		trimmedLead := strings.TrimRight(content[lead:], " \t")

		matched := false
		if trimmed == "" {
			// A blank row. It is a line of its own, and it lines up
			// with the blank logical line the renderer produced.
			if idx, ok := findBlankLogical(logical, li); ok {
				cm.rows = append(cm.rows, mapped(contentStart, 0, base+idx, 0))
				li = idx + 1
				searchFrom = 0
			} else {
				cm.logical = append(cm.logical, "")
				cm.rows = append(cm.rows, mapped(contentStart, 0, len(cm.logical)-1, 0))
			}
			continue
		}
		for _, cand := range []struct {
			text  string
			shift int
		}{
			{trimmed, 0},
			{trimmedLead, lead},
		} {
			if cand.text == "" {
				continue
			}
			idx, off, ok := findInLogical(logical, cand.text, li, searchFrom)
			if !ok {
				continue
			}
			cm.rows = append(cm.rows, mapped(
				contentStart+cand.shift, len(cand.text), base+idx, off))
			li = idx
			searchFrom = off + len(cand.text)
			matched = true
			break
		}
		if matched {
			continue
		}
		// The row could not be traced back to a logical line — a
		// rendering HAL does not know about. It still has to be
		// copyable, so it becomes a logical line of its own.
		text := trimmed
		cm.logical = append(cm.logical, text)
		cm.rows = append(cm.rows, mapped(contentStart, len(text), len(cm.logical)-1, 0))
	}
}

// textColumns returns the range of columns that hold the row's visible
// text.
//
// The label in front of a row, the indent and the renderer's margin are
// interface decoration, and the padding behind the text is not part of
// it. A selection covers the text alone, so those columns stay outside
// the highlighted range: highlighting them would paint a pale block in
// the margin and, on a blank line, a block where there is no text at
// all.
func textColumns(cells []cellSpan, text string, lo, hi int) (int, int) {
	start, end := 0, 0
	found := false
	for _, c := range cells {
		if c.end <= lo || c.start >= hi {
			continue
		}
		if strings.TrimSpace(text[c.start:c.end]) == "" {
			continue
		}
		if !found {
			start = c.column
			found = true
		}
		end = c.column + c.width
	}
	return start, end
}

// findBlankLogical finds the next logical line that holds no text.
func findBlankLogical(logical []string, li int) (int, bool) {
	for l := li; l < len(logical); l++ {
		if strings.TrimSpace(logical[l]) == "" {
			return l, true
		}
	}
	return 0, false
}

// findInLogical searches the logical lines for text, starting at the
// given line and offset, and returns where it was found.
func findInLogical(logical []string, text string, li int, from int) (int, int, bool) {
	for l := li; l < len(logical); l++ {
		offsets := []int{0}
		if l == li && from > 0 {
			offsets = []int{from, 0}
		}
		for _, start := range offsets {
			if start > len(logical[l]) {
				continue
			}
			if q := strings.Index(logical[l][start:], text); q >= 0 {
				return l, start + q, true
			}
		}
	}
	return 0, 0, false
}

// rowWidth is the display width of a scanned row, in cells.
func rowWidth(cells rowCells) int {
	w := 0
	for _, c := range cells.cells {
		w += c.width
	}
	return w
}

// byteRange converts a column range of a row into a byte range in the
// row's text, clamped to the text that is actually mapped.
func (row convRow) byteRange(c0, c1 int) (int, int) {
	cells := rowCells{cells: row.cells}
	s, e := cells.byteRangeAt(c0, c1)
	if s < row.textStart {
		s = row.textStart
	}
	if e > row.textStart+row.textLen {
		e = row.textStart + row.textLen
	}
	if e < s {
		e = s
	}
	return s, e
}

// extract returns the text covered by a selection between two content
// positions.
//
// The text is taken from the logical lines, so a wrapped line is copied
// as the one line it was written as: no artificial line break, and no
// space invented where a long word was broken across two rows. Rows of
// different logical lines are separated by a newline, because those
// really are separate lines of text.
func (cm convMap) extract(a, b cellPos) string {
	if b.row < a.row || (b.row == a.row && b.cell < a.cell) {
		a, b = b, a
	}
	var groups []string
	cur := -1
	start, end := 0, 0
	for r := a.row; r <= b.row && r < len(cm.rows); r++ {
		row := cm.rows[r]
		if row.logical == decorationRow {
			// A decoration row ends the group of text before it and
			// contributes no text of its own: a hint or a metadata
			// line is never part of what a selection copies.
			if cur >= 0 {
				groups = append(groups, cm.line(cur, start, end))
				cur = -1
			}
			continue
		}
		// The columns of the selection, in the row's own width.
		c0, c1 := 0, row.width
		if r == a.row {
			c0 = a.cell
		}
		if r == b.row {
			c1 = b.cell
		}
		if c0 < 0 {
			c0 = 0
		}
		if c1 > row.width {
			c1 = row.width
		}
		if c1 < c0 {
			c1 = c0
		}
		s, e := row.byteRange(c0, c1)
		ls := row.logicalOff + (s - row.textStart)
		le := row.logicalOff + (e - row.textStart)
		if row.logical != cur {
			if cur >= 0 {
				groups = append(groups, cm.line(cur, start, end))
			}
			cur = row.logical
			start, end = ls, le
			continue
		}
		if le > end {
			end = le
		}
	}
	if cur >= 0 {
		groups = append(groups, cm.line(cur, start, end))
	}
	return strings.Join(groups, "\n")
}

// line returns a slice of a logical line, clamped to its bounds.
func (cm convMap) line(idx, start, end int) string {
	if idx < 0 || idx >= len(cm.logical) {
		return ""
	}
	line := cm.logical[idx]
	if start < 0 {
		start = 0
	}
	if end > len(line) {
		end = len(line)
	}
	if end <= start {
		return ""
	}
	return line[start:end]
}

// selectedRange returns the normalised start and end of a selection.
func (m Model) selectedRange() (cellPos, cellPos, bool) {
	if !m.selMoved {
		return cellPos{}, cellPos{}, false
	}
	a, b := m.selAnchor, m.selFocus
	if b.row < a.row || (b.row == a.row && b.cell < a.cell) {
		a, b = b, a
	}
	return a, b, true
}
