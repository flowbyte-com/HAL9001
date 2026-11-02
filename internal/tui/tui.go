// Package tui is HAL9001's dedicated terminal interface: a scrollable
// conversation area, a multiline composer with explicit send, an
// onboarding screen, and a model picker. Presentation only — the
// conversation core lives in internal/engine, configuration in
// internal/config. There is deliberately no coding IDE, command console
// or task dashboard here.
package tui

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
)

// Keys. Enter always means newline in the composer; sending, quitting and
// navigation are explicit chords.
const (
	keySend   = "ctrl+s"
	keyQuit   = "ctrl+c"
	keyQuit2  = "ctrl+q"
	keyPicker = "ctrl+m"
	keyNew    = "ctrl+n"
)

// ColorEnabled reports whether styled output is appropriate. NO_COLOR and
// dumb terminals get plain text: the eye motif and labels never depend on
// ANSI escapes to be legible.
func ColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return strings.ToLower(os.Getenv("TERM")) != "dumb"
}

// OnboardData is one onboarding form submission.
type OnboardData struct {
	Endpoint string
	Model    string
	Key      string
}

// ValidateOnboard checks the three onboarding fields without touching the
// network or the disk.
func ValidateOnboard(d OnboardData) error {
	endpoint := strings.TrimSpace(d.Endpoint)
	if endpoint == "" {
		return errOnboard("endpoint is required")
	}
	if !strings.HasPrefix(endpoint, "https://") && !strings.HasPrefix(endpoint, "http://localhost") && !strings.HasPrefix(endpoint, "http://127.") {
		return errOnboard("endpoint must be an https URL (http is allowed for localhost only)")
	}
	if strings.TrimSpace(d.Model) == "" {
		return errOnboard("model name is required")
	}
	if strings.TrimSpace(d.Key) == "" {
		return errOnboard("API key is required")
	}
	return nil
}

type onboardError struct{ msg string }

func errOnboard(msg string) error { return &onboardError{msg: msg} }

func (e *onboardError) Error() string { return e.msg }

// ModelInfo describes one configured model for the picker.
type ModelInfo struct {
	ID     string
	Label  string
	Active bool
}

// Hooks connect the interface to the application. Every hook is optional
// in tests; main wires them all.
type Hooks struct {
	// SaveOnboard persists onboarding data and returns the new engine.
	SaveOnboard func(OnboardData) (Engine, error)
	// TestOnboard probes the endpoint and key, returning a human summary.
	TestOnboard func(OnboardData) (string, error)
	// HasConfig reports whether any model is configured yet.
	HasConfig func() bool
	// ListModels returns configured models with the active one marked.
	ListModels func() []ModelInfo
	// SwitchModel activates a configured model id, returning the engine.
	SwitchModel func(id string) (Engine, error)
	// CurrentOnboard prefills the add-model form with current values.
	CurrentOnboard func() OnboardData
	// NewSession starts a fresh conversation, returning the engine.
	NewSession func() (Engine, error)
	// Theme returns the persisted theme name (empty means default).
	Theme func() string
	// SaveTheme persists the theme name.
	SaveTheme func(name string) error
	// ListSessions returns stored conversation names, oldest first.
	ListSessions func() ([]string, error)
	// SwitchSession loads a stored conversation, returning the engine
	// and its lines (user text raw, assistant text raw Markdown).
	SwitchSession func(name string) (Engine, []SessionLine, error)
	// Copy writes text to the system clipboard (OSC 52). Nil means
	// clipboard access is unavailable.
	Copy func(text string) error
}

// Engine runs the conversation behind the interface. It matches
// internal/engine's streaming method; tests supply scripted doubles.
type Engine interface {
	RespondStream(ctx context.Context, input string, emit func(string)) (string, error)
}

// View selects the full-screen mode.
type View int

const (
	viewChat View = iota
	viewOnboard
	viewPicker
	viewTheme
	viewHelp
	viewSessions
)

// SessionLine is one stored conversation turn, for session resume.
type SessionLine struct {
	Role string
	Text string
}

// maxComposerHeight caps the growing composer; the viewport keeps the rest.
const maxComposerHeight = 8

// Model is the Bubble Tea application state.
type Model struct {
	engine   Engine
	hooks    Hooks
	viewport viewport.Model
	composer textarea.Model
	lines    []string
	display  []string
	pending  string
	width    int
	height   int
	ready    bool
	sending  bool
	quitting bool
	notice   string
	color    bool
	theme    Theme
	view     View
	sender   func(tea.Msg)
	cancel   context.CancelFunc
	model    string
	// Onboarding form.
	obEndpoint textinput.Model
	obModel    textinput.Model
	obKey      textinput.Model
	obFocus    int
	obNotice   string
	// Picker state.
	pickerModels []ModelInfo
	pickerCursor int
	pickerNotice string
	// Theme picker state.
	themeCursor  int
	themePreview Theme
	themeNotice  string
	// Last completed assistant reply (raw Markdown), for /copy.
	lastResponse string
	// Sessions view state.
	sessionNames  []string
	sessionCursor int
	sessionNotice string
	// Help view state.
	helpNotice string
}

