package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/flowbyte-com/HAL9001/internal/provider"
)

// renderMarkdown renders an assistant reply for the viewport width in
// the active theme. On any failure it returns the raw text: formatting
// must never lose content.
func (m Model) renderMarkdown(text string, width int) string {
	if width < 20 {
		width = 80
	}
	text = sanitizeMarkdown(text)
	prepared, links := prepareMarkdownLinks(text)
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(m.theme.glamourStyle()),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return strings.Trim(text, "\n")
	}
	out, err := renderer.Render(prepared)
	if err != nil {
		return strings.Trim(text, "\n")
	}
	// Headings use a zero-width prefix to keep glamour's heading style
	// (and therefore the theme accent) on the text. The marker itself is
	// never part of the transcript.
	out = strings.Trim(strings.ReplaceAll(out, "\u200b", ""), "\n")
	return scrubUnsafeHyperlinks(applyPreparedLinks(out, links, m.theme.styles().link))
}

// logicalWrapWidth is the width used for the unwrapped render, and the
// bound it may grow to. The width only has to be wide enough that no
// line of the reply is broken, so it follows the text: a short reply is
// cheap to render, and a long one still comes out unwrapped.
const (
	logicalWrapWidth    = 1024
	maxLogicalWrapWidth = 8192
)

// renderMarkdownLogical renders the same Markdown without wrapping.
//
// It is the ground truth for mouse selection: the wrapped view on screen
// can be mapped back onto these lines, so a selection copies the text as
// it was written — no artificial line breaks, and no space invented where
// a long word was broken across two rows.
func (m Model) renderMarkdownLogical(text string) []string {
	width := logicalWrapWidth
	for _, line := range strings.Split(text, "\n") {
		if w := ansi.StringWidth(line); w > width {
			width = w
		}
	}
	if width > maxLogicalWrapWidth {
		width = maxLogicalWrapWidth
	}
	return m.renderMarkdownLogicalWidth(text, width)
}

func (m Model) renderMarkdownLogicalWidth(text string, width int) []string {
	text = sanitizeMarkdown(text)
	prepared, _ := prepareMarkdownLinks(text)
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(m.theme.glamourStyle()),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return strings.Split(text, "\n")
	}
	out, err := renderer.Render(prepared)
	if err != nil {
		return strings.Split(text, "\n")
	}
	out = strings.ReplaceAll(out, "\u200b", "")
	out = strings.ReplaceAll(out, linkMarker, "")
	lines := strings.Split(strings.Trim(out, "\n"), "\n")
	logical := make([]string, 0, len(lines))
	for _, line := range lines {
		logical = append(logical, strings.TrimRight(ansi.Strip(line), " \t"))
	}
	return logical
}

// enterOnboard opens the setup form, optionally prefilled.
func (m *Model) enterOnboard(prefill OnboardData) {
	m.closeSuggestions()
	m.view = viewOnboard
	m.obFocus = 0
	m.obNotice = ""
	m.obEndpoint = namedInput("Provider endpoint (https://…)", prefill.Endpoint, false)
	m.obModel = namedInput("Model name / identifier", prefill.Model, false)
	m.obKey = namedInput("API key", prefill.Key, true)
	m.obEndpoint.Focus()
}

func namedInput(placeholder, value string, secret bool) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	if secret {
		ti.EchoMode = textinput.EchoPassword
	}
	ti.SetValue(value)
	return ti
}

func (m *Model) focusedInput() *textinput.Model {
	switch m.obFocus {
	case 1:
		return &m.obModel
	default:
		m.obFocus = 0
		return &m.obEndpoint
	case 2:
		return &m.obKey
	}
}

func (m *Model) onboardData() OnboardData {
	return OnboardData{
		Endpoint: strings.TrimSpace(m.obEndpoint.Value()),
		Model:    strings.TrimSpace(m.obModel.Value()),
		Key:      strings.TrimSpace(m.obKey.Value()),
	}
}

