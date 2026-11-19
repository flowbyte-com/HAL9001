package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
)

// Show Thinking is a presentation control: it says whether the
// reasoning a provider exposed is shown.
//
// It never changes what is asked of the provider, never changes the
// reasoning level, and never hides the answer.

// thinkingModel returns a model that has finished one turn whose
// provider exposed reasoning.
func thinkingModel(t *testing.T, show bool) Model {
	t.Helper()
	m := testModel()
	m.SetShowThinking(show)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	// The provider exposed reasoning while the turn ran.
	updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: "the model weighed two answers"})
	m = updated.(Model)
	updated, _ = m.Update(streamDeltaMsg{id: m.streamID, text: "the answer"})
	m = updated.(Model)
	updated, _ = m.Update(responseMsg{id: m.streamID, text: "the answer"})
	m = updated.(Model)
	return m
}

// The reasoning is hidden unless the operator asked to see it.
func TestShowThinkingIsHiddenByDefault(t *testing.T) {
	m := thinkingModel(t, false)
	view := string(m.View().Content)
	if strings.Contains(view, "the model weighed two answers") {
		t.Errorf("the reasoning was shown while it was off:\n%s", view)
	}
	if !strings.Contains(view, "the answer") {
		t.Errorf("the answer is missing:\n%s", view)
	}
}

// The reasoning is shown when the operator asked to see it, and it is
// set apart from the answer.
func TestShowThinkingShowsTheProviderReasoning(t *testing.T) {
	m := thinkingModel(t, true)
	view := string(m.View().Content)
	if !strings.Contains(view, "the model weighed two answers") {
		t.Errorf("the reasoning was not shown:\n%s", view)
	}
	if !strings.Contains(view, "the answer") {
		t.Errorf("the answer is missing:\n%s", view)
	}
	// The block is labelled, so it cannot be mistaken for the answer.
	if !strings.Contains(view, "thinking") {
		t.Errorf("the reasoning block is not labelled:\n%s", view)
	}
	// The reasoning sits above the answer.
	reasoningAt := strings.Index(view, "the model weighed two answers")
	answerAt := strings.Index(view, "the answer")
	if reasoningAt < 0 || answerAt < 0 || reasoningAt > answerAt {
		t.Errorf("the reasoning does not sit above the answer:\n%s", view)
	}
}

// The visibility and the reasoning level are two different choices: one
// never moves the other.
func TestTheVisibilityAndTheEffortAreIndependent(t *testing.T) {
	chosen := ""
	m := NewWithHooks(&scriptEngine{}, effortHooks("high", true, func(l string) { chosen = l }))
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	m.SetEffort("high", true)
	m.SetShowThinking(false)

	m = runCommand(m, "/thinking")
	if !m.ShowThinking() {
		t.Error("the visibility did not turn on")
	}
	if m.effort != "high" {
		t.Errorf("the effort changed to %q when the visibility changed", m.effort)
	}

	m = runCommand(m, "/effort low")
	if chosen != "low" {
		t.Errorf("the effort was not applied: %q", chosen)
	}
	if m.effort != "low" {
		t.Errorf("the effort did not change to low: %q", m.effort)
	}
	if !m.ShowThinking() {
		t.Error("the effort change turned the visibility off")
	}
}

// A model that exposes no reasoning shows nothing at all: there is no
// placeholder, because a placeholder would be text HAL made up.
func TestAModelWithoutReasoningShowsNothing(t *testing.T) {
	m := testModel()
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	updated, _ = m.Update(streamDeltaMsg{id: m.streamID, text: "the answer"})
	m = updated.(Model)
	updated, _ = m.Update(responseMsg{id: m.streamID, text: "the answer"})
	m = updated.(Model)

	view := string(m.View().Content)
	if strings.Contains(view, "thinking") {
		t.Errorf("a turn without reasoning showed a block:\n%s", view)
	}
	if !strings.Contains(view, "the answer") {
		t.Errorf("the answer is missing:\n%s", view)
	}
}

// The reasoning is decoration: a selection never copies it, and it is
// never part of the answer's text.
func TestTheReasoningIsNeverPartOfTheAnswer(t *testing.T) {
	m := thinkingModel(t, true)
	if len(m.entries) == 0 {
		t.Fatal("no answer was recorded")
	}
	last := m.entries[len(m.entries)-1]
	if strings.Contains(last.text, "the model weighed two answers") {
		t.Errorf("the reasoning is part of the answer's text: %q", last.text)
	}
	if last.thinking == nil || last.thinking.Text() != "the model weighed two answers" {
		t.Errorf("the entry's reasoning = %q", last.thinking.Text())
	}
	// Every reasoning row is decoration, so a selection skips it.
	for i, row := range last.rows {
		if strings.Contains(row, "the model weighed two answers") && !last.isDecoration(i) {
			t.Errorf("row %d carries the reasoning and is not decoration: %q", i, row)
		}
	}
}

