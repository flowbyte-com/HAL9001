package tui

import (
	"strings"
	"unicode"

	rw "github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// The composer has two jobs that the textarea alone cannot do together:
// it shows at most five rows of the draft, and the wheel scrolls that
// window without moving the editing cursor.
//
// The bubbles textarea pins its own window to the cursor: every update
// scrolls the view back to where the caret is. HAL therefore keeps the
// window itself, renders the whole draft through the textarea, and shows
// the slice the operator is looking at.

// composerTop is the screen row where the composer starts.
func (m Model) composerTop() int {
	top := m.viewportTop() + m.frame.viewport
	top += m.frame.suggest
	return top
}

// wrapRunes mirrors the soft wrap the bubbles textarea applies to one
// line of its value.
//
// It is a copy of the textarea's own wrap (MIT), and it has to stay in
// step with it: HAL uses it to know how tall the draft is and which
// screen row the cursor is on, and both only work if the rows HAL counts
// are the rows the textarea draws.
func wrapRunes(runes []rune, width int) [][]rune {
	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)
	if width < 1 {
		width = 1
	}
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}
		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces = 0
				word = nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces = 0
				word = nil
			}
		} else {
			// The last character may be double width, in which case it
			// may not fit on this line.
			lastCharLen := rw.RuneWidth(word[len(word)-1])
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}
	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], repeatSpaces(spaces)...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], repeatSpaces(spaces)...)
	}
	return lines
}

func repeatSpaces(n int) []rune {
	return []rune(strings.Repeat(" ", n))
}

