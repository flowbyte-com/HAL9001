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
	"github.com/rivo/uniseg"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/skills"
	"github.com/flowbyte-com/HAL9001/internal/usage"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
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
	keySend       = "ctrl+s"
	keyQuit       = "ctrl+c"
	keyQuit2      = "ctrl+q"
	keyPicker     = "ctrl+k"
	keyNew        = "ctrl+n"
	keyEnter      = "enter"
	keyPermission = "ctrl+p"
)

// The draft keys. The hint row and the clear notice are built from
// these, so a rebound key can never leave a stale hint on screen.
const (
	keyClearDraft   = "ctrl+u"
	keyRestoreDraft = "ctrl+z"
)

// keyNewline is the chord the hint row offers for a line break. It is
// the reliable one: alt+enter and shift+enter depend on what the
// terminal reports, ctrl+j does not.
const keyNewline = "ctrl+j"

// The approval keys. Chords on purpose: a stray letter, typed text or a
// paste can never decide a write request, only a deliberate chord in the
// approval view can.
const (
	keyApproveWrite = "ctrl+y"
	keyRejectWrite  = "ctrl+n"
)

// newlineKeys insert a line break in the composer instead of sending.
// ctrl+j is the universally reliable fallback; alt+enter arrives as ESC
// CR on xterm-compatible terminals; shift+enter requires the Kitty
// keyboard protocol and is simply a plain Enter elsewhere.
var newlineKeys = map[string]bool{
	keyNewline:    true,
	"alt+enter":   true,
	"shift+enter": true,
}

// escapeGrace is the window during which an Enter right after an Escape
// is treated as the second half of an escape sequence. Alt+Enter reaches
// the application as ESC CR; a terminal that delivers the two bytes in
// separate reads would otherwise turn the CR into an accidental send.
const escapeGrace = 60 * time.Millisecond

// noticeLifetime is how long a status notice stays under the footer. The
// row itself is always part of the frame, so a notice appearing or
// expiring never shifts the conversation or the composer. It is a
// variable so the tests can shorten it; the interface never changes it.
var noticeLifetime = 5 * time.Second

// noticeExpireMsg clears the notice it belongs to. seq names the notice:
// a message about a notice that has already been replaced is ignored.
type noticeExpireMsg struct{ seq int }

// noticeExpiryCmd schedules the removal of one notice.
func noticeExpiryCmd(seq int) tea.Cmd {
	return tea.Tick(noticeLifetime, func(time.Time) tea.Msg {
		return noticeExpireMsg{seq: seq}
	})
}

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
	// ListSessions returns stored conversations, most recently active
	// first, with the current one marked.
	ListSessions func() ([]SessionInfo, error)
	// SwitchSession loads a stored conversation, returning the engine
	// and its lines (user text raw, assistant text raw Markdown). It also
	// selects the session for the next launch.
	SwitchSession func(name string) (Engine, []SessionLine, error)
	// RenameSession sets the display title of the active conversation and
	// returns the effective title to show.
	RenameSession func(title string) (string, error)
	// SessionTitle reports the display title of the active conversation.
	SessionTitle func() string
	// SessionID reports the immutable identity of the active conversation.
	// It is used to bind the session-scoped 666 permission.
	SessionID func() string
	// Copy writes text to the system clipboard (OSC 52). Nil means
	// clipboard access is unavailable.
	Copy func(text string) error
	// Effort returns the active reasoning level and whether the active
	// model supports effort control at all.
	Effort func() (string, bool)
	// Usage returns the cumulative accounting for the active session.
	Usage func() usage.Session
	// RecordTurn stores one finished turn's usage. A turn without
	// provider-reported usage is recorded as incomplete by the callee.
	RecordTurn func(stats provider.TurnStats)
	// RecordFailed stores a turn that produced no answer. Usage the
	// provider did report still counts; otherwise only the failed counter
	// moves. A nil argument means no statistics exist at all.
	RecordFailed func(stats *provider.TurnStats)
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
	viewUsage
	viewRename
	viewApprove
	viewSkills
	viewPermission
)

// SessionLine is one stored conversation turn, for session resume.
type SessionLine struct {
	Role string
	Text string
	// Attribution names the skills that shaped this answer, when the
	// stored turn recorded any. It is presentation metadata and never
	// part of Text.
	Attribution *session.Attribution
}

// SessionInfo describes one stored conversation for the browser. The ID is
// the immutable session identifier; the title is its display name.
type SessionInfo struct {
	ID      string
	Title   string
	Created time.Time
	Updated time.Time
	Current bool
}

// Composer geometry: one visible text row by default, growing with the
// content up to maxComposerHeight. Two full-width rules frame it — one
// above the text and one below — and the text sits one cell inside them.
const (
	maxComposerHeight = 5
	minComposerRows   = 1
	maxSuggestions    = 8
	// composerPadding is the space on each side of the draft. The
	// composer has no side borders, so this is all that keeps the text
	// off the terminal edge.
	composerPadding = 2
)

// Long user prompts are collapsed to a short preview: a pasted prompt is
// often a whole page, and it would otherwise push HAL's answer out of
// view. The full text is always kept in the entry and in the session.
const (
	// userPreviewRows is the most rows a collapsed message may show,
	// before the viewport-relative budget is applied.
	userPreviewRows = 4
)

// collapseState says how a long user message is presented.
//
// The states are distinct on purpose. A message the operator collapsed
// stays collapsed; a message that was only collapsed to make room for
// the answer opens again when the answer is there. The state is
// presentation only: it never touches the stored message.
type collapseState int

const (
	// collapseShown means the message is shown in full.
	collapseShown collapseState = iota
	// collapseManual means the operator collapsed the message.
	collapseManual
	// collapseAuto means the message was collapsed for the running turn.
	collapseAuto
)

// entry is one conversation turn. Text is the original, untouched: user
// text as typed and assistant text as Markdown. rows caches the rendered
// form for the current width. note marks an explanatory line that is not
// an answer (a cancellation or stall explanation), so metadata records
// never attach to it. meta carries the reported usage of a real answer.
type entry struct {
	role     string // "user" or "assistant"
	text     string
	collapse collapseState
	note     bool
	meta     usage.Turn
	hasMeta  bool
	// attribution names the skills that shaped this answer. It is
	// presentation metadata about the turn: it is never part of the
	// text, and it never reaches the model.
	attribution *session.Attribution
	rows        []string
	// deco marks the rows that are interface decoration rather than
	// message text: the collapse hint and the answer's metadata line.
	// They stay on screen, but a selection never copies them.
	deco []bool
	// logical holds the entry's unwrapped lines of text. The rows are
	// wrapped for the screen; the logical lines are what a selection
	// copies, so a wrapped line never becomes two lines.
	logical []string
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
	// notice is the transient status line under the footer. noticeSeq
	// identifies it, so the expiry of a notice that has already been
	// replaced is ignored.
	notice    string
	noticeSeq int
	color     bool
	theme     Theme
	view      View
	sender    func(tea.Msg)
	cancel    context.CancelFunc
	model     string
	title     string
	// sessionID is the immutable identity of the conversation. The
	// header shows the title, which can be renamed; this stays.
	sessionID string
	// frame is the resolved vertical geometry. It is recomputed on every
	// resize so the frame always fits the terminal exactly.
	frame chatFrame
	// desiredComposer is the height the content asks for, before the
	// frame fits it to the terminal.
	desiredComposer int
	// viewHeight and compHeight mirror frame.viewport and frame.composer.
	viewHeight int
	compHeight int
	// keyDisambiguation records whether the terminal reports modifier
	// combinations (Kitty keyboard protocol). It only affects what the
	// help panel claims about Shift+Enter.
	keyDisambiguation bool
	// mouse requests terminal mouse reporting so the wheel scrolls and
	// the pointer can select text. Off by default: reporting replaces
	// the terminal's own text selection.
	mouse bool
	// selecting is true while the pointer drags a selection.
	selecting bool
	// selAnchor and selFocus are the two ends of the selection, in
	// conversation coordinates, so scrolling the history does not move
	// them.
	selAnchor cellPos
	selFocus  cellPos
	// selMoved records that the pointer actually travelled. An ordinary
	// click must never copy anything.
	selMoved bool
	// selPointer is the last position of the pointer during a drag, in
	// screen coordinates. The auto-scroll needs it to know when the
	// pointer rests at the edge of the conversation.
	selPointerX     int
	selPointerY     int
	selPointerValid bool
	// selScrollDir is the direction of the auto-scroll: -1 up, 0 idle,
	// 1 down.
	selScrollDir int
	// selScrollGen identifies the running auto-scroll chain. A tick of
	// an older chain is ignored.
	selScrollGen int
	// conv maps the conversation's display cells to the text behind
	// them. It is rebuilt with the conversation.
	conv convMap
	// draftOffset is the first screen row of the draft the composer
	// shows. The wheel moves it; editing pulls it back to the cursor.
	draftOffset int
	// undoDraft is the last cleared draft, restorable with Ctrl+Z. A
	// single slot is enough: it is cleared on submit and on every
	// session change so an old draft can never leak into another
	// conversation.
	undoDraft string
	undoValid bool
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
	// turnAttribution is the credit of the last finished turn: the
	// skills whose instructions were supplied to the model. It is
	// cleared when a new turn starts.
	turnAttribution *session.Attribution
	// turnID is the identity of the last turn, as the session records
	// it.
	turnID string
	// effort is the active reasoning level and effortSupported reports
	// whether the active model exposes effort control at all.
	effort          string
	effortSupported bool
	// Effort picker state.
	effortCursor int
	effortNotice string
	// tokens is the cumulative accounting for the active session, and
	// turnStats is the usage of the turn that is finishing.
	tokens    usage.Session
	turnStats *provider.TurnStats
	// Slash-command suggestions.
	suggestOpen   bool
	suggestItems  []*command
	suggestCursor int
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
	// themeOriginal restores the appearance when a preview is dismissed.
	themeOriginal Theme
	// Last completed assistant reply (raw Markdown), for /copy.
	lastResponse string
	// Sessions view state.
	sessionItems  []SessionInfo
	sessionCursor int
	sessionNotice string
	// Rename overlay state.
	renameInput textinput.Model
	renameOpen  bool
	// Help view state.
	helpNotice string
	// workspace is the trusted store for approved writes. Nil means the
	// interface has no write capability: a proposal can never be
	// approved, and HAL stays 444.
	workspace *workspace.Store
	// write is the proposal awaiting the operator's decision, writeOpen
	// tells whether the approval view is showing, and writeState names
	// the capability state (444, 666 REQUESTED, 666 WRITING).
	write      workspace.Proposal
	writeOpen  bool
	writeState writeState
	// skills reaches the MPM skill store. It is shared with the engine,
	// which routes each turn, so the operator's override is seen by both.
	skills *skills.Selector
	// lastSkill is the routing decision of the latest turn.
	lastSkill *skills.Decision
	// permission is the trusted session-scoped 444/666 authority. It is
	// shared with the engine, which only receives its read-only Gate.
	permission *permission.Controller
	// lastTools is the execution-boundary record of the latest turn.
	lastTools []engine.ToolActivity
	// permissionNotice is local to the permission overlay.
	permissionNotice string
}

