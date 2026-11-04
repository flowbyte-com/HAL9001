package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// openHelp runs the /help command the way the dispatcher does, without
// touching the composer.
func openHelp(t *testing.T, m Model) Model {
	t.Helper()
	c, ok := commandByName("/help")
	if !ok {
		t.Fatal("/help is not registered")
	}
	c.run(&m, nil)
	return m
}

// helpBody is the scrollable help text, with styling removed.
func helpBody(m Model) string {
	return stripANSI(strings.Join(m.helpRows(), "\n"))
}

// rowWidthsFit checks every rendered row against the terminal width.
func rowWidthsFit(t *testing.T, rows []string, width int) {
	t.Helper()
	for _, row := range rows {
		if got := lipgloss.Width(row); got > width {
			t.Errorf("row is %d cells wide, terminal is %d: %q", got, width, stripANSI(row))
		}
	}
}

// TestHelpLayoutMatrix is the responsive matrix: every section, every
// registered command and every documented shortcut is present at the
// four reference sizes, on every theme, without a row overflowing.
func TestHelpLayoutMatrix(t *testing.T) {
	sizes := [][2]int{{60, 20}, {80, 24}, {120, 30}, {170, 38}}
	wants := []string{
		"Ctrl+J", "Ctrl+O", "Ctrl+U", "Ctrl+Z", "Ctrl+End",
		"Ctrl+Y", "Ctrl+N", "666", "one-off", "pending",
		"wheel", "drag", "paste", "MPM",
	}
	for _, theme := range Themes() {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s/%dx%d", theme.Name, size[0], size[1]), func(t *testing.T) {
				m := testModel()
				m.SetColor(true)
				m.SetTheme(theme)
				updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				m = updated.(Model)
				m = openHelp(t, m)

				body := helpBody(m)
				for _, title := range helpSectionTitles {
					if !strings.Contains(body, title) {
						t.Errorf("help missing section %q:\n%s", title, body)
					}
				}
				for _, c := range commandTable {
					if !strings.Contains(body, c.name) {
						t.Errorf("help missing command %q:\n%s", c.name, body)
					}
				}
				for _, want := range wants {
					if !strings.Contains(body, want) {
						t.Errorf("help missing %q:\n%s", want, body)
					}
				}

				rowWidthsFit(t, m.helpRows(), size[0])
				rowWidthsFit(t, m.layoutRows(), size[0])
			})
		}
	}
}

// TestHelpColumnsDoNotCollide checks that the label column is padded to
// one width and every description starts at the same cell, including a
// wrapped continuation.
func TestHelpColumnsDoNotCollide(t *testing.T) {
	m := testModel()
	sections := []helpSection{{
		title: "Test",
		entries: []helpEntry{
			{"LONG/LABEL", "wrapping description with enough words to continue"},
			{"x", "short"},
		},
	}}
	labelWidth := helpLabelWidth(sections)
	descAt := helpIndent + labelWidth + helpColumnGap
	// A description column of eight cells puts every "alpha" word on
	// its own continuation row, so both the first line and the wrapped
	// lines can be checked against the same column.
	wrapped := []helpSection{{
		title: "Test",
		entries: []helpEntry{
			{"LONG/LABEL", "alpha alpha alpha alpha"},
			{"x", "short"},
		},
	}}
	rows := m.helpOneColumnRows(wrapped, descAt+8)
	alphaRows := 0
	for _, row := range rows {
		plain := stripANSI(row)
		for _, fragment := range []string{"alpha", "short"} {
			at := strings.Index(plain, fragment)
			if at < 0 {
				continue
			}
			if at != descAt {
				t.Errorf("%q starts at cell %d, want %d: %q", fragment, at, descAt, plain)
			}
			if fragment == "alpha" {
				alphaRows++
			}
		}
	}
	if alphaRows != 4 {
		t.Errorf("the long description wrapped into %d rows, want 4:\n%s",
			alphaRows, stripANSI(strings.Join(rows, "\n")))
	}

	// The narrow fallback stacks the label and description, so the two
	// can never share a row.
	stacked := stripANSI(strings.Join(m.helpStackedRows(sections, 24), "\n"))
	if !strings.Contains(stacked, "LONG/LABEL") || !strings.Contains(stacked, "wrapping") {
		t.Errorf("stacked help lost content:\n%s", stacked)
	}
}

