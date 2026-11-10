package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The completion of a turn must reach the conversation whatever panel
// is open.
//
// A turn runs while the operator reads a panel. The events of that turn
// are the conversation's: the deltas, the tool activity, the statistics
// and the end of the turn. A panel that swallowed them would lose the
// answer, keep the interface sending and leave the operator with a
// conversation that never finished.
//
// The tests drive a real interface around a real engine and a provider
// that holds its first request: the operator opens a panel while the
// answer is on its way.

// panelAnswer is the marker of the answer. It is deliberately unlike
// any word the panels or the session print, so a check can tell the
// real answer from incidental text.
const panelAnswer = "PANEL-ANSWER-7f3a9c"

// delayedProvider holds its first request until it is released and
// records every request it receives.
type delayedProvider struct {
	mu       sync.Mutex
	requests [][]provider.Message
	// release opens the hold.
	release chan struct{}
	// arrived is closed when the first request arrives.
	arrived chan struct{}
	once    sync.Once
	// answer is the text of the answer.
	answer string
	// fail makes the held request fail instead of answering.
	fail bool
	// toolCalls are proposed with the first answer, when there are any.
	toolCalls []provider.ToolCall
	// proposed says that the operation was already proposed.
	proposed bool
	// texts counts the answers the provider has given.
	texts int
}

func (p *delayedProvider) Name() string { return "delayed" }

// record stores a request and returns how many there are.
func (p *delayedProvider) record(messages []provider.Message) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, append([]provider.Message(nil), messages...))
	return len(p.requests)
}

// calls reports how many requests the provider received.
func (p *delayedProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

// Chat answers the conversation. It is the fallback for an interface
// that does not stream.
func (p *delayedProvider) Chat(ctx context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	n := p.record(messages)
	if n == 1 {
		p.once.Do(func() { close(p.arrived) })
		if err := p.hold(ctx); err != nil {
			return provider.Response{}, err
		}
	}
	return p.reply()
}

// ChatStream holds the first request and streams the answer.
func (p *delayedProvider) ChatStream(ctx context.Context, messages []provider.Message,
	_ provider.Model, emit func(provider.Delta) error) (provider.Response, error) {
	n := p.record(messages)
	if n == 1 {
		p.once.Do(func() { close(p.arrived) })
		if err := p.hold(ctx); err != nil {
			return provider.Response{}, err
		}
	}
	if p.proposeTool() {
		// The operation is proposed first: the tool activity arrives
		// before the answer.
		return provider.Response{ToolCalls: p.toolCalls, FinishReason: "tool_calls"}, nil
	}
	text := p.nextText()
	if emit != nil {
		if err := emit(provider.Delta{Text: text}); err != nil {
			return provider.Response{}, err
		}
	}
	return provider.Response{Content: text, FinishReason: "stop"}, nil
}

// proposeTool reports whether the operation is proposed now.
func (p *delayedProvider) proposeTool() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.toolCalls) == 0 || p.proposed {
		return false
	}
	p.proposed = true
	return true
}

// nextText is the text of the next answer.
//
// Every answer has its own text: a check can tell the first answer
// from the second one.
func (p *delayedProvider) nextText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.texts++
	if p.texts == 1 {
		return p.answer
	}
	return p.answer + "-second"
}

