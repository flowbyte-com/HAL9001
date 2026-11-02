// Package tui is HAL9001's dedicated terminal interface: a fullscreen
// conversation area, a multiline composer, an onboarding screen, and
// model/theme pickers. Presentation only — the conversation core lives in
// internal/engine, configuration in internal/config. There is
// deliberately no coding IDE, command console or task dashboard here.
package tui

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
)

// Keys.
//
// Enter sends the message. Newlines need an explicit chord, because most
// terminals report Shift+Enter as a plain Enter and would submit the
// message instead of breaking the line. Ctrl+J (sent as LF) and Alt+Enter
// (sent as ESC CR) are distinguishable on every terminal HAL supports;
// Shift+Enter only arrives as its own key on terminals that implement the
// Kitty keyboard protocol.
const (
	keySend   = "ctrl+s"
	keyQuit   = "ctrl+c"
	keyQuit2  = "ctrl+q"
	keyPicker = "ctrl+k"
	keyNew    = "ctrl+n"
	keyEnter  = "enter"
)

// newlineKeys insert a line break in the composer instead of sending.
// ctrl+j is the universally reliable fallback; alt+enter arrives as ESC
// CR on xterm-compatible terminals; shift+enter requires the Kitty
// keyboard protocol and is simply a plain Enter elsewhere.
var newlineKeys = map[string]bool{
	"ctrl+j":      true,
	"alt+enter":   true,
	"shift+enter": true,
}

// escapeGrace is the window during which an Enter right after an Escape
// is treated as the second half of an escape sequence. Alt+Enter reaches
// the application as ESC CR; a terminal that delivers the two bytes in
// separate reads would otherwise turn the CR into an accidental send.
const escapeGrace = 60 * time.Millisecond

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
	notice   string
	color    bool
	theme    Theme
	view     View
	sender   func(tea.Msg)
	cancel   context.CancelFunc
	model    string
	title    string
	// viewHeight and compHeight are the row budgets for the conversation
	// area and the composer. They are recomputed on every resize so the
	// frame always fits the terminal exactly.
	viewHeight int
	compHeight int
	// keyDisambiguation records whether the terminal reports modifier
	// combinations (Kitty keyboard protocol). It only affects what the
	// help panel claims about Shift+Enter.
	keyDisambiguation bool
	// escapedAt marks the moment an Escape key was seen, so an Enter
	// that completes an escape sequence is not mistaken for a send.
	escapedAt time.Time
	// now is the clock, replaceable in tests.
	now func() time.Time
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
	// panelOffset is the first visible row of the active overlay. It
	// keeps long panels usable in short windows.
	panelOffset int
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
	ta.Placeholder = "Type a message — Enter sends, Ctrl+J adds a line"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.SetHeight(1)
	// Ctrl+K opens the model picker at the application level, so the
	// composer's own delete-after-cursor binding moves out of the way.
	ta.KeyMap.DeleteAfterCursor = key.NewBinding(key.WithKeys("ctrl+shift+k"))
	ta.Focus()

	vp := viewport.New()
	// Long lines wrap instead of being cut off. The viewport handles no
	// keys of its own: scrolling is routed explicitly so the arrow keys
	// stay with the composer.
	vp.SoftWrap = true
	vp.KeyMap = viewport.KeyMap{}

	return Model{
		engine:   engine,
		hooks:    hooks,
		composer: ta,
		viewport: vp,
		color:    ColorEnabled(),
		theme:    ThemeHALClassic,
		now:      time.Now,
	}
}

// clock returns the current time through the replaceable clock.
func (m Model) clock() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

// SetTheme applies a preset; used by the picker and tests.
func (m *Model) SetTheme(t Theme) { m.theme = t }

// Theme returns the active preset, for tests.
func (m Model) Theme() Theme { return m.theme }

// SetModelName sets the footer model label.
func (m *Model) SetModelName(name string) { m.model = name }

