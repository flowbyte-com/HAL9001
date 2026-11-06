package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// command is one local slash command. The table below is the single
// source of truth: dispatch, the suggestion panel and /help all read it,
// so they cannot drift apart.
type command struct {
	name        string
	aliases     []string
	usage       string
	description string
	takesArgs   bool
	run         func(m *Model, args []string) tea.Cmd
}

// commandTable lists every local command in suggestion order.
var commandTable = []*command{
	{
		name:        "/help",
		description: "Show available commands",
		run: func(m *Model, _ []string) tea.Cmd {
			m.view = viewHelp
			m.panelOffset = 0
			return nil
		},
	},
	{
		name:        "/copy",
		usage:       "/copy [code]",
		description: "Copy the last response (/copy code)",
		takesArgs:   true,
		run: func(m *Model, args []string) tea.Cmd {
			if len(args) > 0 && args[0] == "code" {
				return m.copyCmd(true)
			}
			if len(args) > 0 {
				m.notice = "Usage: /copy [code]"
				return nil
			}
			return m.copyCmd(false)
		},
	},
	{
		name:        "/clear",
		description: "Start a fresh conversation",
		run: func(m *Model, args []string) tea.Cmd {
			if len(args) > 0 {
				m.notice = "Usage: /clear"
				return nil
			}
			m.clearConversation()
			return nil
		},
	},
	{
		name:        "/rename",
		usage:       "/rename [title]",
		description: "Rename this conversation",
		takesArgs:   true,
		run: func(m *Model, args []string) tea.Cmd {
			if len(args) == 0 {
				m.enterRename()
				return nil
			}
			m.applyRename(strings.Join(args, " "))
			return nil
		},
	},
	{
		name:        "/usage",
		description: "Session token usage",
		run: func(m *Model, _ []string) tea.Cmd {
			m.view = viewUsage
			m.panelOffset = 0
			return nil
		},
	},
	{
		name:        "/quit",
		aliases:     []string{"/q", "/exit"},
		description: "Quit HAL9001 (/q, /exit)",
		run: func(m *Model, _ []string) tea.Cmd {
			return m.quit()
		},
	},
	{
		name:        "/model",
		description: "Select a model",
		run: func(m *Model, _ []string) tea.Cmd {
			m.enterPicker()
			return nil
		},
	},
	{
		name:        "/effort",
		usage:       "/effort [level]",
		description: "Change thinking level",
		takesArgs:   true,
		run: func(m *Model, args []string) tea.Cmd {
			if len(args) == 0 {
				m.enterEffort()
				return nil
			}
			if len(args) > 1 {
				m.notice = "Usage: /effort [low|medium|high|xhigh|max]"
				return nil
			}
			m.applyEffort(args[0])
			return nil
		},
	},
	{
		name:        "/theme",
		description: "Change appearance",
		run: func(m *Model, _ []string) tea.Cmd {
			m.enterTheme()
			return nil
		},
	},
	{
		name:        "/tool-icons",
		usage:       "/tool-icons [classic|emoji]",
		description: "Switch tool icons (emoji default)",
		takesArgs:   true,
		run: func(m *Model, args []string) tea.Cmd {
			if len(args) == 0 {
				m.notice = "Tool icons: " + m.toolIconsLabel() + " · choose classic or emoji."
				return nil
			}
			if len(args) > 1 {
				m.notice = "Usage: /tool-icons [classic|emoji]"
				return nil
			}
			switch strings.ToLower(args[0]) {
			case "classic":
				m.applyToolIcons(toolIconsClassic)
				m.notice = "Tool icons: classic."
			case "emoji":
				m.applyToolIcons(toolIconsEmoji)
				m.notice = "Tool icons: emoji."
			default:
				m.notice = "Usage: /tool-icons [classic|emoji]"
			}
			return nil
		},
	},
	{
		name:        "/sessions",
		description: "Resume a conversation",
		run: func(m *Model, _ []string) tea.Cmd {
			m.enterSessions()
			return nil
		},
	},
	{
		name:        "/permission",
		description: "Switch between 444 and session-scoped 666",
		run: func(m *Model, _ []string) tea.Cmd {
			m.enterPermission()
			return nil
		},
	},
	{
		name:        "/diag",
		description: "Show provider diagnostics",
		run: func(m *Model, _ []string) tea.Cmd {
			m.view = viewDiag
			m.panelOffset = 0
			return nil
		},
	},
	{
		name:        "/new",
		description: "New conversation",
		run: func(m *Model, _ []string) tea.Cmd {
			m.newSession()
			return nil
		},
	},
	{
		name:        "/expand",
		description: "Expand long prompts",
		run: func(m *Model, _ []string) tea.Cmd {
			m.setAllUserExpanded(true)
			m.notice = "Long messages expanded."
			return nil
		},
	},
	{
		name:        "/skills",
		description: "List the available skills",
		run: func(m *Model, _ []string) tea.Cmd {
			m.view = viewSkills
			m.panelOffset = 0
			return nil
		},
	},
	{
		name:        "/skill",
		usage:       "/skill <name>|auto",
		description: "Pin a skill for the writing task (/skill auto restores routing)",
		takesArgs:   true,
		run: func(m *Model, args []string) tea.Cmd {
			return m.skillCmd(args)
		},
	},
	{
		name:        "/collapse",
		description: "Collapse long prompts",
		run: func(m *Model, _ []string) tea.Cmd {
			m.setAllUserExpanded(false)
			m.notice = "Long messages collapsed."
			return nil
		},
	},
}

