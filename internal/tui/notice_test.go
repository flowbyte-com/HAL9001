package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The transient notice and the live status of a turn.
//
// A notice is a message the operator has to read: a message that was
// queued, a copy that was made, a command that failed. The live status
// of a turn is a line that is redrawn every few frames: the clock, the
// phase, the tool that is running.
//
// They shared one field once. The redraw of the status overwrote the
// notice, so a message that was set while HAL was working was gone
// before the operator could read it. They have a field of their own
// now, and the row shows the notice while there is one.

// noticeTestTurn is a model with a turn in flight and its live status
// on the row.
func noticeTestTurn(t *testing.T) Model {
	t.Helper()
	m := testModel()
	m.sender = func(tea.Msg) {}
	m.streamID = 1
	m.sending = true
	m.streamStarted = m.clock()
	m.beginActivity()
	m.setActivity()
	return m
}

// A notice that was set while HAL was working survives the repaints of
// the live status.
func TestANoticeSurvivesTheRepaintsOfTheLiveStatus(t *testing.T) {
	m := noticeTestTurn(t)
	updated, cmd := m.Update(copyResultMsg{chars: 4, label: "response"})
	m = updated.(Model)
	if !strings.Contains(m.Notice(), "Copied") {
		t.Fatalf("the notice = %q", m.Notice())
	}
	if cmd == nil {
		t.Fatal("a notice must be given a lifetime, even while a turn is running")
	}
	// The heartbeat keeps ticking. It must not take the notice away.
	for i := 0; i < 5; i++ {
		updated, _ = m.Update(animationTickMsg{gen: m.activityGen, turn: 1})
		m = updated.(Model)
		if !strings.Contains(m.Notice(), "Copied") {
			t.Fatalf("the repaint of the status took the notice away: %q", m.Notice())
		}
	}
}

// A notice expires on its own, even while a turn is running: it does
// not stay on the screen for the whole turn.
//
// The expiry is a one-shot tick whose sequence is compared, so the
// timer of a notice that has been replaced can never clear a newer one.
func TestANoticeExpiresWhileATurnIsRunning(t *testing.T) {
	m := noticeTestTurn(t)
	updated, cmd := m.Update(copyResultMsg{chars: 4, label: "response"})
	m = updated.(Model)
	seq := m.noticeSeq
	// The expiry of this notice is on its way.
	if cmd == nil {
		t.Fatal("the notice must expire on its own")
	}
	updated, _ = m.Update(noticeExpireMsg{seq: seq})
	m = updated.(Model)
	if m.notice != "" {
		t.Fatalf("the notice is still on screen: %q", m.notice)
	}
	// The row belongs to the live status again.
	if !strings.Contains(m.Notice(), "Waiting for model") {
		t.Fatalf("the status of the turn = %q", m.Notice())
	}
}

// The timer of an old notice never clears a newer one.
func TestTheExpiryOfAnOldNoticeDoesNotClearANewerOne(t *testing.T) {
	m := noticeTestTurn(t)
	updated, _ := m.Update(copyResultMsg{chars: 4, label: "response"})
	m = updated.(Model)
	old := m.noticeSeq
	updated, _ = m.Update(copyResultMsg{chars: 9, label: "code block"})
	m = updated.(Model)
	if !strings.Contains(m.Notice(), "code block") {
		t.Fatalf("the notice = %q", m.Notice())
	}
	// The timer of the first notice arrives late.
	updated, _ = m.Update(noticeExpireMsg{seq: old})
	m = updated.(Model)
	if !strings.Contains(m.Notice(), "code block") {
		t.Fatalf("the old timer took the newer notice away: %q", m.Notice())
	}
}

// The row shows the notice while there is one, and the live status of
// the turn when there is not.
func TestTheNoticeRowShowsTheNoticeBeforeTheLiveStatus(t *testing.T) {
	m := noticeTestTurn(t)
	if !strings.Contains(m.noticeRow(), "Waiting for model") {
		t.Fatalf("the row = %q", m.noticeRow())
	}
	m.notice = "Copied response."
	if !strings.Contains(m.noticeRow(), "Copied response") {
		t.Fatalf("the row = %q", m.noticeRow())
	}
}

// The status of a new turn takes the row: the notice of what came
// before is stale, and the operator is looking at a turn now.
func TestTheStatusOfTheNewTurnTakesTheRow(t *testing.T) {
	m := testModel()
	m.sender = func(tea.Msg) {}
	updated, _ := m.Update(copyResultMsg{chars: 4, label: "response"})
	m = updated.(Model)
	next, _ := m.startTurn("hello", nil, -1)
	m = next.(Model)
	if m.notice != "" {
		t.Fatalf("the stale notice is still on screen: %q", m.notice)
	}
	if !strings.Contains(m.Notice(), "Waiting for model") {
		t.Fatalf("the status of the turn = %q", m.Notice())
	}
}
