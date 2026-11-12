package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/lifecycle"
)

// The lifecycle at the interface.
//
// The engine runs the work, so the engine owns its state. The
// interface asks about it — through an optional interface, so an engine
// that does not implement it behaves exactly as before — and the
// interface owns the one thing the engine cannot have: the cancel
// function of the running turn.
//
// The two commands are registered from this file rather than from the
// command table, so that the table itself stays where it has always
// been: one place for the dispatch, the suggestions and /help.

// LifecycleEngine is implemented by an engine that owns the lifecycle of
// the exchange it runs.
//
// The interface asks; it never assumes. An engine without it behaves
// exactly as before: nothing is stopped, and nothing is continued.
type LifecycleEngine interface {
	// Lifecycle reports the state of the exchange.
	Lifecycle() lifecycle.Operation
	// Resume continues the exchange that was interrupted, from its
	// checkpoint. It fails when there is no checkpoint to continue from.
	Resume(ctx context.Context, emit func(engine.Event)) (string, error)
}

// init registers the lifecycle commands.
//
// The command table is the single source of truth for the dispatch, the
// suggestion panel and /help. Appending to it here keeps that property
// while leaving the table itself alone.
func init() {
	commandTable = append(commandTable, lifecycleCommands()...)
}

// lifecycleCommands are the commands that act on the running exchange.
func lifecycleCommands() []*command {
	return []*command{
		{
			name:        "/stop",
			description: "Stop the running turn",
			run: func(m *Model, args []string) tea.Cmd {
				if len(args) > 0 {
					m.notice = "Usage: /stop"
					return nil
				}
				return m.stopTurn()
			},
		},
		{
			name:        "/resume",
			description: "Continue the exchange that was interrupted",
			run: func(m *Model, args []string) tea.Cmd {
				if len(args) > 0 {
					m.notice = "Usage: /resume"
					return nil
				}
				return m.resumeTurn()
			},
		},
	}
}

// lifecycleEngine returns the engine that owns the lifecycle, if it
// does.
func (m *Model) lifecycleEngine() (LifecycleEngine, bool) {
	if m.engine == nil {
		return nil, false
	}
	eng, ok := m.engine.(LifecycleEngine)
	return eng, ok
}

// lifecycleOperation reports the state of the exchange.
//
// The zero operation means nothing has run yet.
func (m *Model) lifecycleOperation() lifecycle.Operation {
	eng, ok := m.lifecycleEngine()
	if !ok {
		return lifecycle.Operation{}
	}
	return eng.Lifecycle()
}

// lifecycleStatus renders the state of the exchange for the operator.
//
// It names the state in words: running, completed, stopped or failed.
// The four are never conflated, because they call for different
// actions.
func (m *Model) lifecycleStatus() string {
	op := m.lifecycleOperation()
	if op.State == "" {
		return string(lifecycle.StateIdle)
	}
	return op.Status()
}

// stopTurn asks the running turn to stop.
//
// The cancellation is cooperative and travels through the request's
// context: the provider's request is aborted, a command that is
// running is terminated together with its process group, and an MCP
// tool call is cancelled. A server that does not implement
// cancellation may still finish the work it had already accepted.
//
// The interface is usable again at once: the composer never waits for
// the request to unwind, and the turn's outcome arrives when it does.
func (m *Model) stopTurn() tea.Cmd {
	if !m.sending {
		m.notice = "nothing is running — the last turn was " + m.lifecycleStatus()
		return nil
	}
	m.stopStream()
	m.sending = false
	m.endActivity()
	m.reasoningSeen = false
	// The turn is over for the interface: the prompt that was
	// collapsed for it opens again instead of staying abbreviated.
	m.expandAfterTurn()
	m.notice = "stopping — the request is being cancelled"
	return nil
}

