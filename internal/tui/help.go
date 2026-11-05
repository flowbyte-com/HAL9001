package tui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/rivo/uniseg"
)

// The help panel is a reference, not a conversation. It is built from
// the same command table as dispatch and autocomplete, with the keys
// grouped beside the commands they belong to. Every column is measured
// in terminal cells, so a wide character or a colour code can never
// push a description into the next column.

// helpEntry is one labelled line of help: a command or a key, and what
// it does.
type helpEntry struct {
	label       string
	description string
}

// helpSection is one labelled group of entries.
type helpSection struct {
	title   string
	entries []helpEntry
}

const (
	// helpIndent is the left margin of the panel.
	helpIndent = 2
	// helpColumnGap separates a label from its description.
	helpColumnGap = 2
	// helpMinDescription is the narrowest description that still reads
	// as a sentence rather than a stack of single words.
	helpMinDescription = 8
)

// helpCommandSection assigns every command in commandTable to one
// section. A command that is added without a group is still shown: the
// fallback below keeps it visible in the first section rather than
// silently dropping it.
var helpCommandSection = map[string]string{
	"/help":     "Conversation and editing",
	"/clear":    "Conversation and editing",
	"/expand":   "Conversation and editing",
	"/collapse": "Conversation and editing",

	"/copy": "Navigation and clipboard",

	"/new":        "Sessions and preferences",
	"/rename":     "Sessions and preferences",
	"/sessions":   "Sessions and preferences",
	"/permission": "Sessions and preferences",
	"/model":      "Sessions and preferences",
	"/effort":     "Sessions and preferences",
	"/theme":      "Sessions and preferences",
	"/usage":      "Sessions and preferences",
	"/quit":       "Sessions and preferences",

	"/skills": "MPM skills and diagnostics",
	"/skill":  "MPM skills and diagnostics",
	"/diag":   "MPM skills and diagnostics",
}

// helpSectionTitles is the fixed order of the groups.
var helpSectionTitles = []string{
	"Conversation and editing",
	"Navigation and clipboard",
	"Sessions and preferences",
	"MPM skills and diagnostics",
	"Permissions and file writes",
}

// helpSections assembles the panel from the key reference and the
// command registry. Commands come first within a section so the fixed
// vocabulary is visible before the shortcuts that operate on it.
func (m Model) helpSections() []helpSection {
	sections := make([]helpSection, 0, len(helpSectionTitles))
	index := make(map[string]int, len(helpSectionTitles))
	for i, title := range helpSectionTitles {
		index[title] = i
		sections = append(sections, helpSection{title: title})
	}

	// Keys and explanatory entries, grouped by the same headings.
	sections[index["Conversation and editing"]].entries = []helpEntry{
		{"Enter", "send the message"},
		{keyName(keySend), "send (alternative)"},
		{keyName(keyNewline), "newline (any terminal)"},
		{"Alt+Enter", "newline (xterm-style terminals)"},
		{"Ctrl+O", "expand or collapse the last long prompt"},
		{keyName(keyClearDraft), "clear the draft"},
		{keyName(keyRestoreDraft), "restore the last cleared draft"},
		{"Esc", "cancel a running reply or close a panel"},
		{"/", "open command suggestions"},
		{"//", "send a literal slash-prefixed message"},
		{"paste", "insert text without sending"},
	}
	sections[index["Navigation and clipboard"]].entries = []helpEntry{
		{"PgUp/PgDn", "scroll the conversation a page"},
		{"Home/End", "jump to the start or end of the conversation"},
		{keyName(keyJumpLatest), "jump to the latest message"},
		{"wheel", "scroll history, or the draft over the composer"},
		{"drag", "select text and copy it on release"},
	}
	sections[index["Sessions and preferences"]].entries = []helpEntry{
		{keyName(keyPicker), "open the model picker"},
		{keyName(keyPermission), "open the 444/666 permission control"},
		{keyName(keyQuit) + "/" + keyName(keyQuit2), "quit HAL9001"},
	}
	sections[index["MPM skills and diagnostics"]].entries = []helpEntry{
		{"MPM", "read-only: HAL lists and reads skills, never writes them"},
	}
	sections[index["Permissions and file writes"]].entries = []helpEntry{
		{"444", "read-only: conversation, MPM knowledge, Firecrawl web research and one-off write approval"},
		{"666", "session-scoped unrestricted mode: read, write, edit and bash run with your OS permissions"},
		{keyName(keyPermission), "open the permission control; only you can enable or disable 666"},
		{keyName(keyApproveWrite), "enable 666 (permission screen) or approve one reviewed write (approval screen)"},
		{keyName(keyRejectWrite), "disable 666 (permission screen) or reject the pending one-off write (approval screen)"},
		{"Esc", "close the permission screen or reject the pending one-off write"},
	}

	// Commands are appended from the registry, in table order. The
	// first section is the fallback, so a new command is never lost.
	commands := make([][]helpEntry, len(sections))
	for _, c := range commandTable {
		entry := helpEntry{commandLabel(c), c.description}
		i, ok := index[helpCommandSection[c.name]]
		if !ok {
			i = 0
		}
		commands[i] = append(commands[i], entry)
	}
	for i := range sections {
		if len(commands[i]) == 0 {
			continue
		}
		entries := make([]helpEntry, 0, len(commands[i])+len(sections[i].entries))
		entries = append(entries, commands[i]...)
		entries = append(entries, sections[i].entries...)
		sections[i].entries = entries
	}
	return sections
}

