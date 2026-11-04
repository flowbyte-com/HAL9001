package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/flowbyte-com/HAL9001/internal/usage"
)

// The frame of the chat view is fixed: the header, the conversation,
// the composer, one row of keyboard hints, a thin divider and the
// footer. Every row has its place, and nothing moves when the contents
// of one of them change.

// composerBorder finds the rows of the composer's border.
func composerBorder(rows []string) (int, int) {
	top, bottom := -1, -1
	for i, row := range rows {
		plain := stripANSI(row)
		if strings.HasPrefix(plain, "╭") {
			top = i
		}
		if strings.HasPrefix(plain, "╰") {
			bottom = i
		}
	}
	return top, bottom
}

// TestTheFrameHasItsPartsInOrder checks the order of the rows: the
// composer, then the hint, then the divider, then the footer.
func TestTheFrameHasItsPartsInOrder(t *testing.T) {
	m := testModel()
	m.SetModelName("MiniMax-M3.1-Flash")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	rows := m.layoutRows()
	top, bottom := composerBorder(rows)
	if top < 0 || bottom < 0 {
		t.Fatalf("the composer border is missing:\n%s", strings.Join(rows, "\n"))
	}
	if bottom+1 >= len(rows) {
		t.Fatal("there is no row below the composer")
	}
	hint := stripANSI(rows[bottom+1])
	if !strings.Contains(hint, "Enter send") {
		t.Errorf("the row below the composer is not the hint: %q", hint)
	}
	divider := stripANSI(rows[bottom+2])
	if !strings.HasPrefix(divider, "─") {
		t.Errorf("the row above the footer is not the divider: %q", divider)
	}
	footer := stripANSI(rows[bottom+3])
	if !strings.Contains(footer, "444 READ ONLY") {
		t.Errorf("the footer is missing: %q", footer)
	}
}

// TestTheFrameFillsTheTerminalExactly checks that the layout is exactly
// as tall as the terminal, at every size.
func TestTheFrameFillsTheTerminalExactly(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 30}, {170, 38}, {40, 12}, {100, 8}} {
		m := testModel()
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = updated.(Model)
		rows := m.layoutRows()
		if len(rows) != size[1] {
			t.Errorf("%dx%d: the frame has %d rows", size[0], size[1], len(rows))
		}
		for i, row := range rows {
			if w := lipgloss.Width(stripANSI(row)); w > size[0] {
				t.Errorf("%dx%d: row %d is %d cells wide: %q",
					size[0], size[1], i, w, stripANSI(row))
			}
		}
	}
}

// TestTheLayoutIsTheSameOnEveryTheme checks that the geometry does not
// depend on the palette: the themes change the colours, not the shape.
func TestTheLayoutIsTheSameOnEveryTheme(t *testing.T) {
	var reference []string
	for _, theme := range Themes() {
		m := testModel()
		m.SetColor(true)
		m.SetTheme(theme)
		m.SetModelName("MiniMax-M3.1-Flash")
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m = updated.(Model)
		m = typeText(m, "hello")
		rows := m.layoutRows()
		plain := make([]string, len(rows))
		for i, row := range rows {
			plain[i] = stripANSI(row)
		}
		if reference == nil {
			reference = plain
			continue
		}
		for i := range plain {
			if plain[i] != reference[i] {
				t.Errorf("%s: row %d = %q, want %q", theme.Name, i, plain[i], reference[i])
			}
		}
	}
}

// TestTheComposerExpandsToFiveRows checks that a long draft grows the
// composer to its maximum and no further.
func TestTheComposerExpandsToFiveRows(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	rows := m.layoutRows()
	top, bottom := composerBorder(rows)
	if bottom-top-1 != minComposerRows {
		t.Errorf("the composer shows %d rows, want %d", bottom-top-1, minComposerRows)
	}
	m = pasteText(m, numberedLines(30))
	rows = m.layoutRows()
	top, bottom = composerBorder(rows)
	if bottom-top-1 != maxComposerHeight {
		t.Errorf("the composer shows %d rows, want %d", bottom-top-1, maxComposerHeight)
	}
}

// TestTheNotificationDoesNotMoveAnything checks that a notice never
// shifts the composer or the footer.
func TestTheNotificationDoesNotMoveAnything(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	before := m.layoutRows()
	m.notice = "Draft cleared · Ctrl+Z to restore"
	after := m.layoutRows()
	if len(before) != len(after) {
		t.Fatalf("the frame grew: %d -> %d", len(before), len(after))
	}
	// Only the notice row itself may change: nothing above it moves.
	for i := 0; i < len(before)-1; i++ {
		if stripANSI(before[i]) != stripANSI(after[i]) {
			t.Errorf("row %d moved: %q -> %q", i, stripANSI(before[i]), stripANSI(after[i]))
		}
	}
	if !strings.Contains(stripANSI(after[len(after)-1]), "Draft cleared") {
		t.Errorf("the notice is not on the last row: %q", stripANSI(after[len(after)-1]))
	}
}

// TestTheNoticeIsVisuallySubordinate checks that the notification is
// styled apart from the rows that carry information.
func TestTheNoticeIsVisuallySubordinate(t *testing.T) {
	m := testModel()
	m.SetColor(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.notice = "something happened"
	row := ""
	for _, r := range m.layoutRows() {
		if strings.Contains(stripANSI(r), "something happened") {
			row = r
		}
	}
	if row == "" {
		t.Fatal("the notice is not on screen")
	}
	if !strings.Contains(row, "\x1b[") {
		t.Error("the notice is not styled")
	}
}

// TestTheFooterIsOneRow checks that the footer keeps its three parts on
// one row, whatever the terminal's width.
func TestTheFooterIsOneRow(t *testing.T) {
	for _, width := range []int{170, 120, 80, 60, 40, 30, 20} {
		m := testModel()
		m.SetModelName("MiniMax-M3.1-Flash")
		m.SetEffort("medium", true)
		m.SetUsage(usage.Session{Input: 1200, Output: 340, Turns: 2})
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = updated.(Model)
		footer := footerText(m)
		if strings.Contains(footer, "\n") {
			t.Errorf("width %d: the footer wraps: %q", width, footer)
		}
		if w := lipgloss.Width(footer); w > width {
			t.Errorf("width %d: the footer is %d cells wide: %q", width, w, footer)
		}
		if !strings.Contains(footer, "444") {
			t.Errorf("width %d: the footer lost the permission: %q", width, footer)
		}
	}
}

// TestTheSessionTitleIsInTheHeader checks that the header names the
// conversation.
func TestTheSessionTitleIsInTheHeader(t *testing.T) {
	m := testModel()
	m.SetTitle("the quarterly review")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	header := stripANSI(m.headerRow())
	if !strings.Contains(header, "HAL9001") {
		t.Errorf("header = %q", header)
	}
	if !strings.Contains(header, "the quarterly review") {
		t.Errorf("header = %q, want the session title", header)
	}
}