// New builds the interface. Color follows ColorEnabled at construction so
// tests can pin either mode deterministically.
func New(engine Engine) Model {
	return NewWithHooks(engine, Hooks{})
}

// NewWithHooks builds the interface with application hooks.
func NewWithHooks(engine Engine, hooks Hooks) Model {
	ta := textarea.New()
	// No placeholder: the composer is a writing surface, not a form
	// field, and an empty draft has to show the caret instead of a
	// label telling the operator what the surface is for.
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.SetHeight(minComposerRows)
	// HAL shows its own five-row window on the draft, so the textarea has
	// to render the whole draft from its first row. Its default maximum
	// height (99 rows) made it scroll its own viewport once a draft grew
	// past that, and the window HAL sliced out of the scrolled view could
	// land past the end: the composer went blank while the text was still
	// in the buffer. With no maximum the textarea never scrolls by itself.
	ta.MaxHeight = 0
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

	m := Model{
		engine:          engine,
		hooks:           hooks,
		composer:        ta,
		viewport:        vp,
		color:           ColorEnabled(),
		theme:           ThemeHALClassic,
		now:             time.Now,
		desiredComposer: minComposerRows,
		permission:      permission.New(),
	}
	m.setTheme(ThemeHALClassic)
	return m
}

// clock returns the current time through the replaceable clock.
func (m Model) clock() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

// SetTheme applies a preset; used by the picker and tests.
func (m *Model) SetTheme(t Theme) { m.setTheme(t) }

// setTheme applies a preset to the whole interface, including the
// composer's textarea, whose own defaults carry a fixed dark palette.
func (m *Model) setTheme(t Theme) {
	m.theme = t
	m.composer.SetStyles(t.composerStyles())
}

// Theme returns the active preset, for tests.
func (m Model) Theme() Theme { return m.theme }

// SetModelName sets the footer model label.
func (m *Model) SetModelName(name string) { m.model = name }

// SetUsage sets the cumulative accounting shown in the footer and /usage.
func (m *Model) SetUsage(u usage.Session) { m.tokens = u }

// refreshUsage reloads the active session's totals after a session change.
func (m *Model) refreshUsage() {
	if m.hooks.Usage != nil {
		m.tokens = m.hooks.Usage()
	}
}

// SetMouse enables or disables terminal mouse reporting.
func (m *Model) SetMouse(enabled bool) { m.mouse = enabled }

// SetSkills attaches the MPM skill selector.
//
// The selector is shared with the engine: the engine routes each turn
// through it, and the interface reads the catalogue and the last
// decision from it. Without one, no skill is ever selected and HAL
// behaves exactly as it did before skills existed.
func (m *Model) SetSkills(sel *skills.Selector) { m.skills = sel }

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
//
// The title is the human-readable name of the conversation. The
// immutable session identity is set separately and stays in the
// diagnostics.
func (m *Model) SetTitle(title string) { m.title = title }

// SetSessionID sets the immutable identity of the conversation.
//
// The header shows the human-readable title, which the operator can
// change; this is the name the conversation is stored under, and it
// never changes. A change always returns the permission gate to 444.
func (m *Model) SetSessionID(id string) {
	if m.sessionID != id {
		if m.permission != nil {
			m.permission.Disable()
			m.permission.SetSession(id)
		}
	}
	m.sessionID = id
}

// SetPermission attaches the trusted permission controller. It is shared
// with the engine, which only sees the controller's read-only Gate.
func (m *Model) SetPermission(c *permission.Controller) {
	if c == nil {
		c = permission.New()
	}
	m.permission = c
	if m.sessionID != "" {
		c.SetSession(m.sessionID)
	}
}

// PermissionMode reports the current authority, for tests and diagnostics.
func (m Model) PermissionMode() permission.Mode {
	if m.permission == nil {
		return permission.Guarded
	}
	return m.permission.Mode()
}

// resetPermission returns to 444 without changing which conversation the
// gate is bound to.
func (m *Model) resetPermission() {
	if m.permission != nil {
		m.permission.Disable()
	}
}

// SetHistory replaces the conversation view with stored turns (raw user
// text, raw assistant Markdown rendered on display).
//
// A stored turn is shown in full: the collapse that belonged to a
// finished turn is not stored, so reopening a conversation never
// perpetuates it.
func (m *Model) SetHistory(lines []SessionLine) {
	m.cancelWrite("session changed")
	m.resetPermission()
	m.entries = nil
	m.pending = ""
	m.clearUndo()
	m.clearSelection()
	for _, line := range lines {
		m.entries = append(m.entries, entry{
			role:        line.Role,
			text:        line.Text,
			collapse:    collapseShown,
			attribution: line.Attribution,
		})
	}
	m.attachHistoryMeta()
	m.rewrap()
	m.renderConversation()
	m.viewport.GotoBottom()
}