// hold blocks until the provider is released.
func (p *delayedProvider) hold(ctx context.Context) error {
	select {
	case <-p.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if p.fail {
		return errors.New("provider: 503 service unavailable")
	}
	return nil
}

// reply is the plain answer.
func (p *delayedProvider) reply() (provider.Response, error) {
	return provider.Response{Content: p.nextText(), FinishReason: "stop"}, nil
}

// releaseNow opens the hold.
func (p *delayedProvider) releaseNow() {
	select {
	case <-p.release:
	default:
		close(p.release)
	}
}

// panelModel builds a real interface around a real engine.
//
// The sender is the interface's own channel: the messages the engine
// produces arrive there, exactly as they arrive in the running program.
func panelModel(t *testing.T, p *delayedProvider, privileged bool) (Model, chan tea.Msg) {
	t.Helper()
	store, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	gate := permission.New()
	gate.SetSession("chat")
	eng := engine.New(store, p, provider.Model{ID: "m"}).WithPermission(gate)
	if privileged {
		if err := gate.Enable("chat"); err != nil {
			t.Fatal(err)
		}
		eng = eng.WithExecution(agenttools.New(gate, nil))
	}
	m := New(eng)
	m.SetColor(false)
	// The effort panel needs a model that exposes the reasoning level.
	m.SetEffort("medium", true)
	msgs := make(chan tea.Msg, 256)
	m.SetSender(func(msg tea.Msg) {
		select {
		case msgs <- msg:
		default:
		}
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return updated.(Model), msgs
}

// pump feeds the interface the messages the engine produced, until the
// condition holds.
//
// The commands the messages return are not run: the animation tick
// would never stop, and nothing here needs it.
func pump(t *testing.T, m *Model, msgs chan tea.Msg, until func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !until() {
		select {
		case msg := <-msgs:
			updated, _ := m.Update(msg)
			if next, ok := updated.(Model); ok {
				*m = next
			}
		case <-time.After(10 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("the interface did not reach the expected state in time")
			}
		}
	}
}

// answers counts the assistant entries that carry the marker.
//
// The count is over the conversation itself, not over a rendered
// screen: a panel may echo the text of the conversation, and that is
// not an answer.
func answers(m *Model, marker string) int {
	n := 0
	for _, entry := range m.entries {
		if entry.role == "assistant" && entry.text == marker {
			n++
		}
	}
	return n
}

// thePanels are the panels that swallow the events of a running turn.
var thePanels = []struct {
	command string
	view    View
}{
	{"/help", viewHelp},
	{"/diag", viewDiag},
	{"/model", viewManage},
	{"/theme", viewTheme},
	{"/sessions", viewSessions},
	{"/usage", viewUsage},
	{"/effort", viewEffort},
}

// A turn that finishes while a panel is open keeps its answer, clears
// the sending state, leaves the panel open and lets the operator carry
// on afterwards.
func TestTheCompletionReachesTheConversationBehindEveryPanel(t *testing.T) {
	for _, panel := range thePanels {
		t.Run(panel.command, func(t *testing.T) {
			p := &delayedProvider{
				release: make(chan struct{}),
				arrived: make(chan struct{}),
				answer:  panelAnswer,
			}
			m, msgs := panelModel(t, p, false)

			// The operator asks a question.
			m = typeText(m, "what is the state of the build?")
			m = sendCurrent(m)
			// The provider has the request and is still thinking.
			waitFor(t, func() bool { return p.calls() == 1 })

			// The operator opens a panel while the answer is on its way.
			m = runSlash(m, panel.command)
			if m.view != panel.view {
				t.Fatalf("view = %v, want %v", m.view, panel.view)
			}

			// The provider answers.
			p.releaseNow()
			pump(t, &m, msgs, func() bool { return !m.Sending() })

			// The answer is in the conversation, exactly once.
			if n := answers(&m, panelAnswer); n != 1 {
				t.Fatalf("the answer is in the conversation %d time(s), want once", n)
			}
			// The interface is not sending any more.
			if m.Sending() {
				t.Error("the interface is still sending")
			}
			// The panel is still open.
			if m.view != panel.view {
				t.Errorf("view = %v, want the panel to stay open", m.view)
			}

			// Closing the panel shows the finished conversation.
			m = pressKey(m, tea.KeyEscape, 0, "esc")
			if m.view != viewChat {
				t.Fatalf("view = %v, want the conversation", m.view)
			}
			joined := strings.Join(m.Lines(), "\n")
			if strings.Count(joined, panelAnswer) != 1 {
				t.Errorf("the answer is on screen %d time(s), want once:\n%s",
					strings.Count(joined, panelAnswer), joined)
			}

			// The operator can ask again, and the provider is asked.
			m = typeText(m, "and the tests?")
			m = sendCurrent(m)
			waitFor(t, func() bool { return p.calls() == 2 })
			pump(t, &m, msgs, func() bool { return !m.Sending() })
			if n := answers(&m, panelAnswer); n != 1 {
				t.Errorf("the answer is in the conversation %d time(s), want once", n)
			}
		})
	}
}

// A provider that fails while a panel is open is reported to the
// operator, and the panel stays usable.
func TestAFailureWhileAPanelIsOpenIsReported(t *testing.T) {
	p := &delayedProvider{
		release: make(chan struct{}),
		arrived: make(chan struct{}),
		answer:  panelAnswer,
		fail:    true,
	}
	m, msgs := panelModel(t, p, false)
	m = typeText(m, "what is the state of the build?")
	m = sendCurrent(m)
	waitFor(t, func() bool { return p.calls() == 1 })
	m = runSlash(m, "/diag")
	if m.view != viewDiag {
		t.Fatalf("view = %v, want the panel", m.view)
	}
	p.releaseNow()
	pump(t, &m, msgs, func() bool { return !m.Sending() })
	if m.Sending() {
		t.Error("the interface is still sending")
	}
	if !strings.Contains(m.Notice(), "503") {
		t.Errorf("notice = %q, want the failure to be reported", m.Notice())
	}
	if m.view != viewDiag {
		t.Errorf("view = %v, want the panel to stay open", m.view)
	}
	if n := answers(&m, panelAnswer); n != 0 {
		t.Errorf("an answer was stored for a failed turn: %d", n)
	}
}

// A turn that is stopped while a panel is open ends the same way: the
// cancellation reaches the conversation and the panel stays open.
func TestAStopWhileAPanelIsOpenEndsTheTurn(t *testing.T) {
	p := &delayedProvider{
		release: make(chan struct{}),
		arrived: make(chan struct{}),
		answer:  panelAnswer,
	}
	m, msgs := panelModel(t, p, false)
	m = typeText(m, "what is the state of the build?")
	m = sendCurrent(m)
	waitFor(t, func() bool { return p.calls() == 1 })
	m = runSlash(m, "/help")
	if m.view != viewHelp {
		t.Fatalf("view = %v, want the panel", m.view)
	}
	// The operator stops the turn. A panel owns the keyboard, so the
	// command is the interface's own dispatcher: the stop happens
	// while the panel is open.
	m.dispatchSlash("/stop")
	if m.Sending() {
		t.Error("the interface is still sending after the stop")
	}
	// The cancellation reaches the conversation while the panel is open.
	pump(t, &m, msgs, func() bool { return strings.HasPrefix(m.Notice(), "cancelled") })
	if !strings.HasPrefix(m.Notice(), "cancelled") {
		t.Errorf("notice = %q, want the cancellation to be reported", m.Notice())
	}
	if m.view != viewHelp {
		t.Errorf("view = %v, want the panel to stay open", m.view)
	}
	if n := answers(&m, panelAnswer); n != 0 {
		t.Errorf("an answer was stored for a stopped turn: %d", n)
	}
}

// The exchange that was stopped can be continued after the panel is
// closed.
func TestTheExchangeIsContinuedAfterThePanelCloses(t *testing.T) {
	p := &delayedProvider{
		release: make(chan struct{}),
		arrived: make(chan struct{}),
		answer:  panelAnswer,
	}
	m, msgs := panelModel(t, p, false)
	m = typeText(m, "what is the state of the build?")
	m = sendCurrent(m)
	waitFor(t, func() bool { return p.calls() == 1 })
	m = runSlash(m, "/diag")
	// The operator stops the turn while the panel is open.
	m.dispatchSlash("/stop")
	pump(t, &m, msgs, func() bool { return strings.HasPrefix(m.Notice(), "cancelled") })
	// The panel is closed and the exchange continued.
	m = pressKey(m, tea.KeyEscape, 0, "esc")
	if m.view != viewChat {
		t.Fatalf("view = %v, want the conversation", m.view)
	}
	m = runSlash(m, "/resume")
	pump(t, &m, msgs, func() bool { return !m.Sending() && p.calls() == 2 })
	if p.calls() != 2 {
		t.Fatalf("the provider saw %d requests, want the continuation", p.calls())
	}
	if n := answers(&m, panelAnswer); n != 1 {
		t.Errorf("the answer is in the conversation %d time(s), want once", n)
	}
}

// The tool activity of a running turn reaches the conversation while a
// panel is open, and the answer that follows it is kept.
func TestTheToolActivityReachesTheConversationBehindAPanel(t *testing.T) {
	p := &delayedProvider{
		release: make(chan struct{}),
		arrived: make(chan struct{}),
		answer:  panelAnswer,
		toolCalls: []provider.ToolCall{{
			ID:        "call_bash",
			Name:      "bash",
			Arguments: map[string]any{"command": "echo panel-tool"},
		}},
	}
	m, msgs := panelModel(t, p, true)
	m = typeText(m, "run the tool")
	m = sendCurrent(m)
	waitFor(t, func() bool { return p.calls() == 1 })
	m = runSlash(m, "/diag")
	p.releaseNow()
	pump(t, &m, msgs, func() bool { return !m.Sending() && p.calls() == 2 })
	if len(m.lastTools) == 0 {
		t.Error("the tool activity never reached the conversation")
	}
	if n := answers(&m, panelAnswer); n != 1 {
		t.Errorf("the answer is in the conversation %d time(s), want once", n)
	}
	if m.view != viewDiag {
		t.Errorf("view = %v, want the panel to stay open", m.view)
	}
}

// A response of an operation that is not the current one changes
// nothing.
func TestALateResponseOfAnOldTurnChangesNothing(t *testing.T) {
	p := &delayedProvider{
		release: make(chan struct{}),
		arrived: make(chan struct{}),
		answer:  panelAnswer,
	}
	m, msgs := panelModel(t, p, false)
	m = typeText(m, "what is the state of the build?")
	m = sendCurrent(m)
	waitFor(t, func() bool { return p.calls() == 1 })
	m = runSlash(m, "/help")
	// A late answer from a turn that is not the current one.
	updated, _ := m.Update(responseMsg{id: m.streamID + 100, text: panelAnswer})
	m = updated.(Model)
	if n := answers(&m, panelAnswer); n != 0 {
		t.Errorf("a late answer was stored: %d", n)
	}
	if !m.Sending() {
		t.Error("a late answer cleared the sending state")
	}
	// The real answer still arrives exactly once.
	p.releaseNow()
	pump(t, &m, msgs, func() bool { return !m.Sending() })
	if n := answers(&m, panelAnswer); n != 1 {
		t.Errorf("the answer is in the conversation %d time(s), want once", n)
	}
}

// waitFor waits until the condition holds.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the condition did not hold in time")
}
