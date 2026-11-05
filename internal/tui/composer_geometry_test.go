package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The composer is two full-width rules around one to five rows of the
// draft. These tests pin the geometry to what is actually drawn: the
// height must follow the visible rows of the text, never the paste event,
// the terminal width, or the palette.

// composerRulesAt asserts the composer's frame and returns its text rows.
func composerRulesAt(t *testing.T, m Model, width int) []string {
	t.Helper()
	rows := m.layoutRows()
	top, bottom := composerRules(rows)
	if top < 0 || bottom < 0 {
		t.Fatalf("composer rules missing:\n%s", strings.Join(rows, "\n"))
	}
	if got := ruleWidth(rows[top]); got != width {
		t.Errorf("top rule width = %d, want %d", got, width)
	}
	if got := ruleWidth(rows[bottom]); got != width {
		t.Errorf("bottom rule width = %d, want %d", got, width)
	}
	for _, marker := range []string{"╭", "╮", "╰", "╯", "│"} {
		if strings.Contains(strings.Join(rows[top:bottom+1], "\n"), marker) {
			t.Errorf("the composer still draws %q", marker)
		}
	}
	return composerDisplay(rows)
}

// TestComposerHeightFollowsVisibleRows is the regression matrix: empty,
// one character, a short single-line paste, genuine line breaks, wrapped
// lines, trailing newlines, Unicode and wide characters—at all three
// reference terminal sizes. In every case the height is the number of
// rows actually drawn, capped at five.
func TestComposerHeightFollowsVisibleRows(t *testing.T) {
	type tc struct {
		name    string
		body    string
		typed   bool
		wantMin int // -1 means "derive from the draft"
	}
	cases := []tc{
		{name: "empty", body: "", wantMin: 1},
		{name: "one character", body: "x", typed: true, wantMin: 1},
		{name: "one typed line", body: "hello HAL", typed: true, wantMin: 1},
		{name: "short single-line paste", body: "a short line pasted from another window", wantMin: 1},
		{name: "two lines", body: "alpha\nbravo", wantMin: 2},
		{name: "three lines", body: "alpha\nbravo\ncharlie", wantMin: 3},
		{name: "four lines", body: numberedLines(4), wantMin: 4},
		{name: "five lines", body: numberedLines(5), wantMin: 5},
		{name: "six lines", body: numberedLines(6), wantMin: 5},
		{name: "large multiline paste", body: numberedLines(80), wantMin: 5},
		{name: "trailing newline", body: "alpha\nbravo\n", wantMin: 3},
		{name: "no trailing newline", body: "alpha\nbravo", wantMin: 2},
		{name: "two trailing newlines", body: "alpha\nbravo\n\n", wantMin: 4},
		{name: "unicode", body: "héllo wörld 日本語", wantMin: 1},
		{name: "wide characters", body: strings.Repeat("日", 50), wantMin: -1},
		{name: "one wrapped line", body: strings.Repeat("word ", 60), wantMin: -1},
		{name: "long unbroken word", body: strings.Repeat("x", 200), wantMin: -1},
	}
	sizes := [][2]int{{80, 24}, {120, 30}, {170, 38}}
	for _, size := range sizes {
		for _, c := range cases {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], c.name), func(t *testing.T) {
				m := testModel()
				updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				m = updated.(Model)
				if c.body != "" || c.typed {
					if c.typed {
						m = typeText(m, c.body)
					} else {
						m = pasteText(m, c.body)
					}
				}
				if got := m.Composer().Value(); got != c.body {
					t.Fatalf("the draft holds %q, want %q", got, c.body)
				}

				want := m.draftRows()
				if want < minComposerRows {
					want = minComposerRows
				}
				if want > maxComposerHeight {
					want = maxComposerHeight
				}
				if c.wantMin >= 0 && want != c.wantMin {
					t.Fatalf("draft rows = %d, want %d", want, c.wantMin)
				}
				if got := m.ComposerRows(); got != want {
					t.Errorf("composer rows = %d, want %d", got, want)
				}
				shown := composerRulesAt(t, m, size[0])
				if len(shown) != want {
					t.Fatalf("the frame shows %d composer rows, want %d: %q", len(shown), want, shown)
				}

				// Every row inside the window is a row of the text; the
				// displayed height is not padding or a leftover widget row.
				if m.draftRows() <= want {
					lines := m.draftLines()
					for i, row := range shown {
						if i >= len(lines) {
							t.Fatalf("row %d is beyond the draft's %d rows", i, len(lines))
						}
						if row != strings.TrimRight(lines[i], " ") {
							t.Errorf("row %d = %q, want %q", i, row, strings.TrimRight(lines[i], " "))
						}
					}
				} else {
					// The window is an abbreviation of a longer draft; it
					// still has to show real rows, and its last row is the
					// caret's row.
					last := strings.TrimRight(m.draftLines()[m.draftRows()-1], " ")
					if shown[len(shown)-1] != last {
						t.Errorf("last visible row = %q, want %q", shown[len(shown)-1], last)
					}
					for i, row := range shown {
						if strings.TrimSpace(row) == "" {
							t.Errorf("window row %d is blank: %q", i, shown)
						}
					}
				}
				if c.body == "" {
					// An empty composer shows the caret, not a placeholder.
					rows := m.layoutRows()
					top, _ := composerRules(rows)
					if !strings.Contains(rows[top+1], "\x1b[7") {
						t.Errorf("the empty composer has no visible caret: %q", rows[top+1])
					}
					if strings.Contains(strings.Join(rows, "\n"), "Message HAL") {
						t.Error("the old placeholder is still drawn")
					}
				}
			})
		}
	}
}