// attachHistoryMeta assigns stored per-answer records to the answers in
// order. Notes are skipped, and answers without a record keep no
// metadata.
func (m *Model) attachHistoryMeta() {
	h := m.tokens.History
	i := 0
	for j := range m.entries {
		if m.entries[j].role != "assistant" || m.entries[j].note {
			continue
		}
		if i < len(h) {
			m.attachMeta(j, h[i])
		}
		i++
	}
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
type streamToolMsg struct {
	id       int
	activity engine.ToolActivity
}

// skillDecisionMsg carries the routing decision of a turn back to the
// interface, so the diagnostics view can show which skill was used and
// the reply can be credited with the one that actually reached the
// model.
//
// The attribution is bound to the turn it belongs to: id is the
// interface's own turn number, and turn is the identity the session
// stores. A decision that arrives for an older turn is ignored.
type skillDecisionMsg struct {
	id          int
	turn        string
	decision    skills.Decision
	attribution *session.Attribution
}

type responseMsg struct {
	id   int
	text string
	// proposal is one prepared write awaiting the operator's decision.
	proposal *workspace.Proposal
	// decision is the routing decision of this turn, when the engine
	// reported one.
	decision *skills.Decision
	// attribution is the credit for this answer: the skills whose
	// instructions were supplied to the model. It travels with the
	// answer, so it cannot be attached to a different one.
	attribution *session.Attribution
	// turn is the identity of this exchange, as the session records it.
	turn string
	// stats is the telemetry the provider reported for this turn.
	stats *provider.TurnStats
	// tools is the execution-boundary record of every tool call in the
	// turn. It never contains file contents or command text.
	tools []engine.ToolActivity
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
	var (
		proposal    *workspace.Proposal
		decision    *skills.Decision
		attribution *session.Attribution
		turn        string
		stats       *provider.TurnStats
		tools       []engine.ToolActivity
	)
	reply, err := m.respondEvents(ctx, input, func(ev engine.Event) {
		switch {
		case ev.Proposal != nil:
			p := *ev.Proposal
			proposal = &p
		case ev.Tool != nil:
			tools = append(tools, *ev.Tool)
		case ev.Skill != nil:
			d := *ev.Skill
			decision = &d
			attribution = ev.Attribution
			turn = ev.TurnID
		case ev.Stats != nil:
			s := *ev.Stats
			stats = &s
		}
	})
	if err != nil {
		return errorMsg{id: id, err: err}
	}
	return responseMsg{
		id:          id,
		text:        reply,
		proposal:    proposal,
		decision:    decision,
		attribution: attribution,
		turn:        turn,
		stats:       stats,
		tools:       tools,
	}
}

// respondEvents runs one turn and forwards its events. Engines without
// progress events still answer; they simply bring no proposals.
func (m Model) respondEvents(ctx context.Context, input string, emit func(engine.Event)) (string, error) {
	if pe, ok := m.engine.(ProgressEngine); ok {
		return pe.RespondStreamEvents(ctx, input, emit)
	}
	return m.engine.RespondStream(ctx, input, func(string) {})
}

// runStream drives the live turn. The engine reports reasoning presence
// and content-free statistics as well as text; none of the reasoning
// text itself ever reaches the interface.
func (m Model) runStream(id int, input string, ctx context.Context) {
	emit := func(ev engine.Event) {
		switch {
		case ev.Stats != nil:
			m.sender(streamStatsMsg{id: id, stats: *ev.Stats})
		case ev.Tool != nil:
			m.sender(streamToolMsg{id: id, activity: *ev.Tool})
		case ev.Text != "":
			m.sender(streamDeltaMsg{id: id, text: ev.Text})
		case ev.Reasoning:
			m.sender(streamThinkingMsg{id: id})
		}
	}
	var (
		proposal    *workspace.Proposal
		decision    *skills.Decision
		attribution *session.Attribution
		turn        string
		tools       []engine.ToolActivity
	)
	collect := func(ev engine.Event) {
		if ev.Proposal != nil {
			p := *ev.Proposal
			proposal = &p
		}
		if ev.Tool != nil {
			tools = append(tools, *ev.Tool)
		}
		if ev.Skill != nil {
			d := *ev.Skill
			decision = &d
			attribution = ev.Attribution
			turn = ev.TurnID
			// The diagnostics learn the selection while the turn is
			// still running.
			m.sender(skillDecisionMsg{id: id, turn: ev.TurnID, decision: d})
		}
		emit(ev)
	}
	var (
		reply string
		err   error
	)
	if pe, ok := m.engine.(ProgressEngine); ok {
		reply, err = pe.RespondStreamEvents(ctx, input, collect)
	} else {
		reply, err = m.engine.RespondStream(ctx, input, func(text string) {
			m.sender(streamDeltaMsg{id: id, text: text})
		})
	}
	if err != nil {
		m.sender(errorMsg{id: id, err: err})
		return
	}
	m.sender(responseMsg{
		id:          id,
		text:        reply,
		proposal:    proposal,
		decision:    decision,
		attribution: attribution,
		turn:        turn,
		tools:       tools,
	})
}

// Update implements tea.Model.
//
// Every notice the interface shows is transient: a new one replaces the
// old one, and the one on screen disappears after noticeLifetime. The
// wrapper watches the notice, so a status line can never outlive its
// moment or quietly overwrite a newer message. The expiry is a one-shot
// tick whose generation is compared, so a replaced notice can never be
// cleared by the timer of the notice it replaced.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if exp, ok := msg.(noticeExpireMsg); ok {
		if exp.seq == m.noticeSeq {
			m.notice = ""
		}
		return m, nil
	}
	before := m.notice
	next, cmd := m.update(msg)
	out, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	if out.notice != before {
		out.noticeSeq++
		// While a turn is in flight the notice is the live status of
		// that turn: it is refreshed every second and cleared when the
		// turn ends, so it does not expire on its own.
		if out.notice != "" && !out.sending {
			cmd = tea.Batch(cmd, noticeExpiryCmd(out.noticeSeq))
		}
	}
	return out, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	case viewUsage:
		return m.updateUsage(msg)
	case viewRename:
		return m.updateRename(msg)
	case viewApprove:
		return m.updateApprove(msg)
	case viewSkills:
		return m.updateSkills(msg)
	case viewPermission:
		return m.updatePermission(msg)
	}
	return m.updateChat(msg)
}

func (m Model) updateChat(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		anchor := m.anchor()
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.sizePanes()
		m.rewrap()
		m.renderConversation()
		m.restoreAnchor(anchor)
		// The rows are wrapped again, so a selection would point at
		// different text than it did before.
		m.clearSelection()
		// The draft wraps differently at the new width: its height and
		// the window around the caret are recomputed, so the caret is
		// never left outside the composer after a resize.
		m.fitComposer()
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
		// Page keys are matched by code, not by their string form. A
		// terminal may report them with modifier bits set (Fn+PageUp on
		// a laptop arrives as shift+pgup, some VTE builds as ctrl+pgup)
		// and the key is still Page Up. Matching the string alone was
		// why paging appeared dead while Home/End worked.
		switch msg.Code {
		case tea.KeyPgUp:
			m.viewport.PageUp()
			return m, nil
		case tea.KeyPgDown:
			m.viewport.PageDown()
			return m, nil
		}
		if m.suggestOpen {
			switch key {
			case "up", "shift+tab":
				m.moveSuggestion(-1)
				return m, nil
			case "down":
				m.moveSuggestion(1)
				return m, nil
			case "tab":
				m.completeSuggestion()
				return m, nil
			case "esc":
				// Close the panel but keep the draft.
				m.closeSuggestions()
				return m, nil
			}
		}
		switch key {
		case keyQuit, keyQuit2:
			return m, m.quit()
		case keyPicker:
			m.enterPicker()
			return m, nil
		case keyNew:
			m.newSession()
			return m, nil
		case keyPermission:
			m.enterPermission()
			return m, nil
		case "esc":
			if m.selecting {
				// A drag in progress is cancelled: the selection and
				// its auto-scroll go away.
				m.clearSelection()
				m.notice = "Selection cleared."
				return m, nil
			}
			if m.sending {
				m.stopStream()
				m.sending = false
				m.notice = "cancelled — stored nothing from the assistant"
				// The turn is over, so the prompt that was collapsed
				// for it opens again.
				m.expandAfterTurn()
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
		case keyClearDraft:
			m.clearDraft()
			return m, nil
		case keyRestoreDraft:
			m.restoreDraft()
			return m, nil
		}
		switch key {
		case "home":
			m.viewport.GotoTop()
			return m, nil
		case "end":
			m.viewport.GotoBottom()
			return m, nil
		case keyJumpLatest:
			// The shortcut of the jump control. It works with the
			// pointer turned off: the keyboard is not the mouse.
			m.jumpToLatest()
			return m, nil
		case "ctrl+o":
			m.toggleLastUser()
			return m, nil
		}
	case tea.PasteMsg:
		// A paste ends any escape-sequence window: the user is typing.
		// Pasted text is inserted, never executed.
		m.escapedAt = time.Time{}
		m.composer.InsertString(clipboardText(msg.Content))
		m.refreshSuggestions()
		m.fitComposer()
		return m, nil
	case tea.MouseWheelMsg:
		return m.updateWheel(msg)
	case tea.MouseClickMsg:
		return m.mousePress(msg)
	case tea.MouseMotionMsg:
		return m.mouseDrag(msg)
	case tea.MouseReleaseMsg:
		return m.mouseRelease(msg)
	case selectionScrollMsg:
		return m.selectionScrollTick(msg)
	case streamThinkingMsg:
		if !m.currentTurn(msg.id) {
			return m, nil
		}
		m.reasoningSeen = true
		m.notice = m.streamingNotice()
		return m, nil
	case streamToolMsg:
		if !m.currentTurn(msg.id) {
			return m, nil
		}
		m.lastTools = mergeToolActivity(m.lastTools, msg.activity)
		m.notice = toolActivityLine(msg.activity)
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
		m.turnStats = &stats
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
		reported := msg.stats
		if reported == nil {
			reported = m.turnStats
		}
		m.recordTurn()
		if m.lastStats != nil && m.lastStats.OutputLimitHit {
			m.notice = "output limit reached — the reply was cut at max_tokens"
		} else {
			m.notice = ""
		}
		if msg.decision != nil {
			d := *msg.decision
			m.lastSkill = &d
		}
		// The credit for the answer arrives with the answer, in the same
		// message: it can only ever belong to this reply.
		m.turnAttribution = msg.attribution
		m.turnID = msg.turn
		m.lastTools = append([]engine.ToolActivity(nil), msg.tools...)
		m.appendToolNotes(msg.tools)
		i := m.appendEntry("assistant", msg.text)
		// Attach this turn's reported usage directly: the answer on screen
		// and its metadata stay together even before any refresh.
		if reported != nil && reported.HasUsage {
			m.attachMeta(i, turnFromStats(*reported))
		}
		m.attachAttribution(i)
		// The prompt was collapsed to make room for the answer. Now
		// that the answer is here, the prompt opens again.
		m.expandAfterTurn()
		if msg.proposal != nil {
			// One operation is ready for review. The overlay is the only
			// authority: nothing is written until the operator says so.
			m.requestWrite(*msg.proposal)
		}
		return m, nil
	case skillDecisionMsg:
		d := msg.decision
		m.lastSkill = &d
		m.turnID = msg.turn
		return m, nil
	case writeResultMsg:
		return m.updateWrite(msg)
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
		m.recordFailed()
		switch {
		case errors.Is(msg.err, context.Canceled):
			m.notice = "cancelled — stored nothing from the assistant"
		case errors.Is(msg.err, engine.ErrNoProvider):
			m.notice = "No provider configured — message stored in this session."
		case errors.Is(msg.err, engine.ErrNoVisibleAnswer):
			m.notice = "no visible answer — see /diag"
			m.appendNote("assistant", m.emptyAnswerNote(msg.err))
		case errors.Is(msg.err, provider.ErrStreamIdle):
			m.notice = "the provider went silent — the request was closed to avoid a silent charge"
			m.appendNote("assistant", "(the provider stopped sending data; the request was closed)")
		case errors.Is(msg.err, provider.ErrTurnTimeout):
			m.notice = "the turn hit its time limit — the request was closed"
			m.appendNote("assistant", "(the turn exceeded its time limit; the request was closed)")
		default:
			m.notice = "error: " + msg.err.Error()
			m.appendNote("assistant", "(I couldn't answer that: "+msg.err.Error()+")")
		}
		// The turn is over, however it ended: a prompt that was
		// collapsed for it opens again instead of staying abbreviated.
		m.expandAfterTurn()
		return m, nil
	}
	if m.ready {
		var cmds []tea.Cmd
		var cmd tea.Cmd
		m.composer, cmd = m.composer.Update(msg)
		cmds = append(cmds, cmd)
		// The suggestion panel follows the composer text.
		m.refreshSuggestions()
		// Every edit refits the composer: the draft may have grown a
		// wrapped row without adding a line, and the caret has to stay
		// in view.
		m.fitComposer()
		return m, tea.Batch(cmds...)
	}
	// Before the first frame the composer still takes the keystroke, so
	// nothing typed during startup is lost.
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
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
		// A complete command runs as typed, with its arguments. Anything
		// shorter runs the highlighted suggestion, so "/" + Enter does
		// something predictable instead of reporting an unknown command.
		if _, ok := commandByName(input); !ok && m.suggestOpen && m.suggestCursor < len(m.suggestItems) {
			input = m.suggestItems[m.suggestCursor].name
		}
		m.composer.Reset()
		m.closeSuggestions()
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
	m.clearUndo()
	m.fitComposer()
	m.streamID++
	id := m.streamID
	// A new turn starts with no attribution: whatever the previous turn
	// used says nothing about this one.
	m.turnAttribution = nil
	i := m.appendEntry("user", input)
	// A long prompt is collapsed while HAL answers: the answer is what
	// the operator wants to read. The collapse is temporary and the
	// prompt opens again when the answer is there.
	m.collapseForTurn(i)
	m.viewport.GotoBottom()
	m.pending = ""
	m.sending = true
	m.streamStarted = m.clock()
	m.reasoningSeen = false
	m.turnStats = nil
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

// formatDuration renders a wall-clock duration compactly: 12s, 2m 20s.
func formatDuration(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	s := ms / 1000
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %02ds", s/60, s%60)
}

// streamingNotice describes the turn without exposing private reasoning:
// the phase, the elapsed wall-clock time and the progress so far. Live
// token figures are estimates (marked ~); exact provider usage arrives
// with the final statistics.
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
	progress := m.liveProgress()
	if progress == "" {
		return fmt.Sprintf("%s… %s · Esc cancels", phase, elapsed)
	}
	return fmt.Sprintf("%s… %s · %s · Esc cancels", phase, elapsed, progress)
}

