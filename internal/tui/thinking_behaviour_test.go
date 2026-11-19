package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
)

// Show Thinking is a presentation control.
//
// Turning it on or off changes what the operator sees and nothing else:
// the request the provider receives is the same, the running turn is not
// rewritten, and the answer is never hidden.

// thinkingRecorder records the requests it was given and answers with a
// reasoning of its own.
type thinkingRecorder struct {
	requests  []string
	reasoning string
}

func (e *thinkingRecorder) RespondStream(_ context.Context, input string, _ func(string)) (string, error) {
	e.requests = append(e.requests, input)
	return "the answer", nil
}

func (e *thinkingRecorder) RespondStreamEvents(_ context.Context, input string, emit func(engine.Event)) (string, error) {
	e.requests = append(e.requests, input)
	if e.reasoning != "" {
		emit(engine.Event{Reasoning: e.reasoning})
	}
	return "the answer", nil
}

// The visibility never changes what the provider is asked.
//
// The same request with the visibility on and with it off reaches the
// provider unchanged: the control is the operator's, and the provider
// knows nothing about it.
func TestTheVisibilityNeverChangesTheRequest(t *testing.T) {
	for _, show := range []bool{true, false} {
		eng := &thinkingRecorder{}
		m := New(eng)
		m.SetColor(false)
		m.SetShowThinking(show)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
		m = updated.(Model)

		m = typeText(m, "hi")
		m = sendCurrent(m)
		if len(eng.requests) != 1 {
			t.Fatalf("the provider was asked %d times", len(eng.requests))
		}
		if eng.requests[0] != "hi" {
			t.Errorf("the request = %q", eng.requests[0])
		}
	}
}

// A provider that does not stream still has reasoning to show: the
// operator sees what the provider exposed, however it arrived.
func TestTheReasoningOfANonStreamingProviderIsShown(t *testing.T) {
	eng := &thinkingRecorder{reasoning: "the model weighed two answers"}
	m := New(eng)
	m.SetColor(false)
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	m = typeText(m, "hi")
	m = sendCurrent(m)

	view := string(m.View().Content)
	if !strings.Contains(view, "the model weighed two answers") {
		t.Errorf("the reasoning was not shown:\n%s", view)
	}
	if !strings.Contains(view, "the answer") {
		t.Errorf("the answer is missing:\n%s", view)
	}
}

// A provider that does not stream and exposes no reasoning shows no
// block: a placeholder would be text HAL made up.
func TestANonStreamingProviderWithoutReasoningShowsNoBlock(t *testing.T) {
	eng := &thinkingRecorder{}
	m := New(eng)
	m.SetColor(false)
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	m = typeText(m, "hi")
	m = sendCurrent(m)

	view := string(m.View().Content)
	if strings.Contains(view, "thinking") {
		t.Errorf("a turn without reasoning showed a block:\n%s", view)
	}
}

// The visibility can change while the turn runs: the block appears and
// disappears as the operator asks, and the running turn is not
// rewritten.
func TestTheVisibilityCanChangeWhileTheTurnRuns(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, Hooks{})
	m.SetColor(false)
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: "the model weighed two answers"})
	m = updated.(Model)
	if !strings.Contains(string(m.View().Content), "the model weighed two answers") {
		t.Fatalf("the reasoning was not shown:\n%s", m.View().Content)
	}

	// Off: the reasoning goes away, the answer stays.
	m = runCommand(m, "/thinking")
	if m.ShowThinking() {
		t.Fatal("the visibility did not turn off")
	}
	if strings.Contains(string(m.View().Content), "the model weighed two answers") {
		t.Errorf("the reasoning is still shown:\n%s", m.View().Content)
	}

	// On again: the reasoning comes back, unchanged.
	m = runCommand(m, "/thinking")
	if !m.ShowThinking() {
		t.Fatal("the visibility did not turn on")
	}
	if !strings.Contains(string(m.View().Content), "the model weighed two answers") {
		t.Errorf("the reasoning did not come back:\n%s", m.View().Content)
	}
}

// An interrupted turn leaves nothing behind: the reasoning of a turn
// that never finished is not shown.
func TestAnInterruptedTurnShowsNoReasoning(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, Hooks{})
	m.SetColor(false)
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: "the model weighed two answers"})
	m = updated.(Model)
	updated, _ = m.Update(errorMsg{id: m.streamID, err: context.Canceled})
	m = updated.(Model)

	if m.pendingThinking != nil {
		t.Errorf("the interrupted turn left reasoning behind: %q", m.pendingThinking.Text())
	}
	if strings.Contains(string(m.View().Content), "the model weighed two answers") {
		t.Errorf("the interrupted reasoning is shown:\n%s", m.View().Content)
	}
}

