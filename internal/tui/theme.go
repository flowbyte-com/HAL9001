package tui

import (
	"image/color"

	lipgloss "charm.land/lipgloss/v2"
)

// Theme is one named appearance for the whole interface. Colours are
// semantic: every view derives its styles from the same four roles, so a
// theme change applies consistently to the header, conversation,
// composer, overlays and footer.
type Theme struct {
	Name       string
	Background string
	Text       string
	Accent     string
	Muted      string
}

// Preset themes. HAL Classic is the default.
var (
	ThemeHALClassic = Theme{Name: "HAL Classic", Background: "#121316", Text: "#E5E7EB", Accent: "#EC5359", Muted: "#8A8F98"}
	ThemeMidnight   = Theme{Name: "Midnight", Background: "#101C2D", Text: "#D7E7F7", Accent: "#75B9EE", Muted: "#5E7A93"}
	ThemeAmber      = Theme{Name: "Amber", Background: "#211B12", Text: "#F7DFAE", Accent: "#E6AA46", Muted: "#8A7551"}
	ThemePaper      = Theme{Name: "Paper", Background: "#F4F1E9", Text: "#323337", Accent: "#B84347", Muted: "#8A8578"}
	ThemeMono       = Theme{Name: "Monochrome", Background: "#111111", Text: "#EDEDED", Accent: "#FFFFFF", Muted: "#8A8A8A"}
)

// Themes lists the presets in picker order.
func Themes() []Theme {
	return []Theme{ThemeHALClassic, ThemeMidnight, ThemeAmber, ThemePaper, ThemeMono}
}

// ThemeByName resolves a preset, defaulting to HAL Classic.
func ThemeByName(name string) Theme {
	for _, t := range Themes() {
		if t.Name == name {
			return t
		}
	}
	return ThemeHALClassic
}

// styles are the theme-derived lipgloss styles for one view render.
type styles struct {
	header lipgloss.Style
	user   lipgloss.Style
	hal    lipgloss.Style
	rule   lipgloss.Style
	footer lipgloss.Style
	active lipgloss.Style
	notice lipgloss.Style
}

func (t Theme) styles() styles {
	fg := func(c string) color.Color { return lipgloss.Color(c) }
	return styles{
		header: lipgloss.NewStyle().Bold(true).Foreground(fg(t.Accent)),
		user:   lipgloss.NewStyle().Bold(true).Foreground(fg(t.Accent)),
		hal:    lipgloss.NewStyle().Bold(true).Foreground(fg(t.Text)),
		rule:   lipgloss.NewStyle().Foreground(fg(t.Muted)),
		footer: lipgloss.NewStyle().Foreground(fg(t.Muted)),
		active: lipgloss.NewStyle().Bold(true).Foreground(fg(t.Accent)),
		notice: lipgloss.NewStyle().Foreground(fg(t.Muted)),
	}
}