// liveProgress estimates output tokens from streamed characters: roughly
// four characters per token. The tilde marks it as an estimate; it never
// touches the recorded accounting. Streamed characters are a lower bound
// when the provider is also reasoning, so reasoning presence is shown
// alongside rather than folded into the number.
func (m Model) liveProgress() string {
	runes := len([]rune(m.pending))
	if runes == 0 && !m.reasoningSeen {
		return ""
	}
	est := runes / 4
	if est < 1 {
		est = 1
	}
	out := "~" + compactTokens(est) + " out"
	if m.reasoningSeen {
		return out + " + reasoning"
	}
	return out
}

// insertNewline breaks the line in the composer without sending.
func (m *Model) insertNewline() {
	m.composer.InsertString("\n")
	m.refreshSuggestions()
	m.fitComposer()
}

// fitComposer grows or shrinks the composer with its content, within a
// reasonable maximum, and reclaims the space for the viewport.
// updateWheel routes the wheel by pointer position. Over the
// conversation it pages the history through the viewport, which owns no
// keys of its own; over the composer it scrolls a long draft in place;
// over a panel it scrolls the panel. The composer text is never touched,
// and manual scrolling suspends bottom-follow until the bottom is reached
// again. Wheel events only arrive when mouse reporting is enabled.
func (m Model) updateWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.view != viewChat {
		if msg.Button == tea.MouseWheelUp {
			m.scrollPanel(-3)
		} else {
			m.scrollPanel(3)
		}
		return m, nil
	}
	if msg.Y >= 0 && msg.Y < m.viewportTop()+m.frame.viewport {
		// The conversation scrolls a comfortable step per notch. The
		// viewport keeps its own clamping, so the limits are exact.
		m.viewport.MouseWheelDelta = m.wheelRows()
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		if m.selecting {
			// The wheel takes over from the auto-scroll: only one of
			// them moves the conversation. The selection follows the
			// content, so it still covers what the pointer is over.
			m.stopSelectionScroll()
			m.selPointerX, m.selPointerY, m.selPointerValid = msg.X, msg.Y, true
			m.extendSelectionToPointer()
		}
		return m, cmd
	}
	// The wheel moves the composer window. It must not reach the
	// textarea: the textarea would scroll its own view and pull the
	// caret around with it.
	m.scrollDraft(draftWheelDelta(msg))
	return m, nil
}

// conversationWheelRows is how many rows one wheel notch moves the
// conversation.
//
// Three rows a notch meant a lot of notching to cross a long reply.
// Eight rows is a comfortable step, and the conversation is where the
// wheel is meant to be used.
//
// The step is separate from the auto-scroll of a drag selection, which
// stays at one row per tick: selecting text needs precision, reading
// does not.
const conversationWheelRows = 8

// wheelRows is the effective step for the current conversation area.
//
// A short area scrolls a screen at a time rather than overshooting it.
func (m Model) wheelRows() int {
	height := m.frame.viewport
	if height < 1 {
		return conversationWheelRows
	}
	if height < conversationWheelRows {
		return height
	}
	return conversationWheelRows
}

// draftWheelDelta is how far one wheel notch moves the composer
// window.
func draftWheelDelta(msg tea.MouseWheelMsg) int {
	if msg.Button == tea.MouseWheelUp {
		return -3
	}
	return 3
}

// clearDraft empties the composer without submitting, keeping the text in
// a one-slot undo buffer. The conversation and the session are untouched.
func (m *Model) clearDraft() {
	if strings.TrimSpace(m.composer.Value()) == "" {
		return
	}
	m.undoDraft = m.composer.Value()
	m.undoValid = true
	m.composer.Reset()
	m.refreshSuggestions()
	m.fitComposer()
	m.notice = "Draft cleared · " + keyName(keyRestoreDraft) + " to restore"
}

// restoreDraft puts the last cleared draft back, but only into an empty
// composer: restoring over typed text would destroy it.
func (m *Model) restoreDraft() {
	if !m.undoValid {
		m.notice = "Nothing to restore."
		return
	}
	if strings.TrimSpace(m.composer.Value()) != "" {
		m.notice = "Composer not empty — clear it first (Ctrl+U)."
		return
	}
	m.composer.SetValue(m.undoDraft)
	m.composer.CursorEnd()
	m.undoDraft = ""
	m.undoValid = false
	m.refreshSuggestions()
	m.fitComposer()
	m.notice = "Draft restored"
}

// clearUndo drops the undo buffer so a draft can never leak into another
// conversation or reappear after it was used.
func (m *Model) clearUndo() {
	m.undoDraft = ""
	m.undoValid = false
}

// fitComposer grows or shrinks the composer with its content: one row
// by default, up to maxComposerHeight, and then the draft scrolls inside
// the window.
func (m *Model) fitComposer() {
	if !m.ready {
		return
	}
	want := m.draftRows()
	if want < minComposerRows {
		want = minComposerRows
	}
	if want > maxComposerHeight {
		want = maxComposerHeight
	}
	if want != m.desiredComposer {
		m.desiredComposer = want
		m.resizeFrame()
	}
	// The textarea renders the whole draft, so HAL can show any window
	// of it.
	m.composer.SetHeight(max(m.draftRows(), m.frame.composer))
	// Editing is cursor-aware: the caret is always in view.
	m.followCursor()
}

// recordTurn reports the finished turn's usage once, then refreshes the
// totals. Without provider-reported usage the callee records the turn as
// incomplete rather than inventing numbers.
func (m *Model) recordTurn() {
	if m.turnStats == nil {
		return
	}
	if m.hooks.RecordTurn != nil {
		m.hooks.RecordTurn(*m.turnStats)
	}
	m.turnStats = nil
	if m.hooks.Usage != nil {
		m.tokens = m.hooks.Usage()
	}
}