func (m Model) updateOnboard(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case onboardTestMsg:
		return m.updateOnboardResult(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		for _, ti := range []*textinput.Model{&m.obEndpoint, &m.obModel, &m.obKey} {
			ti.SetWidth(m.width - 24)
		}
		// The conversation behind the setup form is laid out too: setup
		// is the only view that can hand over to the chat without the
		// chat ever having been sized, and an unsized frame has no
		// composer rows at all.
		m.sizePanes()
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, tea.Quit
		case "esc":
			if m.hooks.HasConfig == nil || m.hooks.HasConfig() {
				m.view = viewChat
				m.obNotice = ""
				return m, nil
			}
			m.obNotice = "Finish setup or press Ctrl+C to quit."
			return m, nil
		case "tab", "shift+tab", "up", "down":
			m.focusedInput().Blur()
			if msg.String() == "shift+tab" || msg.String() == "up" {
				m.obFocus = (m.obFocus + 2) % 3
			} else {
				m.obFocus = (m.obFocus + 1) % 3
			}
			m.focusedInput().Focus()
			return m, nil
		case "enter":
			if m.obFocus < 2 {
				m.focusedInput().Blur()
				m.obFocus++
				m.focusedInput().Focus()
				return m, nil
			}
			data := m.onboardData()
			if err := ValidateOnboard(data); err != nil {
				m.obNotice = err.Error()
				return m, nil
			}
			m.obNotice = "Testing connection…"
			return m, m.testOnboardCmd(data)
		case "ctrl+t":
			data := m.onboardData()
			if err := ValidateOnboard(data); err != nil {
				m.obNotice = err.Error()
				return m, nil
			}
			m.obNotice = "Testing connection…"
			return m, m.testOnboardCmd(data)
		case "ctrl+s":
			data := m.onboardData()
			if err := ValidateOnboard(data); err != nil {
				m.obNotice = err.Error()
				return m, nil
			}
			if m.hooks.SaveOnboard == nil {
				m.obNotice = "Saving is not wired up."
				return m, nil
			}
			eng, err := m.hooks.SaveOnboard(data)
			if err != nil {
				m.obNotice = "Save failed: " + err.Error()
				return m, nil
			}
			if eng != nil {
				m.engine = eng
			}
			m.view = viewChat
			m.notice = "Provider configured. Say hello."
			return m, nil
		}
	}
	var cmd tea.Cmd
	*m.focusedInput(), cmd = m.focusedInput().Update(msg)
	return m, cmd
}

type onboardTestMsg struct {
	summary string
	err     error
}

func (m Model) testOnboardCmd(data OnboardData) tea.Cmd {
	if m.hooks.TestOnboard == nil {
		return func() tea.Msg { return onboardTestMsg{err: errTestNotWired} }
	}
	if m.sender == nil {
		summary, err := m.hooks.TestOnboard(data)
		return func() tea.Msg { return onboardTestMsg{summary: summary, err: err} }
	}
	return func() tea.Msg {
		go func() {
			summary, err := m.hooks.TestOnboard(data)
			m.sender(onboardTestMsg{summary: summary, err: err})
		}()
		return nil
	}
}

type onboardTestError struct{ msg string }

func (e *onboardTestError) Error() string { return e.msg }

var errTestNotWired = &onboardTestError{msg: "Connection test is not wired up."}

func (m Model) updateOnboardResult(msg onboardTestMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.obNotice = "Connection failed: " + msg.err.Error() + " — fix and retry, or Ctrl+S to save anyway."
		return m, nil
	}
	m.obNotice = "Connection ok: " + msg.summary + " — Ctrl+S to save."
	return m, nil
}

func (m Model) onboardRows() []string {
	rows := []string{
		"First run — three values connect HAL9001:",
		"",
	}
	fields := []struct {
		label string
		input textinput.Model
	}{
		{"Endpoint", m.obEndpoint},
		{"Model", m.obModel},
		{"API key", m.obKey},
	}
	for i, f := range fields {
		marker := "  "
		if i == m.obFocus {
			marker = "▸ "
		}
		rows = append(rows, marker+f.label+": "+f.input.View())
	}
	rows = append(rows, "")
	rows = append(rows, "Tab moves · Enter next · Ctrl+T test connection · Ctrl+S save")
	if m.obNotice != "" {
		rows = append(rows, "", m.obNotice)
	}
	return rows
}

// enterPicker opens the model picker.
func (m *Model) enterPicker() {
	m.closeSuggestions()
	models := []ModelInfo{}
	if m.hooks.ListModels != nil {
		models = m.hooks.ListModels()
	}
	m.pickerModels = models
	m.pickerCursor = 0
	for i, info := range models {
		if info.Active {
			m.pickerCursor = i
		}
	}
	m.pickerNotice = ""
	m.panelOffset = 0
	m.view = viewPicker
}

