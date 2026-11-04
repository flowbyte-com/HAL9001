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

// displayColumn is the zero-based terminal cell where fragment starts in
// row, after removing styling. It measures display width, not bytes, so a
// wide character or a colour code cannot disguise a collision.
func displayColumn(row, fragment string) int {
	plain := stripANSI(row)
	at := strings.Index(plain, fragment)
	if at < 0 {
		return -1
	}
	return lipgloss.Width(plain[:at])
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
			at := displayColumn(plain, fragment)
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

// TestHelpStaysOneColumnAndAligned is the display-cell contract: the
// reference is one vertical list at every width, each label starts at the
// same column, each description starts at the same later column, and no
// content starts in a second column.
func TestHelpStaysOneColumnAndAligned(t *testing.T) {
	sizes := [][2]int{{60, 20}, {80, 24}, {120, 30}, {170, 38}}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := testModel()
			updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = updated.(Model)
			m = openHelp(t, m)

			sections := m.helpSections()
			labelWidth := helpLabelWidth(sections)
			descAt := helpIndent + labelWidth + helpColumnGap
			rows := m.helpRows()
			rowWidthsFit(t, rows, size[0])
			plainRows := make([]string, len(rows))
			for i, row := range rows {
				plainRows[i] = stripANSI(row)
			}

			// Sections are a vertical sequence, one heading per row.
			last := -1
			for _, title := range helpSectionTitles {
				found := -1
				for i, row := range plainRows {
					if strings.Contains(row, title) {
						if col := displayColumn(row, title); col != helpIndent {
							t.Errorf("%q starts at cell %d, want %d: %q",
								title, col, helpIndent, row)
						}
						if found < 0 {
							found = i
						}
					}
				}
				if found < 0 {
					t.Errorf("missing section %q", title)
					continue
				}
				if found <= last {
					t.Errorf("section %q appears before the previous section", title)
				}
				last = found
				count := 0
				for _, row := range plainRows {
					if strings.Contains(row, title) {
						count++
					}
				}
				if count != 1 {
					t.Errorf("section %q appears on %d rows, want 1", title, count)
				}
			}

			// A content row starts either with a label at helpIndent or
			// with a wrapped description at descAt. Anything starting in
			// a second column is a collision.
			for i, row := range plainRows {
				trimmed := strings.TrimLeft(row, " ")
				if trimmed == "" {
					continue
				}
				col := lipgloss.Width(row[:len(row)-len(trimmed)])
				if col != helpIndent && col != descAt {
					t.Errorf("row %d starts at cell %d, want %d or %d: %q",
						i, col, helpIndent, descAt, row)
				}
			}

			// Every registered command is in the one label column.
			for _, c := range commandTable {
				label := commandLabel(c)
				found := false
				for _, row := range plainRows {
					trimmed := strings.TrimLeft(row, " ")
					if strings.HasPrefix(trimmed, label+" ") || trimmed == label {
						if col := displayColumn(row, label); col != helpIndent {
							t.Errorf("%q starts at cell %d, want %d: %q",
								label, col, helpIndent, row)
						}
						found = true
						break
					}
				}
				if !found {
					t.Errorf("command %q is not in the label column", label)
				}
			}
		})
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
	rows := m.helpRows()
	body := m.helpBodyHeight()
	heading := -1
	for i, row := range rows {
		if strings.Contains(stripANSI(row), "Permissions and file writes") {
			heading = i
			break
		}
	}
	if heading < 0 {
		t.Fatal("help has no permissions section")
	}
	m.panelOffset = heading
	if m.panelOffset > len(rows)-body {
		m.panelOffset = len(rows) - body
	}
	m.clampHelpOffset()
	plain := stripANSI(strings.Join(m.layoutRows(), "\n"))
	if !strings.Contains(plain, "Permissions and file writes") {
		t.Errorf("the permissions heading is not reachable:\n%s", plain)
	}
	// The tail carries the controls, even if the heading has scrolled off.
	m.panelOffset = len(rows) - body
	m.clampHelpOffset()
	plain = stripANSI(strings.Join(m.layoutRows(), "\n"))
	for _, want := range []string{"666", "Ctrl+Y", "Ctrl+N", "Esc close"} {
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
