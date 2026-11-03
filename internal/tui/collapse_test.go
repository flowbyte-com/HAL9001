package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/usage"
)

// A long prompt is collapsed while HAL answers, so the answer has room
// to be read. When the answer is there, the prompt opens again: the
// collapse was a way to make room, not a way to shorten the message.
//
// The collapse is presentation only. The stored message, the history
// and everything copied from it stay complete.

// longPrompt is a prompt long enough to be collapsed.
func longPrompt() string {
	return strings.Repeat("museum thought experiment line\n", 20)
}

// collapseTestModel is a model with a long prompt in flight.
func collapseTestModel(t *testing.T) Model {
	t.Helper()
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = paste(m, longPrompt())
	return startTurn(m)
}

// TestPromptCollapsesWhileHALAnswers checks that a long prompt is
// abbreviated while the answer is being written.
func TestPromptCollapsesWhileHALAnswers(t *testing.T) {
	m := collapseTestModel(t)
	if !m.Sending() {
		t.Fatal("the turn must be in flight")
	}
	entry := m.Entries()[0]
	if entry.collapse != collapseAuto {
		t.Errorf("the prompt must be collapsed for the turn, state = %d", entry.collapse)
	}
	if len(entry.rows) > m.collapsedPreviewRows()+1 {
		t.Errorf("collapsed rows = %d, want at most %d", len(entry.rows), m.collapsedPreviewRows()+1)
	}
	if !strings.Contains(strings.Join(entry.rows, "\n"), "Ctrl+O to expand") {
		t.Errorf("the collapsed prompt must offer the control:\n%s", strings.Join(entry.rows, "\n"))
	}
	// The message itself is untouched.
	if entry.text != strings.TrimRight(longPrompt(), "\n") {
		t.Error("the stored prompt must stay complete")
	}
}

// TestPromptOpensWhenTheAnswerArrives checks that the prompt is shown
// in full once the answer is there.
func TestPromptOpensWhenTheAnswerArrives(t *testing.T) {
	m := collapseTestModel(t)
	collapsed := len(m.Entries()[0].rows)
	m = finishTurn(m, "the answer is here")
	entry := m.Entries()[0]
	if entry.collapse != collapseShown {
		t.Errorf("the prompt must be open again, state = %d", entry.collapse)
	}
	if len(entry.rows) <= collapsed {
		t.Errorf("the prompt must be shown in full: %d rows", len(entry.rows))
	}
	if strings.Contains(strings.Join(entry.rows, "\n"), "Ctrl+O to expand") {
		t.Error("the prompt is still abbreviated")
	}
}

// TestPromptExpansionKeepsTheAnswerVisible checks that the answer stays
// on screen when the prompt opens again: the view must not jump up into
// the expanded prompt.
func TestPromptExpansionKeepsTheAnswerVisible(t *testing.T) {
	m := collapseTestModel(t)
	updated, _ := m.Update(streamDeltaMsg{id: m.streamID, text: "the answer is here"})
	m = updated.(Model)
	m = finishTurn(m, "the answer is here")
	view := stripANSI(m.viewport.View())
	if !strings.Contains(view, "the answer is here") {
		t.Errorf("the answer must stay visible:\n%s", view)
	}
	if !m.viewport.AtBottom() {
		t.Error("the reading position must stay at the answer")
	}
}

// TestManualCollapseSurvivesTheAnswer checks that a prompt the operator
// collapsed by hand stays collapsed when the answer arrives.
func TestManualCollapseSurvivesTheAnswer(t *testing.T) {
	m := collapseTestModel(t)
	// Expand it, then collapse it again: the second choice is the
	// operator's own.
	m = pressKey(m, 'o', tea.ModCtrl, "")
	m = pressKey(m, 'o', tea.ModCtrl, "")
	collapsed := len(m.Entries()[0].rows)
	if m.Entries()[0].collapse != collapseManual {
		t.Fatalf("the collapse must be manual, state = %d", m.Entries()[0].collapse)
	}
	m = finishTurn(m, "the answer is here")
	if got := len(m.Entries()[0].rows); got != collapsed {
		t.Errorf("a manual collapse must be respected: %d rows, want %d", got, collapsed)
	}
	if m.Entries()[0].collapse != collapseManual {
		t.Errorf("the manual collapse must survive the answer, state = %d", m.Entries()[0].collapse)
	}
}

// TestManualExpandSurvivesTheAnswer checks that a prompt the operator
// opened by hand stays open when the answer arrives.
func TestManualExpandSurvivesTheAnswer(t *testing.T) {
	m := collapseTestModel(t)
	m = pressKey(m, 'o', tea.ModCtrl, "")
	expanded := len(m.Entries()[0].rows)
	m = finishTurn(m, "the answer is here")
	if got := len(m.Entries()[0].rows); got != expanded {
		t.Errorf("a manual expansion must be respected: %d rows, want %d", got, expanded)
	}
	if m.Entries()[0].collapse != collapseShown {
		t.Errorf("the prompt must stay open, state = %d", m.Entries()[0].collapse)
	}
}

// TestCancelledTurnOpensThePrompt checks that a cancelled turn leaves no
// abbreviated prompt behind.
func TestCancelledTurnOpensThePrompt(t *testing.T) {
	m := collapseTestModel(t)
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.Entries()[0].collapse != collapseShown {
		t.Errorf("a cancelled turn must open the prompt, state = %d", m.Entries()[0].collapse)
	}
	if strings.Contains(strings.Join(m.Entries()[0].rows, "\n"), "Ctrl+O to expand") {
		t.Error("the prompt is still abbreviated after the cancel")
	}
}