// commandByName finds a command by its name or one of its aliases.
func commandByName(name string) (*command, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, c := range commandTable {
		if c.name == name {
			return c, true
		}
		for _, alias := range c.aliases {
			if alias == name {
				return c, true
			}
		}
	}
	return nil, false
}

// matches reports whether a command matches a typed prefix by name or
// alias, so "/q" finds the quit command.
func (c *command) matches(prefix string) bool {
	if strings.HasPrefix(c.name, prefix) {
		return true
	}
	for _, alias := range c.aliases {
		if strings.HasPrefix(alias, prefix) {
			return true
		}
	}
	return false
}

// suggestionList filters the table by the leading token of the composer.
// Suggestions appear only for a single-line "/word" draft: once the
// operator types a space they are writing arguments, not a command name,
// and the panel would only take space away from the conversation.
func suggestionList(value string) []*command {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return nil
	}
	if strings.Contains(value, "\n") || strings.Contains(value, " ") || strings.Contains(value, "\t") {
		return nil
	}
	prefix := strings.ToLower(value)
	out := make([]*command, 0, len(commandTable))
	for _, c := range commandTable {
		if c.matches(prefix) {
			out = append(out, c)
		}
	}
	return out
}

// dispatchSlash handles a leading-slash command locally. Slash input is
// always consumed here and never reaches the engine, so commands are
// never submitted as user messages. A leading double slash (//help) is
// unescaped by the caller and sent as an ordinary message.
func (m *Model) dispatchSlash(input string) tea.Cmd {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return nil
	}
	cmd, args := fields[0], fields[1:]
	c, ok := commandByName(cmd)
	if !ok {
		m.notice = "Unknown command " + cmd + " — try /help."
		return nil
	}
	return c.run(m, args)
}

// refreshSuggestions recomputes the panel from the composer text and
// keeps the selection on the same command when it is still listed.
func (m *Model) refreshSuggestions() {
	value := m.composer.Value()
	if m.suggestDismissed && value != m.suggestDismissedValue {
		// Editing the draft is a new intent; allow the panel to open
		// again. Non-edit updates, such as a cursor blink, leave the
		// dismissal intact.
		m.suggestDismissed = false
		m.suggestDismissedValue = ""
	}
	if m.suggestDismissed {
		return
	}
	before := m.suggestRows()
	items := suggestionList(value)
	selected := ""
	if m.suggestOpen && m.suggestCursor >= 0 && m.suggestCursor < len(m.suggestItems) {
		selected = m.suggestItems[m.suggestCursor].name
	}
	m.suggestItems = items
	m.suggestOpen = len(items) > 0
	m.suggestCursor = 0
	for i, c := range items {
		if c.name == selected {
			m.suggestCursor = i
		}
	}
	if m.suggestRows() != before {
		m.resizeFrame()
	}
}