// commandLabel is the form shown in help and suggestions: the usage when
// the command takes arguments, otherwise its name.
func commandLabel(c *command) string {
	if c.usage != "" {
		return c.usage
	}
	return c.name
}

// helpRows renders the scrollable body of the panel. The title row and
// the exit instruction are drawn by the frame, not here, so they stay
// fixed while this body scrolls.
func (m Model) helpRows() []string {
	width := m.width
	if width < 1 {
		width = 80
	}
	sections := m.helpSections()

	// One column at every width: the command and key reference is a
	// single vertical list, so sections never interleave across the
	// terminal and a description can never be mistaken for belonging to
	// a label in another column.
	rows := m.helpOneColumnRows(sections, width)

	// The terminal note is part of the reference, not decoration.
	if note := m.helpTerminalNote(); note != "" {
		rows = append(rows, "")
		for _, line := range wrapHelpText(note, width-helpIndent) {
			rows = append(rows, strings.Repeat(" ", helpIndent)+line)
		}
	}
	return rows
}

// helpTerminalNote explains the one key whose meaning depends on what
// the terminal reports, using the same detection the input layer uses.
func (m Model) helpTerminalNote() string {
	if m.keyDisambiguation {
		return "This terminal reports modifier combinations: Shift+Enter inserts a newline."
	}
	return "This terminal does not report modifier combinations: Shift+Enter sends; use Ctrl+J for a newline."
}

// helpTitleRow is the fixed heading of the panel: it names the view and
// keeps the way out visible while the body scrolls.
func (m Model) helpTitleRow() string {
	variants := []string{
		"Help · Keys and commands · ↑/↓ scroll · Esc close",
		"Help · Keys and commands · Esc close",
		"Help · Esc close",
	}
	text := variants[len(variants)-1]
	for _, variant := range variants {
		if m.width < 1 || lipgloss.Width(variant) <= m.width {
			text = variant
			break
		}
	}
	if !m.color {
		return text
	}
	st := m.theme.styles()
	return st.header.Render("Help") + st.footer.Render(strings.TrimPrefix(text, "Help"))
}

// helpOneColumnRows renders the readable default: one aligned label and
// description per line, wrapping descriptions inside their own column.
func (m Model) helpOneColumnRows(sections []helpSection, width int) []string {
	labelWidth := helpLabelWidth(sections)
	descWidth := width - helpIndent - labelWidth - helpColumnGap
	if descWidth < helpMinDescription {
		return m.helpStackedRows(sections, width)
	}
	return m.helpBlockRows(sections, labelWidth, descWidth, helpIndent)
}