func (m Model) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, tea.Quit
		case "esc":
			m.view = viewChat
			m.pickerNotice = ""
			return m, nil
		case "up", "shift+tab":
			if m.pickerCursor > 0 {
				m.pickerCursor--
			}
			m.ensureVisible(2+m.pickerCursor, len(m.pickerRows()), contentHeight(m.height))
			return m, nil
		case "down", "tab":
			if m.pickerCursor < len(m.pickerModels)-1 {
				m.pickerCursor++
			}
			m.ensureVisible(2+m.pickerCursor, len(m.pickerRows()), contentHeight(m.height))
			return m, nil
		case "enter":
			if len(m.pickerModels) == 0 || m.hooks.SwitchModel == nil {
				m.pickerNotice = "Switching is not wired up."
				return m, nil
			}
			chosen := m.pickerModels[m.pickerCursor]
			eng, err := m.hooks.SwitchModel(chosen.Endpoint, chosen.ID)
			if err != nil {
				m.pickerNotice = "Switch failed: " + err.Error()
				return m, nil
			}
			if eng != nil {
				m.engine = eng
			}
			m.model = chosen.ID
			if m.hooks.Effort != nil {
				m.SetEffort(m.hooks.Effort())
			}
			m.enterPicker()
			m.view = viewChat
			m.notice = "Model: " + chosen.ID + " (applies to the next request)"
			return m, nil
		case "a", "A":
			prefill := OnboardData{}
			if m.hooks.CurrentOnboard != nil {
				prefill = m.hooks.CurrentOnboard()
			}
			m.enterOnboard(prefill)
			return m, nil
		}
	}
	return m, nil
}

func (m Model) pickerRows() []string {
	st := m.theme.styles()
	rows := []string{"Models (Enter switches, A adds, /model list shows everything, Esc back):", ""}
	if len(m.pickerModels) == 0 {
		rows = append(rows, "  (no models configured — press A to add one)")
	}
	for i, info := range m.pickerModels {
		marker, active := "  ", "        "
		if i == m.pickerCursor {
			marker = "▸ "
		}
		if info.Active {
			active = " (active)"
		}
		// The identifier and the endpoint name can come from the
		// provider. They are flattened before they are drawn: a hostile
		// endpoint must not be able to move the cursor or forge a row
		// with control characters in its own catalogue.
		name := oneLine(info.ID)
		if info.Endpoint != "" {
			name = oneLine(info.Endpoint) + " · " + name
		}
		line := marker + name + active
		if i == m.pickerCursor {
			line = st.active.Render(line)
		}
		rows = append(rows, line)
		if info.Review != "" {
			rows = append(rows, "    needs review: "+oneLine(info.Review))
		}
	}
	if m.pickerNotice != "" {
		rows = append(rows, "", m.pickerNotice)
	}
	return rows
}

// enterTheme opens the theme picker with a live preview.
func (m *Model) enterTheme() {
	m.closeSuggestions()
	current := ThemeHALClassic.Name
	if m.hooks.Theme != nil {
		if name := m.hooks.Theme(); name != "" {
			current = name
		}
	}
	m.themeCursor = 0
	for i, t := range Themes() {
		if t.Name == current || (current == "" && i == 0) {
			m.themeCursor = i
		}
		if t.Name == m.theme.Name {
			m.themePreview = t
		}
	}
	if m.themePreview.Name == "" {
		m.themePreview = Themes()[m.themeCursor]
	}
	m.themeNotice = ""
	m.themeOriginal = m.theme
	m.panelOffset = 0
	m.view = viewTheme
	m.applyThemePreview()
}

// applyThemePreview recolours the whole interface — including Markdown
// already on screen — for the highlighted preset.
func (m *Model) applyThemePreview() {
	if m.theme == m.themePreview {
		return
	}
	anchor := m.anchor()
	m.setTheme(m.themePreview)
	m.rewrap()
	m.renderConversation()
	m.restoreAnchor(anchor)
	m.clearSelection()
}

