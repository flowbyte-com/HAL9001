package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/lifecycle"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The lifecycle at the interface.
//
// The interface asks the engine about the exchange and never assumes:
// an engine without the lifecycle behaves exactly as before.

// lifecycleEngineDouble is an engine that owns a lifecycle.
type lifecycleEngineDouble struct {
	state     lifecycle.Operation
	resumed   int
	resumeErr error
}

func (e *lifecycleEngineDouble) RespondStream(_ context.Context, _ string, _ func(string)) (string, error) {
	return "answer", nil
}

func (e *lifecycleEngineDouble) Lifecycle() lifecycle.Operation {
	return e.state
}

func (e *lifecycleEngineDouble) Resume(_ context.Context, _ func(engine.Event)) (string, error) {
	e.resumed++
	if e.resumeErr != nil {
		return "", e.resumeErr
	}
	e.state = lifecycle.Operation{ID: "turn-2", Kind: "turn", State: lifecycle.StateCompleted}
	return "the continued answer", nil
}

// lifecycleModel builds a model whose engine owns a lifecycle.
func lifecycleModel(state lifecycle.Operation) (Model, *lifecycleEngineDouble) {
	double := &lifecycleEngineDouble{state: state}
	m := New(double)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(Model), double
}

// /stop with nothing running says so, and names the last state.
func TestStopWithNothingRunningSaysSo(t *testing.T) {
	m, _ := lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateCompleted,
	})
	m = runSlash(m, "/stop")
	if m.Sending() {
		t.Error("nothing was running")
	}
	if !strings.Contains(m.Notice(), "nothing is running") {
		t.Errorf("notice = %q, want it to say nothing is running", m.Notice())
	}
	if !strings.Contains(m.Notice(), "completed") {
		t.Errorf("notice = %q, want the last state named", m.Notice())
	}
}

// /stop cancels the running turn and leaves the interface usable.
func TestStopCancelsTheRunningTurn(t *testing.T) {
	m, _ := lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateRunning,
	})
	m = typeText(m, "hold the line")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl, Text: ""})
	m = updated.(Model)
	if !m.Sending() {
		t.Fatal("the turn is not running")
	}
	cancelled := false
	m.cancel = func() { cancelled = true }
	m = runSlash(m, "/stop")
	if !cancelled {
		t.Error("/stop must cancel the running request")
	}
	if m.Sending() {
		t.Error("the interface must be usable again at once")
	}
	if !strings.Contains(m.Notice(), "stopping") {
		t.Errorf("notice = %q, want it to say the request is being cancelled", m.Notice())
	}
}

// The cancellation notice says whether the exchange can be continued.
func TestTheCancellationNoticeNamesTheContinuation(t *testing.T) {
	m, _ := lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateStopped, Resumable: true,
	})
	if !strings.Contains(m.cancelledNotice(), "/resume") {
		t.Errorf("notice = %q, want it to name the continuation", m.cancelledNotice())
	}
	m, _ = lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateStopped,
	})
	if strings.Contains(m.cancelledNotice(), "/resume") {
		t.Errorf("notice = %q, want no continuation to promise", m.cancelledNotice())
	}
}

// /resume is refused while a turn is running.
func TestResumeIsRefusedWhileATurnRuns(t *testing.T) {
	m, double := lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateRunning,
	})
	m = runSlash(m, "/resume")
	if double.resumed != 0 {
		t.Error("a running turn must not be continued")
	}
	if !strings.Contains(m.Notice(), "still running") {
		t.Errorf("notice = %q, want it to say the turn is still running", m.Notice())
	}
}

// /resume never replays a turn that finished.
func TestResumeNeverReplaysAFinishedTurn(t *testing.T) {
	m, double := lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateCompleted,
	})
	m = runSlash(m, "/resume")
	if double.resumed != 0 {
		t.Error("a finished turn must never be replayed")
	}
	if !strings.Contains(m.Notice(), "completed") {
		t.Errorf("notice = %q, want it to say the turn completed", m.Notice())
	}
}

// /resume is refused without a checkpoint.
func TestResumeIsRefusedWithoutACheckpoint(t *testing.T) {
	m, double := lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateStopped,
	})
	m = runSlash(m, "/resume")
	if double.resumed != 0 {
		t.Error("there was nothing to continue")
	}
	if !strings.Contains(m.Notice(), "no interrupted exchange") {
		t.Errorf("notice = %q, want it to say there is nothing to continue", m.Notice())
	}
}

// /resume continues the interrupted exchange.
func TestResumeContinuesTheInterruptedExchange(t *testing.T) {
	m, double := lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateStopped, Resumable: true,
	})
	m = runSlash(m, "/resume")
	if double.resumed != 1 {
		t.Fatalf("the exchange was continued %d time(s), want once", double.resumed)
	}
	joined := strings.Join(m.Lines(), "\n")
	if !strings.Contains(joined, "the continued answer") {
		t.Errorf("the answer is not on screen: %v", m.Lines())
	}
}