// helpStackedRows is the narrow fallback: the label gets its own row
// and the description is indented below it. Nothing is cut off, and no
// label can run into a description because they are never side by side.
func (m Model) helpStackedRows(sections []helpSection, width int) []string {
	st := m.theme.styles()
	descWidth := width - (helpIndent + helpColumnGap)
	if descWidth < 1 {
		descWidth = 1
	}
	var rows []string
	for i, section := range sections {
		if i > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, strings.Repeat(" ", helpIndent)+st.footer.Bold(true).Render(section.title))
		for _, entry := range section.entries {
			rows = append(rows, strings.Repeat(" ", helpIndent)+st.active.Render(entry.label))
			for _, line := range wrapHelpText(entry.description, descWidth) {
				rows = append(rows, strings.Repeat(" ", helpIndent+helpColumnGap)+line)
			}
		}
	}
	return rows
}

// helpBlockRows renders sections at a fixed label and description width.
// indent is the left margin inside the column.
func (m Model) helpBlockRows(sections []helpSection, labelWidth, descWidth, indent int) []string {
	st := m.theme.styles()
	var rows []string
	for i, section := range sections {
		if i > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, strings.Repeat(" ", indent)+st.footer.Bold(true).Render(section.title))
		for _, entry := range section.entries {
			lines := wrapHelpText(entry.description, descWidth)
			if len(lines) == 0 {
				lines = []string{""}
			}
			prefix := strings.Repeat(" ", indent)
			continuation := strings.Repeat(" ", indent+labelWidth+helpColumnGap)
			for j, line := range lines {
				if j == 0 {
					rows = append(rows, prefix+padTo(st.active.Render(entry.label), labelWidth)+
						strings.Repeat(" ", helpColumnGap)+line)
					continue
				}
				rows = append(rows, continuation+line)
			}
		}
	}
	return rows
}

// helpLabelWidth is the widest label in any section, measured in display
// cells so a coloured or wide label is padded correctly.
func helpLabelWidth(sections []helpSection) int {
	width := 0
	for _, section := range sections {
		for _, entry := range section.entries {
			if w := lipgloss.Width(entry.label); w > width {
				width = w
			}
		}
	}
	return width
}

// wrapHelpText wraps a description within width display cells. It keeps
// words whole where it can and breaks a word wider than the column by
// grapheme cluster, so Unicode and emoji cannot overflow the frame.
func wrapHelpText(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if width < 1 {
		width = 1
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line == "" {
			parts := splitHelpWord(word, width)
			lines = append(lines, parts[:len(parts)-1]...)
			line = parts[len(parts)-1]
			continue
		}
		if uniseg.StringWidth(line)+1+uniseg.StringWidth(word) <= width {
			line += " " + word
			continue
		}
		lines = append(lines, line)
		parts := splitHelpWord(word, width)
		lines = append(lines, parts[:len(parts)-1]...)
		line = parts[len(parts)-1]
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// splitHelpWord breaks one word into display-cell chunks. A grapheme
// cluster wider than the column is kept on a line of its own rather
// than being mangled.
func splitHelpWord(word string, width int) []string {
	if width < 1 {
		width = 1
	}
	if uniseg.StringWidth(word) <= width {
		return []string{word}
	}
	var parts []string
	line := ""
	lineWidth := 0
	graphemes := uniseg.NewGraphemes(word)
	for graphemes.Next() {
		cluster := graphemes.Str()
		clusterWidth := graphemes.Width()
		if line != "" && lineWidth+clusterWidth > width {
			parts = append(parts, line)
			line = ""
			lineWidth = 0
		}
		if line == "" && clusterWidth > width {
			parts = append(parts, cluster)
			continue
		}
		line += cluster
		lineWidth += clusterWidth
	}
	if line != "" {
		parts = append(parts, line)
	}
	if len(parts) == 0 {
		parts = []string{""}
	}
	return parts
}

// helpBodyHeight is the number of scrollable help rows between the fixed
// title row and the footer. It mirrors overlayLayout exactly so a page
// key moves what is actually on screen.
func (m Model) helpBodyHeight() int {
	n := overlayPanelHeight(m.height)
	if n > 1 {
		n--
	}
	if n < 1 {
		n = 1
	}
	return n
}

// clampHelpOffset keeps the scroll position inside the help body after
// a resize changes how many rows the content needs.
func (m *Model) clampHelpOffset() {
	total := len(m.helpRows())
	n := m.helpBodyHeight()
	maxOffset := total - n
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.panelOffset > maxOffset {
		m.panelOffset = maxOffset
	}
	if m.panelOffset < 0 {
		m.panelOffset = 0
	}
}