// clipboardText turns the line endings a clipboard carries into the one
// line break the composer draws.
//
// Text copied from a browser, a text field or a program on another
// platform usually ends its lines with CR LF. The textarea's own
// sanitizer maps each of CR and LF to a newline, so a CRLF arrives as two
// breaks: one pasted line becomes an extra blank row, and a short
// two-line paste can open the composer at its full five rows on its own.
// A CRLF is one line break, so it becomes one newline; a lone CR is a
// break too, because some terminals send CR for a pasted newline.
//
// Nothing is dropped: every line and every intentional break survives,
// and the draft keeps the text as it was written.
func clipboardText(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// draftWidth is the width the draft is wrapped at.
//
// The terminal is the source of truth, and the textarea is set to the
// same width when the frame is sized. The widget's own width is only a
// fallback for a model that was never given a terminal size: before the
// first layout the textarea still carries its default, and wrapping the
// draft at that width would misjudge how tall it is.
func (m Model) draftWidth() int {
	w := m.width - composerPadding
	if w < 1 {
		w = m.composer.Width()
	}
	if w < 1 {
		w = 1
	}
	return w
}

// draftRows is how many screen rows the whole draft takes.
func (m Model) draftRows() int {
	width := m.draftWidth()
	n := 0
	for _, line := range strings.Split(m.composer.Value(), "\n") {
		n += len(wrapRunes([]rune(line), width))
	}
	return n
}

// cursorDraftRow is the screen row of the draft the cursor is on.
func (m Model) cursorDraftRow() int {
	width := m.draftWidth()
	lines := strings.Split(m.composer.Value(), "\n")
	row := 0
	limit := m.composer.Line()
	if limit > len(lines) {
		limit = len(lines)
	}
	for i := 0; i < limit; i++ {
		row += len(wrapRunes([]rune(lines[i]), width))
	}
	return row + m.composer.LineInfo().RowOffset
}

// clampDraftOffset keeps the composer window inside the draft.
func (m *Model) clampDraftOffset(total, visible int) {
	if visible < 1 {
		visible = 1
	}
	if total < visible {
		total = visible
	}
	if m.draftOffset > total-visible {
		m.draftOffset = total - visible
	}
	if m.draftOffset < 0 {
		m.draftOffset = 0
	}
}

// followCursor scrolls the composer window so the cursor is visible.
// Editing is cursor-aware: the operator always sees the caret, however
// far the draft has been scrolled.
func (m *Model) followCursor() {
	visible := m.frame.composer
	if visible < 1 {
		visible = 1
	}
	cur := m.cursorDraftRow()
	if cur < m.draftOffset {
		m.draftOffset = cur
	}
	if cur >= m.draftOffset+visible {
		m.draftOffset = cur - visible + 1
	}
	m.clampDraftOffset(m.draftRows(), visible)
}

// scrollDraft moves the composer window without touching the draft or
// the cursor.
func (m *Model) scrollDraft(delta int) {
	visible := m.frame.composer
	if visible < 1 {
		visible = 1
	}
	m.draftOffset += delta
	m.clampDraftOffset(m.draftRows(), visible)
}

// composerWindow returns the visible rows of the draft.
//
// The textarea renders the whole draft; HAL shows the slice the operator
// scrolled to, so the wheel can move the window without the textarea
// pulling it back to the cursor.
//
// Two safeguards keep the window honest. The textarea may have scrolled
// its own view (it does when its height is smaller than its content, for
// example between a width change and the next frame), so the window is
// aligned with that offset instead of applying both offsets; and a window
// that would show nothing while the draft has text falls back to the
// draft's own rows. A blank composer over a full draft is never correct.
func (m Model) composerWindow(visible int) []string {
	if visible < 1 {
		visible = 1
	}
	total := m.draftRows()
	if m.composer.ScrollYOffset() > 0 {
		// HAL owns the draft window: the textarea is sized to the whole
		// draft so it should never scroll. If it has scrolled anyway
		// (a frame behind, a widget that clamped its own height), its
		// view starts at the wrong row and trusting it hides the top of
		// a draft that fits. Draw the draft's own rows instead; showing
		// the text is more important than the widget's styling.
		return m.draftWindow(visible)
	}
	if total <= visible {
		// The whole draft fits: the textarea's own end-of-buffer rows
		// fill the rest.
		return splitRows(m.composer.View(), visible)
	}
	rows := strings.Split(m.composer.View(), "\n")
	if len(rows) < total {
		// The textarea's view is shorter than the draft: it is clamping
		// its own height, or its content is a frame behind. Its rows
		// cannot describe the draft, so draw the draft itself.
		return m.draftWindow(visible)
	}
	offset := m.draftOffset - m.composer.ScrollYOffset()
	if maxOffset := len(rows) - visible; offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		offset = 0
	}
	out := make([]string, 0, visible)
	for i := 0; i < visible; i++ {
		if offset+i < len(rows) {
			out = append(out, rows[offset+i])
			continue
		}
		out = append(out, "")
	}
	if strings.TrimSpace(m.composer.Value()) != "" && blankRows(out) {
		return m.draftWindow(visible)
	}
	return out
}

// draftWindow renders the draft's own rows, without the textarea.
//
// It is the fallback for a textarea view that has gone blank: the rows
// come from the same wrap that drives the height and the cursor, so the
// operator sees the text they wrote even when the widget's rendering
// cannot be trusted.
func (m Model) draftWindow(visible int) []string {
	lines := m.draftLines()
	offset := m.draftOffset
	if maxOffset := len(lines) - visible; offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		offset = 0
	}
	out := make([]string, 0, visible)
	for i := 0; i < visible; i++ {
		if offset+i < len(lines) {
			out = append(out, lines[offset+i])
			continue
		}
		out = append(out, "")
	}
	return out
}

// draftLines is the whole draft as wrapped display rows.
func (m Model) draftLines() []string {
	width := m.draftWidth()
	lines := make([]string, 0, 16)
	for _, line := range strings.Split(m.composer.Value(), "\n") {
		for _, wrapped := range wrapRunes([]rune(line), width) {
			lines = append(lines, string(wrapped))
		}
	}
	return lines
}

// blankRows reports whether every row is empty.
func blankRows(rows []string) bool {
	for _, row := range rows {
		if strings.TrimSpace(stripANSI(row)) != "" {
			return false
		}
	}
	return true
}