func (m Model) updateTheme(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, tea.Quit
		case "esc":
			// Dismiss the preview: the previously active theme returns.
			m.setTheme(m.themeOriginal)
			m.rewrap()
			m.renderConversation()
			m.clearSelection()
			m.view = viewChat
			m.themeNotice = ""
			return m, nil
		case "up", "shift+tab":
			if m.themeCursor > 0 {
				m.themeCursor--
			}
			m.themePreview = Themes()[m.themeCursor]
			m.applyThemePreview()
			m.ensureVisible(2+m.themeCursor, len(m.themeRows()), contentHeight(m.height))
			return m, nil
		case "down", "tab":
			if m.themeCursor < len(Themes())-1 {
				m.themeCursor++
			}
			m.themePreview = Themes()[m.themeCursor]
			m.applyThemePreview()
			m.ensureVisible(2+m.themeCursor, len(m.themeRows()), contentHeight(m.height))
			return m, nil
		case "enter":
			name := Themes()[m.themeCursor].Name
			if m.hooks.SaveTheme != nil {
				if err := m.hooks.SaveTheme(name); err != nil {
					m.themeNotice = "Save failed: " + err.Error()
					return m, nil
				}
			}
			m.setTheme(Themes()[m.themeCursor])
			m.rewrap()
			m.renderConversation()
			m.clearSelection()
			m.view = viewChat
			m.notice = "Theme: " + name
			return m, nil
		}
	}
	return m, nil
}

func (m Model) themeRows() []string {
	st := m.themePreview.styles()
	rows := []string{"Themes (Enter applies, Esc back) — live preview:", ""}
	for i, t := range Themes() {
		marker := "  "
		if i == m.themeCursor {
			marker = "▸ "
		}
		line := marker + t.Name
		if i == m.themeCursor {
			line = st.active.Render(line)
		}
		rows = append(rows, line)
	}
	rows = append(rows, "", "Preview:")
	rows = append(rows, st.header.Render("🔴 HAL9001"))
	rows = append(rows, strings.Repeat(" ", transcriptGutter)+st.user.Render("preview message"))
	rows = append(rows, st.eye.Render("🔴")+" "+"preview reply")
	rows = append(rows, st.footer.Render(m.permissionLabel()+" · "+m.modelNameOr("model")))
	if !m.color {
		rows = append(rows, "", "Colour is disabled (NO_COLOR), so themes cannot be previewed.")
		rows = append(rows, "The selection is still saved and used when colour is enabled.")
	}
	if m.themeNotice != "" {
		rows = append(rows, "", m.themeNotice)
	}
	return rows
}

// enterSessions opens the stored-conversation picker.
func (m *Model) enterSessions() {
	m.closeSuggestions()
	m.sessionItems = nil
	m.sessionCursor = 0
	m.sessionNotice = ""
	if m.hooks.ListSessions != nil {
		items, err := m.hooks.ListSessions()
		if err != nil {
			m.sessionNotice = "List failed: " + err.Error()
		} else {
			m.sessionItems = items
		}
	}
	m.panelOffset = 0
	m.view = viewSessions
}

func (m Model) updateSessions(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, tea.Quit
		case "esc":
			m.view = viewChat
			m.sessionNotice = ""
			return m, nil
		case "up", "shift+tab":
			if m.sessionCursor > 0 {
				m.sessionCursor--
			}
			m.ensureVisible(2+m.sessionCursor, len(m.sessionsRows()), contentHeight(m.height))
			return m, nil
		case "down", "tab":
			if m.sessionCursor < len(m.sessionItems)-1 {
				m.sessionCursor++
			}
			m.ensureVisible(2+m.sessionCursor, len(m.sessionsRows()), contentHeight(m.height))
			return m, nil
		case "enter":
			if len(m.sessionItems) == 0 || m.hooks.SwitchSession == nil {
				m.sessionNotice = "Switching is not wired up."
				return m, nil
			}
			item := m.sessionItems[m.sessionCursor]
			// The queue belongs to the conversation it was written in:
			// the waiting messages stay with the session they were
			// written for.
			var (
				eng   Engine
				lines []SessionLine
				err   error
			)
			m.moveQueue(func() {
				eng, lines, err = m.hooks.SwitchSession(item.ID)
			})
			if err != nil {
				m.sessionNotice = "Switch failed: " + err.Error()
				return m, nil
			}
			if eng != nil {
				m.engine = eng
			}
			// Bind the new conversation before loading its history, so a
			// session change clears the old tools and SetHistory can
			// rebind the new ledger.
			m.SetSessionID(item.ID)
			m.SetHistory(lines)
			m.title = item.Title
			if m.title == "" {
				m.title = item.ID
			}
			m.refreshUsage()
			m.view = viewChat
			m.notice = "Resumed: " + m.title
			return m, nil
		}
	}
	return m, nil
}

