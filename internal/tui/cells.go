package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/clipperhouse/displaywidth"
)

// cellSpan is one display cell of a rendered row: a byte range in the
// row's plain text and the number of terminal columns it occupies.
//
// The cell map is what makes mouse selection possible at all: a terminal
// reports cells, not text, so every selection has to be translated back
// into the characters that produced those cells.
type cellSpan struct {
	start int // byte offset in the plain text
	end   int // byte offset in the plain text
	width int // display width in columns
}

// byteOrigin records where a byte of the plain text came from in the
// original row, so a styled row can be rebuilt with a highlight inserted
// at exactly the right place.
type byteOrigin struct {
	start int
	end   int
}

// rowCells is the cell map of one rendered row.
type rowCells struct {
	text    string     // the printable text of the row, ANSI stripped
	cells   []cellSpan // one entry per display cell
	origins []byteOrigin
}

// cellCount is the number of display cells in the row.
func (r rowCells) cellCount() int { return len(r.cells) }

// byteAt returns the byte offset in the plain text for a cell index. A
// cell index outside the row clamps to the nearest end, so a pointer past
// the end of a line selects the whole line.
func (r rowCells) byteAt(cell int) int {
	switch {
	case cell < 0:
		return 0
	case cell >= len(r.cells):
		if len(r.cells) == 0 {
			return 0
		}
		return r.cells[len(r.cells)-1].end
	default:
		return r.cells[cell].start
	}
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
	var origins []byteOrigin

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
			origins = append(origins, byteOrigin{start: i, end: i + 1})
			i++
			continue
		}
		if r < 0x20 || r == 0x7f {
			// Control characters are not printable cells.
			i += size
			continue
		}
		text.WriteString(row[i : i+size])
		origins = append(origins, byteOrigin{start: i, end: i + size})
		i += size
	}

	out := rowCells{text: text.String(), origins: origins}
	pos := 0
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
		out.cells = append(out.cells, cellSpan{start: start, end: end, width: width})
	}
	if pending >= 0 && len(out.cells) > 0 {
		last := &out.cells[len(out.cells)-1]
		last.end = pending + 1
	}
	return out
}

// highlightCells wraps the cells in [from, to) of a rendered row in
// reverse video, so a selection is visible. The row's own styling is
// preserved: only the video attribute is added and removed again.
func highlightCells(row string, from, to int) string {
	if from < 0 || to <= from {
		return row
	}
	cells := scanRow(row)
	if from >= len(cells.cells) {
		return row
	}
	if to > len(cells.cells) {
		to = len(cells.cells)
	}
	startCell := cells.cells[from]
	endCell := cells.cells[to-1]
	if startCell.start >= len(cells.origins) || endCell.end > len(cells.origins) {
		return row
	}
	lo := cells.origins[startCell.start].start
	hi := cells.origins[endCell.end-1].end
	if lo < 0 || hi > len(row) || lo >= hi {
		return row
	}
	var b strings.Builder
	b.Grow(len(row) + 12)
	b.WriteString(row[:lo])
	b.WriteString("\x1b[7m")
	b.WriteString(row[lo:hi])
	b.WriteString("\x1b[27m")
	b.WriteString(row[hi:])
	return b.String()
}
