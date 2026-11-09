package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/lifecycle"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The lifecycle of an exchange.
//
// The engine runs the work, so the engine owns its state. These tests
// hold it to that: a cancelled exchange is stopped and not failed, a
// finished one is completed, a continuation starts from the checkpoint
// and never replays an operation that already ran.

// blockingProvider answers only when the request is cancelled, so a
// turn can be interrupted at a known point.
type blockingProvider struct {
	calls int
	// released is closed when the provider is asked to answer.
	released chan struct{}
}

func (p *blockingProvider) Name() string { return "blocking" }

func (p *blockingProvider) Chat(ctx context.Context, _ []provider.Message, _ provider.Model) (provider.Response, error) {
	p.calls++
	select {
	case <-ctx.Done():
		return provider.Response{}, ctx.Err()
	case <-p.released:
		return provider.Response{Content: "too late"}, nil
	}
}

// scriptedProvider replays one response per call and records the
// requests it received.
type scriptedProvider struct {
	steps    []provider.Response
	requests [][]provider.Message
	// before runs at the start of every call, with the number of the
	// call. It is where a test interrupts an exchange.
	before func(step int)
	calls  int
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Chat(ctx context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	p.requests = append(p.requests, append([]provider.Message(nil), messages...))
	p.calls++
	if p.before != nil {
		p.before(p.calls)
	}
	if err := ctx.Err(); err != nil {
		return provider.Response{}, err
	}
	if len(p.steps) == 0 {
		return provider.Response{Content: "script exhausted"}, nil
	}
	resp := p.steps[0]
	p.steps = p.steps[1:]
	return resp, nil
}

// lateProvider holds a cancelled turn until it is released, so that a
// newer exchange can start while the old one is still unwinding.
type lateProvider struct {
	release chan struct{}
	// cancelled says that the first call has seen the cancellation.
	cancelled atomic.Bool
	calls     int
}

func (p *lateProvider) Name() string { return "late" }

func (p *lateProvider) Chat(ctx context.Context, _ []provider.Message, _ provider.Model) (provider.Response, error) {
	p.calls++
	if p.calls == 1 {
		<-ctx.Done()
		p.cancelled.Store(true)
		<-p.release
		return provider.Response{}, ctx.Err()
	}
	return provider.Response{Content: "the second answer"}, nil
}

// A turn that was cancelled is stopped, and the checkpoint is waiting.
func TestACancelledTurnIsStoppedAndResumable(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &blockingProvider{released: make(chan struct{})}
	eng := New(st, p, provider.Model{ID: "m"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.RespondStreamEvents(ctx, "hold the line", nil)
		done <- err
	}()
	// The turn is running.
	waitFor(t, func() bool { return eng.Lifecycle().State == lifecycle.StateRunning })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	op := eng.Lifecycle()
	if op.State != lifecycle.StateStopped {
		t.Fatalf("state = %q, want stopped", op.State)
	}
	if !op.Resumable {
		t.Error("the interrupted exchange must be continuable")
	}
	if !strings.Contains(op.Status(), "stopped") {
		t.Errorf("status = %q, want it to say stopped", op.Status())
	}
}

// A turn that finished is completed, and it is never resumable.
func TestAFinishedTurnIsCompleted(t *testing.T) {
	eng := testEngine(t, []provider.Response{{Content: "hello there"}})
	if _, err := eng.Respond(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	op := eng.Lifecycle()
	if op.State != lifecycle.StateCompleted {
		t.Fatalf("state = %q, want completed", op.State)
	}
	if op.Resumable {
		t.Error("a finished exchange is not resumable")
	}
}

// A turn that failed is failed, and it is not a stop.
func TestAFailedTurnIsFailed(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	eng := New(st, nil, provider.Model{ID: "m"})
	if _, err := eng.Respond(context.Background(), "hi"); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err = %v, want the missing provider", err)
	}
	op := eng.Lifecycle()
	if op.State != lifecycle.StateFailed {
		t.Fatalf("state = %q, want failed", op.State)
	}
}

// There is nothing to continue without a checkpoint.
func TestThereIsNothingToResumeWithoutACheckpoint(t *testing.T) {
	eng := testEngine(t, []provider.Response{{Content: "hello there"}})
	if _, err := eng.Respond(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Resume(context.Background(), nil); !errors.Is(err, ErrNothingToResume) {
		t.Fatalf("err = %v, want nothing to resume", err)
	}
}

// The continuation answers the operator's message that is still
// unanswered, and it does not store it a second time.
func TestTheContinuationAnswersTheUnansweredMessage(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &blockingProvider{released: make(chan struct{})}
	eng := New(st, p, provider.Model{ID: "m"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.RespondStreamEvents(ctx, "what is the state of the build?", nil)
		done <- err
	}()
	waitFor(t, func() bool { return eng.Lifecycle().State == lifecycle.StateRunning })
	cancel()
	<-done

	// The provider answers the continued exchange.
	p.released = make(chan struct{})
	close(p.released)
	reply, err := eng.Resume(context.Background(), nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if reply != "too late" {
		t.Errorf("reply = %q", reply)
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history = %d messages, want the question and the answer", len(history))
	}
	if history[0].Role != session.RoleUser || history[0].Content != "what is the state of the build?" {
		t.Errorf("the operator's message = %#v", history[0])
	}
	if history[1].Role != session.RoleAssistant {
		t.Errorf("the second message = %#v", history[1])
	}
	if history[0].TurnID != history[1].TurnID {
		t.Error("the answer belongs to another exchange")
	}
}

// A continuation is refused when the conversation has moved on: the
// operator's message must still be the last one.
func TestTheContinuationIsRefusedWhenTheConversationMovedOn(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &blockingProvider{released: make(chan struct{})}
	eng := New(st, p, provider.Model{ID: "m"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.RespondStreamEvents(ctx, "first question", nil)
		done <- err
	}()
	waitFor(t, func() bool { return eng.Lifecycle().State == lifecycle.StateRunning })
	cancel()
	<-done
	// The operator asks something else.
	if _, err := st.AppendTurn(session.RoleUser, "second question", session.Turn{ID: session.NewTurnID()}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Resume(context.Background(), nil); !errors.Is(err, ErrNothingToResume) {
		t.Fatalf("err = %v, want nothing to resume", err)
	}
}

// An operation that already ran inside the interrupted exchange is not
// run again: the recorded result is handed to the model instead.
func TestTheContinuationNeverReplaysAnOperationThatRan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	perm := permission.New()
	perm.SetSession("chat")
	if err := perm.Enable("chat"); err != nil {
		t.Fatal(err)
	}
	// The provider asks for a read; the read runs; the exchange is
	// interrupted before the answer arrives.
	p := &scriptedProvider{steps: []provider.Response{
		{
			Content: "I'll look.",
			ToolCalls: []provider.ToolCall{{
				ID: "call_read", Name: "read",
				Arguments: map[string]any{"path": path},
			}},
		},
	}}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	ctx, cancel := context.WithCancel(context.Background())
	p.before = func(step int) {
		if step == 2 {
			// The read has run; the exchange is interrupted now.
			cancel()
		}
	}
	ran := 0
	_, err = eng.RespondStreamEvents(ctx, "read the file", func(ev Event) {
		if ev.Tool != nil && ev.Tool.Status == "ok" {
			ran++
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if ran != 1 {
		t.Fatalf("the read ran %d time(s), want once", ran)
	}
	if !eng.Lifecycle().Resumable {
		t.Fatal("the interrupted exchange must be continuable")
	}

	// The continuation proposes the very same read again.
	p.steps = []provider.Response{
		{
			Content: "Reading it again.",
			ToolCalls: []provider.ToolCall{{
				ID: "call_read_2", Name: "read",
				Arguments: map[string]any{"path": path},
			}},
		},
		{Content: "the file says data"},
	}
	ran = 0
	if _, err := eng.Resume(context.Background(), func(ev Event) {
		if ev.Tool != nil && ev.Tool.Status == "ok" {
			ran++
		}
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if ran != 0 {
		t.Fatalf("the read ran %d more time(s); it must not run again", ran)
	}
	// The model is told that the operation is already done.
	found := false
	for _, request := range p.requests {
		for _, message := range request {
			if message.Role != "tool" {
				continue
			}
			if strings.Contains(message.Content, "already_done") {
				found = true
			}
		}
	}
	if !found {
		t.Error("the model was not told that the operation had already run")
	}
}

// A new exchange starts from a clean slate: the checkpoint of an older
// interrupted exchange does not guard it.
func TestANewExchangeStartsFromACleanSlate(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &blockingProvider{released: make(chan struct{})}
	eng := New(st, p, provider.Model{ID: "m"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.RespondStreamEvents(ctx, "first question", nil)
		done <- err
	}()
	waitFor(t, func() bool { return eng.Lifecycle().State == lifecycle.StateRunning })
	cancel()
	<-done
	if !eng.Lifecycle().Resumable {
		t.Fatal("the interrupted exchange must be continuable")
	}
	// The operator asks something else instead of continuing.
	p.released = make(chan struct{})
	close(p.released)
	if _, err := eng.Respond(context.Background(), "another question"); err != nil {
		t.Fatalf("the new exchange failed: %v", err)
	}
	if eng.Lifecycle().Resumable {
		t.Error("the checkpoint of the old exchange must not guard the new one")
	}
}

// A late report from an interrupted exchange does not leave a stale
// checkpoint behind: a newer exchange has taken the operator's
// attention, and its safe point could only continue a question that is
// no longer the current one.
func TestALateInterruptionLeavesNoStaleCheckpoint(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &lateProvider{release: make(chan struct{})}
	eng := New(st, p, provider.Model{ID: "m"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.RespondStreamEvents(ctx, "first question", nil)
		done <- err
	}()
	waitFor(t, func() bool { return eng.Lifecycle().State == lifecycle.StateRunning })
	// The turn is cancelled, but it takes a while to unwind.
	cancel()
	waitFor(t, func() bool { return p.cancelled.Load() })

	// The operator asks something else before the old turn unwinds.
	if _, err := eng.Respond(context.Background(), "second question"); err != nil {
		t.Fatalf("the new exchange failed: %v", err)
	}
	// Now the old turn unwinds.
	close(p.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if eng.Lifecycle().Resumable {
		t.Error("the old exchange must not leave a checkpoint behind")
	}
	if _, err := eng.Resume(context.Background(), nil); !errors.Is(err, ErrNothingToResume) {
		t.Fatalf("err = %v, want nothing to resume", err)
	}
}

// A continuation that is interrupted as well keeps what it did: a
// second continuation does not run those operations again.
func TestAnInterruptedContinuationKeepsWhatItDid(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	second := filepath.Join(dir, "second.txt")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("data\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	perm := permission.New()
	perm.SetSession("chat")
	if err := perm.Enable("chat"); err != nil {
		t.Fatal(err)
	}
	readFirst := provider.ToolCall{
		ID: "call_first", Name: "read",
		Arguments: map[string]any{"path": first},
	}
	readSecond := provider.ToolCall{
		ID: "call_second", Name: "read",
		Arguments: map[string]any{"path": second},
	}
	p := &scriptedProvider{steps: []provider.Response{
		{Content: "I'll look.", ToolCalls: []provider.ToolCall{readFirst}},
	}}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	ctx, cancel := context.WithCancel(context.Background())
	p.before = func(step int) {
		if step == 2 {
			// The first read has run; the exchange is interrupted now.
			cancel()
		}
	}
	ran := 0
	if _, err := eng.RespondStreamEvents(ctx, "read the file", func(ev Event) {
		if ev.Tool != nil && ev.Tool.Status == "ok" {
			ran++
		}
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if ran != 1 {
		t.Fatalf("the first read ran %d time(s), want once", ran)
	}

	// The continuation asks for another file. It runs, and the
	// continuation is interrupted as well.
	p.steps = []provider.Response{
		{Content: "Now the other one.", ToolCalls: []provider.ToolCall{readSecond}},
	}
	ctx, cancel = context.WithCancel(context.Background())
	p.before = func(step int) {
		if step == 4 {
			// The second read has run; the continuation is interrupted.
			cancel()
		}
	}
	ran = 0
	if _, err := eng.Resume(ctx, func(ev Event) {
		if ev.Tool != nil && ev.Tool.Status == "ok" {
			ran++
		}
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if ran != 1 {
		t.Fatalf("the second read ran %d time(s), want once", ran)
	}

	// The second continuation asks for it again: it must not run.
	p.steps = []provider.Response{
		{Content: "The other one again.", ToolCalls: []provider.ToolCall{readSecond}},
		{Content: "the file says data"},
	}
	p.before = nil
	ran = 0
	if _, err := eng.Resume(context.Background(), func(ev Event) {
		if ev.Tool != nil && ev.Tool.Status == "ok" {
			ran++
		}
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if ran != 0 {
		t.Fatalf("the second read ran %d more time(s); it must not run again", ran)
	}
}

// waitFor waits until the condition holds.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the condition did not hold in time")
}