// resumeTurn continues the exchange that was interrupted.
//
// It is refused unless there is a checkpoint:
//
//   - a turn that is still running must be stopped first;
//   - a turn that finished is never replayed;
//   - a turn that failed or was stopped can be continued.
//
// The continuation is a turn like any other: the operator's message is
// not asked again, and the operations that already ran inside the
// interrupted exchange are not run again.
func (m *Model) resumeTurn() tea.Cmd {
	eng, ok := m.lifecycleEngine()
	if !ok {
		m.notice = "this session cannot continue an interrupted exchange"
		return nil
	}
	op := eng.Lifecycle()
	switch op.State {
	case lifecycle.StateRunning:
		m.notice = "a turn is still running — stop it first"
		return nil
	case lifecycle.StateCompleted:
		m.notice = "the last turn completed — there is nothing to continue"
		return nil
	}
	if !op.Resumable {
		m.notice = "there is no interrupted exchange to continue"
		return nil
	}
	// The continuation is a turn like any other.
	m.streamID++
	id := m.streamID
	m.sending = true
	m.streamStarted = m.clock()
	m.reasoningSeen = false
	m.turnStats = nil
	m.beginActivity()
	m.setActivity()
	// The notice that was on screen belongs to what came before: this
	// turn has a status of its own, and the row is better off showing
	// it than a message about a turn that is over.
	m.notice = ""
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	cmd := m.resumeCmd(id, eng, ctx)
	if m.sender == nil {
		return cmd
	}
	if tick := m.animationTickCmd(); tick != nil {
		return tea.Batch(cmd, tick)
	}
	return cmd
}

// resumeCmd starts the continued exchange.
func (m Model) resumeCmd(id int, eng LifecycleEngine, ctx context.Context) tea.Cmd {
	if m.sender == nil {
		return func() tea.Msg { return m.runResumeInline(id, eng, ctx) }
	}
	return func() tea.Msg {
		go m.runResume(id, eng, ctx)
		return streamStartedMsg{id: id}
	}
}

// runResume drives the continued exchange in the background.
func (m Model) runResume(id int, eng LifecycleEngine, ctx context.Context) {
	m.runTurn(id, ctx, func(ctx context.Context, collect func(engine.Event)) (string, error) {
		return eng.Resume(ctx, collect)
	})
}

// runResumeInline runs the continued exchange headless.
func (m Model) runResumeInline(id int, eng LifecycleEngine, ctx context.Context) tea.Msg {
	return m.runTurnInline(id, ctx, func(ctx context.Context, collect func(engine.Event)) (string, error) {
		return eng.Resume(ctx, collect)
	})
}

// cancelledNotice explains how a cancelled turn ended.
//
// A turn that was stopped usually can be continued: the operator's
// message is still unanswered and the exchange left a checkpoint. The
// notice says so, because a stopped turn and a failed turn call for
// different actions.
func (m *Model) cancelledNotice() string {
	op := m.lifecycleOperation()
	if op.Resumable {
		return "cancelled — the exchange can be continued with /resume"
	}
	return "cancelled — stored nothing from the assistant"
}

// failedNotice explains how a failed turn ended.
//
// A turn that failed usually can be continued as well: the operator's
// message is still unanswered and the exchange left a checkpoint. The
// notice says so, because the operator has to know that the question
// was not lost.
func (m *Model) failedNotice(err error) string {
	notice := "error: " + err.Error()
	if m.lifecycleOperation().Resumable {
		return notice + " — the exchange can be continued with /resume"
	}
	return notice
}

// lifecycleDiag renders the state of the exchange for the diagnostics
// panel.
//
// It carries counts and states only: never conversation text, never
// file contents, never a credential.
func (m *Model) lifecycleDiag() string {
	op := m.lifecycleOperation()
	if op.State == "" {
		return ""
	}
	parts := []string{string(op.State)}
	if op.Steps > 0 {
		parts = append(parts, fmt.Sprintf("%d operation(s)", op.Steps))
	}
	if op.Resumable {
		parts = append(parts, "can be continued with /resume")
	}
	if op.Reason != "" {
		parts = append(parts, op.Reason)
	}
	return strings.Join(parts, " · ")
}