// TestComposerCRLFPaste checks that a clipboard's CRLF is one line break,
// not two. Every line and every intentional break survives.
func TestComposerCRLFPaste(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		rows int
	}{
		{name: "single line", body: "hello\r\n", want: "hello\n", rows: 2},
		{name: "lone CR", body: "hello\r", want: "hello\n", rows: 2},
		{name: "two lines", body: "line one\r\nline two", want: "line one\nline two", rows: 2},
		{name: "trailing CRLF", body: "line one\r\nline two\r\n", want: "line one\nline two\n", rows: 3},
		{name: "blank line", body: "one\r\n\r\ntwo", want: "one\n\ntwo", rows: 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := testModel()
			m = pasteText(m, c.body)
			if got := m.Composer().Value(); got != c.want {
				t.Errorf("draft = %q, want %q", got, c.want)
			}
			if got := m.draftRows(); got != c.rows {
				t.Errorf("draft rows = %d, want %d", got, c.rows)
			}
			want := min(c.rows, maxComposerHeight)
			if got := m.ComposerRows(); got != want {
				t.Errorf("composer rows = %d, want %d", got, want)
			}
			if shown := composerRulesAt(t, m, 80); len(shown) != want {
				t.Errorf("the frame shows %d rows, want %d", len(shown), want)
			}
		})
	}
}

// TestComposerResizeRewraps checks that a line that fits on a wide
// terminal wraps on a narrow one, and that the height follows the wrap.
func TestComposerResizeRewraps(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 170, Height: 38})
	m = updated.(Model)
	body := strings.Repeat("x", 120)
	m = pasteText(m, body)
	if got := m.ComposerRows(); got != 1 {
		t.Fatalf("at 170x38: composer rows = %d, want 1 (draft rows %d)", got, m.draftRows())
	}
	composerRulesAt(t, m, 170)

	for _, step := range []struct {
		width, height int
		rows          int
	}{
		{80, 24, 2},
		{120, 30, 2},
		{170, 38, 1},
	} {
		updated, _ = m.Update(tea.WindowSizeMsg{Width: step.width, Height: step.height})
		m = updated.(Model)
		if got := m.ComposerRows(); got != step.rows {
			t.Errorf("%dx%d: composer rows = %d, want %d (draft rows %d)",
				step.width, step.height, got, step.rows, m.draftRows())
		}
		if got := m.Composer().Value(); got != body {
			t.Errorf("%dx%d: resizing changed the draft", step.width, step.height)
		}
		if shown := composerRulesAt(t, m, step.width); len(shown) != step.rows {
			t.Errorf("%dx%d: the frame shows %d rows, want %d", step.width, step.height, len(shown), step.rows)
		}
	}
}

