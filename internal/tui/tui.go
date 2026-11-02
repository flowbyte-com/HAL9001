// Package tui is HAL9001's dedicated terminal interface: a fullscreen
// conversation area, a multiline composer, an onboarding screen, and
// model/theme pickers. Presentation only — the conversation core lives in
// internal/engine, configuration in internal/config. There is
// deliberately no coding IDE, command console or task dashboard here.
package tui

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/flowbyte-com/HAL9001/internal/provider"
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
	// Effort returns the active reasoning level and whether the active
	// model supports effort control at all.
	Effort func() (string, bool)
	// SetEffort persists a level for the active model and returns the
	// engine for subsequent requests.
	SetEffort func(level string) (Engine, error)
}

// Engine runs the conversation behind the interface. It matches
// internal/engine's streaming method; tests supply scripted doubles.
type Engine interface {
	RespondStream(ctx context.Context, input string, emit func(string)) (string, error)
}

// ProgressEngine is implemented by engines that can also report
// reasoning presence and content-free turn statistics. The interface
// prefers it when available and falls back to Engine otherwise.
type ProgressEngine interface {
	RespondStreamEvents(ctx context.Context, input string, emit func(engine.Event)) (string, error)
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
	viewDiag
	viewEffort
)

// SessionLine is one stored conversation turn, for session resume.
type SessionLine struct {
	Role string
	Text string
}

// maxComposerHeight caps the growing composer; the viewport keeps the rest.
const maxComposerHeight = 8

// Long user prompts are collapsed to a short preview: a pasted prompt is
// often a whole page, and it would otherwise push HAL's answer out of
// view. The full text is always kept in the entry and in the session.
const (
	// userPreviewRows is how many rows of a long message stay visible.
	userPreviewRows = 4
	// collapseAt is the row count above which a message is collapsed on
	// submission.
	collapseAt = 6
)

// entry is one conversation turn. Text is the original, untouched: user
// text as typed and assistant text as Markdown. rows caches the rendered
// form for the current width.
type entry struct {
	role     string // "user" or "assistant"
	text     string
	expanded bool
	rows     []string
}

