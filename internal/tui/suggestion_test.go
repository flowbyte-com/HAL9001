package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// suggestionScreen is the rendered frame with styling removed.
func suggestionScreen(m Model) string {
	return stripANSI(string(m.View().Content))
}

// TestSuggestionsExposeEveryCommand checks that the panel is complete,
// that it is capped to a readable height, and that scrolling reaches the
// commands at the end of the registry.
func TestSuggestionsExposeEveryCommand(t *testing.T) {
	for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 30}, {170, 38}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := testModel()
			updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = updated.(Model)
			m = typeText(m, "/")

			if !m.suggestOpen {
				t.Fatal("/ did not open the suggestions")
			}
			if got, want := len(m.suggestItems), len(commandTable); got != want {
				t.Fatalf("suggestions hold %d commands, want %d", got, want)
			}
			if got := m.suggestRows(); got > maxSuggestions || got > m.suggestionLimit() {
				t.Errorf("the panel reserves %d rows, limit %d", got, m.suggestionLimit())
			}
			if got := m.suggestRows(); got > size[1]/2 {
				t.Errorf("the panel takes %d of %d rows", got, size[1])
			}
			initial := suggestionScreen(m)
			if !strings.Contains(initial, "more") || !strings.Contains(initial, "scroll") {
				t.Errorf("the overflowing panel does not advertise more results:\n%s", initial)
			}

			for i := 0; i < len(commandTable)-1; i++ {
				m = pressKey(m, tea.KeyDown, 0, "")
			}
			if got, want := m.suggestCursor, len(commandTable)-1; got != want {
				t.Fatalf("cursor = %d, want %d", got, want)
			}
			view := suggestionScreen(m)
			for _, want := range []string{"/skills", "/skill", "/collapse", "above", "scroll"} {
				if !strings.Contains(view, want) {
					t.Errorf("the scrolled panel is missing %q:\n%s", want, view)
				}
			}
			for _, row := range m.layoutRows() {
				if got := lipgloss.Width(row); got > size[0] {
					t.Errorf("suggestion row is %d cells, terminal is %d: %q", got, size[0], stripANSI(row))
				}
			}
			if got := m.Composer().Value(); got != "/" {
				t.Errorf("scrolling the suggestions changed the draft: %q", got)
			}
			if len(m.Lines()) != 0 {
				t.Errorf("scrolling the suggestions submitted something: %v", m.Lines())
			}
		})
	}
}

// TestSuggestionsFilterSkills checks that both MPM commands are found
// and that their descriptions are aligned.
func TestSuggestionsFilterSkills(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m = updated.(Model)
	m = typeText(m, "/sk")

	if len(m.suggestItems) != 2 {
		t.Fatalf("/sk matched %d commands, want 2: %v", len(m.suggestItems), m.suggestItems)
	}
	view := suggestionScreen(m)
	for _, want := range []string{"/skills", "/skill", "List the available skills", "Pin a skill"} {
		if !strings.Contains(view, want) {
			t.Errorf("/sk is missing %q:\n%s", want, view)
		}
	}

	// Tab completes the highlighted command. /skills takes no argument;
	// /skill does, and gets the space that invites its argument.
	if got := m.suggestItems[m.suggestCursor].name; got != "/skills" {
		t.Fatalf("first match = %q, want /skills", got)
	}
	m = pressKey(m, tea.KeyTab, 0, "")
	if got := m.Composer().Value(); got != "/skills" {
		t.Errorf("Tab completed %q, want /skills", got)
	}

	m2 := testModel()
	updated, _ = m2.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m2 = updated.(Model)
	m2 = typeText(m2, "/sk")
	m2 = pressKey(m2, tea.KeyDown, 0, "")
	m2 = pressKey(m2, tea.KeyTab, 0, "")
	if got := m2.Composer().Value(); got != "/skill " {
		t.Errorf("Tab completed %q, want %q", got, "/skill ")
	}
}

// TestSuggestionCancelKeepsDraft checks that moving the selection and
// cancelling never loses what was typed.
func TestSuggestionCancelKeepsDraft(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/sk")
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.suggestOpen {
		t.Error("Esc did not close the suggestions")
	}
	if got := m.Composer().Value(); got != "/sk" {
		t.Errorf("Esc lost the draft: %q", got)
	}
	if len(m.Lines()) != 0 {
		t.Errorf("Esc submitted the draft: %v", m.Lines())
	}
}

// TestSuggestionNarrowTerminalsFit checks that the panel drops the
// description rather than overflowing when there is no room for two
// columns.
func TestSuggestionNarrowTerminalsFit(t *testing.T) {
	for _, width := range []int{24, 32, 36, 40, 60} {
		m := testModel()
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 20})
		m = updated.(Model)
		m = typeText(m, "/")
		view := suggestionScreen(m)
		if !strings.Contains(view, "/help") {
			t.Errorf("width %d lost the first command:\n%s", width, view)
		}
		for _, row := range m.layoutRows() {
			if got := lipgloss.Width(row); got > width {
				t.Errorf("width %d: row is %d cells: %q", width, got, stripANSI(row))
			}
		}
	}
}