// SetTitle sets the conversation title shown next to the header.
func (m *Model) SetTitle(title string) { m.title = title }

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
			m.display = append(m.display, "HAL > "+renderMarkdown(line.Text, m.assistantWidth()))
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
	case tea.KeyboardEnhancementsMsg:
		m.keyDisambiguation = msg.SupportsKeyDisambiguation()
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		switch key {
		case keyQuit, keyQuit2:
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
			// Remember the escape: a terminal that splits Alt+Enter into
			// ESC and CR must not let the CR submit the message.
			m.escapedAt = m.clock()
			return m, nil
		case keyEnter, keySend:
			// A held key repeats; only the first press may submit.
			if msg.IsRepeat {
				return m, nil
			}
			if m.completesEscape() {
				// The Enter completed an ESC CR sequence: the user
				// meant a newline, not a send.
				m.insertNewline()
				return m, nil
			}
			return m.submit()
		}
		if newlineKeys[key] {
			m.insertNewline()
			return m, nil
		}
		switch key {
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
	case tea.MouseWheelMsg:
		// The viewport owns scrolling; the composer never sees mouse input.
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
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
		return m, tea.Batch(cmds...)
	}
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	m.fitComposer()
	return m, cmd
}

// completesEscape reports whether an Enter right after an Escape is the
// tail of an escape sequence (ESC CR) rather than a send.
func (m Model) completesEscape() bool {
	if m.escapedAt.IsZero() {
		return false
	}
	if m.clock().Sub(m.escapedAt) >= escapeGrace {
		m.escapedAt = time.Time{}
		return false
	}
	m.escapedAt = time.Time{}
	return true
}