// The reasoning shown while the turn runs belongs to that turn.
func TestTheReasoningIsShownWhileTheTurnRuns(t *testing.T) {
	m := testModel()
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: "weighing it up"})
	m = updated.(Model)

	view := string(m.View().Content)
	if !strings.Contains(view, "weighing it up") {
		t.Errorf("the running reasoning was not shown:\n%s", view)
	}
}

// A turn that was interrupted leaves no reasoning behind: the failure
// path clears it, so nothing half-finished is shown.
func TestAnInterruptedTurnLeavesNoReasoning(t *testing.T) {
	m := testModel()
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: "weighing it up"})
	m = updated.(Model)
	updated, _ = m.Update(errorMsg{id: m.streamID, err: context.Canceled})
	m = updated.(Model)

	if m.pendingThinking != nil {
		t.Errorf("the interrupted turn left reasoning behind: %q", m.pendingThinking.Text())
	}
	view := string(m.View().Content)
	if strings.Contains(view, "weighing it up") {
		t.Errorf("the interrupted reasoning was shown:\n%s", view)
	}
}

// The preference is the operator's own and it is persisted.
func TestTheVisibilityPreferenceIsPersisted(t *testing.T) {
	saved := []bool{}
	m := NewWithHooks(&scriptEngine{}, Hooks{
		SaveShowThinking: func(on bool) error {
			saved = append(saved, on)
			return nil
		},
	})
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	m = runCommand(m, "/thinking")
	if len(saved) != 1 || !saved[0] {
		t.Fatalf("the preference was not persisted: %v", saved)
	}
	if !m.ShowThinking() {
		t.Error("the visibility did not turn on")
	}
	m = runCommand(m, "/thinking")
	if len(saved) != 2 || saved[1] {
		t.Fatalf("the preference was not persisted again: %v", saved)
	}
	if m.ShowThinking() {
		t.Error("the visibility did not turn off")
	}
}

// A preference that cannot be written leaves the interface as it was.
func TestAFailedSaveLeavesTheVisibilityUnchanged(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, Hooks{
		SaveShowThinking: func(bool) error { return errSaveFailed },
	})
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	m = runCommand(m, "/thinking")
	if m.ShowThinking() {
		t.Error("the visibility changed although the preference could not be saved")
	}
	if !strings.Contains(m.Notice(), "unchanged") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// A chunk that carries reasoning and text at once loses neither: the
// reasoning is sent as well as the answer.
func TestAChunkWithBothReasoningAndTextKeepsBoth(t *testing.T) {
	m := NewWithHooks(&bothChunkEngine{}, Hooks{})
	m.SetColor(false)
	m.SetShowThinking(true)
	msgs := make(chan tea.Msg, 32)
	m.SetSender(func(msg tea.Msg) {
		select {
		case msgs <- msg:
		default:
		}
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	m = typeText(m, "hi")
	m = sendCurrent(m)
	// The engine's own event carries both, and the interface must not
	// drop one of them.
	deadline := time.Now().Add(5 * time.Second)
	for m.Sending() && time.Now().Before(deadline) {
		select {
		case msg := <-msgs:
			updated, _ = m.Update(msg)
			m = updated.(Model)
		case <-time.After(50 * time.Millisecond):
		}
	}

	view := string(m.View().Content)
	if !strings.Contains(view, "the model weighed two answers") {
		t.Errorf("the reasoning was lost:\n%s", view)
	}
	if !strings.Contains(view, "the answer") {
		t.Errorf("the answer was lost:\n%s", view)
	}
}

// bothChunkEngine reports one chunk that carries reasoning and text at
// the same time.
type bothChunkEngine struct{}

func (e *bothChunkEngine) RespondStream(context.Context, string, func(string)) (string, error) {
	return "the answer", nil
}

func (e *bothChunkEngine) RespondStreamEvents(_ context.Context, _ string, emit func(engine.Event)) (string, error) {
	emit(engine.Event{Text: "the answer", Reasoning: "the model weighed two answers"})
	return "the answer", nil
}

// errSaveFailed is a preference that cannot be written.
var errSaveFailed = errorString("the configuration could not be written")

// errorString is a plain error with a fixed message.
type errorString string

func (e errorString) Error() string { return string(e) }