// recordFailed reports a turn that produced no answer. Usage the provider
// did report still counts; otherwise only the failed counter moves.
func (m *Model) recordFailed() {
	if m.turnStats != nil && m.hooks.RecordFailed != nil {
		m.hooks.RecordFailed(m.turnStats)
	} else if m.turnStats == nil && m.hooks.RecordFailed != nil {
		m.hooks.RecordFailed(nil)
	}
	m.turnStats = nil
	if m.hooks.Usage != nil {
		m.tokens = m.hooks.Usage()
	}
}

// turnFromStats converts provider statistics into a stored usage record.
func turnFromStats(s provider.TurnStats) usage.Turn {
	return usage.Turn{
		Input:     s.Usage.PromptTokens,
		Output:    s.Usage.CompletionTokens,
		Reasoning: s.Usage.ReasoningTokens,
		Cached:    s.Usage.CachedTokens,
		Duration:  s.Duration.Milliseconds(),
		Finish:    s.FinishReason,
		Truncated: s.OutputLimitHit,
		Reported:  s.HasUsage,
	}
}

// quit cancels any live turn and asks the program to exit. Ctrl+C and the
// exit commands share this path, so there is one shutdown implementation.
func (m *Model) quit() tea.Cmd {
	m.cancelWrite("application exit")
	m.resetPermission()
	m.stopStream()
	m.sending = false
	m.pending = ""
	return tea.Quit
}

func (m *Model) stopStream() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

// sizePanes recomputes the frame. It reports whether the geometry
// changed, so callers can preserve the reading position.
func (m *Model) sizePanes() bool {
	f := computeChatFrame(m.height, m.desiredComposer, m.suggestRows())
	changed := f != m.frame
	m.frame = f
	m.viewHeight = m.frame.viewport
	m.compHeight = m.frame.composer
	// The textarea renders the whole draft so HAL can show any window
	// of it; the frame decides how many rows that window has.
	m.composer.SetHeight(max(m.draftRows(), m.frame.composer))
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(m.frame.viewport)
	// The composer text sits one cell inside each full-width rule.
	w := m.width - composerPadding
	if w < 1 {
		w = 1
	}
	m.composer.SetWidth(w)
	return changed
}

// scrollAnchor records the conversation row at the top of the viewport.
// It is anchored to an entry and a row inside that entry, not to a global
// line number, so re-wrapping Markdown at a new width cannot move the
// reader somewhere else.
type scrollAnchor struct {
	entry    int
	row      int
	count    int
	sub      int
	text     string
	atBottom bool
}

// wrappedHeight is the number of screen rows one content line occupies at
// the current viewport width.
func (m Model) wrappedHeight(line string) int {
	w := m.viewport.Width()
	if w < 1 {
		return 1
	}
	n := (lipgloss.Width(line) + w - 1) / w
	if n < 1 {
		n = 1
	}
	return n
}

// anchor captures the reading position.
func (m Model) anchor() scrollAnchor {
	if m.viewport.AtBottom() {
		return scrollAnchor{atBottom: true}
	}
	offset := m.viewport.YOffset()
	acc := 0
	for e := range m.entries {
		rows := m.entries[e].rows
		for r := range rows {
			h := m.wrappedHeight(rows[r])
			if offset < acc+h {
				sub := offset - acc
				// A blank row gives a re-wrap nothing to match on, so
				// anchor to the nearest row that carries text.
				rr := r
				for rr < len(rows) && strings.TrimSpace(stripANSI(rows[rr])) == "" {
					rr++
				}
				if rr >= len(rows) {
					rr = r
					for rr > 0 && strings.TrimSpace(stripANSI(rows[rr])) == "" {
						rr--
					}
				}
				if rr != r {
					sub = 0
				}
				return scrollAnchor{
					entry: e,
					row:   rr,
					count: len(rows),
					sub:   sub,
					text:  strings.TrimSpace(stripANSI(rows[rr])),
				}
			}
			acc += h
		}
	}
	// The top row is in the streaming block, which has no stable
	// position; follow the bottom instead of guessing.
	return scrollAnchor{atBottom: true}
}

// findRow locates the row whose text matches the anchor, tolerating the
// way a re-wrap splits or joins a line.
func findRow(rows []string, want string) (int, bool) {
	for i, row := range rows {
		if strings.TrimSpace(stripANSI(row)) == want {
			return i, true
		}
	}
	for i, row := range rows {
		got := strings.TrimSpace(stripANSI(row))
		if got == "" {
			continue
		}
		if strings.HasPrefix(got, want) || strings.HasPrefix(want, got) {
			return i, true
		}
	}
	return 0, false
}

// resizeFrame recomputes the frame and keeps the reader's position when
// the geometry actually changed.
func (m *Model) resizeFrame() {
	anchor := m.anchor()
	if m.sizePanes() {
		m.renderConversation()
		m.restoreAnchor(anchor)
	}
}

// restoreAnchor returns to the captured reading position, clamped to the
// content that still exists.
func (m *Model) restoreAnchor(a scrollAnchor) {
	if a.atBottom || a.entry < 0 || a.entry >= len(m.entries) {
		m.viewport.GotoBottom()
		return
	}
	rows := m.entries[a.entry].rows
	if len(rows) == 0 {
		m.viewport.GotoBottom()
		return
	}
	row := a.row
	// Markdown re-wraps differently at a new width, so a row number is
	// only stable while the entry's row count is. Find the same text
	// first, then fall back to the proportional position.
	if a.text != "" {
		if i, ok := findRow(rows, a.text); ok {
			row = i
		} else if a.count > 0 && len(rows) != a.count {
			row = a.row * len(rows) / a.count
		}
	}
	if row >= len(rows) {
		row = len(rows) - 1
	}
	if row < 0 {
		m.viewport.GotoBottom()
		return
	}
	offset := 0
	for e := 0; e < a.entry; e++ {
		for _, text := range m.entries[e].rows {
			offset += m.wrappedHeight(text)
		}
	}
	for r := 0; r < row; r++ {
		offset += m.wrappedHeight(rows[r])
	}
	offset += a.sub
	m.viewport.SetYOffset(offset)
}

// contentHeight returns the number of rows available between the header
// rule and the bottom rule. Short windows drop the rules and the notice
// line instead of letting the frame overflow.
// contentHeight is the number of rows an overlay panel gets between the
// header rule and the footer rule.
func contentHeight(height int) int {
	n := overlayPanelHeight(height)
	if n < 1 {
		n = 1
	}
	return n
}

// overlayPanelHeight is the number of rows an overlay gets after the
// header, the rules and the notice line have been accounted for. It
// matches overlayLayout exactly, including short windows.
func overlayPanelHeight(height int) int {
	n := height - 2
	if height >= 8 {
		n -= 2
	}
	if height >= 6 {
		n--
	}
	if n < 1 {
		n = 1
	}
	return n
}

// frameSizes splits the content area between the conversation viewport and
// the composer. The composer keeps at least one row and never starves the
// viewport.
// chatFrame is the resolved vertical geometry of the conversation view,
// computed once per size change so rendering and height accounting can
// never disagree.
type chatFrame struct {
	viewport int
	composer int
	suggest  int
	rules    bool
	divider  bool
	hint     bool
	notice   bool
}

// chrome counts every row that is neither the viewport nor the composer
// text: header, rule, footer, notice, hint, divider, suggestions,
// composer rules.
//
// The hint row and the divider are reserved: they exist whether or not
// they have anything to say, so the composer and the footer never move
// when their contents change.
func (f chatFrame) chrome() int {
	n := 2 // header + footer
	if f.notice {
		n++
	}
	if f.hint {
		n++
	}
	if f.divider {
		n++
	}
	n += f.suggest
	if f.rules {
		n += 2
	}
	return n
}

func (f chatFrame) total() int { return f.chrome() + f.composer + f.viewport }

// computeChatFrame fits the frame to the terminal. When space runs out it
// gives up the suggestion panel, the notice, the hint row, the divider
// and finally the composer rules — never the header, the composer text or
// the footer.
func computeChatFrame(height, desiredComposer, suggestions int) chatFrame {
	f := chatFrame{
		composer: desiredComposer,
		suggest:  suggestions,
		rules:    true,
		divider:  true,
		hint:     true,
		notice:   true,
	}
	if f.composer < 1 {
		f.composer = 1
	}
	if f.composer > maxComposerHeight {
		f.composer = maxComposerHeight
	}
	for height > 0 && f.chrome()+f.composer+1 > height {
		switch {
		case f.composer > 1:
			f.composer--
		case f.suggest > 0:
			f.suggest = 0
		case f.notice:
			f.notice = false
		case f.hint:
			f.hint = false
		case f.divider:
			f.divider = false
		case f.rules:
			f.rules = false
		default:
			f.viewport = 1
			return f
		}
	}
	f.viewport = height - f.chrome() - f.composer
	if f.viewport < 1 {
		f.viewport = 1
	}
	return f
}

// appendEntry adds one conversation turn and re-renders it. It follows
// the new content only when the reader was already at the bottom, and
// returns the entry index so the caller can attach metadata.
func (m *Model) appendEntry(role, text string) int {
	stick := m.viewport.AtBottom()
	m.entries = append(m.entries, entry{role: role, text: text})
	i := len(m.entries) - 1
	m.renderEntry(i)
	m.renderConversation()
	if stick {
		m.viewport.GotoBottom()
	}
	return i
}

