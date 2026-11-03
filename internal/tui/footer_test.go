package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// The footer row under the composer is contextual: it offers the keys
// that act on the draft while there is one, the undo hint while a cleared
// draft can be restored, and the ordinary permission line otherwise. It
// reuses the existing row, so nothing moves when the hint changes.

// footerText is the footer row of the rendered layout, without styling.
// The notice row sits below it when the frame keeps one.
func footerText(m Model) string {
	rows := m.layoutRows()
	i := len(rows) - 1
	if m.frame.notice {
		i--
	}
	if i < 0 || i >= len(rows) {
		return ""
	}
	return stripANSI(rows[i])
}

// draftHints is the exact hint the interface promises for a draft.
var draftHint = "Enter send · Ctrl+J newline · Ctrl+U clear · /help"

// TestFooterHintFollowsTheDraft checks every state of the draft against
// the footer it must show.
func TestFooterHintFollowsTheDraft(t *testing.T) {
	m := testModel()
	m.SetModelName("MiniMax-M3.1-Flash")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	if got := footerText(m); !strings.Contains(got, "444 READ ONLY · /help") {
		t.Errorf("empty composer footer = %q, want the minimal line", got)
	}
	if strings.Contains(footerText(m), "Ctrl+U") {
		t.Error("an empty composer must not offer Ctrl+U")
	}

	m = typeText(m, "hello")
	got := footerText(m)
	if !strings.Contains(got, draftHint) {
		t.Errorf("typed footer = %q, want %q", got, draftHint)
	}
	if !strings.Contains(got, "MiniMax-M3.1-Flash") {
		t.Errorf("typed footer lost the model: %q", got)
	}

	m = pressKey(m, 'u', tea.ModCtrl, "")
	got = footerText(m)
	if !strings.Contains(got, "Ctrl+Z restore · /help") {
		t.Errorf("cleared footer = %q, want the restore hint", got)
	}
	if strings.Contains(got, "Ctrl+U") {
		t.Errorf("a cleared draft must not offer Ctrl+U: %q", got)
	}

	m = pressKey(m, 'z', tea.ModCtrl, "")
	if got := footerText(m); !strings.Contains(got, draftHint) {
		t.Errorf("restored footer = %q, want %q", got, draftHint)
	}

	m = enterCurrent(m)
	if got := footerText(m); !strings.Contains(got, "444 READ ONLY · /help") {
		t.Errorf("submitted footer = %q, want the minimal line", got)
	}
}

// TestFooterHintFollowsAPaste checks that a paste and a deletion update
// the row without a keypress loop.
func TestFooterHintFollowsAPaste(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m = pasteText(m, numberedLines(3))
	if got := footerText(m); !strings.Contains(got, draftHint) {
		t.Errorf("after a paste: %q", got)
	}
	// Delete every character again.
	for i := 0; i < len(numberedLines(3)); i++ {
		m = pressKey(m, tea.KeyBackspace, 0, "")
	}
	if got := footerText(m); strings.Contains(got, "Ctrl+U") {
		t.Errorf("after deleting the draft: %q", got)
	}
}

// TestFooterHintUsesTheBoundKeys checks that the hint is built from the
// keys the interface actually handles, so a rebind cannot leave a stale
// hint behind.
func TestFooterHintUsesTheBoundKeys(t *testing.T) {
	if keyClearDraft != "ctrl+u" || keyRestoreDraft != "ctrl+z" {
		t.Fatalf("the draft keys changed: %q %q", keyClearDraft, keyRestoreDraft)
	}
	if got, want := keyName(keyClearDraft), "Ctrl+U"; got != want {
		t.Errorf("keyName(%q) = %q, want %q", keyClearDraft, got, want)
	}
	if got, want := keyName(keyRestoreDraft), "Ctrl+Z"; got != want {
		t.Errorf("keyName(%q) = %q, want %q", keyRestoreDraft, got, want)
	}
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m = typeText(m, "x")
	if got := footerText(m); !strings.Contains(got, keyName(keyClearDraft)+" clear") {
		t.Errorf("the hint does not use the bound clear key: %q", got)
	}
	m = pressKey(m, 'u', tea.ModCtrl, "")
	if got := footerText(m); !strings.Contains(got, keyName(keyRestoreDraft)+" restore") {
		t.Errorf("the hint does not use the bound restore key: %q", got)
	}
}

// TestFooterHintNeverChangesTheFrame checks that the contextual row adds
// no height and moves nothing.
func TestFooterHintNeverChangesTheFrame(t *testing.T) {
	m := testModel()
	m.SetModelName("MiniMax-M3.1-Flash")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	height, viewport := len(m.layoutRows()), m.frame.viewport
	offset := m.viewport.YOffset()

	m = typeText(m, "a longer draft line that changes the hint")
	if got := len(m.layoutRows()); got != height {
		t.Errorf("the layout grew to %d rows, want %d", got, height)
	}
	if m.frame.viewport != viewport {
		t.Errorf("the conversation shrank to %d rows, want %d", m.frame.viewport, viewport)
	}
	if m.viewport.YOffset() != offset {
		t.Errorf("the viewport moved: %d -> %d", offset, m.viewport.YOffset())
	}
}

// TestFooterHintAtEveryWidth checks that the hint stays legible: the row
// never exceeds the terminal, and Ctrl+U is visible while a draft exists.
func TestFooterHintAtEveryWidth(t *testing.T) {
	for _, width := range []int{120, 100, 80, 58, 40, 30, 20, 14, 12, 8} {
		m := testModel()
		m.SetModelName("MiniMax-M3.1-Flash")
		m.SetEffort("medium", true)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = updated.(Model)
		m = typeText(m, "hello")
		got := footerText(m)
		if w := lipgloss.Width(got); w > width {
			t.Errorf("width %d: footer is %d cells wide: %q", width, w, got)
		}
		if !strings.Contains(got, keyName(keyClearDraft)) {
			t.Errorf("width %d: the clear key is missing: %q", width, got)
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("width %d: the footer is empty", width)
		}
	}
}

// TestFooterHintOnEveryTheme checks that the row is styled by the palette
// and still says the same thing.
func TestFooterHintOnEveryTheme(t *testing.T) {
	for _, theme := range Themes() {
		m := testModel()
		m.SetColor(true)
		m.SetTheme(theme)
		m.SetModelName("MiniMax-M3.1-Flash")
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m = updated.(Model)
		m = typeText(m, "hello")
		row := ""
		for _, r := range m.layoutRows() {
			if strings.Contains(stripANSI(r), "Ctrl+U") {
				row = r
			}
		}
		if row == "" {
			t.Fatalf("%s: no hint row", theme.Name)
		}
		if !strings.Contains(row, "\x1b[") {
			t.Errorf("%s: the hint is not styled", theme.Name)
		}
		if !strings.Contains(stripANSI(row), draftHint) {
			t.Errorf("%s: hint = %q", theme.Name, stripANSI(row))
		}
		// The row must fit the terminal in display cells.
		if w := lipgloss.Width(stripANSI(row)); w > 100 {
			t.Errorf("%s: footer is %d cells wide", theme.Name, w)
		}
	}
}