// TestComposerClearAndRestoreShortDraft checks Ctrl+U and Ctrl+Z on a
// draft that fits entirely in the window.
func TestComposerClearAndRestoreShortDraft(t *testing.T) {
	m := testModel()
	body := "alpha\nbravo\ncharlie"
	m = pasteText(m, body)
	if got := m.ComposerRows(); got != 3 {
		t.Fatalf("composer rows = %d, want 3", got)
	}
	m = pressKey(m, 'u', tea.ModCtrl, "")
	if got := m.ComposerRows(); got != 1 {
		t.Errorf("Ctrl+U left the composer at %d rows, want 1", got)
	}
	if shown := composerRulesAt(t, m, 80); len(shown) != 1 {
		t.Errorf("Ctrl+U left %d displayed rows, want 1", len(shown))
	}
	m = pressKey(m, 'z', tea.ModCtrl, "")
	if got := m.ComposerRows(); got != 3 {
		t.Errorf("Ctrl+Z restored the composer at %d rows, want 3", got)
	}
	if got := m.Composer().Value(); got != body {
		t.Errorf("Ctrl+Z restored %q, want %q", got, body)
	}
	if shown := composerRulesAt(t, m, 80); len(shown) != 3 {
		t.Errorf("Ctrl+Z restored %d displayed rows, want 3", len(shown))
	}
}

// TestComposerThemesKeepTheGeometry checks that all five palettes draw
// the same rules and the same height, and that the rules are styled.
func TestComposerThemesKeepTheGeometry(t *testing.T) {
	body := "alpha\nbravo\ncharlie"
	for _, theme := range Themes() {
		m := testModel()
		m.SetColor(true)
		m.SetTheme(theme)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = updated.(Model)
		m = pasteText(m, body)
		shown := composerRulesAt(t, m, 80)
		if len(shown) != 3 {
			t.Errorf("%s: showed %d rows, want 3", theme.Name, len(shown))
		}
		if got := m.ComposerRows(); got != 3 {
			t.Errorf("%s: composer rows = %d, want 3", theme.Name, got)
		}
		rows := m.layoutRows()
		top, bottom := composerRules(rows)
		if !strings.Contains(rows[top], "\x1b[") || !strings.Contains(rows[bottom], "\x1b[") {
			t.Errorf("%s: the rules are not styled", theme.Name)
		}
		if got := m.Composer().Value(); got != body {
			t.Errorf("%s: the draft changed", theme.Name)
		}
	}
}

// TestComposerGrowthKeepsTheReadingPosition checks that growing and
// collapsing the composer does not jump the conversation to the bottom.
func TestComposerGrowthKeepsTheReadingPosition(t *testing.T) {
	m := longConversation(t, 80)
	m = pressKey(m, tea.KeyHome, 0, "")
	m = pressKey(m, tea.KeyPgDown, 0, "")
	m = pressKey(m, tea.KeyPgDown, 0, "")
	if m.Viewport().AtBottom() {
		t.Fatal("precondition: the conversation should be scrolled up")
	}
	topBefore := firstVisibleLine(m.viewport.View())

	m = pasteText(m, numberedLines(4))
	if got := m.ComposerRows(); got != 4 {
		t.Fatalf("composer rows = %d, want 4", got)
	}
	if m.Viewport().AtBottom() {
		t.Error("growing the composer jumped the conversation to the bottom")
	}
	if !strings.Contains(stripANSI(m.viewport.View()), topBefore) {
		t.Errorf("growing the composer lost the reading position %q", topBefore)
	}

	m = pressKey(m, 'u', tea.ModCtrl, "")
	if got := m.ComposerRows(); got != 1 {
		t.Fatalf("after Ctrl+U: composer rows = %d, want 1", got)
	}
	if m.Viewport().AtBottom() {
		t.Error("collapsing the composer jumped the conversation to the bottom")
	}
	if !strings.Contains(stripANSI(m.viewport.View()), topBefore) {
		t.Errorf("collapsing the composer lost the reading position %q", topBefore)
	}
}