// The reasoning of a long turn stays inside its window: the block on
// screen is bounded, and the operator is told that the beginning is no
// longer shown.
func TestTheReasoningStaysInsideItsWindow(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, Hooks{})
	m.SetColor(false)
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	// Far more rows than the window holds.
	for i := 0; i < maxThinkingRows*3; i++ {
		updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: strings.Repeat("word ", 40) + "\n"})
		m = updated.(Model)
	}
	rows := m.pendingThinking.Rows()
	if len(rows) > maxThinkingRows+2 {
		t.Errorf("the block grew to %d rows, want at most %d", len(rows), maxThinkingRows+2)
	}
	if !strings.Contains(rows[0], thinkingDropNotice) {
		t.Errorf("the operator was not told that the beginning is not shown: %q", rows[0])
	}
}

// The layout of the block is the layout a whole-text wrap produces: the
// same text, wrapped the same way, whether it arrived in one piece or
// in many.
func TestTheLayoutDoesNotDependOnHowTheReasoningArrived(t *testing.T) {
	text := "the model weighed two answers and then it weighed them again, " +
		"which took a while and produced a long thinking block that had to be wrapped"

	whole := NewWithHooks(&scriptEngine{}, Hooks{})
	whole.SetColor(false)
	whole.SetShowThinking(true)
	updated, _ := whole.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	whole = updated.(Model)
	updated, _ = whole.Update(streamThinkingMsg{id: whole.streamID, text: text})
	whole = updated.(Model)

	pieces := NewWithHooks(&scriptEngine{}, Hooks{})
	pieces.SetColor(false)
	pieces.SetShowThinking(true)
	updated, _ = pieces.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	pieces = updated.(Model)
	for _, piece := range strings.SplitAfter(text, " ") {
		updated, _ = pieces.Update(streamThinkingMsg{id: pieces.streamID, text: piece})
		pieces = updated.(Model)
	}

	want := whole.pendingThinking.Rows()
	got := pieces.pendingThinking.Rows()
	if len(want) != len(got) {
		t.Fatalf("the layout depends on how the reasoning arrived: %d rows against %d", len(got), len(want))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// min is the smaller of two numbers, for the tests above.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// The reasoning of a turn that is still running is shown while it
// runs: the operator does not have to wait for the answer to see what
// the model is doing.
func TestTheRunningReasoningIsShownWhileItArrives(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, Hooks{})
	m.SetColor(false)
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: "weighing"})
	m = updated.(Model)
	updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: " it up"})
	m = updated.(Model)

	view := string(m.View().Content)
	if !strings.Contains(view, "weighing it up") {
		t.Errorf("the reasoning was not shown:\n%s", view)
	}
}

// A long word is broken by grapheme cluster, and the block stays inside
// the width of the frame.
func TestALongWordIsBrokenAndTheFrameKeepsItsShape(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, Hooks{})
	m.SetColor(false)
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: strings.Repeat("x", 500)})
	m = updated.(Model)

	for _, row := range m.pendingThinking.Rows() {
		if len([]rune(row)) > 100 {
			t.Errorf("a row is %d cells wide, want at most 100: %q", len([]rune(row)), row)
		}
	}
}

// The turn that is running is not rewritten when the visibility
// changes: the answer that is arriving is untouched.
func TestTheVisibilityDoesNotRewriteTheRunningTurn(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, Hooks{})
	m.SetColor(false)
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: "weighing it up"})
	m = updated.(Model)
	updated, _ = m.Update(streamDeltaMsg{id: m.streamID, text: "the answer so far"})
	m = updated.(Model)

	m = runCommand(m, "/thinking")
	view := string(m.View().Content)
	if !strings.Contains(view, "the answer so far") {
		t.Errorf("the answer was rewritten:\n%s", view)
	}
}

// The interface stays responsive while a long reasoning arrives: the
// work of a piece does not grow with the reasoning already received.
func TestTheInterfaceStaysResponsiveWhileTheReasoningArrives(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, Hooks{})
	m.SetColor(false)
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	start := time.Now()
	for i := 0; i < 200; i++ {
		updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: strings.Repeat("word ", 100)})
		m = updated.(Model)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the interface took %s for 200 pieces of reasoning", elapsed)
	}
}