// New builds the interface. Color follows ColorEnabled at construction so
// tests can pin either mode deterministically.
func New(engine Engine) Model {
	return NewWithHooks(engine, Hooks{})
}

// NewWithHooks builds the interface with application hooks.
func NewWithHooks(engine Engine, hooks Hooks) Model {
	ta := textarea.New()
	ta.Placeholder = "Type a message (Enter newline, Ctrl+S send, Ctrl+C quit)"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.SetHeight(1)
	ta.Focus()
	return Model{engine: engine, hooks: hooks, composer: ta, color: ColorEnabled(), theme: ThemeHALClassic}
}

// SetTheme applies a preset; used by the picker and tests.
func (m *Model) SetTheme(t Theme) { m.theme = t }

// Theme returns the active preset, for tests.
func (m Model) Theme() Theme { return m.theme }

// SetModelName sets the footer model label.
func (m *Model) SetModelName(name string) { m.model = name }

// SetHistory replaces the conversation view with stored turns (raw user
// text, raw assistant Markdown rendered on display).
func (m *Model) SetHistory(lines []SessionLine) {
	m.lines = nil
	m.display = nil
	m.pending = ""
	for _, line := range lines {
		if line.Role == "user" {
			m.lines = append(m.lines, "You > "+line.Text)
			m.display = append(m.display, "You > "+line.Text)
		} else {
			m.lines = append(m.lines, "HAL > "+line.Text)
			m.display = append(m.display, "HAL > "+renderMarkdown(line.Text, m.width))
		}
	}
	m.renderConversation()
	m.viewport.GotoBottom()
}

// SetColor overrides color mode; used by tests.
func (m *Model) SetColor(enabled bool) { m.color = enabled }

// SetSender installs the live message sender (the tea.Program). Headless
// tests leave it nil and run synchronously instead.
func (m *Model) SetSender(sender func(tea.Msg)) { m.sender = sender }

// SetEngine swaps the conversation core (model switching keeps history).
func (m *Model) SetEngine(e Engine) { m.engine = e }

// EnterOnboarding opens the setup form, for first run.
func (m *Model) EnterOnboarding() { m.enterOnboard(OnboardData{}) }

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

type streamDeltaMsg struct{ text string }
type streamStartedMsg struct{}
type responseMsg struct{ text string }
type errorMsg struct{ err error }

// sendCmd starts the streamed turn. With a live sender it runs in the
// background and posts deltas; headless it runs inline for determinism.
func (m Model) sendCmd(input string, ctx context.Context) tea.Cmd {
	if m.sender == nil {
		return func() tea.Msg { return m.runRespond(input, ctx) }
	}
	return func() tea.Msg {
		go m.runStream(input, ctx)
		return streamStartedMsg{}
	}
}

func (m Model) runRespond(input string, ctx context.Context) tea.Msg {
	reply, err := m.engine.RespondStream(ctx, input, nil)
	if err != nil {
		return errorMsg{err: err}
	}
	return responseMsg{text: reply}
}

func (m Model) runStream(input string, ctx context.Context) {
	reply, err := m.engine.RespondStream(ctx, input, func(text string) {
		m.sender(streamDeltaMsg{text: text})
	})
	if err != nil {
		m.sender(errorMsg{err: err})
		return
	}
	m.sender(responseMsg{text: reply})
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.view {
	case viewOnboard:
		return m.updateOnboard(msg)
	case viewPicker:
		return m.updatePicker(msg)
	case viewTheme:
		return m.updateTheme(msg)
	case viewHelp:
		return m.updateHelp(msg)
	case viewSessions:
		return m.updateSessions(msg)
	}
	return m.updateChat(msg)
}

