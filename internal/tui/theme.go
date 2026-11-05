package tui

import (
	"image/color"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2/ansi"
	glamourstyles "charm.land/glamour/v2/styles"
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
	// Eye is the restrained red of HAL's assistant marker. It is a
	// separate semantic colour so every theme can keep the eye red
	// without recolouring the rest of its palette.
	Eye string
}

// Preset themes. HAL Classic is the default.
var (
	ThemeHALClassic = Theme{Name: "HAL Classic", Background: "#121316", Text: "#E5E7EB", Accent: "#EC5359", Muted: "#8A8F98", Eye: "#FF4D5A"}
	ThemeMidnight   = Theme{Name: "Midnight", Background: "#101C2D", Text: "#D7E7F7", Accent: "#75B9EE", Muted: "#5E7A93", Eye: "#FF6B6B"}
	ThemeAmber      = Theme{Name: "Amber", Background: "#211B12", Text: "#F7DFAE", Accent: "#E6AA46", Muted: "#8A7551", Eye: "#FF5A4E"}
	ThemePaper      = Theme{Name: "Paper", Background: "#F4F1E9", Text: "#323337", Accent: "#B84347", Muted: "#8A8578", Eye: "#B84347"}
	ThemeMono       = Theme{Name: "Monochrome", Background: "#111111", Text: "#EDEDED", Accent: "#FFFFFF", Muted: "#8A8A8A", Eye: "#FF5A5A"}
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
	eye    lipgloss.Style
	tool   lipgloss.Style
	rule   lipgloss.Style
	footer lipgloss.Style
	active lipgloss.Style
	notice lipgloss.Style
	// danger is the persistent 666 indicator. It is red on every theme
	// because it marks an authority increase, not a decorative accent.
	danger lipgloss.Style
}

// composerStyles maps the palette onto the composer's textarea.
//
// The textarea's own defaults carry a fixed palette: the line the caret
// is on is painted with a black background, which swallows the text on a
// light theme, and the body colour ignores the theme altogether. The
// composer is where the operator writes, so it follows the same palette
// as the rest of the interface.
func (t Theme) composerStyles() textarea.Styles {
	s := textarea.DefaultStyles(t.dark())
	body := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	cursorLine := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Text)).
		Background(lipgloss.Color(t.Background))
	placeholder := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	endOfBuffer := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Background))
	for _, state := range []*textarea.StyleState{&s.Focused, &s.Blurred} {
		state.Text = body
		state.CursorLine = cursorLine
		state.Placeholder = placeholder
		state.EndOfBuffer = endOfBuffer
	}
	s.Cursor = textarea.CursorStyle{
		Color: lipgloss.Color(t.Accent),
		Shape: tea.CursorBlock,
		Blink: true,
	}
	return s
}

func (t Theme) styles() styles {
	fg := func(c string) color.Color { return lipgloss.Color(c) }
	return styles{
		header: lipgloss.NewStyle().Bold(true).Foreground(fg(t.Accent)),
		user:   lipgloss.NewStyle().Bold(true).Foreground(fg(t.Text)),
		eye:    lipgloss.NewStyle().Bold(true).Foreground(fg(t.Eye)),
		tool:   lipgloss.NewStyle().Foreground(fg(t.Muted)),
		rule:   lipgloss.NewStyle().Foreground(fg(t.Muted)),
		footer: lipgloss.NewStyle().Foreground(fg(t.Muted)),
		active: lipgloss.NewStyle().Bold(true).Foreground(fg(t.Accent)),
		notice: lipgloss.NewStyle().Foreground(fg(t.Muted)),
		danger: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF3B30")),
	}
}

// dark reports whether the palette is a dark one, from the luminance of
// its own background rather than from its name.
func (t Theme) dark() bool {
	r, g, b, ok := parseHex(t.Background)
	if !ok {
		return true
	}
	lum := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
	return lum < 128
}

func parseHex(s string) (r, g, b int, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff), true
}

// glamourStyle maps the semantic palette onto glamour's Markdown style.
// Every colour-bearing rule is overridden, so the conversation body is
// themed too — the body is most of the screen, and leaving it on
// glamour's built-in palette is what made the themes look identical.
//
// Background colours are cleared: the view paints the terminal
// background, so Markdown must not paint a second one.
func (t Theme) glamourStyle() ansi.StyleConfig {
	cfg := glamourstyles.DarkStyleConfig
	if !t.dark() {
		cfg = glamourstyles.LightStyleConfig
	}
	text, accent, muted := t.Text, t.Accent, t.Muted

	// The conversation owns the left gutter now. Glamour's document and
	// code-block margins would add a second, invisible column and make
	// wrapped assistant text disagree with the user text.
	zero := uint(0)
	cfg.Document.Margin = &zero
	cfg.Document.BlockPrefix = ""
	cfg.Document.BlockSuffix = ""
	cfg.CodeBlock.Margin = &zero
	cfg.Document.Color = &text
	cfg.Document.BackgroundColor = nil
	cfg.Paragraph.Color = &text
	cfg.Heading.Color = &accent
	for _, h := range []*ansi.StyleBlock{&cfg.H1, &cfg.H2, &cfg.H3, &cfg.H4, &cfg.H5, &cfg.H6} {
		h.Color = &accent
		h.BackgroundColor = nil
		h.Prefix = "\u200b"
		h.Suffix = ""
	}
	cfg.Text.Color = &text
	cfg.Emph.Color = &text
	cfg.Strong.Color = &accent
	cfg.Strikethrough.Color = &muted
	cfg.Link.Color = &accent
	cfg.LinkText.Color = &accent
	cfg.Code.Color = &accent
	cfg.Code.Prefix = ""
	cfg.Code.Suffix = ""
	cfg.Code.BackgroundColor = nil
	cfg.CodeBlock.Color = &text
	cfg.CodeBlock.BackgroundColor = nil
	cfg.CodeBlock.Theme = "dracula"
	if !t.dark() {
		cfg.CodeBlock.Theme = "github"
	}
	cfg.Item.Color = &accent
	cfg.Enumeration.Color = &accent
	cfg.HorizontalRule.Color = &muted
	cfg.BlockQuote.Color = &muted
	cfg.BlockQuote.IndentToken = nil
	return cfg
}
