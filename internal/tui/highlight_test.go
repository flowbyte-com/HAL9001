package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"
)

// The highlight has to mark exactly the text the selection covers.
//
// Two things went wrong before: the marked range started at the first
// cell of the row, so the label, the indent and the renderer's margin
// were painted as well — a pale block in the left margin — and the mark
// was one escape sequence around the whole range, so the reset between
// the row's styled segments cancelled it after the first segment.

// reverseCells reports, for every display cell of a rendered row, whether
// the reverse-video attribute is active on it.
//
// A wide character covers two cells, so the cells are counted the way
// the terminal counts them.
func reverseCells(row string) []bool {
	out := []bool{}
	reverse := false
	i := 0
	for i < len(row) {
		if n := escapeLength(row[i:]); n > 0 {
			seq := row[i : i+n]
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				for _, p := range strings.Split(seq[2:len(seq)-1], ";") {
					switch p {
					case "7":
						reverse = true
					case "27", "0", "":
						reverse = false
					}
				}
			}
			i += n
			continue
		}
		r, size := decodeRune(row[i:])
		if r < 0x20 || r == 0x7f {
			i += size
			continue
		}
		w := runewidth.RuneWidth(r)
		if w > 0 {
			for k := 0; k < w; k++ {
				out = append(out, reverse)
			}
		}
		i += size
	}
	return out
}

func decodeRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 1
}

// markedRange returns the first and the column after the last marked
// cell of a rendered row. A row with nothing marked returns (0, 0).
func markedRange(row string) (int, int) {
	lo, hi := -1, -1
	for i, marked := range reverseCells(row) {
		if !marked {
			continue
		}
		if lo < 0 {
			lo = i
		}
		hi = i + 1
	}
	if lo < 0 {
		return 0, 0
	}
	return lo, hi
}

// viewportRowOf returns the index of the viewport row that shows the
// given conversation row.
func viewportRowOf(m Model, convRow int) int {
	rows := m.viewportRows(m.frame.viewport)
	for i := range rows {
		vy := i - m.conversationPad()
		if vy < 0 {
			continue
		}
		idx, _, ok := m.contentRowAt(m.viewport.YOffset() + vy)
		if ok && idx == convRow {
			return i
		}
	}
	return -1
}

// selectAll marks the whole conversation as selected.
func selectAll(m *Model) {
	last := len(m.conv.rows) - 1
	if last < 0 {
		return
	}
	m.selAnchor = cellPos{row: 0, cell: 0}
	m.selFocus = cellPos{row: last, cell: m.conv.rows[last].width}
	m.selMoved = true
}

// highlightOf returns the rendered viewport row that shows the given
// conversation row.
func highlightOf(m Model, convRow int) string {
	i := viewportRowOf(m, convRow)
	if i < 0 {
		return ""
	}
	rows := m.highlightSelection(m.viewportRows(m.frame.viewport))
	return rows[i]
}

// TestHighlightCoversOnlyTheText checks that the marked cells are the
// row's text and nothing else: not the label, not the indent, not the
// renderer's margin and not the padding behind the text.
func TestHighlightCoversOnlyTheText(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("assistant", "## A heading\n\nA paragraph of plain text.\n")
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	selectAll(&m)

	for i, row := range m.conv.rows {
		got := highlightOf(m, i)
		if got == "" {
			t.Fatalf("conversation row %d is not on screen", i)
		}
		lo, hi := markedRange(got)
		if row.cellEnd <= row.cellStart {
			// A row without text, such as the blank line between
			// two paragraphs, must not be marked at all.
			if lo != 0 || hi != 0 {
				t.Errorf("row %d (%q) is marked at %d..%d, want nothing",
					i, stripANSI(got), lo, hi)
			}
			continue
		}
		if lo != row.cellStart || hi != row.cellEnd {
			t.Errorf("row %d (%q) is marked at %d..%d, want %d..%d",
				i, stripANSI(got), lo, hi, row.cellStart, row.cellEnd)
		}
	}
}

// TestHighlightSurvivesStyleResets checks that the mark covers the whole
// of a styled run. The row resets its attributes between its segments,
// and the mark must not be cancelled by that.
func TestHighlightSurvivesStyleResets(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("assistant", "## A heading\n")
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	selectAll(&m)

	// The heading is rendered in several styled segments.
	idx := -1
	for i, r := range m.entries[0].rows {
		if strings.Contains(stripANSI(r), "A heading") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("the heading was not found")
	}
	row := m.conv.rows[idx]
	got := highlightOf(m, idx)
	lo, hi := markedRange(got)
	if lo != row.cellStart || hi != row.cellEnd {
		t.Errorf("the heading is marked at %d..%d, want %d..%d (%q)",
			lo, hi, row.cellStart, row.cellEnd, stripANSI(got))
	}
	if !strings.Contains(stripANSI(got), "A heading") {
		t.Errorf("the heading text is not on screen: %q", stripANSI(got))
	}
}

// TestHighlightStartsAtTheLeftEdge checks that a selection that starts
// in the margin marks the text, not the margin.
func TestHighlightStartsAtTheLeftEdge(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("user", "hello")
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.selAnchor = cellPos{row: 0, cell: 0}
	m.selFocus = cellPos{row: 0, cell: m.conv.rows[0].width}
	m.selMoved = true

	got := highlightOf(m, 0)
	lo, hi := markedRange(got)
	if lo != entryPrefixCells || hi != entryPrefixCells+5 {
		t.Errorf("the text is marked at %d..%d, want %d..%d (%q)",
			lo, hi, entryPrefixCells, entryPrefixCells+5, stripANSI(got))
	}
}