func (m Model) sessionsRows() []string {
	st := m.theme.styles()
	rows := []string{"Sessions (Enter resumes, Esc back):", ""}
	if len(m.sessionItems) == 0 {
		rows = append(rows, "  (no stored conversations)")
	}
	for i, item := range m.sessionItems {
		title := item.Title
		if title == "" {
			title = item.ID
		}
		detail := ""
		if !item.Updated.IsZero() {
			detail = item.Updated.Local().Format("2006-01-02 15:04")
		}
		current := ""
		if item.Current {
			current = " (current)"
		}
		line := "  " + title + current
		if detail != "" {
			line += "  ·  " + detail
		}
		if i == m.sessionCursor {
			line = st.active.Render("▸ " + strings.TrimPrefix(line, "  "))
		}
		rows = append(rows, line)
	}
	if m.sessionNotice != "" {
		rows = append(rows, "", m.sessionNotice)
	}
	return rows
}

// enterRename opens the rename overlay with the current title prefilled.
func (m *Model) enterRename() {
	m.closeSuggestions()
	m.renameInput = namedInput("Session title", strings.TrimSpace(m.title), false)
	m.renameInput.Focus()
	m.renameOpen = true
	m.panelOffset = 0
	m.view = viewRename
}

func (m Model) renameRows() []string {
	return []string{
		"Rename this conversation (Enter saves, Esc keeps the old title)",
		"",
		m.renameInput.View(),
		"",
		"Names are display only and never change stored history.",
	}
}

func (m Model) updateRename(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, m.quit()
		case "esc":
			m.view = viewChat
			m.renameOpen = false
			return m, nil
		case "enter":
			title := m.renameInput.Value()
			m.renameOpen = false
			m.view = viewChat
			m.applyRename(title)
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.renameInput, cmd = m.renameInput.Update(msg)
	return m, cmd
}

// applyRename sets the display title through the application hook.
func (m *Model) applyRename(title string) {
	if m.hooks.RenameSession == nil {
		m.notice = "Renaming is not wired up."
		return
	}
	display, err := m.hooks.RenameSession(title)
	if err != nil {
		m.notice = "Rename failed: " + err.Error()
		return
	}
	if display != "" {
		m.title = display
	}
	m.notice = "Conversation renamed."
}

// padTo pads a styled string to a visible width.
func padTo(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func (m Model) modelNameOr(fallback string) string {
	if strings.TrimSpace(m.model) != "" {
		return m.model
	}
	return fallback
}

func (m Model) updateHelp(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.clampHelpOffset()
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, tea.Quit
		case "esc", "enter", "q":
			m.view = viewChat
			return m, nil
		case "up", "shift+tab":
			m.scrollPanel(-1)
			return m, nil
		case "down", "tab":
			m.scrollPanel(1)
			return m, nil
		case "pgup":
			m.scrollPanel(-m.helpBodyHeight())
			return m, nil
		case "pgdown":
			m.scrollPanel(m.helpBodyHeight())
			return m, nil
		}
	}
	return m, nil
}