func (m Model) updateChat(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.sizePanes()
		m.rewrap()
		m.renderConversation()
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		switch key {
		case keyQuit, keyQuit2:
			m.quitting = true
			m.stopStream()
			return m, tea.Quit
		case keyPicker:
			m.enterPicker()
			return m, nil
		case keyNew:
			m.newSession()
			return m, nil
		case "esc":
			if m.sending {
				m.stopStream()
				m.sending = false
				m.notice = "cancelled — stored nothing from the assistant"
				return m, nil
			}
		case keySend:
			if m.sending {
				return m, nil
			}
			input := strings.TrimSpace(m.composer.Value())
			if input == "" {
				return m, nil
			}
			if strings.HasPrefix(input, "/") {
				m.composer.Reset()
				m.fitComposer()
				if strings.HasPrefix(input, "//") {
					input = strings.TrimSpace(input[1:])
				} else {
					return m, m.dispatchSlash(input)
				}
			}
			m.composer.Reset()
			m.fitComposer()
			m.appendLine("You > " + input)
			m.pending = ""
			m.sending = true
			m.notice = "streaming… (Esc cancels)"
			ctx, cancel := context.WithCancel(context.Background())
			m.cancel = cancel
			return m, m.sendCmd(input, ctx)
		case "pgup":
			m.viewport.PageUp()
			return m, nil
		case "pgdown":
			m.viewport.PageDown()
			return m, nil
		}
	case tea.PasteMsg:
		m.composer.InsertString(msg.Content)
		m.fitComposer()
		return m, nil
	case streamDeltaMsg:
		stick := m.viewport.AtBottom()
		m.pending += msg.text
		m.renderConversation()
		if stick {
			m.viewport.GotoBottom()
		}
		return m, nil
	case responseMsg:
		m.sending = false
		m.stopStream()
		m.notice = ""
		m.pending = ""
		m.lastResponse = msg.text
		m.replaceLast(msg.text)
		return m, nil
	case copyResultMsg:
		return m.updateCopyResult(msg)
	case errorMsg:
		m.sending = false
		m.stopStream()
		if errors.Is(msg.err, context.Canceled) {
			m.notice = "cancelled — stored nothing from the assistant"
			return m, nil
		}
		if errors.Is(msg.err, engine.ErrNoProvider) {
			m.notice = "No provider configured — message stored in this session."
			return m, nil
		}
		m.notice = "error: " + msg.err.Error()
		m.appendLine("HAL > (I couldn't answer that: " + msg.err.Error() + ")")
		return m, nil
	}
	if m.ready {
		var cmds []tea.Cmd
		var cmd tea.Cmd
		before := m.composer.LineCount()
		m.composer, cmd = m.composer.Update(msg)
		cmds = append(cmds, cmd)
		if m.composer.LineCount() != before {
			m.fitComposer()
		}
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	m.fitComposer()
	return m, cmd
}

// fitComposer grows or shrinks the composer with its content, within a
// reasonable maximum, and reclaims the space for the viewport.
func (m *Model) fitComposer() {
	if !m.ready {
		return
	}
	want := m.composer.LineCount() + 1
	if want < 1 {
		want = 1
	}
	if want > maxComposerHeight {
		want = maxComposerHeight
	}
	if want != m.composer.Height() {
		m.composer.SetHeight(want)
		m.sizePanes()
	}
}

func (m *Model) stopStream() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *Model) sizePanes() {
	// Fixed chrome: header + blank, top rule, bottom rule, footer,
	// and one reserved notice line so status text never moves layout.
	const chrome = 2 + 1 + 1 + 1 + 1
	compHeight := m.composer.Height()
	if compHeight < 1 {
		compHeight = 1
	}
	viewHeight := m.height - chrome - compHeight
	if viewHeight < 1 {
		viewHeight = 1
	}
	if m.viewport.Width() == 0 {
		m.viewport = viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(viewHeight))
	} else {
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(viewHeight)
	}
	m.composer.SetWidth(m.width - 2)
}

func (m *Model) appendLine(line string) {
	m.lines = append(m.lines, line)
	m.display = append(m.display, line)
	m.renderConversation()
	m.viewport.GotoBottom()
}

// replaceLast swaps the pending raw stream for the final rendered answer.
func (m *Model) replaceLast(text string) {
	stick := m.viewport.AtBottom()
	rendered := renderMarkdown(text, m.width)
	m.lines = append(m.lines, "HAL > "+text)
	m.display = append(m.display, "HAL > "+rendered)
	m.renderConversation()
	if stick {
		m.viewport.GotoBottom()
	}
}