// An engine without the lifecycle is left alone: nothing is stopped and
// nothing is continued.
func TestAnEngineWithoutTheLifecycleIsLeftAlone(t *testing.T) {
	m := testModel()
	m = runSlash(m, "/stop")
	if !strings.Contains(m.Notice(), "nothing is running") {
		t.Errorf("notice = %q", m.Notice())
	}
	m = runSlash(m, "/resume")
	if !strings.Contains(m.Notice(), "cannot continue") {
		t.Errorf("notice = %q, want it to say the session cannot continue", m.Notice())
	}
}

// The diagnostics panel names the state of the exchange.
func TestTheDiagnosticsNameTheState(t *testing.T) {
	m, _ := lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateStopped,
		Steps: 2, Resumable: true,
	})
	rows := strings.Join(m.diagRows(), "\n")
	if !strings.Contains(rows, "lifecycle") {
		t.Fatalf("the panel does not carry the state:\n%s", rows)
	}
	if !strings.Contains(rows, "stopped") {
		t.Errorf("the panel does not name the state:\n%s", rows)
	}
	if !strings.Contains(rows, "/resume") {
		t.Errorf("the panel does not say the exchange can be continued:\n%s", rows)
	}
}

// A failed exchange is not dressed up as a stopped one.
func TestTheStatesAreNotConflated(t *testing.T) {
	for _, state := range []lifecycle.State{
		lifecycle.StateRunning, lifecycle.StateCompleted,
		lifecycle.StateStopped, lifecycle.StateFailed,
	} {
		m, _ := lifecycleModel(lifecycle.Operation{ID: "turn-1", Kind: "turn", State: state})
		rows := strings.Join(m.diagRows(), "\n")
		if !strings.Contains(rows, string(state)) {
			t.Errorf("the panel does not name %q:\n%s", state, rows)
		}
	}
}

// The command table carries the two commands, so /help and the
// suggestions know about them.
func TestTheLifecycleCommandsAreRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, c := range commandTable {
		names[c.name] = true
	}
	if !names["/stop"] {
		t.Error("/stop is not in the command table")
	}
	if !names["/resume"] {
		t.Error("/resume is not in the command table")
	}
}

// The ordinary turn — the one without attachments — reaches the engine
// that owns the lifecycle.
//
// The interface never guesses: after a turn the engine knows what
// happened, and the interface can ask it about the exchange.
func TestTheOrdinaryTurnReachesTheLifecycle(t *testing.T) {
	store, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(store, &provider.MockProvider{
		Replies: []provider.Response{{Content: "the answer"}},
	}, provider.Model{ID: "m"})
	m := New(eng)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "hello there")
	m = sendCurrent(m)
	op := eng.Lifecycle()
	if op.State != lifecycle.StateCompleted {
		t.Fatalf("state = %q, want completed: the ordinary turn must reach the lifecycle", op.State)
	}
	if op.Resumable {
		t.Error("a finished exchange is not continuable")
	}
	// The interface asks the engine about the exchange.
	if m.lifecycleStatus() != "completed" {
		t.Errorf("status = %q, want completed", m.lifecycleStatus())
	}
}

// A failed exchange is continued, and the notice says so.
func TestAFailedExchangeIsContinued(t *testing.T) {
	m, double := lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateFailed, Resumable: true,
	})
	m = runSlash(m, "/resume")
	if double.resumed != 1 {
		t.Fatalf("the exchange was continued %d time(s), want once", double.resumed)
	}
	joined := strings.Join(m.Lines(), "\n")
	if !strings.Contains(joined, "the continued answer") {
		t.Errorf("the answer is not on screen: %v", m.Lines())
	}
}

// The failure notice says that the exchange can be continued: the
// operator has to know that the question was not lost.
func TestTheFailureNoticeNamesTheContinuation(t *testing.T) {
	m, _ := lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateFailed, Resumable: true,
	})
	notice := m.failedNotice(errors.New("provider: 503 service unavailable"))
	if !strings.Contains(notice, "error:") {
		t.Errorf("notice = %q, want it to name the error", notice)
	}
	if !strings.Contains(notice, "/resume") {
		t.Errorf("notice = %q, want it to name the continuation", notice)
	}
	m, _ = lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateFailed,
	})
	notice = m.failedNotice(errors.New("provider: 503 service unavailable"))
	if strings.Contains(notice, "/resume") {
		t.Errorf("notice = %q, want no continuation to promise", notice)
	}
}

// A failed continuation is reported, not swallowed.
func TestAFailedContinuationIsReported(t *testing.T) {
	m, double := lifecycleModel(lifecycle.Operation{
		ID: "turn-1", Kind: "turn", State: lifecycle.StateStopped, Resumable: true,
	})
	double.resumeErr = errors.New("there is no interrupted exchange to continue")
	m = runSlash(m, "/resume")
	if !strings.Contains(m.Notice(), "no interrupted exchange") {
		t.Errorf("notice = %q, want the reason", m.Notice())
	}
}
