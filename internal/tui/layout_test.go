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

// isRuleRow reports whether a row is one unbroken horizontal rule.
func isRuleRow(row string) bool {
	plain := strings.TrimSpace(stripANSI(row))
	if plain == "" {
		return false
	}
	for _, r := range plain {
		if r != '─' {
			return false
		}
	}
	return true
}

// ruleWidth is the cell width of a rule row, or 0 when the row is not a
// rule.
func ruleWidth(row string) int {
	if !isRuleRow(row) {
		return 0
	}
	return lipgloss.Width(strings.TrimSpace(stripANSI(row)))
}

// frameParts locates the bottom of the frame: the composer's two rules,
// the hint row and the divider above the footer. The divider is the last
// rule in the frame; the footer and any notice follow it.
func frameParts(rows []string) (top, bottom, hint, divider, footer int) {
	top, bottom, hint, divider, footer = -1, -1, -1, -1, -1
	for i := len(rows) - 1; i >= 0; i-- {
		if isRuleRow(rows[i]) {
			divider = i
			break
		}
	}
	if divider < 2 {
		return
	}
	hint = divider - 1
	plainHint := stripANSI(rows[hint])
	if !strings.Contains(plainHint, "Enter send") &&
		!strings.Contains(plainHint, "Ctrl+U") &&
		!strings.Contains(plainHint, "Ctrl+Z") &&
		!strings.Contains(plainHint, "Draft cleared") {
		return
	}
	if !isRuleRow(rows[hint-1]) {
		return
	}
	bottom = hint - 1
	for i := bottom - 1; i >= 0; i-- {
		if isRuleRow(rows[i]) {
			top = i
			break
		}
	}
	if divider+1 < len(rows) {
		footer = divider + 1
	}
	return
}

// composerRules finds the two full-width rules that frame the composer.
func composerRules(rows []string) (int, int) {
	top, bottom, _, _, _ := frameParts(rows)
	return top, bottom
}

// composerDisplay is the draft as displayed: the rows between the
// composer's two rules, stripped of styling and the one-cell padding.
func composerDisplay(rows []string) []string {
	top, bottom := composerRules(rows)
	if top < 0 || bottom < 0 || bottom <= top+1 {
		return nil
	}
	out := make([]string, 0, bottom-top-1)
	for _, row := range rows[top+1 : bottom] {
		line := stripANSI(row)
		line = strings.TrimPrefix(line, " ")
		out = append(out, strings.TrimRight(line, " "))
	}
	return out
}

// TestTheFrameHasItsPartsInOrder checks the order of the rows: the
// composer, then the hint, then the divider, then the footer.
func TestTheFrameHasItsPartsInOrder(t *testing.T) {
	m := testModel()
	m.SetModelName("MiniMax-M3.1-Flash")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	rows := m.layoutRows()
	top, bottom, hintRow, dividerRow, footerRow := frameParts(rows)
	if top < 0 || bottom < 0 || hintRow < 0 || dividerRow < 0 || footerRow < 0 {
		t.Fatalf("the frame is missing one of its parts:\n%s", strings.Join(rows, "\n"))
	}
	if !(top < bottom && bottom < hintRow && hintRow < dividerRow && dividerRow < footerRow) {
		t.Fatalf("the frame parts are out of order: top=%d bottom=%d hint=%d divider=%d footer=%d",
			top, bottom, hintRow, dividerRow, footerRow)
	}
	hint := stripANSI(rows[hintRow])
	if !strings.Contains(hint, "Enter send") {
		t.Errorf("the row below the composer is not the hint: %q", hint)
	}
	divider := stripANSI(rows[dividerRow])
	if !strings.HasPrefix(divider, "─") {
		t.Errorf("the row above the footer is not the divider: %q", divider)
	}
	footer := stripANSI(rows[footerRow])
	if !strings.Contains(footer, "444 READ ONLY") {
		t.Errorf("the footer is missing: %q", footer)
	}
	for _, row := range []int{top, bottom, dividerRow} {
		if w := ruleWidth(rows[row]); w != 100 {
			t.Errorf("row %d is a rule %d cells wide, want 100", row, w)
		}
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
	top, bottom := composerRules(rows)
	if bottom-top-1 != minComposerRows {
		t.Errorf("the composer shows %d rows, want %d", bottom-top-1, minComposerRows)
	}
	m = pasteText(m, numberedLines(30))
	rows = m.layoutRows()
	top, bottom = composerRules(rows)
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

// TestTheDiagnosticsCarryTheSessionIdentity checks that the immutable
// identity of the conversation is reachable: the header shows the title,
// which can be renamed, and the diagnostics keep the name it is stored
// under.
func TestTheDiagnosticsCarryTheSessionIdentity(t *testing.T) {
	m := testModel()
	m.SetTitle("the quarterly review")
	m.SetSessionID("cli-20261104-1208")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	header := stripANSI(m.headerRow())
	if !strings.Contains(header, "the quarterly review") {
		t.Errorf("header = %q, want the title", header)
	}
	rows := stripANSI(strings.Join(m.diagRows(), "\n"))
	if !strings.Contains(rows, "cli-20261104-1208") {
		t.Errorf("the diagnostics do not carry the session identity:\n%s", rows)
	}
}

// TestTruncateMiddleKeepsBothEnds checks that a long path is shortened
// in the middle: the directory it lives in and the name of the file are
// both still readable.
func TestTruncateMiddleKeepsBothEnds(t *testing.T) {
	path := "/home/operator/projects/hal/notes/letter.md"
	got := truncateMiddle(path, 24)
	if lipgloss.Width(got) != 24 {
		t.Errorf("width = %d, want 24 (%q)", lipgloss.Width(got), got)
	}
	if !strings.HasPrefix(got, "/home") {
		t.Errorf("%q does not keep the head", got)
	}
	if !strings.HasSuffix(got, "letter.md") {
		t.Errorf("%q does not keep the tail", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("%q does not mark the cut", got)
	}
	// A string that fits is left alone.
	if got := truncateMiddle("short.md", 24); got != "short.md" {
		t.Errorf("got %q, want the string unchanged", got)
	}
	// Wide characters are never cut in half.
	got = truncateMiddle("日本語のファイル.md", 10)
	if lipgloss.Width(got) > 10 {
		t.Errorf("width = %d, want at most 10 (%q)", lipgloss.Width(got), got)
	}
}