// appendNote adds an explanatory line that is not an answer. Notes never
// receive usage metadata and are never sent anywhere.
func (m *Model) appendNote(role, text string) {
	stick := m.viewport.AtBottom()
	m.entries = append(m.entries, entry{role: role, text: text, note: true})
	m.renderEntry(len(m.entries) - 1)
	m.renderConversation()
	if stick {
		m.viewport.GotoBottom()
	}
}

// mergeToolActivity keeps one final record per tool call while allowing a
// running record to be replaced by its completion.
func mergeToolActivity(list []engine.ToolActivity, next engine.ToolActivity) []engine.ToolActivity {
	if next.CallID == "" {
		return append(list, next)
	}
	for i := range list {
		if list[i].CallID == next.CallID && list[i].Name == next.Name {
			list[i] = next
			return list
		}
	}
	return append(list, next)
}

// toolActivityLine is the compact, content-free line shown in the
// conversation and diagnostics.
func toolActivityLine(a engine.ToolActivity) string {
	line := "⚙ " + a.Name
	if a.Action != "" && a.Action != a.Name {
		line += " " + a.Action
	}
	if a.Target != "" {
		line += " " + truncateMiddle(a.Target, 64)
	}
	switch a.Status {
	case "ok":
		if a.Name == "bash" && a.ExitCode != nil {
			line += fmt.Sprintf(" · exit %d", *a.ExitCode)
		} else {
			line += " · ok"
		}
	case "proposed":
		line += " · awaiting approval"
	case "denied":
		line += " · denied"
	case "cancelled":
		line += " · cancelled"
	case "timeout":
		line += " · timed out"
	default:
		if a.Status != "" {
			line += " · " + a.Status
		}
	}
	if a.Bytes > 0 {
		line += " · " + humanSize(int64(a.Bytes))
	}
	if a.Duration > 0 {
		line += " · " + a.Duration.Round(10*time.Millisecond).String()
	}
	if a.Truncated {
		line += " · truncated"
	}
	return line
}

// appendToolNotes puts each completed tool activity above the answer. A
// proposal is already described by the answer itself, and a running record
// is not final.
func (m *Model) appendToolNotes(tools []engine.ToolActivity) {
	for _, a := range tools {
		if a.Status == "proposed" || a.Status == "running" || a.Status == "" {
			continue
		}
		m.appendNote("assistant", toolActivityLine(a))
	}
}

// attachMeta assigns the reported usage of the just-finished turn to its
// answer entry. Unreported turns leave no metadata behind.
func (m *Model) attachMeta(i int, turn usage.Turn) {
	if i < 0 || i >= len(m.entries) || m.entries[i].note {
		return
	}
	m.entries[i].meta = turn
	m.entries[i].hasMeta = turn.Reported
	m.renderEntry(i)
	m.renderConversation()
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
		rows, deco := m.renderUser(e)
		e.rows = rows
		e.deco = deco
		// The user's own line breaks are the logical lines: a wrapped
		// row is not a new line of what was written.
		if len(e.logical) == 0 {
			e.logical = strings.Split(e.text, "\n")
		}
	default:
		label := "HAL > "
		indent := strings.Repeat(" ", len(label))
		body := m.renderMarkdown(e.text, m.assistantWidth())
		lines := strings.Split(body, "\n")
		rows := make([]string, 0, len(lines)+1)
		for j, line := range lines {
			if j == 0 {
				rows = append(rows, label+line)
				continue
			}
			rows = append(rows, indent+line)
		}
		e.deco = nil
		if e.hasMeta || e.attribution != nil {
			rows = append(rows, m.metaLine(e.meta, e.attribution))
			// The metadata line is interface, not answer: it stays on
			// screen but is never part of a selection.
			e.deco = decorationRows(len(rows))
		}
		e.rows = rows
		if len(e.logical) == 0 {
			// The unwrapped render does not depend on the width, so it
			// is computed once and kept for the life of the entry.
			e.logical = m.renderMarkdownLogical(e.text)
		}
	}
}

// renderUser wraps a user message and decides how much of it to show.
// The full text stays in e.text; only the rendering is shortened.
//
// The last row of a shortened message is the hint that says how to
// expand it. The hint is interface, not text: it is marked as
// decoration, so a selection never copies it.
func (m Model) renderUser(e *entry) ([]string, []bool) {
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
	preview := m.collapsedPreviewRows()
	if len(rows) <= preview {
		return rows, nil
	}
	if e.collapse == collapseShown {
		rows = append(rows, m.collapseHint("Ctrl+O to collapse"))
		return rows, decorationRows(len(rows))
	}
	visible := rows[:preview]
	hidden := len(rows) - preview
	hint := fmt.Sprintf("… %d more line(s) · Ctrl+O to expand", hidden)
	if hidden == 1 {
		hint = "… 1 more line · Ctrl+O to expand"
	}
	visible = append(visible, m.collapseHint(hint))
	return visible, decorationRows(len(visible))
}

// decorationRows marks the last row of a set as interface decoration.
func decorationRows(n int) []bool {
	if n < 1 {
		return nil
	}
	deco := make([]bool, n)
	deco[n-1] = true
	return deco
}

// isDecoration reports whether a row of the entry is interface rather
// than message text.
func (e entry) isDecoration(row int) bool {
	return row < len(e.deco) && e.deco[row]
}

// metaLine renders one assistant turn's completion metadata: wall-clock
// duration, provider-reported counts and the skill credit.
//
// It is display only — the entry text, and therefore /copy, the mouse
// selection and the model context, never contains it.
func (m Model) metaLine(t usage.Turn, a *session.Attribution) string {
	var parts []string
	if t.Reported {
		parts = append(parts, formatDuration(t.Duration)+" · "+
			compactTokens(t.Input)+" in / "+compactTokens(t.Output)+" out")
	}
	if label := attributionLabel(a); label != "" {
		parts = append(parts, label)
	}
	line := strings.Join(parts, " · ")
	if !m.color {
		return line
	}
	return m.theme.styles().footer.Render(line)
}

