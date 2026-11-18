package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// HAL presents itself as a raised 9001 in front of the word.
//
// The digits are superscript characters, so the font draws them small
// and raised: the 9001 reads as a superscript of HAL and not as another
// word, and it is never a full-size part of the name.
func TestTheWordmarkCarriesARaised9001(t *testing.T) {
	if !strings.HasPrefix(Wordmark, "⁹⁰⁰¹") {
		t.Errorf("the wordmark does not begin with the raised 9001: %q", Wordmark)
	}
	if !strings.HasSuffix(Wordmark, "HAL") {
		t.Errorf("the wordmark does not end with HAL: %q", Wordmark)
	}
	// The 9001 comes before the H, and the H is a plain capital.
	rest := strings.TrimPrefix(Wordmark, "⁹⁰⁰¹")
	if rest != "HAL" {
		t.Errorf("what follows the raised digits is %q, want HAL", rest)
	}
	// The digits are the superscript codepoints, not ASCII digits: a
	// full-size 9001 would be the suffix this wordmark replaces.
	for _, r := range Wordmark {
		switch r {
		case '⁹', '⁰', '¹':
		case '0', '1', '9':
			t.Errorf("the wordmark carries a full-size digit %q: %q", r, Wordmark)
		case 'H', 'A', 'L':
		default:
			t.Errorf("the wordmark carries an unexpected character %q: %q", r, Wordmark)
		}
	}
}

// The wordmark is the same width as the plain name it replaces, so the
// header's geometry does not move.
func TestTheWordmarkKeepsTheWidthOfTheNameItReplaces(t *testing.T) {
	if got, want := lipgloss.Width(Wordmark), lipgloss.Width("HAL9001"); got != want {
		t.Errorf("the wordmark is %d columns wide, want %d", got, want)
	}
}

// The header carries the wordmark, and never the old suffix.
func TestTheHeaderShowsTheWordmark(t *testing.T) {
	m := testModel()
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	header := stripANSI(m.headerRow())
	if !strings.Contains(header, Wordmark) {
		t.Errorf("the header does not carry the wordmark: %q", header)
	}
	if strings.Contains(header, "HAL9001") {
		t.Errorf("the header still carries the old name: %q", header)
	}
	// The eye stays in front of the word.
	if !strings.HasPrefix(header, "🔴 ") {
		t.Errorf("the header does not start with the eye: %q", header)
	}
}

// The title still fits beside the wordmark: the raised digits take the
// same room the plain digits did.
func TestTheHeaderStaysAlignedWithTheWordmark(t *testing.T) {
	for _, width := range []int{20, 40, 80, 120} {
		m := testModel()
		m.SetColor(false)
		m.SetTitle("a very long conversation title that will not fit in a narrow terminal")
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = updated.(Model)
		header := stripANSI(m.headerRow())
		if lipgloss.Width(header) > width {
			t.Errorf("width %d: the header is %d cells wide: %q", width, lipgloss.Width(header), header)
		}
		if !strings.Contains(header, Wordmark) {
			t.Errorf("width %d: the identity is missing: %q", width, header)
		}
	}
}

// The theme preview shows the same mark the header does.
func TestTheThemePreviewShowsTheWordmark(t *testing.T) {
	m := testModel()
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	m.enterTheme()
	rows := m.themeRows()
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, Wordmark) {
		t.Errorf("the theme preview does not carry the wordmark:\n%s", joined)
	}
	if strings.Contains(joined, "HAL9001") {
		t.Errorf("the theme preview still carries the old name:\n%s", joined)
	}
}