// TestHighlightInWhitespace checks that a selection that starts or ends
// in the whitespace around the text marks the text alone, and copies
// the text alone.
func TestHighlightInWhitespace(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("user", "hello")
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	// Start in the space before the text, end in the space after it.
	m.selAnchor = cellPos{row: 0, cell: entryPrefixCells - 1}
	m.selFocus = cellPos{row: 0, cell: entryPrefixCells + 6}
	m.selMoved = true

	got := highlightOf(m, 0)
	lo, hi := markedRange(got)
	if lo != entryPrefixCells || hi != entryPrefixCells+5 {
		t.Errorf("the text is marked at %d..%d, want %d..%d (%q)",
			lo, hi, entryPrefixCells, entryPrefixCells+5, stripANSI(got))
	}
	text := m.conv.extract(m.selAnchor, m.selFocus)
	if text != "hello" {
		t.Errorf("copied %q, want %q", text, "hello")
	}
}

// TestHighlightFollowsWrappedText checks that every row of a wrapped
// paragraph is marked, and that the copy is still one line.
func TestHighlightFollowsWrappedText(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	text := "The quick brown fox jumps over the lazy dog and keeps on running until the very end of the sentence."
	m.appendEntry("assistant", text)
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	selectAll(&m)

	marked := 0
	for i, row := range m.conv.rows {
		if row.cellEnd <= row.cellStart {
			continue
		}
		got := highlightOf(m, i)
		lo, hi := markedRange(got)
		if lo != row.cellStart || hi != row.cellEnd {
			t.Errorf("row %d is marked at %d..%d, want %d..%d (%q)",
				i, lo, hi, row.cellStart, row.cellEnd, stripANSI(got))
			continue
		}
		marked++
	}
	if marked < 2 {
		t.Errorf("the paragraph was expected to wrap, %d row(s) marked", marked)
	}
	// The copy is the paragraph as it was written: one line, no line
	// break where the renderer wrapped it.
	first := 1
	last := len(m.conv.rows) - 1
	got := m.conv.extract(cellPos{row: first, cell: 8},
		cellPos{row: last, cell: m.conv.rows[last].width})
	if strings.Contains(got, "\n") {
		t.Errorf("the copy has a line break: %q", got)
	}
	if !strings.Contains(got, "The quick brown fox") {
		t.Errorf("the copy does not hold the paragraph: %q", got)
	}
}

// TestHighlightWithWideCharacters checks that a wide character is
// marked as the two columns it covers, and that it is copied whole.
func TestHighlightWithWideCharacters(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("user", "a 日本語 b")
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	selectAll(&m)

	row := m.conv.rows[0]
	got := highlightOf(m, 0)
	lo, hi := markedRange(got)
	if lo != row.cellStart || hi != row.cellEnd {
		t.Errorf("the text is marked at %d..%d, want %d..%d (%q)",
			lo, hi, row.cellStart, row.cellEnd, stripANSI(got))
	}
	text := m.conv.extract(m.selAnchor, m.selFocus)
	if text != "a 日本語 b" {
		t.Errorf("copied %q, want %q", text, "a 日本語 b")
	}
}

// TestHighlightFollowsScrolling checks that the mark stays on the text
// it belongs to when the history is scrolled.
func TestHighlightFollowsScrolling(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	for i := 0; i < 40; i++ {
		m.appendEntry("assistant", "row of the answer")
	}
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	selectAll(&m)

	m.viewport.GotoTop()
	m.viewport.SetYOffset(10)
	for i, row := range m.conv.rows {
		got := highlightOf(m, i)
		if got == "" {
			continue // not on screen
		}
		lo, hi := markedRange(got)
		if row.cellEnd <= row.cellStart {
			continue
		}
		if lo != row.cellStart || hi != row.cellEnd {
			t.Errorf("row %d is marked at %d..%d, want %d..%d (%q)",
				i, lo, hi, row.cellStart, row.cellEnd, stripANSI(got))
		}
	}
}

// TestHighlightGoneAfterResize checks that a resize leaves no mark
// behind: the rows are wrapped again, so the old mark would point at
// different text.
func TestHighlightGoneAfterResize(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("user", "hello")
	m.renderConversation()
	m.selAnchor = cellPos{row: 0, cell: 1}
	m.selFocus = cellPos{row: 0, cell: 5}
	m.selMoved = true
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	for _, row := range m.highlightSelection(m.viewportRows(m.frame.viewport)) {
		if lo, hi := markedRange(row); lo != 0 || hi != 0 {
			t.Errorf("a mark survived the resize at %d..%d", lo, hi)
		}
	}
}

// TestDragCopiesStyledText checks the whole pointer path over a styled
// answer: the text reaches the clipboard plain and uncut.
func TestDragCopiesStyledText(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	var copied string
	m.hooks.Copy = func(s string) error {
		copied = s
		return nil
	}
	m.appendEntry("assistant", "## A heading\n\nA paragraph with a word long enough to need wrapping in a narrow terminal.\n")
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	row, ok := m.screenRowOf(1)
	if !ok {
		t.Fatal("the answer is not on screen")
	}
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 8, Y: row})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 40, Y: row})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 40, Y: row})
	m = updated.(Model)

	if copied == "" {
		t.Fatal("nothing was copied")
	}
	if strings.Contains(copied, "\x1b") {
		t.Errorf("the copy holds escape sequences: %q", copied)
	}
	if !strings.Contains(copied, "A heading") {
		t.Errorf("the copy does not hold the heading: %q", copied)
	}
}