// submit sends the composer contents. Empty input never submits, a
// repeated key event never submits twice, and a slash command is handled
// locally instead of reaching the model.
func (m Model) submit() (tea.Model, tea.Cmd) {
	if m.sending {
		return m, nil
	}
	raw := m.composer.Value()
	// Only trailing whitespace is dropped: leading indentation belongs to
	// the prompt and must survive.
	input := strings.TrimRight(raw, " \t\n")
	if strings.TrimSpace(input) == "" {
		return m, nil
	}
	if strings.HasPrefix(input, "/") {
		m.composer.Reset()
		m.fitComposer()
		if strings.HasPrefix(input, "//") {
			// "//" escapes to a literal slash-prefixed message.
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
}

// insertNewline breaks the line in the composer without sending.
func (m *Model) insertNewline() {
	m.composer.InsertString("\n")
	m.fitComposer()
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
	vh, ch := frameSizes(contentHeight(m.height), m.composer.Height())
	m.viewHeight = vh
	m.compHeight = ch
	m.composer.SetHeight(ch)
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(vh)
	w := m.width - 2
	if w < 1 {
		w = 1
	}
	m.composer.SetWidth(w)
}

// contentHeight returns the number of rows available between the header
// rule and the bottom rule. Short windows drop the rules and the notice
// line instead of letting the frame overflow.
func contentHeight(height int) int {
	if height < 7 {
		if height < 3 {
			return 1
		}
		return height - 2
	}
	return height - 5
}

// frameSizes splits the content area between the conversation viewport and
// the composer. The composer keeps at least one row and never starves the
// viewport.
func frameSizes(available, desiredComposer int) (viewportRows, composerRows int) {
	composerRows = desiredComposer
	if composerRows < 1 {
		composerRows = 1
	}
	if composerRows > maxComposerHeight {
		composerRows = maxComposerHeight
	}
	if composerRows > available-1 {
		composerRows = available - 1
	}
	if composerRows < 1 {
		composerRows = 1
	}
	viewportRows = available - composerRows
	if viewportRows < 1 {
		viewportRows = 1
	}
	return viewportRows, composerRows
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
	rendered := renderMarkdown(text, m.assistantWidth())
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
			m.display = append(m.display, "HAL > "+renderMarkdown(rest, m.assistantWidth()))
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

// View implements tea.View. HAL takes over the terminal with the
// alternate screen buffer, so the shell output is hidden while the
// conversation runs and the previous screen comes back on exit.
func (m Model) View() tea.View {
	content := strings.Join(m.layoutRows(), "\n")
	if !m.color {
		content = stripANSI(content)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// layoutRows builds the whole screen as exactly the terminal height, so
// the frame never scrolls the terminal: header, rule, content region,
// rule, status footer and a notice line.
func (m Model) layoutRows() []string {
	if !m.ready {
		return []string{"Starting HAL9001…"}
	}
	header := m.headerRow()
	if m.height < 7 {
		// Very short window: the header and the footer survive, the
		// decorative chrome does not.
		rows := []string{header}
		rows = append(rows, m.contentRows(contentHeight(m.height))...)
		rows = append(rows, m.statusBar())
		return rows
	}
	rows := make([]string, 0, m.height)
	rows = append(rows, header, m.rule())
	rows = append(rows, m.contentRows(contentHeight(m.height))...)
	rows = append(rows, m.rule(), m.statusBar(), m.noticeRow())
	return rows
}

// contentRows renders the region between the rules: the conversation and
// the composer in chat mode, an overlay panel otherwise.
func (m Model) contentRows(n int) []string {
	switch m.view {
	case viewOnboard:
		return m.panelRows(m.onboardRows(), n)
	case viewPicker:
		return m.panelRows(m.pickerRows(), n)
	case viewTheme:
		return m.panelRows(m.themeRows(), n)
	case viewHelp:
		return m.panelRows(m.helpRows(), n)
	case viewSessions:
		return m.panelRows(m.sessionsRows(), n)
	}
	return m.chatRows(n)
}

// chatRows stacks the conversation viewport above the composer.
func (m Model) chatRows(n int) []string {
	vh, ch := frameSizes(n, m.compHeight)
	rows := make([]string, 0, n)
	rows = append(rows, splitRows(m.viewport.View(), vh)...)
	rows = append(rows, splitRows(m.composer.View(), ch)...)
	return rows
}

// panelRows pads or trims an overlay panel to exactly n rows, honouring
// the panel scroll offset so long panels stay reachable in short windows.
func (m Model) panelRows(lines []string, n int) []string {
	offset := m.panelOffset
	if offset > len(lines)-1 {
		offset = len(lines) - 1
	}
	if offset < 0 {
		offset = 0
	}
	rows := make([]string, 0, n)
	for i := 0; i < n; i++ {
		idx := offset + i
		if idx < len(lines) {
			rows = append(rows, m.fitWidth(lines[idx]))
			continue
		}
		rows = append(rows, "")
	}
	return rows
}

// ensureVisible scrolls the panel so the given row stays on screen.
func (m *Model) ensureVisible(row, total, n int) {
	if n < 1 {
		return
	}
	if row < m.panelOffset {
		m.panelOffset = row
	}
	if row >= m.panelOffset+n {
		m.panelOffset = row - n + 1
	}
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

// splitRows returns exactly n rows of text.
func splitRows(text string, n int) []string {
	parts := strings.Split(text, "\n")
	rows := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if i < len(parts) {
			rows = append(rows, parts[i])
			continue
		}
		rows = append(rows, "")
	}
	return rows
}

// fitWidth trims a rendered line to the terminal width so a long line can
// never push the frame out of shape.
func (m Model) fitWidth(line string) string {
	if m.width < 1 {
		return ""
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(line)
}

// headerRow renders the fixed header: the eye and the name, plus the
// conversation title when one is known.
func (m Model) headerRow() string {
	st := m.theme.styles()
	eye := "●"
	title := strings.TrimSpace(m.title)
	if title == "" {
		if !m.color {
			return eye + " HAL9001"
		}
		return st.header.Render(eye + " HAL9001")
	}
	if !m.color {
		return eye + " HAL9001 · " + title
	}
	return st.header.Render(eye+" HAL9001") + " " + st.footer.Render("· "+title)
}

// noticeRow renders the status line under the footer. The row is always
// present so the layout never shifts when a notice appears.
func (m Model) noticeRow() string {
	if m.notice == "" {
		return ""
	}
	if !m.color {
		return m.notice
	}
	return m.theme.styles().notice.Render(m.notice)
}

func (m Model) heading() string {
	return m.headerRow()
}

// assistantWidth is the width available for a rendered assistant reply.
// The "HAL > " label and the viewport margin are subtracted so a reply
// wraps inside the frame instead of being wrapped twice.
func (m Model) assistantWidth() int {
	w := m.width - len("HAL > ") - 2
	if w < 20 {
		return 20
	}
	return w
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
