package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/provider"
)

// The statistics of a turn belong to that turn.
//
// The model keeps the telemetry of the last finished turn for /diag,
// and the telemetry of the turn that is finishing for the diagnostics
// of that turn. The first one is not evidence about the second: a turn
// that ends without a visible answer must read the statistics of its
// own turn, or say nothing about numbers it does not have.

// statsModel is a model with a window to talk in.
func statsModel(t *testing.T) Model {
	t.Helper()
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(Model)
}

// truncatedTurn ends a turn that reached its output limit.
func truncatedTurn(t *testing.T, m Model, id int) Model {
	t.Helper()
	m.streamID = id
	m.sending = true
	updated, _ := m.Update(streamStatsMsg{id: id, stats: provider.TurnStats{
		Status: provider.StatusFinished, FinishReason: "length", OutputLimitHit: true,
		ReasoningChunks: 812, HasUsage: true,
		Usage: provider.Usage{PromptTokens: 1204, CompletionTokens: 15000, TotalTokens: 16204},
	}})
	m = updated.(Model)
	updated, _ = m.Update(responseMsg{id: id, text: "a cut off answer"})
	return updated.(Model)
}

// emptyTurn ends a turn without a visible answer and without statistics.
func emptyTurn(t *testing.T, m Model, id int) Model {
	t.Helper()
	m.streamID = id
	m.sending = true
	updated, _ := m.Update(errorMsg{id: id, err: engine.ErrNoVisibleAnswer})
	return updated.(Model)
}

// noteWith returns the conversation line that carries the text.
func noteWith(lines []string, text string) string {
	for _, line := range lines {
		if strings.Contains(line, text) {
			return line
		}
	}
	return ""
}

// noteText returns the text of the conversation line that carries the
// text, without the role label.
func noteText(lines []string, text string) string {
	line := noteWith(lines, text)
	if line == "" {
		return ""
	}
	if _, rest, ok := strings.Cut(line, "> "); ok {
		return rest
	}
	return line
}

// Case A: the stale statistics.
//
// A turn that reached its output limit is followed by a turn that ends
// without a visible answer and reports nothing of its own. The second
// turn did not provide the evidence for the first turn's conclusion,
// and it must not be told that it exhausted its output budget.
func TestAnEmptyAnswerDoesNotInheritTheStatisticsOfTheTurnBefore(t *testing.T) {
	m := statsModel(t)
	// The first turn reaches its output limit.
	m = truncatedTurn(t, m, 1)
	// The second turn ends without a visible answer and reports nothing.
	m = emptyTurn(t, m, 2)

	lines := m.Lines()
	note := noteWith(lines, "no visible answer")
	if note == "" {
		t.Fatalf("the empty answer was not explained: %v", lines)
	}
	for _, unwanted := range []string{
		"output budget",
		"finish_reason=length",
		"15000",
		"reasoning events",
	} {
		if strings.Contains(note, unwanted) {
			t.Errorf("the empty answer inherited %q from the turn before it: %q",
				unwanted, note)
		}
	}
	// The explanation is the neutral one: the turn reported nothing
	// about itself, so nothing is said about its budget.
	if got := noteText(lines, "no visible answer"); got != "(no visible answer · see /diag)" {
		t.Errorf("the explanation is not the neutral one: %q", got)
	}
	// The first turn keeps its own warning, exactly once.
	if n := strings.Count(strings.Join(lines, "\n"), "cut at max_tokens"); n != 1 {
		t.Errorf("the truncation warning is recorded %d times: %v", n, lines)
	}
}

