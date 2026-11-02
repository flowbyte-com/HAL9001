package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
)

// renderMarkdown renders an assistant reply for the viewport width. On any
// failure it returns the raw text: formatting must never lose content.
func renderMarkdown(text string, width int) string {
	if width < 20 {
		width = 80
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithEnvironmentConfig(),
		glamour.WithWordWrap(width-2),
	)
	if err != nil {
		return text
	}
	out, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimRight(out, "\n")
}

// enterOnboard opens the setup form, optionally prefilled.
func (m *Model) enterOnboard(prefill OnboardData) {
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
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			m.quitting = true
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

func (m Model) onboardView() string {
	var sb strings.Builder
	sb.WriteString(m.heading())
	sb.WriteString("\n\nFirst run — three values connect HAL9001:\n\n")
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
		sb.WriteString(marker + f.label + ": " + f.input.View() + "\n")
	}
	sb.WriteString("\nTab moves · Enter next · Ctrl+T test connection · Ctrl+S save\n")
	if m.obNotice != "" {
		sb.WriteString("\n" + m.obNotice + "\n")
	}
	out := sb.String()
	if !m.color {
		out = stripANSI(out)
	}
	return out
}

// enterPicker opens the model picker.
func (m *Model) enterPicker() {
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
			m.quitting = true
			return m, tea.Quit
		case "esc":
			m.view = viewChat
			m.pickerNotice = ""
			return m, nil
		case "up", "shift+tab":
			if m.pickerCursor > 0 {
				m.pickerCursor--
			}
			return m, nil
		case "down", "tab":
			if m.pickerCursor < len(m.pickerModels)-1 {
				m.pickerCursor++
			}
			return m, nil
		case "enter":
			if len(m.pickerModels) == 0 || m.hooks.SwitchModel == nil {
				m.pickerNotice = "Switching is not wired up."
				return m, nil
			}
			id := m.pickerModels[m.pickerCursor].ID
			eng, err := m.hooks.SwitchModel(id)
			if err != nil {
				m.pickerNotice = "Switch failed: " + err.Error()
				return m, nil
			}
			if eng != nil {
				m.engine = eng
			}
			m.model = id
			m.enterPicker()
			m.view = viewChat
			m.notice = "Model: " + id + " (applies to the next request)"
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

func (m Model) pickerView() string {
	var sb strings.Builder
	sb.WriteString(m.heading())
	sb.WriteString("\n\nModels (Enter switches, A adds, Esc back):\n\n")
	if len(m.pickerModels) == 0 {
		sb.WriteString("  (no models configured — press A to add one)\n")
	}
	for i, info := range m.pickerModels {
		marker, active := "  ", "        "
		if i == m.pickerCursor {
			marker = "▸ "
		}
		if info.Active {
			active = " (active)"
		}
		sb.WriteString(marker + info.ID + active + "\n")
	}
	if m.pickerNotice != "" {
		sb.WriteString("\n" + m.pickerNotice + "\n")
	}
	out := sb.String()
	if !m.color {
		out = stripANSI(out)
	}
	return out
}

// enterTheme opens the theme picker with a live preview.
func (m *Model) enterTheme() {
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
	m.view = viewTheme
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
			m.quitting = true
			return m, tea.Quit
		case "esc":
			m.view = viewChat
			m.themeNotice = ""
			return m, nil
		case "up", "shift+tab":
			if m.themeCursor > 0 {
				m.themeCursor--
			}
			m.themePreview = Themes()[m.themeCursor]
			return m, nil
		case "down", "tab":
			if m.themeCursor < len(Themes())-1 {
				m.themeCursor++
			}
			m.themePreview = Themes()[m.themeCursor]
			return m, nil
		case "enter":
			name := Themes()[m.themeCursor].Name
			if m.hooks.SaveTheme != nil {
				if err := m.hooks.SaveTheme(name); err != nil {
					m.themeNotice = "Save failed: " + err.Error()
					return m, nil
				}
			}
			m.theme = Themes()[m.themeCursor]
			m.view = viewChat
			m.notice = "Theme: " + name
			return m, nil
		}
	}
	return m, nil
}