// Model is the Bubble Tea application state.
type Model struct {
	engine   Engine
	hooks    Hooks
	viewport viewport.Model
	composer textarea.Model
	entries  []entry
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
	// streamID identifies the current turn. Messages from an older turn
	// are ignored, so cancelling one stream can never disturb the next.
	streamID int
	// streamStarted is when the current turn began.
	streamStarted time.Time
	// reasoningSeen reports that the provider sent reasoning events for
	// this turn. The text itself is never seen here.
	reasoningSeen bool
	// lastStats is the content-free telemetry of the last finished turn.
	lastStats *provider.TurnStats
	// effort is the active reasoning level and effortSupported reports
	// whether the active model exposes effort control at all.
	effort          string
	effortSupported bool
	// Effort picker state.
	effortCursor int
	effortNotice string
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

// SetEffort sets the active reasoning level and whether the model
// supports it, for the footer and the selector.
func (m *Model) SetEffort(level string, supported bool) {
	m.effort = provider.NormalizeEffort(level)
	m.effortSupported = supported
	if !supported {
		m.effort = ""
	}
}

// SetTitle sets the conversation title shown next to the header.
func (m *Model) SetTitle(title string) { m.title = title }

// SetHistory replaces the conversation view with stored turns (raw user
// text, raw assistant Markdown rendered on display).
func (m *Model) SetHistory(lines []SessionLine) {
	m.entries = nil
	m.pending = ""
	for _, line := range lines {
		m.entries = append(m.entries, entry{role: line.Role, text: line.Text})
	}
	m.rewrap()
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

type streamDeltaMsg struct {
	id   int
	text string
}
type streamThinkingMsg struct{ id int }
type streamStatsMsg struct {
	id    int
	stats provider.TurnStats
}
type streamStartedMsg struct{ id int }
type responseMsg struct {
	id   int
	text string
}
type errorMsg struct {
	id  int
	err error
}
type tickMsg time.Time

// tickCmd re-renders the streaming notice once a second so a slow turn
// shows elapsed time instead of a frozen label.
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// sendCmd starts the streamed turn. With a live sender it runs in the
// background and posts deltas; headless it runs inline for determinism.
func (m Model) sendCmd(id int, input string, ctx context.Context) tea.Cmd {
	if m.sender == nil {
		return func() tea.Msg { return m.runRespond(id, input, ctx) }
	}
	return func() tea.Msg {
		go m.runStream(id, input, ctx)
		return streamStartedMsg{id: id}
	}
}

func (m Model) runRespond(id int, input string, ctx context.Context) tea.Msg {
	reply, err := m.engine.RespondStream(ctx, input, nil)
	if err != nil {
		return errorMsg{id: id, err: err}
	}
	return responseMsg{id: id, text: reply}
}

// runStream drives the live turn. The engine reports reasoning presence
// and content-free statistics as well as text; none of the reasoning
// text itself ever reaches the interface.
func (m Model) runStream(id int, input string, ctx context.Context) {
	emit := func(ev engine.Event) {
		switch {
		case ev.Stats != nil:
			m.sender(streamStatsMsg{id: id, stats: *ev.Stats})
		case ev.Text != "":
			m.sender(streamDeltaMsg{id: id, text: ev.Text})
		case ev.Reasoning:
			m.sender(streamThinkingMsg{id: id})
		}
	}
	var (
		reply string
		err   error
	)
	if pe, ok := m.engine.(ProgressEngine); ok {
		reply, err = pe.RespondStreamEvents(ctx, input, emit)
	} else {
		reply, err = m.engine.RespondStream(ctx, input, func(text string) {
			m.sender(streamDeltaMsg{id: id, text: text})
		})
	}
	if err != nil {
		m.sender(errorMsg{id: id, err: err})
		return
	}
	m.sender(responseMsg{id: id, text: reply})
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
	case viewDiag:
		return m.updateDiag(msg)
	case viewEffort:
		return m.updateEffort(msg)
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
		// Any key other than the Escape itself ends the escape sequence
		// window: only an Enter that immediately follows an Escape can be
		// its second half.
		if key != "esc" && key != keyEnter && key != keySend {
			m.escapedAt = time.Time{}
		}
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
		case "ctrl+o":
			m.toggleLastUser()
			return m, nil
		}
	case tea.PasteMsg:
		// A paste ends any escape-sequence window: the user is typing.
		m.escapedAt = time.Time{}
		m.composer.InsertString(msg.Content)
		m.fitComposer()
		return m, nil
	case tea.MouseWheelMsg:
		// The viewport owns scrolling; the composer never sees mouse input.
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case streamThinkingMsg:
		if !m.currentTurn(msg.id) {
			return m, nil
		}
		m.reasoningSeen = true
		m.notice = m.streamingNotice()
		return m, nil
	case streamDeltaMsg:
		if !m.currentTurn(msg.id) {
			return m, nil
		}
		stick := m.viewport.AtBottom()
		m.pending += msg.text
		m.notice = m.streamingNotice()
		m.renderConversation()
		if stick {
			m.viewport.GotoBottom()
		}
		return m, nil
	case streamStatsMsg:
		if !m.currentTurn(msg.id) {
			return m, nil
		}
		stats := msg.stats
		m.lastStats = &stats
		return m, nil
	case tickMsg:
		if !m.sending || m.sender == nil {
			return m, nil
		}
		m.notice = m.streamingNotice()
		return m, tickCmd()
	case responseMsg:
		if !m.currentTurn(msg.id) {
			return m, nil
		}
		m.sending = false
		m.stopStream()
		m.pending = ""
		m.reasoningSeen = false
		m.lastResponse = msg.text
		if m.lastStats != nil && m.lastStats.OutputLimitHit {
			m.notice = "output limit reached — the reply was cut at max_tokens"
		} else {
			m.notice = ""
		}
		m.appendEntry("assistant", msg.text)
		return m, nil
	case copyResultMsg:
		return m.updateCopyResult(msg)
	case errorMsg:
		if !m.currentTurn(msg.id) {
			// A late message from a cancelled turn must never touch the
			// current one.
			return m, nil
		}
		m.sending = false
		m.stopStream()
		m.reasoningSeen = false
		m.pending = ""
		switch {
		case errors.Is(msg.err, context.Canceled):
			m.notice = "cancelled — stored nothing from the assistant"
		case errors.Is(msg.err, engine.ErrNoProvider):
			m.notice = "No provider configured — message stored in this session."
		case errors.Is(msg.err, engine.ErrNoVisibleAnswer):
			m.notice = "no visible answer — see /diag"
			m.appendEntry("assistant", m.emptyAnswerNote(msg.err))
		case errors.Is(msg.err, provider.ErrStreamIdle):
			m.notice = "the provider went silent — the request was closed to avoid a silent charge"
			m.appendEntry("assistant", "(the provider stopped sending data; the request was closed)")
		case errors.Is(msg.err, provider.ErrTurnTimeout):
			m.notice = "the turn hit its time limit — the request was closed"
			m.appendEntry("assistant", "(the turn exceeded its time limit; the request was closed)")
		default:
			m.notice = "error: " + msg.err.Error()
			m.appendEntry("assistant", "(I couldn't answer that: "+msg.err.Error()+")")
		}
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
	raw := m.composer.Value()
	// Only trailing whitespace is dropped: leading indentation belongs to
	// the prompt and must survive.
	input := strings.TrimRight(raw, " \t\n")
	if strings.TrimSpace(input) == "" {
		return m, nil
	}
	// Slash commands are local, so they may run while a turn is in
	// flight: /clear and /effort are most useful exactly then. A "//"
	// escape is a message, not a command, and stays subject to the
	// in-flight guard below.
	if strings.HasPrefix(input, "/") && !strings.HasPrefix(input, "//") {
		m.composer.Reset()
		m.fitComposer()
		return m, m.dispatchSlash(input)
	}
	if m.sending {
		return m, nil
	}
	if strings.HasPrefix(input, "//") {
		// "//" escapes to a literal slash-prefixed message.
		input = strings.TrimSpace(input[1:])
	}
	m.composer.Reset()
	m.fitComposer()
	m.streamID++
	id := m.streamID
	m.appendEntry("user", input)
	m.viewport.GotoBottom()
	m.pending = ""
	m.sending = true
	m.streamStarted = m.clock()
	m.reasoningSeen = false
	m.notice = m.streamingNotice()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	cmd := m.sendCmd(id, input, ctx)
	if m.sender == nil {
		return m, cmd
	}
	return m, tea.Batch(cmd, tickCmd())
}

// emptyAnswerNote explains a turn that produced no visible text. It uses
// the diagnostics already collected: no retry, no invented answer, just
// what the provider reported.
func (m Model) emptyAnswerNote(err error) string {
	parts := []string{"no visible answer"}
	s := m.lastStats
	if s != nil {
		if s.OutputLimitHit {
			parts = append(parts, "the output budget was exhausted during reasoning")
		}
		if s.FinishReason != "" {
			parts = append(parts, "finish_reason="+s.FinishReason)
		}
		if s.HasUsage && s.Usage.CompletionTokens > 0 {
			parts = append(parts, fmt.Sprintf("completion tokens %d", s.Usage.CompletionTokens))
		}
		if s.ReasoningChunks > 0 {
			parts = append(parts, fmt.Sprintf("reasoning events %d", s.ReasoningChunks))
		}
	} else if err != nil {
		parts = append(parts, err.Error())
	}
	parts = append(parts, "see /diag")
	return "(" + strings.Join(parts, " · ") + ")"
}

// streamingNotice describes the turn without exposing private reasoning:
// it only says which phase the provider is in and how long it has run.
func (m Model) streamingNotice() string {
	var elapsed time.Duration
	if !m.streamStarted.IsZero() {
		elapsed = m.clock().Sub(m.streamStarted).Round(time.Second)
	}
	if elapsed < 0 {
		elapsed = 0
	}
	phase := "waiting for the model"
	switch {
	case m.pending != "":
		phase = "streaming"
	case m.reasoningSeen:
		phase = "thinking"
	}
	return fmt.Sprintf("%s… %s · Esc cancels", phase, elapsed)
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

// appendEntry adds one conversation turn and re-renders it. It follows
// the new content only when the reader was already at the bottom.
func (m *Model) appendEntry(role, text string) {
	stick := m.viewport.AtBottom()
	m.entries = append(m.entries, entry{role: role, text: text})
	m.renderEntry(len(m.entries) - 1)
	m.renderConversation()
	if stick {
		m.viewport.GotoBottom()
	}
}

// currentTurn reports whether a message belongs to the live turn. An
// unset id (0) means it did not come from a stream and always applies.
func (m Model) currentTurn(id int) bool { return id == 0 || id == m.streamID }

// userWidth is the room left for user text after the "You > " label.
func (m Model) userWidth() int {
	w := m.width - len("You > ")
	if w < 10 {
		return 10
	}
	return w
}

// renderEntry refreshes one entry's cached rows for the current width,
// applying the collapse rule to long user messages.
func (m *Model) renderEntry(i int) {
	e := &m.entries[i]
	switch e.role {
	case "user":
		e.rows = m.renderUser(e)
	default:
		label := "HAL > "
		indent := strings.Repeat(" ", len(label))
		body := renderMarkdown(e.text, m.assistantWidth())
		lines := strings.Split(body, "\n")
		rows := make([]string, 0, len(lines))
		for j, line := range lines {
			if j == 0 {
				rows = append(rows, label+line)
				continue
			}
			rows = append(rows, indent+line)
		}
		e.rows = rows
	}
}

// renderUser wraps a user message and decides how much of it to show.
// The full text stays in e.text; only the rendering is shortened.
func (m Model) renderUser(e *entry) []string {
	width := m.userWidth()
	wrapped := strings.Split(lipgloss.NewStyle().Width(width).Render(e.text), "\n")
	lines := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		lines = append(lines, strings.TrimRight(line, " "))
	}
	rows := make([]string, 0, len(lines)+1)
	for j, line := range lines {
		if j == 0 {
			rows = append(rows, "You > "+line)
			continue
		}
		rows = append(rows, strings.Repeat(" ", len("You > "))+line)
	}
	if len(rows) <= collapseAt {
		return rows
	}
	if e.expanded {
		return append(rows, m.collapseHint("Ctrl+O to collapse"))
	}
	visible := rows[:userPreviewRows]
	hidden := len(rows) - userPreviewRows
	hint := fmt.Sprintf("… %d more line(s) · Ctrl+O to expand", hidden)
	if hidden == 1 {
		hint = "… 1 more line · Ctrl+O to expand"
	}
	return append(visible, m.collapseHint(hint))
}

// collapseHint renders the collapsed/expanded affordance aligned with the
// message text.
func (m Model) collapseHint(text string) string {
	hint := strings.Repeat(" ", len("You > ")) + text
	if !m.color {
		return hint
	}
	return strings.Repeat(" ", len("You > ")) + m.theme.styles().footer.Render(text)
}

// collapsible reports whether the entry is long enough to collapse.
func (m Model) collapsible(i int) bool {
	e := m.entries[i]
	if e.role != "user" {
		return false
	}
	full := m.renderUser(&entry{role: e.role, text: e.text, expanded: true})
	return len(full) > collapseAt
}

// toggleLastUser expands or collapses the most recent user message and
// keeps the reading position stable.
func (m *Model) toggleLastUser() {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].role != "user" {
			continue
		}
		if !m.collapsible(i) {
			m.notice = "The last message is short — nothing to expand."
			return
		}
		m.toggleEntry(i)
		return
	}
	m.notice = "No message to expand yet."
}