// TestFailedTurnOpensThePrompt checks the same for a turn that failed.
func TestFailedTurnOpensThePrompt(t *testing.T) {
	m := collapseTestModel(t)
	updated, _ := m.Update(errorMsg{id: m.streamID, err: errors.New("the provider gave up")})
	m = updated.(Model)
	if m.Entries()[0].collapse != collapseShown {
		t.Errorf("a failed turn must open the prompt, state = %d", m.Entries()[0].collapse)
	}
}

// TestReopenedSessionShowsTheFullPrompt checks that reopening a
// conversation does not perpetuate the collapse that belonged to a
// finished turn.
func TestReopenedSessionShowsTheFullPrompt(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.SetHistory([]SessionLine{{Role: "user", Text: strings.TrimRight(longPrompt(), "\n")}})
	entry := m.Entries()[0]
	if entry.collapse != collapseShown {
		t.Errorf("a stored prompt must be shown in full, state = %d", entry.collapse)
	}
	if strings.Contains(strings.Join(entry.rows, "\n"), "Ctrl+O to expand") {
		t.Error("a stored prompt must not be abbreviated")
	}
	if entry.text != strings.TrimRight(longPrompt(), "\n") {
		t.Error("the stored prompt must stay complete")
	}
}

// TestCollapsedPromptIsNeverTruncated checks that the abbreviation is
// presentation only: the entry, the history and the lines HAL exposes
// all carry the complete text.
func TestCollapsedPromptIsNeverTruncated(t *testing.T) {
	m := collapseTestModel(t)
	want := strings.TrimRight(longPrompt(), "\n")
	if got := m.Entries()[0].text; got != want {
		t.Error("the entry must hold the complete prompt")
	}
	if got := m.Lines()[0]; got != "You > "+want {
		t.Error("the conversation lines must hold the complete prompt")
	}
	if strings.Contains(m.Entries()[0].text, "Ctrl+O") {
		t.Error("the hint must not be part of the message")
	}
	// The hint is a row of the rendering, not of the message: it is the
	// only row that is not text of the prompt.
	rows := m.Entries()[0].rows
	for i, row := range rows {
		if strings.Contains(row, "Ctrl+O") && i != len(rows)-1 {
			t.Errorf("row %d holds the hint: %q", i, row)
		}
	}
}

// TestSelectionIsDroppedWhenThePromptOpens checks that a selection in
// progress is dropped when the prompt opens again: its ends point at
// rows that move, and a stale selection would copy the wrong text.
func TestSelectionIsDroppedWhenThePromptOpens(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = paste(m, longPrompt())
	m = startTurn(m)
	m.renderConversation()
	row, ok := m.screenRowOf(0)
	if !ok {
		t.Fatal("the prompt is not on screen")
	}
	m = pressPointer(m, entryPrefixCells, row)
	m = dragPointer(m, entryPrefixCells+3, row)
	if !m.selecting {
		t.Fatal("the press must start a selection")
	}
	m = finishTurn(m, "the answer is here")
	if m.selecting || m.selMoved {
		t.Error("the selection must be dropped when the prompt opens")
	}
}

// TestInterruptedTurnLeavesNoCollapseState checks that a conversation
// that is replaced while HAL is answering leaves nothing abbreviated
// behind.
func TestInterruptedTurnLeavesNoCollapseState(t *testing.T) {
	m := collapseTestModel(t)
	m.hooks.NewSession = func() (Engine, error) { return &scriptEngine{}, nil }
	m.newSession()
	for _, e := range m.Entries() {
		if e.collapse == collapseAuto {
			t.Error("a new conversation must not keep a temporary collapse")
		}
	}
	if strings.Contains(m.viewport.View(), "Ctrl+O to expand") {
		t.Error("the new conversation must not show an abbreviation")
	}
}

// TestSelectionSkipsTheCollapseHint checks that the collapse hint is
// interface: a selection across a collapsed prompt copies the text, not
// the hint.
func TestSelectionSkipsTheCollapseHint(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = paste(m, longPrompt())
	m = startTurn(m)
	m.renderConversation()
	if len(m.conv.rows) < 2 {
		t.Fatal("the collapsed prompt must have rows")
	}
	last := m.conv.rows[len(m.conv.rows)-1]
	got := m.conv.extract(cellPos{row: 0, cell: 0}, cellPos{row: len(m.conv.rows) - 1, cell: last.width})
	if strings.Contains(got, "Ctrl+O") {
		t.Errorf("the selection copied the hint: %q", got)
	}
	if strings.Contains(got, "more line") {
		t.Errorf("the selection copied the hint: %q", got)
	}
	if !strings.HasPrefix(got, "museum thought experiment line") {
		t.Errorf("the selection lost the message: %q", got)
	}
}

// TestSelectionSkipsTheMetadataLine checks that an answer's metadata
// line is interface too: it is never part of a selection.
func TestSelectionSkipsTheMetadataLine(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	m.appendEntry("assistant", "the answer")
	m.attachMeta(0, usage.Turn{Duration: 1200, Input: 10, Output: 20, Reported: true})
	m.renderConversation()
	last := m.conv.rows[len(m.conv.rows)-1]
	got := m.conv.extract(cellPos{row: 0, cell: 0}, cellPos{row: len(m.conv.rows) - 1, cell: last.width})
	if strings.Contains(got, " out") || strings.Contains(got, " in /") {
		t.Errorf("the selection copied the metadata line: %q", got)
	}
	if !strings.Contains(got, "the answer") {
		t.Errorf("the selection lost the answer: %q", got)
	}
}