// dismissSuggestions hides the panel and suppresses it until the draft
// changes. Esc is a deliberate dismissal, not an invitation to reopen on
// the next cursor blink.
func (m *Model) dismissSuggestions() {
	m.closeSuggestions()
	m.suggestDismissed = true
	m.suggestDismissedValue = m.composer.Value()
}

// closeSuggestions hides the panel without touching the draft.
func (m *Model) closeSuggestions() {
	changed := m.suggestOpen || len(m.suggestItems) > 0
	m.suggestOpen = false
	m.suggestItems = nil
	m.suggestCursor = 0
	m.suggestDismissed = false
	m.suggestDismissedValue = ""
	if changed {
		m.resizeFrame()
	}
}

// suggestionGap separates a command from its description.
const suggestionGap = 2

// suggestionLimit caps the panel so it never takes over the
// conversation. Eight rows is the most a full-size terminal offers; a
// short terminal offers a third of its own height instead.
func (m Model) suggestionLimit() int {
	limit := maxSuggestions
	if m.height > 0 {
		byHeight := m.height / 3
		if byHeight < 3 {
			byHeight = 3
		}
		if byHeight < limit {
			limit = byHeight
		}
	}
	if limit < 1 {
		limit = 1
	}
	return limit
}

// suggestRows is how many panel rows the frame should reserve, including
// the row that says more matches are available.
func (m Model) suggestRows() int {
	if !m.suggestOpen {
		return 0
	}
	n := len(m.suggestItems)
	if limit := m.suggestionLimit(); n > limit {
		n = limit
	}
	if n < 0 {
		n = 0
	}
	return n
}

// suggestionWindow resolves the visible slice of the list. One of the
// rows is given to the "more results" indicator when the list overflows,
// so the indicator never hides the selected command.
func (m Model) suggestionWindow(rows int) (start, count, above, below int) {
	total := len(m.suggestItems)
	if rows < 1 || total == 0 {
		return 0, 0, 0, total
	}
	count = rows
	if total > rows {
		if rows >= 2 {
			count = rows - 1
		} else {
			count = 1
		}
	}
	if count > total {
		count = total
	}
	cursor := m.suggestCursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		cursor = total - 1
	}
	if cursor >= count {
		start = cursor - count + 1
	}
	if maxStart := total - count; start > maxStart {
		start = maxStart
	}
	if start < 0 {
		start = 0
	}
	above = start
	below = total - (start + count)
	return start, count, above, below
}

// suggestionRows renders the panel: a marker, an aligned command column
// and a concise description, or only the command when the terminal is
// narrow. Rows never exceed the terminal width.
func (m Model) suggestionRows(n int) []string {
	st := m.theme.styles()
	items := m.suggestItems
	start, count, above, below := m.suggestionWindow(n)
	labelWidth := m.suggestionLabelWidth()
	rows := make([]string, 0, n)
	for i := start; i < start+count && i < len(items); i++ {
		c := items[i]
		label := commandLabel(c)
		marker := "  "
		if i == m.suggestCursor {
			marker = st.active.Render("▸ ")
		}
		line := marker
		if m.width >= 38 {
			// The label column is fixed for the whole list, and a long
			// syntax is shortened rather than allowed to push the
			// description out of its column. Dispatch still receives the
			// real command name and arguments.
			label = truncateCells(label, labelWidth)
			line += padTo(st.active.Render(label), labelWidth)
			room := m.width - 2 - labelWidth - suggestionGap
			if room > 0 {
				line += strings.Repeat(" ", suggestionGap) +
					st.footer.Render(truncateCells(c.description, room))
			}
		} else {
			label = truncateCells(label, m.width-2)
			line += st.active.Render(label)
		}
		rows = append(rows, m.fitWidth(line, m.width))
	}
	if above > 0 || below > 0 {
		rows = append(rows, m.suggestionMoreRow(above, below))
	}
	for len(rows) < n {
		rows = append(rows, "")
	}
	return rows
}