// diagRows renders the last turn's content-free telemetry. It never
// shows prompt text, reply text, reasoning text or credentials.
func (m Model) diagRows() []string {
	st := m.theme.styles()
	rows := []string{"Last turn — diagnostics (Esc back)", ""}
	// The composer's own state comes first: a draft that is in the buffer
	// but not on screen is the one thing worth checking before anything
	// else. Counts only — never the text itself.
	composer := func(label, value string) {
		rows = append(rows, "  "+padTo(st.active.Render(label), 16)+value)
	}
	composer("draft", fmt.Sprintf("%d bytes · %d rows", len(m.composer.Value()), m.draftRows()))
	composer("composer", fmt.Sprintf("%d of %d rows shown · offset %d",
		m.ComposerRows(), m.draftRows(), m.draftOffset))
	composer("textarea", fmt.Sprintf("%d×%d · scrolled %d",
		m.Composer().Width(), m.Composer().Height(), m.Composer().ScrollYOffset()))
	if m.sessionID != "" {
		// The header shows the title, which the operator can rename.
		// The identity the conversation is stored under is here.
		rows = append(rows, "  "+padTo(st.active.Render("session"), 16)+
			m.sessionID+" · "+m.permissionLabel())
	}
	if m.hooks.WebResearch != nil {
		if status := strings.TrimSpace(m.hooks.WebResearch()); status != "" {
			for i, line := range strings.Split(status, "\n") {
				if i == 0 {
					composer("web research", line)
					continue
				}
				rows = append(rows, strings.Repeat(" ", 18)+line)
			}
		}
	}
	if m.hooks.ContextBudget != nil {
		if budget := strings.TrimSpace(m.hooks.ContextBudget()); budget != "" {
			composer("context", budget)
		}
	}
	if len(m.lastTools) > 0 {
		rows = append(rows, "  "+padTo(st.active.Render("last tools"), 16))
		for _, a := range m.lastTools {
			line := toolActivityLineMode(a, max(10, m.width-4), m.toolIcons)
			if m.color {
				line = st.footer.Render(line)
			}
			rows = append(rows, "    "+line)
		}
	}
	if d, ok := m.lastRouting(); ok {
		composer("skill", skillSummary(d))
		// The routing record is broken out, because the four states are
		// different facts: what was considered, what was chosen, what
		// was read, and what really shaped the answer.
		for _, fact := range skillFacts(d, m.turnAttribution) {
			rows = append(rows, "  "+padTo(st.active.Render(fact.Label), 16)+fact.Value)
		}
	}
	s := m.lastStats
	if s == nil {
		rows = append(rows, "", "  No completed turn yet.")
		rows = append(rows, "", "  Diagnostics carry counts and durations only:")
		rows = append(rows, "  no prompt text, no reasoning text, no credentials.")
		return rows
	}
	rows = append(rows, "")
	row := func(label, value string) {
		rows = append(rows, "  "+padTo(st.active.Render(label), 16)+value)
	}
	row("status", s.Status)
	row("elapsed", fmtDuration(s.Duration))
	if s.FirstVisible > 0 {
		row("first output", fmtDuration(s.FirstVisible))
	} else {
		row("first output", "never")
	}
	if s.FirstReasoning > 0 {
		row("first reasoning", fmtDuration(s.FirstReasoning))
	} else {
		row("first reasoning", "none")
	}
	row("sse events", fmt.Sprintf("%d", s.Events))
	row("keep-alives", fmt.Sprintf("%d", s.KeepAlives))
	if s.Malformed > 0 {
		row("malformed", fmt.Sprintf("%d", s.Malformed))
	}
	row("visible", fmt.Sprintf("%d chunks · %d runes", s.VisibleChunks, s.VisibleRunes))
	row("reasoning", fmt.Sprintf("%d events · %d runes (presence only)", s.ReasoningChunks, s.ReasoningRunes))
	finish := s.FinishReason
	if finish == "" {
		finish = "not reported"
	}
	row("finish", finish)
	if s.OutputLimitHit {
		row("limit", "max_tokens reached — the reply was cut")
	}
	if s.HasUsage {
		row("tokens", fmt.Sprintf("prompt %d · completion %d · total %d",
			s.Usage.PromptTokens, s.Usage.CompletionTokens, s.Usage.TotalTokens))
		if s.Usage.ReasoningTokens > 0 {
			row("", fmt.Sprintf("of which reasoning %d", s.Usage.ReasoningTokens))
		}
	} else {
		row("tokens", "not reported by the provider")
	}
	if s.MaxTokens > 0 {
		row("limits", fmt.Sprintf("max_tokens %d · idle %s · turn %s",
			s.MaxTokens, shortDuration(s.IdleTimeout), shortDuration(s.TurnTimeout)))
	}
	rows = append(rows, "", "  Counts and durations only: no prompt, reply or reasoning text.")
	return rows
}

// fmtDuration renders a measured duration, keeping sub-millisecond values
// meaningful.
func fmtDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Millisecond {
		return "<1ms"
	}
	return d.Round(time.Millisecond).String()
}

// enterEffort opens the reasoning-effort selector. Models without effort
// control get an explanation instead of a meaningless picker.
func (m *Model) enterEffort() {
	m.closeSuggestions()
	if !m.effortSupported {
		m.notice = "This model does not expose reasoning effort — nothing to configure."
		return
	}
	m.effortCursor = 0
	for i, level := range provider.EffortLevels {
		if level == m.effort {
			m.effortCursor = i
		}
	}
	m.effortNotice = ""
	m.panelOffset = 0
	m.view = viewEffort
}