// Case B: the genuine exhaustion of the current turn.
//
// The turn that ends without a visible answer is the turn that reports
// the exhaustion, and its diagnostics are kept.
func TestAnEmptyAnswerKeepsTheStatisticsOfItsOwnTurn(t *testing.T) {
	m := statsModel(t)
	m.streamID = 1
	m.sending = true
	updated, _ := m.Update(streamStatsMsg{id: 1, stats: provider.TurnStats{
		Status: provider.StatusFinished, FinishReason: "length", OutputLimitHit: true,
		ReasoningChunks: 812, HasUsage: true,
		Usage: provider.Usage{PromptTokens: 1204, CompletionTokens: 15000, TotalTokens: 16204},
	}})
	m = updated.(Model)
	updated, _ = m.Update(errorMsg{id: 1, err: engine.ErrNoVisibleAnswer})
	m = updated.(Model)

	lines := m.Lines()
	note := noteWith(lines, "no visible answer")
	if note == "" {
		t.Fatalf("the empty answer was not explained: %v", lines)
	}
	for _, want := range []string{
		"no visible answer",
		"output budget",
		"finish_reason=length",
		"15000",
		"reasoning events 812",
		"/diag",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the explanation is missing %q: %q", want, note)
		}
	}
	// The explanation is exactly the one of the old implementation:
	// the turn's own numbers, in the same words.
	if got := noteText(lines, "no visible answer"); got !=
		"(no visible answer · the output budget was exhausted during reasoning · "+
			"finish_reason=length · completion tokens 15000 · reasoning events 812 · see /diag)" {
		t.Errorf("the explanation changed: %q", got)
	}
}

// Case C: four turns in a row.
//
// A truncated turn, a turn that answered, a turn without a visible
// answer and a cancelled turn. None of them inherits the usage, the
// reasoning counts or the finish reason of another one, and the
// truncation warning is written once.
func TestNoTurnInheritsTheStatisticsOfAnotherTurn(t *testing.T) {
	m := statsModel(t)
	// 1. A turn that reached its output limit.
	m = truncatedTurn(t, m, 1)
	// 2. A turn that answered.
	m.streamID = 2
	m.sending = true
	updated, _ := m.Update(responseMsg{id: 2, text: "a plain answer"})
	m = updated.(Model)
	// 3. A turn without a visible answer and without statistics.
	m = emptyTurn(t, m, 3)
	// 4. A cancelled turn.
	m.streamID = 4
	m.sending = true
	updated, _ = m.Update(errorMsg{id: 4, err: context.Canceled})
	m = updated.(Model)

	lines := m.Lines()
	note := noteWith(lines, "no visible answer")
	if note == "" {
		t.Fatalf("the empty answer was not explained: %v", lines)
	}
	for _, unwanted := range []string{
		"output budget",
		"finish_reason=length",
		"15000",
		"reasoning events",
	} {
		if strings.Contains(note, unwanted) {
			t.Errorf("the empty answer inherited %q from an earlier turn: %q",
				unwanted, note)
		}
	}
	// The truncated turn keeps its own warning, and it is written once.
	if n := strings.Count(strings.Join(lines, "\n"), "cut at max_tokens"); n != 1 {
		t.Errorf("the truncation warning is recorded %d times: %v", n, lines)
	}
	// The cancelled turn leaves no note of its own.
	for _, line := range lines {
		if strings.Contains(line, "cancelled") && !strings.Contains(line, "cut at max_tokens") {
			t.Errorf("the cancelled turn left a note: %q", line)
		}
	}
}

// The correction does not touch the queue.
//
// A turn that ends without a visible answer leaves the waiting messages
// waiting, exactly as a failed turn does, and the turn that follows
// sends them in the order they were written.
func TestAnEmptyAnswerLeavesTheQueueAlone(t *testing.T) {
	e := &recordingEngine{}
	m := busyModel(t, e)
	m = paste(m, "first")
	m = enterCurrent(m)
	m = paste(m, "second")
	m = enterCurrent(m)
	if m.QueuePending() != 2 {
		t.Fatalf("the queue holds %d pending messages", m.QueuePending())
	}

	// The running turn ends without a visible answer.
	updated, _ := m.Update(errorMsg{id: m.streamID, err: engine.ErrNoVisibleAnswer})
	m = updated.(Model)

	// The waiting messages keep waiting.
	if m.QueuePending() != 2 {
		t.Errorf("the empty answer cleared the queue: %d pending", m.QueuePending())
	}
	if got := e.sent(); len(got) != 0 {
		t.Errorf("the empty answer sent the waiting messages: %v", got)
	}
	// The order they were written in is kept.
	if got := queueTexts(m); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("the queue changed its order: %v", got)
	}
}