// suggestionLabelWidth is the command column for the current matches.
// It is computed from the whole list, not the visible slice, so the
// column does not jump while scrolling.
func (m Model) suggestionLabelWidth() int {
	width := 0
	for _, c := range m.suggestItems {
		if w := lipgloss.Width(commandLabel(c)); w > width {
			width = w
		}
	}
	if max := m.width - 2 - suggestionGap - 12; width > max {
		width = max
	}
	if width < 1 {
		width = 1
	}
	return width
}

// suggestionMoreRow tells the operator that the list continues, and in
// which direction.
func (m Model) suggestionMoreRow(above, below int) string {
	var text string
	switch {
	case above > 0 && below > 0:
		text = fmt.Sprintf("↑ %d above · ↓ %d below · ↑/↓ scroll", above, below)
	case below > 0:
		text = fmt.Sprintf("↓ %d more · ↑/↓ scroll", below)
	default:
		text = fmt.Sprintf("↑ %d more above · ↑/↓ scroll", above)
	}
	room := m.width - 2
	if room < 1 {
		room = 1
	}
	text = truncateCells(text, room)
	if m.color {
		text = m.theme.styles().footer.Render(text)
	}
	return m.fitWidth("  "+text, m.width)
}

// moveSuggestion moves the selection by delta, clamped to the visible
// panel.
func (m *Model) moveSuggestion(delta int) {
	if !m.suggestOpen || len(m.suggestItems) == 0 {
		return
	}
	m.suggestCursor += delta
	if m.suggestCursor < 0 {
		m.suggestCursor = 0
	}
	if m.suggestCursor >= len(m.suggestItems) {
		m.suggestCursor = len(m.suggestItems) - 1
	}
}

// completeSuggestion writes the highlighted command into the composer.
// Commands that take arguments get a trailing space so the operator can
// type the argument straight away.
func (m *Model) completeSuggestion() {
	if !m.suggestOpen || m.suggestCursor >= len(m.suggestItems) {
		return
	}
	c := m.suggestItems[m.suggestCursor]
	text := c.name
	if c.takesArgs {
		text += " "
	}
	m.composer.SetValue(text)
	m.composer.CursorEnd()
	m.refreshSuggestions()
}

type copyResultMsg struct {
	chars int
	label string
	err   error
}

// copyCmd copies the last assistant response (or its latest code block)
// via the configured clipboard. Copying runs off the event loop; it never
// modifies the conversation.
func (m *Model) copyCmd(codeOnly bool) tea.Cmd {
	text := m.lastResponse
	label := "response"
	if codeOnly {
		block, ok := latestCodeBlock(text)
		if !ok {
			m.notice = "No code block in the last response."
			return nil
		}
		text, label = block, "code block"
	}
	text = ansi.Strip(text)
	if strings.TrimSpace(text) == "" {
		m.notice = "Nothing to copy yet — no completed response."
		return nil
	}
	if m.hooks.Copy == nil {
		m.notice = "Clipboard unavailable: no copy handler (use terminal-native copy as a fallback)."
		return nil
	}
	copy := m.hooks.Copy
	if m.sender == nil {
		if err := copy(text); err != nil {
			return func() tea.Msg { return copyResultMsg{err: err} }
		}
		return func() tea.Msg { return copyResultMsg{chars: len(text), label: label} }
	}
	return func() tea.Msg {
		go func() {
			if err := copy(text); err != nil {
				m.sender(copyResultMsg{err: err})
				return
			}
			m.sender(copyResultMsg{chars: len(text), label: label})
		}()
		return nil
	}
}

var fencePattern = regexp.MustCompile("(?s)```(?:\\w+)?\n(.*?)```")

// latestCodeBlock extracts the most recent fenced code block.
func latestCodeBlock(text string) (string, bool) {
	matches := fencePattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return "", false
	}
	block := strings.Trim(matches[len(matches)-1][1], "\n")
	if block == "" {
		return "", false
	}
	return block, true
}

func (m Model) updateCopyResult(msg copyResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.notice = "Copy failed: " + msg.err.Error()
		return m, nil
	}
	m.notice = "Copied."
	if msg.label != "" {
		m.notice = "Copied " + msg.label + "."
	}
	return m, nil
}