func (m Model) updateEffort(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, tea.Quit
		case "esc":
			m.view = viewChat
			m.effortNotice = ""
			return m, nil
		case "up", "shift+tab":
			if m.effortCursor > 0 {
				m.effortCursor--
			}
			m.ensureVisible(2+m.effortCursor, len(m.effortRows()), contentHeight(m.height))
			return m, nil
		case "down", "tab":
			if m.effortCursor < len(provider.EffortLevels)-1 {
				m.effortCursor++
			}
			m.ensureVisible(2+m.effortCursor, len(m.effortRows()), contentHeight(m.height))
			return m, nil
		case "enter":
			level := provider.EffortLevels[m.effortCursor]
			m.view = viewChat
			m.applyEffort(level)
			return m, nil
		}
	}
	return m, nil
}

// applyEffort changes the level for the active model and confirms it.
func (m *Model) applyEffort(level string) {
	if !m.effortSupported {
		m.notice = "This model does not expose reasoning effort — nothing to configure."
		return
	}
	normalized := provider.NormalizeEffort(level)
	if normalized == "" {
		m.notice = "Unknown effort level " + level + " — use low, medium, high, xhigh or max."
		return
	}
	if m.hooks.SetEffort == nil {
		m.notice = "Effort is not wired up."
		return
	}
	eng, err := m.hooks.SetEffort(normalized)
	if err != nil {
		m.notice = "Effort unchanged: " + err.Error()
		return
	}
	if eng != nil {
		m.engine = eng
	}
	m.effort = normalized
	m.notice = "Effort: " + normalized + " (applies to the next request)"
}

func (m Model) effortRows() []string {
	st := m.theme.styles()
	rows := []string{
		"Reasoning effort (Enter applies, Esc back)",
		"",
	}
	for i, level := range provider.EffortLevels {
		line := "  " + level
		if i == m.effortCursor {
			line = st.active.Render("▸ " + level)
		} else if level == m.effort {
			line += "  (current)"
		}
		rows = append(rows, line)
	}
	rows = append(rows, "",
		"MiniMax reasoning models apply max effort when the parameter is",
		"omitted; HAL sends medium explicitly. Higher levels think longer",
		"and spend more of the output budget on reasoning.")
	if m.effortNotice != "" {
		rows = append(rows, "", m.effortNotice)
	}
	return rows
}

// shortDuration renders a limit compactly (45s, 2m0s).
func shortDuration(d time.Duration) string {
	if d <= 0 {
		return "none"
	}
	return d.Round(time.Second).String()
}

func (m Model) updateDiag(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, tea.Quit
		case "esc", "enter", "q":
			m.view = viewChat
			return m, nil
		case "up", "shift+tab":
			m.scrollPanel(-1)
			return m, nil
		case "down", "tab":
			m.scrollPanel(1)
			return m, nil
		}
	}
	return m, nil
}

// scrollPanel moves the active overlay panel by delta rows.
//
// The approval view scrolls its document preview and nothing else: the
// header that names the operation and the row that carries the
// decisions stay where they are.
func (m *Model) scrollPanel(delta int) {
	var total, n int
	switch m.view {
	case viewHelp:
		total = len(m.helpRows())
		n = m.helpBodyHeight()
	case viewDiag:
		total = len(m.diagRows())
		n = contentHeight(m.height)
	case viewApprove:
		total = len(m.approvalPreviewRows())
		n = m.approvalPreviewHeight()
	default:
		total = len(m.activePanel())
		n = contentHeight(m.height)
	}
	m.panelOffset += delta
	if m.panelOffset > total-n {
		m.panelOffset = total - n
	}
	if m.panelOffset < 0 {
		m.panelOffset = 0
	}
}

func (m *Model) newSession() {
	m.startFreshConversation("New conversation started.")
}

// clearConversation empties the visible conversation and starts a fresh
// context. /clear and /new share the machinery; only the confirmation
// differs, so the operator knows an old session is still recoverable.
func (m *Model) clearConversation() {
	m.startFreshConversation("Conversation cleared — the previous session is kept in /sessions.")
}

