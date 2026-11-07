package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/clipperhouse/displaywidth"
)

// cellSpan is one grapheme cluster of a rendered row: the bytes it
// occupies in the row's plain text and the terminal columns it covers.
//
// The cell map is what makes mouse selection possible at all: a terminal
// reports cells, not text, so every selection has to be translated back
// into the characters that produced those cells.
type cellSpan struct {
	start  int // byte offset in the plain text
	end    int // byte offset in the plain text
	width  int // display width in columns
	column int // the first terminal column the cluster occupies
}

// overlayText writes a label over the columns [from, to) of a rendered
// row.
//
// The row keeps its own escape sequences: only the printable text
// inside the range is replaced, and the label brings its own styling.
// The label must cover exactly as many columns as the range, so the text
// after it does not move.
func overlayText(row string, from, to int, label string) string {
	if to <= from || label == "" {
		return row
	}
	cells := scanRow(row)
	lo, hi := cells.byteRangeAt(from, to)
	if lo >= hi {
		// The row has no text in that range: the label goes after
		// whatever the row has, padded to the column it starts at.
		pad := from - cells.width()
		if pad < 0 {
			pad = 0
		}
		return row + strings.Repeat(" ", pad) + label
	}
	return row[:cells.origin(lo)] + label + row[cells.origin(hi):]
}

// rowCells is the cell map of one rendered row.
type rowCells struct {
	text    string     // the printable text of the row, ANSI stripped
	cells   []cellSpan // one entry per grapheme cluster
	origins []int      // origins[i] is the byte offset in the row of text[i]
}

// width is how many columns the row's cells cover.
func (r rowCells) width() int {
	if len(r.cells) == 0 {
		return 0
	}
	last := r.cells[len(r.cells)-1]
	return last.column + last.width
}

// cellIndexAt returns the index of the first cluster that starts at or
// after column. It is used to skip a gutter whose glyphs may be wide:
// the prefix is measured in terminal columns, while cells are stored
// per grapheme cluster.
func (r rowCells) cellIndexAt(column int) int {
	for i, c := range r.cells {
		if c.column+c.width > column {
			return i
		}
	}
	return len(r.cells)
}

// origin returns the byte offset in the rendered row of a byte offset in
// the row's plain text.
func (r rowCells) origin(offset int) int {
	if offset < 0 {
		return 0
	}
	if offset >= len(r.origins) {
		return len(r.origins) - 1
	}
	return r.origins[offset]
}

// byteRangeAt returns the byte range in the row's plain text that the
// columns [c0, c1) cover.
//
// The columns are terminal columns, not clusters: a wide character
// covers two of them, and a terminal reports the column it was drawn
// in.
func (r rowCells) byteRangeAt(c0, c1 int) (int, int) {
	if c1 <= c0 {
		return 0, 0
	}
	lo, hi := -1, -1
	for _, c := range r.cells {
		if c.column+c.width <= c0 {
			continue // before the range
		}
		if c.column >= c1 {
			break // after the range
		}
		if lo < 0 {
			lo = c.start
		}
		hi = c.end
	}
	if lo < 0 {
		return 0, 0
	}
	return lo, hi
}

// escapeLength returns the length of the ANSI escape sequence at the
// start of s, or 0 when s does not start with one.
func escapeLength(s string) int {
	if len(s) == 0 || s[0] != 0x1b {
		return 0
	}
	if len(s) == 1 {
		return 1
	}
	switch s[1] {
	case '[': // CSI: parameters and intermediates, then a final byte
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']': // OSC: terminated by BEL or ST
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	case 'P', 'X', '^', '_': // DCS, SOS, PM, APC: terminated by ST
		for i := 2; i < len(s); i++ {
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	case '(', ')', '*', '+', '-', '.', '/', '#', '%':
		// Character set selection: two more bytes.
		return min(3, len(s))
	default:
		return 2
	}
}

// scanRow builds the cell map of a rendered row: the printable text, the
// cell boundaries inside it, and the origin of every byte.
//
// Escape sequences contribute no cells. Grapheme clusters are kept whole,
// so a combining mark, a zero-width joiner sequence or a wide character
// is one cell and never split in half.
func scanRow(row string) rowCells {
	var text strings.Builder
	var origins []int
	textEnd := 0 // the row offset just after the last printable character

	i := 0
	for i < len(row) {
		if n := escapeLength(row[i:]); n > 0 {
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(row[i:])
		if r == utf8.RuneError && size <= 1 {
			// Not valid UTF-8: keep the byte so nothing is lost.
			text.WriteByte(row[i])
			origins = append(origins, i)
			textEnd = i + 1
			i++
			continue
		}
		if r < 0x20 || r == 0x7f {
			// Control characters are not printable cells.
			i += size
			continue
		}
		text.WriteString(row[i : i+size])
		for k := 0; k < size; k++ {
			origins = append(origins, i)
		}
		textEnd = i + size
		i += size
	}

	out := rowCells{text: text.String(), origins: origins}
	out.origins = append(out.origins, textEnd)

	pos := 0
	column := 0
	pending := -1 // start of a zero-width cluster waiting for a cell
	iter := displaywidth.StringGraphemes(out.text)
	for iter.Next() {
		cluster := iter.Value()
		width := iter.Width()
		start, end := pos, pos+len(cluster)
		pos = end
		if width == 0 {
			// A zero-width cluster belongs to the cell it follows.
			if pending < 0 {
				pending = start
			}
			continue
		}
		if pending >= 0 {
			start = pending
			pending = -1
		}
		out.cells = append(out.cells, cellSpan{
			start:  start,
			end:    end,
			width:  width,
			column: column,
		})
		column += width
	}
	if pending >= 0 && len(out.cells) > 0 {
		last := &out.cells[len(out.cells)-1]
		last.end = pending + 1
	}
	return out
}

// highlightCells marks the columns [from, to) of a rendered row as
// selected.
//
// The row's own styling survives. The reverse-video attribute is set
// around every run of text inside the range and removed again: the row
// resets the attributes between its styled segments, so one span around
// the whole range would be cancelled by the first reset and only the
// first segment would show as selected.
func highlightCells(row string, from, to int) string {
	if from < 0 || to <= from {
		return row
	}
	cells := scanRow(row)
	lo, hi := cells.byteRangeAt(from, to)
	if lo >= hi {
		return row
	}
	return markRange(row, cells.origin(lo), cells.origin(hi))
}

// markRange marks the bytes in [lo, hi) of a rendered row as selected.
//
// The escapes are copied verbatim, so the row keeps its colours and its
// bold and italic runs. Only the text inside the range is marked, and
// each run of it is marked on its own, so a reset inside the row cannot
// cancel the mark halfway through.
func markRange(row string, lo, hi int) string {
	var b strings.Builder
	b.Grow(len(row) + 16)
	i := 0
	for i < len(row) {
		if n := escapeLength(row[i:]); n > 0 {
			b.WriteString(row[i : i+n])
			i += n
			continue
		}
		// A run of printable text.
		j := i
		for j < len(row) && escapeLength(row[j:]) == 0 {
			j++
		}
		run := row[i:j]
		switch {
		case j <= lo || i >= hi:
			b.WriteString(run)
		default:
			s := max(i, lo)
			e := min(j, hi)
			b.WriteString(run[:s-i])
			b.WriteString("\x1b[7m")
			b.WriteString(run[s-i : e-i])
			b.WriteString("\x1b[27m")
			b.WriteString(run[e-i:])
		}
		i = j
	}
	return b.String()
}