// toggleEntry flips one entry between preview and full text.
func (m *Model) toggleEntry(i int) {
	stick := m.viewport.AtBottom()
	before := m.rowsBefore(i)
	m.entries[i].expanded = !m.entries[i].expanded
	m.renderEntry(i)
	m.renderConversation()
	after := m.rowsBefore(i)
	if stick {
		m.viewport.GotoBottom()
		return
	}
	// If the toggled message sits above the viewport, shift the offset by
	// the number of rows it gained or lost so the text stays put.
	if before < m.viewport.YOffset() {
		offset := m.viewport.YOffset() + (after - before)
		if offset < 0 {
			offset = 0
		}
		m.viewport.SetYOffset(offset)
	}
}

// expandAll and collapseAll apply to every long user message.
func (m *Model) setAllUserExpanded(expanded bool) {
	stick := m.viewport.AtBottom()
	for i := range m.entries {
		if m.entries[i].role != "user" || !m.collapsible(i) {
			continue
		}
		m.entries[i].expanded = expanded
		m.renderEntry(i)
	}
	m.renderConversation()
	if stick {
		m.viewport.GotoBottom()
	}
}

// rowsBefore counts the rendered rows of every entry before i.
func (m Model) rowsBefore(i int) int {
	n := 0
	for j := 0; j < i && j < len(m.entries); j++ {
		n += len(m.entries[j].rows)
	}
	return n
}