// startFreshConversation cancels any live turn, asks the application for
// a new session store and resets the visible conversation. The previous
// session file is never deleted; it stays available through /sessions.
func (m *Model) startFreshConversation(notice string) {
	// A pending write belongs to the conversation it was prepared for.
	m.cancelWrite("conversation changed")
	// 666 is never inherited by a new conversation.
	m.resetPermission()
	m.closeSuggestions()
	if m.sending {
		m.stopStream()
		m.sending = false
		m.endActivity()
		m.pending = ""
		m.reasoningSeen = false
	}
	// Invalidate any message still in flight from the cancelled turn.
	m.streamID++
	if m.hooks.NewSession == nil {
		m.notice = "New sessions are not wired up."
		return
	}
	// The queue belongs to the conversation it was written in: the
	// waiting messages stay with the session they were written for.
	var eng Engine
	var err error
	m.moveQueue(func() {
		eng, err = m.hooks.NewSession()
	})
	if err != nil {
		m.notice = "New session failed: " + err.Error()
		return
	}
	if eng != nil {
		m.engine = eng
	}
	if m.hooks.SessionID != nil {
		m.SetSessionID(m.hooks.SessionID())
	}
	m.entries = nil
	m.pending = ""
	m.clearUndo()
	m.clearSelection()
	m.title = ""
	if m.hooks.SessionTitle != nil {
		m.title = m.hooks.SessionTitle()
	}
	m.lastStats = nil
	m.lastResponse = ""
	m.lastTools = nil
	m.refreshUsage()
	m.renderConversation()
	m.viewport.GotoBottom()
	m.notice = notice
}

// usageRows renders the session's token accounting. It reports only what
// the provider said and never estimates or prices anything.
func (m Model) usageRows() []string {
	st := m.theme.styles()
	rows := []string{"Session usage (Esc back)", ""}
	row := func(label, value string) {
		rows = append(rows, "  "+padTo(st.active.Render(label), 15)+value)
	}
	t := m.tokens
	row("session", m.titleOr(m.sessionTitle()))
	row("input", tokenCount(t.Input))
	row("output", tokenCount(t.Output))
	if t.Reasoning > 0 {
		row("reasoning", tokenCount(t.Reasoning)+"  (included in output)")
	} else {
		row("reasoning", "not reported")
	}
	if t.Cached > 0 {
		row("cached input", tokenCount(t.Cached))
	} else {
		row("cached input", "not reported")
	}
	row("turns", fmt.Sprintf("%d completed · %d incomplete", t.Turns, t.Incomplete))
	if t.Total() {
		row("last turn", fmt.Sprintf("input %s · output %s",
			tokenCount(t.Last.Input), tokenCount(t.Last.Output)))
		if t.Last.Reasoning > 0 {
			rows = append(rows, "                 reasoning "+tokenCount(t.Last.Reasoning))
		}
		if t.Last.Cached > 0 {
			rows = append(rows, "                 cached input "+tokenCount(t.Last.Cached))
		}
		if !t.Last.At.IsZero() {
			rows = append(rows, "                 at "+t.Last.At.Local().Format("2006-01-02 15:04"))
		}
	}
	rows = append(rows, "")
	rows = append(rows, "  Counts are what the provider reported; HAL does not estimate")
	rows = append(rows, "  tokens, price them, or store prompt content in this record.")
	if t.Incomplete > 0 {
		rows = append(rows, "")
		rows = append(rows, "  Incomplete turns ended without reported usage (cancelled,")
		rows = append(rows, "  timed out or provider error); their tokens are unknown.")
	}
	return rows
}

// sessionTitle is the conversation name shown in the header.
func (m Model) sessionTitle() string { return strings.TrimSpace(m.title) }

func (m Model) titleOr(fallback string) string {
	if fallback != "" {
		return fallback
	}
	return "(none)"
}

// tokenCount renders a token count with thousands separators.
func tokenCount(n int) string {
	if n <= 0 {
		return "0"
	}
	s := strconv.Itoa(n)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// compactTokens renders a count for the footer: 812, 12.4k, 1.2m.
func compactTokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 100_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "m"
	}
}

func (m Model) updateUsage(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, m.quit()
		case "esc", "enter", "q":
			m.view = viewChat
			return m, nil
		case "up", "shift+tab":
			m.scrollPanel(-1)
			return m, nil
		case "down", "tab":
			m.scrollPanel(1)
			return m, nil
		}
	}
	return m, nil
}