// rewrap re-renders stored assistant messages for the current width so
// resizing reflows Markdown instead of leaving stale wrapping.
func (m *Model) rewrap() {
	if len(m.lines) == 0 {
		return
	}
	m.display = make([]string, 0, len(m.lines))
	for _, line := range m.lines {
		if rest, ok := strings.CutPrefix(line, "HAL > "); ok {
			m.display = append(m.display, "HAL > "+renderMarkdown(rest, m.width))
			continue
		}
		m.display = append(m.display, line)
	}
}
func (m *Model) renderConversation() {
	st := m.theme.styles()
	styleLine := func(line string) string {
		if !m.color {
			return line
		}
		if rest, ok := strings.CutPrefix(line, "You > "); ok {
			return st.user.Render("You > ") + rest
		}
		if rest, ok := strings.CutPrefix(line, "HAL > "); ok {
			return st.hal.Render("HAL > ") + rest
		}
		return line
	}
	content := make([]string, 0, len(m.display)+1)
	for _, line := range m.display {
		content = append(content, styleLine(line))
	}
	if m.pending != "" {
		pending := "HAL > " + m.pending
		if m.color {
			pending = st.hal.Render("HAL > ") + m.pending
		}
		content = append(content, pending)
	}
	m.viewport.SetContent(strings.Join(content, "\n"))
}

// View implements tea.View.
func (m Model) View() tea.View {
	switch m.view {
	case viewOnboard:
		return tea.NewView(m.onboardView())
	case viewPicker:
		return tea.NewView(m.pickerView())
	case viewTheme:
		return tea.NewView(m.themeView())
	case viewHelp:
		return tea.NewView(m.helpView())
	case viewSessions:
		return tea.NewView(m.sessionsView())
	}
	return tea.NewView(m.chatView())
}

func (m Model) heading() string {
	st := m.theme.styles()
	eye := "●"
	if !m.color {
		return eye + " HAL9001"
	}
	return st.header.Render(eye + " HAL9001")
}

func (m Model) rule() string {
	line := strings.Repeat("─", m.width)
	if line == "" {
		return ""
	}
	if !m.color {
		return line
	}
	return m.theme.styles().rule.Render(line)
}

func (m Model) statusBar() string {
	st := m.theme.styles()
	left := "444 READ ONLY · /help"
	right := strings.TrimSpace(m.model)
	room := m.width - len(left) - len(right) - 1
	if room < 1 {
		// Narrow terminal: the 444 indicator always survives.
		left = "444 READ ONLY"
		right = ""
		if m.width > 0 && len(left) > m.width {
			left = left[:m.width]
		}
		if !m.color {
			return left
		}
		return st.footer.Render(left)
	}
	bar := left + strings.Repeat(" ", room) + right
	if !m.color {
		return bar
	}
	return st.footer.Render(bar)
}

var ansiSequence = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// stripANSI removes escape sequences for monochrome terminals. Widgets
// style themselves internally, so the fallback applies at the final view.
func stripANSI(s string) string {
	return ansiSequence.ReplaceAllString(s, "")
}

func (m Model) chatView() string {
	if m.quitting {
		return "Goodbye.\n"
	}
	if !m.ready {
		return "Starting HAL9001…\n"
	}
	var sb strings.Builder
	sb.WriteString(m.heading())
	sb.WriteString("\n\n")
	sb.WriteString(m.viewport.View())
	sb.WriteString("\n")
	sb.WriteString(m.rule())
	sb.WriteString("\n")
	sb.WriteString(m.composer.View())
	sb.WriteString("\n")
	sb.WriteString(m.rule())
	sb.WriteString("\n")
	sb.WriteString(m.statusBar())
	sb.WriteString("\n")
	if m.notice != "" {
		sb.WriteString(m.notice)
	}
	sb.WriteString("\n")
	out := sb.String()
	if !m.color {
		out = stripANSI(out)
	}
	return out
}

// RunWithModel launches a prebuilt interface (used when the caller sets
// the initial view, for example first-run onboarding).
func RunWithModel(m Model) error {
	var p *tea.Program
	m.sender = func(msg tea.Msg) {
		if p != nil {
			p.Send(msg)
		}
	}
	p = tea.NewProgram(m)
	_, err := p.Run()
	return err
}

// Run launches the interface on the real terminal. The sender is wired
// after program construction through a closure so streamed deltas can
// reach the event loop from the engine goroutine.
func Run(engine Engine, hooks Hooks) error {
	return RunWithModel(NewWithHooks(engine, hooks))
}

// Lines returns the conversation lines, for tests.
func (m Model) Lines() []string { return append([]string(nil), m.lines...) }

// Notice returns the current status notice, for tests.
func (m Model) Notice() string { return m.notice }

// Sending reports whether a response is in flight, for tests.
func (m Model) Sending() bool { return m.sending }

// Viewport exposes the scroll state, for tests.
func (m Model) Viewport() viewport.Model { return m.viewport }

// Composer exposes the composer, for tests.
func (m Model) Composer() textarea.Model { return m.composer }

// InjectResponse delivers a canned engine reply, for tests.
func (m *Model) InjectResponse(text string) (tea.Model, tea.Cmd) {
	return m.Update(responseMsg{text: text})
}