// rewrap re-renders every entry for the current width so resizing
// reflows the conversation instead of leaving stale wrapping.
func (m *Model) rewrap() {
	for i := range m.entries {
		m.renderEntry(i)
	}
}

func (m *Model) renderConversation() {
	st := m.theme.styles()
	styleRow := func(row string) string {
		if !m.color {
			return row
		}
		if rest, ok := strings.CutPrefix(row, "You > "); ok {
			return st.user.Render("You > ") + rest
		}
		if rest, ok := strings.CutPrefix(row, "HAL > "); ok {
			return st.hal.Render("HAL > ") + rest
		}
		return row
	}
	content := make([]string, 0, len(m.entries)+1)
	for _, e := range m.entries {
		for _, row := range e.rows {
			content = append(content, styleRow(row))
		}
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
	case viewDiag:
		return m.panelRows(m.diagRows(), n)
	case viewEffort:
		return m.panelRows(m.effortRows(), n)
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
	if m.effortSupported && m.effort != "" {
		right += " · " + m.effort
	}
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
func (m Model) Lines() []string {
	out := make([]string, 0, len(m.entries))
	for _, e := range m.entries {
		label := "HAL > "
		if e.role == "user" {
			label = "You > "
		}
		out = append(out, label+e.text)
	}
	return out
}

// Entries returns the conversation turns, for tests.
func (m Model) Entries() []entry { return append([]entry(nil), m.entries...) }

// LastStats returns the telemetry of the last finished turn, for tests.
func (m Model) LastStats() *provider.TurnStats { return m.lastStats }

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