// attachAttribution credits the answer with the skills that shaped it.
//
// The credit arrived with the answer, so it belongs to this reply and to
// no other: a turn that was cancelled, a turn that failed and an answer
// from an earlier exchange all leave the reply uncredited.
func (m *Model) attachAttribution(i int) {
	if m.turnAttribution == nil {
		return
	}
	if i < 0 || i >= len(m.entries) {
		return
	}
	m.entries[i].attribution = m.turnAttribution
	m.renderEntry(i)
	m.renderConversation()
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

// collapsible reports whether the entry is longer than the preview the
// current viewport allows. Short prompts stay fully visible.
func (m Model) collapsible(i int) bool {
	e := m.entries[i]
	if e.role != "user" {
		return false
	}
	full, _ := m.renderUser(&entry{role: e.role, text: e.text, collapse: collapseShown})
	return len(full) > m.collapsedPreviewRows()
}

// collapsedPreviewRows bounds a collapsed prompt to roughly one third of
// the conversation viewport, including its expansion hint. It shrinks on
// small terminals and is recomputed on resize.
func (m Model) collapsedPreviewRows() int {
	h := m.frame.viewport
	if h <= 0 {
		h = m.viewHeight
	}
	if h <= 0 {
		return userPreviewRows
	}
	budget := h / 3
	if budget < 2 {
		budget = 2
	}
	rows := budget - 1 // reserve one row for the hint
	if rows > userPreviewRows {
		rows = userPreviewRows
	}
	if rows < 1 {
		rows = 1
	}
	return rows
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
//
// The operator's choice is remembered as a manual collapse: the
// automatic expansion that follows an answer never overrides it.
func (m *Model) toggleEntry(i int) {
	anchor := m.anchor()
	if m.entries[i].collapse == collapseShown {
		m.entries[i].collapse = collapseManual
	} else {
		m.entries[i].collapse = collapseShown
	}
	m.renderEntry(i)
	m.renderConversation()
	m.restoreAnchor(anchor)
}

// setAllUserExpanded applies to every long user message. An expanded
// message is shown in full, a collapsed one is collapsed by hand: the
// automatic expansion that follows an answer never overrides either.
func (m *Model) setAllUserExpanded(expanded bool) {
	stick := m.viewport.AtBottom()
	for i := range m.entries {
		if m.entries[i].role != "user" || !m.collapsible(i) {
			continue
		}
		if expanded {
			m.entries[i].collapse = collapseShown
		} else {
			m.entries[i].collapse = collapseManual
		}
		m.renderEntry(i)
	}
	m.renderConversation()
	if stick {
		m.viewport.GotoBottom()
	}
}

// collapseForTurn collapses a long user message for the running turn.
//
// The collapse is temporary: it only makes room for the answer. The
// message expands again when the turn ends, unless the operator has
// said otherwise in the meantime.
//
// The rows of the message change, so a selection in progress is
// dropped: its ends point at rows that would move.
func (m *Model) collapseForTurn(i int) {
	if i < 0 || i >= len(m.entries) {
		return
	}
	if m.entries[i].role != "user" || m.entries[i].collapse != collapseShown {
		return
	}
	if !m.collapsible(i) {
		return
	}
	m.clearSelection()
	m.entries[i].collapse = collapseAuto
	m.renderEntry(i)
	m.renderConversation()
}

// expandAfterTurn opens the messages that were collapsed for the
// running turn.
//
// The collapse was a way to make room for the answer; now the answer is
// there and the prompt is readable again. A message the operator
// collapsed by hand is left alone.
//
// The reading position is captured before the rows change, so the
// finished answer stays where the operator was reading it instead of
// the view jumping up into the expanded prompt. A selection in progress
// is dropped, because the rows it points at move.
func (m *Model) expandAfterTurn() {
	auto := false
	for i := range m.entries {
		if m.entries[i].collapse == collapseAuto {
			auto = true
			break
		}
	}
	if !auto {
		return
	}
	anchor := m.anchor()
	m.clearSelection()
	for i := range m.entries {
		if m.entries[i].collapse != collapseAuto {
			continue
		}
		m.entries[i].collapse = collapseShown
		m.renderEntry(i)
	}
	m.renderConversation()
	m.restoreAnchor(anchor)
}

// rewrap re-renders every entry for the current width so resizing
// reflows the conversation instead of leaving stale wrapping.
func (m *Model) rewrap() {
	for i := range m.entries {
		m.renderEntry(i)
	}
}

// pendingRows splits the answer being streamed into the rows the
// conversation shows while it is still arriving.
//
// Every row but the first carries the same indent as a finished reply,
// so the rows the terminal shows and the rows the cell map knows about
// are the same rows.
func pendingRows(pending string) []string {
	lines := strings.Split(pending, "\n")
	rows := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == 0 {
			rows = append(rows, "HAL > "+line)
			continue
		}
		rows = append(rows, strings.Repeat(" ", len("HAL > "))+line)
	}
	return rows
}

// pendingLogical is the unwrapped text of the answer being streamed.
func pendingLogical(pending string) []string {
	return strings.Split(pending, "\n")
}

func (m *Model) renderConversation() {
	atBottom := m.viewport.AtBottom()
	offset := m.viewport.YOffset()
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
	m.conv = convMap{}
	if m.mouse {
		// The cell map is only needed for mouse selection, and mapping
		// every row costs time on every frame of a streaming answer.
		m.conv = buildConvMap(m.entries)
	}
	if m.pending != "" {
		// The rows of the answer being streamed are shown one by one,
		// exactly as the cell map knows them: one row per line, so
		// the terminal and the map never disagree about which row is
		// which.
		pending := pendingRows(m.pending)
		for _, row := range pending {
			content = append(content, styleRow(row))
		}
		if m.mouse {
			m.conv.addEntry(entry{role: "assistant", rows: pending, logical: pendingLogical(m.pending)})
		}
	}
	m.viewport.SetContent(strings.Join(content, "\n"))
	// SetContent clamps to the bottom when the content shrank; restore the
	// reader's position unless they were already following it.
	if atBottom {
		m.viewport.GotoBottom()
	} else {
		m.viewport.SetYOffset(offset)
	}
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
	if m.mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	if m.color {
		// Paint the application background and default text colour for
		// this theme. The renderer restores the terminal's own colours
		// on exit, so the palette is never permanently modified.
		v.BackgroundColor = lipgloss.Color(m.theme.Background)
		v.ForegroundColor = lipgloss.Color(m.theme.Text)
	}
	return v
}

// layoutRows builds the whole screen as exactly the terminal height, so
// the frame never scrolls the terminal.
//
// Chat: header, conversation, the composer, the hint row, the divider,
// the footer and the notice.
// Overlay: header, rule, panel, rule, footer, notice.
// Approval: the review's own three zones, so the decisions stay on
// screen while the document scrolls.
func (m Model) layoutRows() []string {
	if !m.ready {
		return []string{"Starting HAL9001…"}
	}
	var rows []string
	switch m.view {
	case viewChat:
		rows = m.chatLayout()
	case viewApprove:
		rows, _ = m.approvalLayout()
	default:
		rows = m.overlayLayout()
	}
	return m.fitToHeight(rows)
}

func (m Model) chatLayout() []string {
	f := m.frame
	rows := make([]string, 0, m.height)
	rows = append(rows, m.headerRow())
	rows = append(rows, m.jumpOverlay(m.highlightSelection(m.viewportRows(f.viewport)))...)
	if f.suggest > 0 {
		rows = append(rows, m.suggestionRows(f.suggest)...)
	}
	rows = append(rows, m.composerBlock(f.composer, f.rules)...)
	// One reserved row of keyboard hints sits immediately below the
	// composer, and a thin divider separates the frame from the footer.
	if f.hint {
		rows = append(rows, m.hintRow())
	}
	if f.divider {
		rows = append(rows, m.rule())
	}
	rows = append(rows, m.statusBar())
	if f.notice {
		rows = append(rows, m.noticeRow())
	}
	return rows
}

// viewportRows renders the conversation bottom-aligned: a short
// conversation sits just above the composer, with the spare rows left
// blank above it. The padding is visual only — the viewport's own offsets
// are untouched, so Home/End and the scroll anchor are unaffected.
func (m Model) viewportRows(n int) []string {
	rows := splitRows(m.viewport.View(), n)
	if !m.viewport.AtTop() || !m.viewport.AtBottom() {
		return rows
	}
	pad := n - m.contentRows()
	if pad <= 0 || pad >= n {
		if pad >= n {
			return make([]string, n)
		}
		return rows
	}
	out := make([]string, 0, n)
	for i := 0; i < pad; i++ {
		out = append(out, "")
	}
	return append(out, rows[:n-pad]...)
}

// contentRows is how many screen rows the conversation occupies at the
// current width, counting soft-wrapped lines.
func (m Model) contentRows() int {
	content := m.viewport.GetContent()
	if strings.TrimSpace(stripANSI(content)) == "" {
		return 0
	}
	n := 0
	for _, line := range strings.Split(content, "\n") {
		n += m.wrappedHeight(line)
	}
	return n
}

func (m Model) overlayLayout() []string {
	h := m.height
	rule := h >= 8
	notice := h >= 6
	rows := []string{m.headerRow()}
	if rule {
		rows = append(rows, m.rule())
	}
	n := overlayPanelHeight(h)
	// Help keeps its title and exit instruction fixed above the
	// scrollable reference body.
	if m.view == viewHelp && n > 1 {
		rows = append(rows, m.helpTitleRow())
		n--
	}
	rows = append(rows, m.panelRows(m.activePanel(), n)...)
	if rule {
		rows = append(rows, m.rule())
	}
	rows = append(rows, m.statusBar())
	if notice {
		rows = append(rows, m.noticeRow())
	}
	return rows
}

// activePanel returns the rows of the overlay that is currently open.
//
// The approval view is not one of them: it has its own layout, because
// its header and its decisions must stay on screen while the document
// scrolls.
func (m Model) activePanel() []string {
	switch m.view {
	case viewOnboard:
		return m.onboardRows()
	case viewPicker:
		return m.pickerRows()
	case viewTheme:
		return m.themeRows()
	case viewHelp:
		return m.helpRows()
	case viewSessions:
		return m.sessionsRows()
	case viewDiag:
		return m.diagRows()
	case viewEffort:
		return m.effortRows()
	case viewUsage:
		return m.usageRows()
	case viewRename:
		return m.renameRows()
	case viewSkills:
		return m.skillsRows()
	case viewPermission:
		return m.permissionRows()
	}
	return nil
}

// fitToHeight guarantees the frame is exactly the terminal height. Normal
// sizes are exact by construction; this only trims a degenerate window.
func (m Model) fitToHeight(rows []string) []string {
	if m.height <= 0 {
		return rows
	}
	for len(rows) < m.height {
		rows = append(rows, "")
	}
	if len(rows) > m.height {
		keep := m.height - 1
		if keep < 1 {
			keep = 1
		}
		out := append([]string{}, rows[:keep]...)
		out = append(out, rows[len(rows)-1])
		return out
	}
	return rows
}

// composerBlock renders the composer as two full-width rules around the
// draft.
//
// There are no side borders and no corners: the composer is part of the
// frame, not a box floating on it. The rules are drawn in the theme's
// muted rule colour, exactly the width of the terminal, and the text sits
// one cell inside them.
func (m Model) composerBlock(ch int, rules bool) []string {
	inner := m.composerWindow(ch)
	if !rules {
		return inner
	}
	width := m.width
	if width < 1 {
		width = 1
	}
	text := width - composerPadding
	if text < 1 {
		text = 1
	}
	rule := strings.Repeat("─", width)
	if m.color {
		rule = m.theme.styles().rule.Render(rule)
	}
	rows := make([]string, 0, ch+2)
	rows = append(rows, rule)
	for _, line := range inner {
		rows = append(rows, " "+m.fitWidth(line, text)+" ")
	}
	rows = append(rows, rule)
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
			rows = append(rows, m.fitWidth(lines[idx], m.width))
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

// fitWidth trims a rendered line to width so a long line can never push
// the frame out of shape.
func (m Model) fitWidth(line string, width int) string {
	if width < 1 {
		return ""
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(line)
}

// headerRow renders the fixed header: the eye and the name, plus the
// conversation title when one is known.
//
// The title is shortened to whatever room the terminal has. The header
// is one row: a title that wrapped would push the conversation down, and
// the immutable session identity stays available in the diagnostics.
func (m Model) headerRow() string {
	st := m.theme.styles()
	eye := "●"
	identity := eye + " HAL9001"
	title := strings.TrimSpace(m.title)
	if title == "" {
		if !m.color {
			return identity
		}
		return st.header.Render(identity)
	}
	room := m.width - lipgloss.Width(identity) - len(" · ")
	if room < 4 {
		room = 4
	}
	title = truncateCells(title, room)
	if !m.color {
		return identity + " · " + title
	}
	return st.header.Render(identity) + " " + st.footer.Render("· "+title)
}

// truncateCells shortens a string to at most n display cells, marking
// the cut with an ellipsis.
//
// The count is in cells, not bytes: a title of wide characters would
// otherwise be cut in the middle of one.
func truncateCells(s string, n int) string {
	if n < 1 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := uniseg.StringWidth(string(r))
		if w+rw > n-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

// truncateMiddle shortens a string to at most n display cells, keeping
// its head and its tail and marking the cut in the middle.
//
// A path is named by both ends — the directory it lives in and the name
// of the file — so cutting only the tail would hide the file itself.
func truncateMiddle(s string, n int) string {
	if n < 1 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	head := (n - 1) / 2
	tail := n - 1 - head
	return headCells(s, head) + "…" + tailCells(s, tail)
}

// headCells returns the first n display cells of a string.
func headCells(s string, n int) string {
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := uniseg.StringWidth(string(r))
		if w+rw > n {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

// tailCells returns the last n display cells of a string.
func tailCells(s string, n int) string {
	runes := []rune(s)
	w := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		rw := uniseg.StringWidth(string(runes[i]))
		if w+rw > n {
			break
		}
		w += rw
		start = i
	}
	return string(runes[start:])
}

// hintRow is the one reserved row below the composer: the keys that act
// on the draft right now.
//
// The row is always there. Only its text changes, so the composer and
// the footer never move when the hint does. The hint is about the
// composer alone: it never depends on the permission state, so a write
// request cannot change the keys the operator is being offered.
func (m Model) hintRow() string {
	text := m.hintText()
	if !m.color {
		return text
	}
	return m.theme.styles().footer.Render(text)
}

// hintText is the contextual hint for the current state of the draft.
//
// The three states are the ones the composer can be in: empty, holding
// a draft, and empty because a draft was cleared and can be restored.
func (m Model) hintText() string {
	variants := m.hintVariants()
	for _, v := range variants {
		if m.width < 1 || lipgloss.Width(v) <= m.width {
			return v
		}
	}
	// Even the shortest form is too wide: it is cut, not wrapped.
	return truncateCells(variants[len(variants)-1], m.width)
}

// hintVariants lists the hint from longest to shortest.
//
// A narrow terminal takes the next one down, so the keys that matter
// most are never the ones that get cut: the editing key survives, and
// the help reference is the first thing to go.
func (m Model) hintVariants() []string {
	clear, restore := keyName(keyClearDraft), keyName(keyRestoreDraft)
	send := "Enter send · " + keyName(keyNewline) + " newline"
	switch {
	case m.draftActive():
		return []string{
			send + " · " + clear + " clear · /help",
			"Enter send · " + clear + " clear · /help",
			clear + " clear · /help",
			clear + " clear",
			clear,
		}
	case m.undoValid:
		return []string{
			"Draft cleared · " + restore + " restore",
			restore + " restore",
			restore,
		}
	default:
		return []string{
			send + " · /help",
			"Enter send · /help",
			"Enter send",
			"/help",
		}
	}
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

// statusBar renders the footer: the permission state on the left, the
// session's token usage in the middle and the model with its reasoning
// effort on the right.
//
// The permission state is never given up, and it does not depend on the
// draft: 444 stays on screen while the operator is typing. Narrow
// terminals shed detail in a fixed order — the effort, then the token
// usage, then the model — so the row never wraps and never grows.
func (m Model) statusBar() string {
	st := m.theme.styles()
	model := strings.TrimSpace(m.model)
	effort := ""
	if m.effortSupported && m.effort != "" {
		effort = m.effort
	}
	right := model
	if effort != "" {
		right = strings.TrimSpace(model + " · " + effort)
	}
	left := m.permissionLabel()
	if m.writeState == writeWriting {
		left = left + " · " + m.writeStatePath()
	}
	muted := func(s string) string {
		if s == "" {
			return ""
		}
		return st.footer.Render(s)
	}
	// A write that needs the operator is shown prominently; the ordinary
	// read-only state stays quiet. An active 666 window is always red,
	// because it is the one state that increases HAL's authority.
	styledLeft := muted(left)
	if m.PermissionMode() == permission.Unrestricted {
		styledLeft = st.danger.Render(left)
	} else if m.writeState != writeIdle {
		styledLeft = st.active.Render(left)
	}
	for _, c := range m.footerCandidates(styledLeft, muted(m.usageLabel()), muted(right), muted(model)) {
		if bar, ok := fitStatusBar(c.left, c.mid, c.right, m.width); ok {
			return bar
		}
	}
	// Nothing fits: the permission state alone, shortened.
	bar := m.permissionLabel()
	if m.width > 0 {
		bar = truncateCells(bar, m.width)
	}
	if m.PermissionMode() == permission.Unrestricted {
		return st.danger.Render(bar)
	}
	return muted(bar)
}

// footerCandidate is one way to lay out the footer: a left hint, an
// optional middle section and a right section.
type footerCandidate struct{ left, mid, right string }

// footerCandidates lists the footer layouts from richest to plainest.
//
// The order is the order detail is given up: the reasoning effort, then
// the token usage, then the model. The permission state is the last
// thing to go, and it never goes.
func (m Model) footerCandidates(left, usage, right, model string) []footerCandidate {
	return []footerCandidate{
		{left, usage, right},
		{left, usage, model},
		{left, "", model},
		{left, "", ""},
	}
}

// writeStatePath is the destination shown beside 666 WRITING.
func (m Model) writeStatePath() string {
	if m.write.Path == "" {
		return "one file"
	}
	return m.displayWritePath(m.write)
}

// draftActive reports whether the composer holds something Ctrl+U would
// clear. It is the same predicate clearDraft uses, so the hint never
// offers a key that would do nothing.
func (m Model) draftActive() bool {
	return strings.TrimSpace(m.composer.Value()) != ""
}

// keyName renders a key binding for a hint: "ctrl+u" becomes "Ctrl+U".
func keyName(binding string) string {
	parts := strings.Split(binding, "+")
	for i, part := range parts {
		switch part {
		case "ctrl":
			parts[i] = "Ctrl"
		case "alt":
			parts[i] = "Alt"
		case "shift":
			parts[i] = "Shift"
		case "enter":
			parts[i] = "Enter"
		case "esc":
			parts[i] = "Esc"
		case "home":
			parts[i] = "Home"
		case "end":
			parts[i] = "End"
		case "tab":
			parts[i] = "Tab"
		case "pgup":
			parts[i] = "PgUp"
		case "pgdn":
			parts[i] = "PgDn"
		default:
			parts[i] = strings.ToUpper(part)
		}
	}
	return strings.Join(parts, "+")
}

// usageLabel is the compact footer indicator. With no accounting at all
// it is empty; with accounting but no token numbers yet it says unknown
// instead of inventing zeros.
func (m Model) usageLabel() string {
	if !m.tokens.Total() {
		return ""
	}
	if !m.tokens.HasTokens() {
		return "Tokens: n/a"
	}
	return "Tokens: " + compactTokens(m.tokens.Input) + " in / " + compactTokens(m.tokens.Output) + " out"
}

// fitStatusBar lays out left, an optional middle section and right within
// width, reporting whether they fit.
//
// The middle section is centred in the room between the two ends, so the
// three parts read as three parts. When there is not enough room for
// that, the middle section sits next to the left one instead: the row
// never wraps and never grows.
//
// Widths are display cells, not bytes: the separators are multi-byte and
// a byte count would leave the right section a cell short for each one.
func fitStatusBar(left, mid, right string, width int) (string, bool) {
	if width < 1 {
		return "", false
	}
	l, m, r := lipgloss.Width(left), lipgloss.Width(mid), lipgloss.Width(right)
	switch {
	case mid == "" && right == "":
		if l > width {
			return "", false
		}
		return left, true
	case mid == "":
		if l+r+1 > width {
			return "", false
		}
		return left + strings.Repeat(" ", width-l-r) + right, true
	case right == "":
		if l+m+1 > width {
			return "", false
		}
		return left + " " + mid, true
	default:
		space := width - l - r
		if space < m+1 {
			return "", false
		}
		leftGap := (space - m) / 2
		if leftGap < 1 {
			leftGap = 1
		}
		rightGap := space - m - leftGap
		if rightGap < 1 {
			return "", false
		}
		return left + strings.Repeat(" ", leftGap) + mid +
			strings.Repeat(" ", rightGap) + right, true
	}
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

// ComposerRows is how many rows of the draft the composer shows.
func (m Model) ComposerRows() int { return m.frame.composer }

// InjectResponse delivers a canned engine reply, for tests.
func (m *Model) InjectResponse(text string) (tea.Model, tea.Cmd) {
	return m.Update(responseMsg{text: text})
}