// TestHelpTwoColumnsStayIndependent checks that the optional second
// column is only used when both description columns are readable, and
// that the gutter really separates them.
func TestHelpTwoColumnsStayIndependent(t *testing.T) {
	m := testModel()
	sections := []helpSection{
		{title: "Left", entries: []helpEntry{{"L", "left description"}}},
		{title: "Right", entries: []helpEntry{{"R", "right description"}}},
	}
	rows, ok := m.helpTwoColumnRows(sections, 120)
	if !ok {
		t.Fatal("120 cells must fit two readable columns")
	}
	labelWidth := helpLabelWidth(sections)
	base := labelWidth + helpColumnGap
	available := 120 - helpIndent - helpColumnGutter
	leftWidth := base + (available-2*base)/2
	rightAt := helpIndent + leftWidth + helpColumnGutter + labelWidth + helpColumnGap
	joined := stripANSI(strings.Join(rows, "\n"))
	rows = strings.Split(joined, "\n")
	found := false
	for _, row := range rows {
		if !strings.Contains(row, "left description") || !strings.Contains(row, "right description") {
			continue
		}
		found = true
		if at := strings.Index(row, "right description"); at != rightAt {
			t.Errorf("right description starts at %d, want %d: %q", at, rightAt, row)
		}
	}
	if !found {
		t.Fatalf("the two columns were not rendered side by side:\n%s", joined)
	}
	rowWidthsFit(t, rows, 120)

	if _, ok := m.helpTwoColumnRows(sections, 50); ok {
		t.Error("50 cells must not squeeze help into two columns")
	}
}

// TestWrapHelpTextUsesDisplayCells covers long words, wide characters
// and emoji clusters: a line may never exceed the allocated cells.
func TestWrapHelpTextUsesDisplayCells(t *testing.T) {
	cases := []struct {
		text  string
		width int
	}{
		{"plain words that wrap", 10},
		{strings.Repeat("x", 50), 12},
		{strings.Repeat("日本語", 12), 10},
		{"a 👩‍💻 emoji and 日本語 text", 8},
	}
	for _, tc := range cases {
		lines := wrapHelpText(tc.text, tc.width)
		if len(lines) == 0 {
			t.Fatalf("wrapping %q produced nothing", tc.text)
		}
		for _, line := range lines {
			if got := lipgloss.Width(line); got > tc.width {
				t.Errorf("wrapped %q to %d cells, limit %d: %q", tc.text, got, tc.width, line)
			}
		}
	}
}

// TestHelpScrollsAndEscRestoresConversation checks the viewport
// contract: the title and exit instruction stay fixed, the body scrolls,
// and Esc returns to the same reading position and draft.
func TestHelpScrollsAndEscRestoresConversation(t *testing.T) {
	m := longConversation(t, 60)
	m = pressKey(m, tea.KeyPgUp, 0, "")
	m = paste(m, "draft survives help")
	beforeOffset := m.viewport.YOffset()
	beforeDraft := m.Composer().Value()
	if beforeOffset == 0 {
		t.Fatal("precondition: the conversation should be scrolled up")
	}

	m = openHelp(t, m)
	total := len(m.helpRows())
	body := m.helpBodyHeight()
	if total <= body {
		t.Fatalf("help body is %d rows for a %d-row viewport; scrolling cannot be tested", total, body)
	}
	rows := m.layoutRows()
	if !strings.Contains(stripANSI(strings.Join(rows, "\n")), "Keys and commands") {
		t.Fatalf("help title is not visible:\n%s", strings.Join(rows, "\n"))
	}

	for i := 0; i < total; i++ {
		m = pressKey(m, tea.KeyDown, 0, "")
	}
	if m.panelOffset == 0 {
		t.Fatal("Down did not scroll the help body")
	}
	rows = m.layoutRows()
	plain := stripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(plain, "Keys and commands") || !strings.Contains(plain, "Esc close") {
		t.Errorf("the title and exit instruction scrolled away:\n%s", plain)
	}
	if !strings.Contains(plain, "Permissions and file writes") {
		t.Errorf("the end of help is not reachable:\n%s", plain)
	}

	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.view != viewChat {
		t.Fatalf("Esc left view = %d, want chat", m.view)
	}
	if got := m.viewport.YOffset(); got != beforeOffset {
		t.Errorf("Esc changed the conversation offset: %d, want %d", got, beforeOffset)
	}
	if got := m.Composer().Value(); got != beforeDraft {
		t.Errorf("Esc changed the draft: %q, want %q", got, beforeDraft)
	}
}

// TestHelpScrollReachesEverySectionAtNarrowSize checks that the narrow
// reference terminal can reach the tail of the reference.
func TestHelpScrollReachesEverySectionAtNarrowSize(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m = updated.(Model)
	m = openHelp(t, m)
	total := len(m.helpRows())
	body := m.helpBodyHeight()
	// One row above the absolute end keeps the section heading in view
	// while still proving that the tail is reachable.
	m.panelOffset = total - body - 1
	m.clampHelpOffset()
	plain := stripANSI(strings.Join(m.layoutRows(), "\n"))
	for _, want := range []string{"Permissions and file writes", "666", "Ctrl+Y", "Ctrl+N", "Esc close"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the bottom of narrow help is missing %q:\n%s", want, plain)
		}
	}
	rowWidthsFit(t, m.layoutRows(), 60)
}

// TestHelpDoesNotChangePermissionState makes the promise explicit:
// reading help never starts, approves or rejects a write.
func TestHelpDoesNotChangePermissionState(t *testing.T) {
	m := testModel()
	before := m.WriteState()
	m = openHelp(t, m)
	if got := m.WriteState(); got != before {
		t.Errorf("opening help changed the write state: %d -> %d", before, got)
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	if got := m.WriteState(); got != before {
		t.Errorf("closing help changed the write state: %d -> %d", before, got)
	}
}
