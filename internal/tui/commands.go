package tui

import (
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// command is one local slash command. The table below is the single
// source of truth: dispatch, the suggestion panel and /help all read it,
// so they cannot drift apart.
type command struct {
	name        string
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
		description: "Copy HAL's last response",
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
		name:        "/sessions",
		description: "Resume a conversation",
		run: func(m *Model, _ []string) tea.Cmd {
			m.enterSessions()
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
		name:        "/collapse",
		description: "Collapse long prompts",
		run: func(m *Model, _ []string) tea.Cmd {
			m.setAllUserExpanded(false)
			m.notice = "Long messages collapsed."
			return nil
		},
	},
}

// commandByName finds a command exactly.
func commandByName(name string) (*command, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, c := range commandTable {
		if c.name == name {
			return c, true
		}
	}
	return nil, false
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
		if strings.HasPrefix(c.name, prefix) {
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
	before := m.suggestRows()
	items := suggestionList(m.composer.Value())
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

// closeSuggestions hides the panel without touching the draft.
func (m *Model) closeSuggestions() {
	if !m.suggestOpen && len(m.suggestItems) == 0 {
		return
	}
	m.suggestOpen = false
	m.suggestItems = nil
	m.suggestCursor = 0
	m.resizeFrame()
}

// suggestRows is how many panel rows the frame should reserve.
func (m Model) suggestRows() int {
	if !m.suggestOpen {
		return 0
	}
	n := len(m.suggestItems)
	if n > maxSuggestions {
		n = maxSuggestions
	}
	if n < 0 {
		n = 0
	}
	return n
}

// suggestionRows renders the panel: the command and a short description,
// or only the command when the terminal is narrow.
func (m Model) suggestionRows(n int) []string {
	st := m.theme.styles()
	items := m.suggestItems
	if len(items) > n {
		items = items[:n]
	}
	rows := make([]string, 0, n)
	for i, c := range items {
		label := c.usage
		if label == "" {
			label = c.name
		}
		line := "  " + label
		if m.width >= 38 {
			// Keep the description, truncated to whatever is left after
			// the command column.
			column := 22
			if m.width < 60 {
				column = 20
			}
			line = padTo("  "+st.active.Render(label), column) + c.description
		}
		if i == m.suggestCursor {
			line = st.active.Render("▸ " + strings.TrimPrefix(line, "  "))
		}
		rows = append(rows, m.fitWidth(line, m.width))
	}
	for len(rows) < n {
		rows = append(rows, "")
	}
	return rows
}

// moveSuggestion moves the selection by delta, clamped to the visible
// panel.
func (m *Model) moveSuggestion(delta int) {
	if !m.suggestOpen || len(m.suggestItems) == 0 {
		return
	}
	limit := len(m.suggestItems)
	if limit > maxSuggestions {
		limit = maxSuggestions
	}
	m.suggestCursor += delta
	if m.suggestCursor < 0 {
		m.suggestCursor = 0
	}
	if m.suggestCursor >= limit {
		m.suggestCursor = limit - 1
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