func (m Model) themeView() string {
	st := m.themePreview.styles()
	var sb strings.Builder
	sb.WriteString(st.header.Render("● HAL9001"))
	sb.WriteString("\n\nThemes (Enter applies, Esc back) — live preview:\n\n")
	for i, t := range Themes() {
		marker := "  "
		if i == m.themeCursor {
			marker = "▸ "
		}
		line := marker + t.Name
		if i == m.themeCursor {
			line = st.active.Render(line)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n" + st.header.Render("● HAL9001") + "\n")
	sb.WriteString(st.user.Render("You > ") + "preview message\n")
	sb.WriteString(st.hal.Render("HAL > ") + "preview reply\n")
	sb.WriteString(st.footer.Render("[444 read-only] " + m.modelNameOr("model")))
	sb.WriteString("\n")
	if m.themeNotice != "" {
		sb.WriteString("\n" + m.themeNotice + "\n")
	}
	out := sb.String()
	if !m.color {
		out = stripANSI(out)
	}
	return out
}

// enterSessions opens the stored-conversation picker.
func (m *Model) enterSessions() {
	m.sessionNames = nil
	m.sessionCursor = 0
	m.sessionNotice = ""
	if m.hooks.ListSessions != nil {
		names, err := m.hooks.ListSessions()
		if err != nil {
			m.sessionNotice = "List failed: " + err.Error()
		} else {
			m.sessionNames = names
		}
	}
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
			m.quitting = true
			return m, tea.Quit
		case "esc":
			m.view = viewChat
			m.sessionNotice = ""
			return m, nil
		case "up", "shift+tab":
			if m.sessionCursor > 0 {
				m.sessionCursor--
			}
			return m, nil
		case "down", "tab":
			if m.sessionCursor < len(m.sessionNames)-1 {
				m.sessionCursor++
			}
			return m, nil
		case "enter":
			if len(m.sessionNames) == 0 || m.hooks.SwitchSession == nil {
				m.sessionNotice = "Switching is not wired up."
				return m, nil
			}
			name := m.sessionNames[m.sessionCursor]
			eng, lines, err := m.hooks.SwitchSession(name)
			if err != nil {
				m.sessionNotice = "Switch failed: " + err.Error()
				return m, nil
			}
			if eng != nil {
				m.engine = eng
			}
			m.SetHistory(lines)
			m.view = viewChat
			m.notice = "Resumed: " + name
			return m, nil
		}
	}
	return m, nil
}

func (m Model) sessionsView() string {
	st := m.theme.styles()
	var sb strings.Builder
	sb.WriteString(st.header.Render("● HAL9001"))
	sb.WriteString("\n\nSessions (Enter resumes, Esc back):\n\n")
	if len(m.sessionNames) == 0 {
		sb.WriteString("  (no stored conversations)\n")
	}
	for i, name := range m.sessionNames {
		line := "  " + name
		if i == m.sessionCursor {
			line = st.active.Render("▸ " + name)
		}
		sb.WriteString(line + "\n")
	}
	if m.sessionNotice != "" {
		sb.WriteString("\n" + m.sessionNotice + "\n")
	}
	out := sb.String()
	if !m.color {
		out = stripANSI(out)
	}
	return out
}

func (m Model) helpView() string {
	st := m.theme.styles()
	rows := [][2]string{
		{"Enter", "newline in the composer"},
		{"Ctrl+S", "send (slash commands run locally, never sent to the model)"},
		{"Ctrl+M", "model picker"},
		{"Ctrl+N", "new conversation"},
		{"Ctrl+C", "quit"},
		{"Esc", "cancel a stream / close a panel"},
		{"PgUp / PgDn", "scroll the conversation"},
		{"paste", "bracketed paste is inserted, never submitted"},
		{"", ""},
		{"/help", "this panel"},
		{"/copy", "copy the last HAL response to the clipboard"},
		{"/copy code", "copy the latest fenced code block"},
		{"/theme", "theme picker with live preview"},
		{"/model", "model picker"},
		{"/new", "new conversation"},
		{"/sessions", "resume a stored conversation"},
	}
	var sb strings.Builder
	sb.WriteString(st.header.Render("● HAL9001"))
	sb.WriteString("\n\nKeys and commands (Esc back):\n\n")
	for _, r := range rows {
		if r[0] == "" {
			sb.WriteString("\n")
			continue
		}
		sb.WriteString("  " + st.active.Render(r[0]) + "  " + r[1] + "\n")
	}
	sb.WriteString("\n" + st.footer.Render("[444 read-only]"))
	out := sb.String()
	if !m.color {
		out = stripANSI(out)
	}
	return out
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
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			m.quitting = true
			return m, tea.Quit
		case "esc", "enter", "q":
			m.view = viewChat
			return m, nil
		}
	}
	return m, nil
}
func (m *Model) newSession() {
	if m.hooks.NewSession == nil {
		m.notice = "New sessions are not wired up."
		return
	}
	eng, err := m.hooks.NewSession()
	if err != nil {
		m.notice = "New session failed: " + err.Error()
		return
	}
	if eng != nil {
		m.engine = eng
	}
	m.lines = nil
	m.display = nil
	m.pending = ""
	m.renderConversation()
	m.notice = "New conversation started."
}
