package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/flowbyte-com/HAL9001/internal/attachments"
	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/queue"
)

// The operator's outbox.
//
// HAL works one turn at a time, and a turn can take a while. The
// operator is not idle in the meantime: they can write the next
// message, or change the direction of the work that is running.
//
// These tests hold the interface to the rules that make that safe: a
// message written while HAL works waits instead of being dropped, the
// waiting messages are sent in the order they were written, a turn
// that failed or was cancelled leaves them waiting, a steering message
// is taken up by the running turn, and the operator can clear what is
// waiting.

// recordingEngine remembers the order in which the turns were sent.
type recordingEngine struct {
	inputs []string
}

// RespondStream implements Engine.
func (e *recordingEngine) RespondStream(_ context.Context, input string, _ func(string)) (string, error) {
	e.inputs = append(e.inputs, input)
	return "answer to " + input, nil
}

// sent returns the messages that were sent, in order.
func (e *recordingEngine) sent() []string { return append([]string(nil), e.inputs...) }

// draining runs the commands a turn produces until nothing is left, so
// the queue has had its chance to drain.
//
// A response that carries a waiting message starts the next turn, and
// that turn has a command of its own. The loop follows them.
func draining(m Model, cmd tea.Cmd) Model {
	for i := 0; i < 20; i++ {
		if cmd == nil {
			return m
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				m = drain(m, sub)
			}
			return m
		}
		if _, ok := msg.(noticeExpireMsg); ok {
			return m
		}
		updated, next := m.Update(msg)
		m = updated.(Model)
		cmd = next
	}
	return m
}

// busyModel returns a model with a turn in flight.
func busyModel(t *testing.T, e Engine) Model {
	t.Helper()
	m := NewWithHooks(e, Hooks{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.sending = true
	m.cancel = func() {}
	return m
}

// queueTexts returns the texts of the queued messages, in order.
func queueTexts(m Model) []string {
	var out []string
	for _, item := range m.QueueSnapshot() {
		out = append(out, item.Text)
	}
	return out
}

// A message written while HAL is working waits in the queue instead of
// being dropped on the floor.
func TestAMessageWrittenWhileHALWorksWaitsInTheQueue(t *testing.T) {
	m := busyModel(t, &recordingEngine{})
	m = paste(m, "and check the logs")
	m = enterCurrent(m)

	if got := m.Composer().Value(); got != "" {
		t.Fatalf("the composer still holds the message: %q", got)
	}
	if m.QueuePending() != 1 {
		t.Fatalf("the message was not queued: %d pending", m.QueuePending())
	}
	if !m.Sending() {
		t.Error("the running turn must keep running")
	}
	// The operator sees the message in the conversation, marked as
	// waiting.
	found := false
	for _, e := range m.Entries() {
		if e.text == "and check the logs" && e.queueState == string(queue.StatePending) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the queued message is not visible: %#v", m.Entries())
	}
	if !strings.Contains(m.Notice(), "queued") {
		t.Errorf("the operator was not told the message is waiting: %q", m.Notice())
	}
}

// The waiting messages are sent in the order they were written.
func TestTheWaitingMessagesAreSentInTheOrderTheyWereWritten(t *testing.T) {
	e := &recordingEngine{}
	m := busyModel(t, e)
	m = paste(m, "first")
	m = enterCurrent(m)
	m = paste(m, "second")
	m = enterCurrent(m)
	m = paste(m, "third")
	m = enterCurrent(m)

	if m.QueuePending() != 3 {
		t.Fatalf("the queue holds %d pending messages", m.QueuePending())
	}

	// The running turn ends with an answer. The waiting messages
	// follow, in the order they were written.
	updated, cmd := m.Update(responseMsg{id: m.streamID, text: "done"})
	m = updated.(Model)
	m = draining(m, cmd)

	got := e.sent()
	if len(got) != 3 {
		t.Fatalf("the queue sent %d messages: %v", len(got), got)
	}
	if got[0] != "first" || got[1] != "second" || got[2] != "third" {
		t.Fatalf("the queue sent the messages out of order: %v", got)
	}
	if m.QueuePending() != 0 {
		t.Fatalf("the queue still holds %d pending messages", m.QueuePending())
	}
}

// A turn that failed leaves the waiting messages alone: the operator
// decides whether the work continues.
func TestAFailedTurnLeavesTheWaitingMessagesAlone(t *testing.T) {
	e := &recordingEngine{}
	m := busyModel(t, e)
	m = paste(m, "still waiting")
	m = enterCurrent(m)

	updated, _ := m.Update(errorMsg{id: m.streamID, err: errors.New("the provider fell over")})
	m = updated.(Model)

	if m.QueuePending() != 1 {
		t.Fatalf("the failed turn cleared the queue: %d pending", m.QueuePending())
	}
	if len(e.sent()) != 0 {
		t.Fatalf("the failed turn sent the waiting message: %v", e.sent())
	}
	if m.Sending() {
		t.Error("the turn must be over")
	}
}

// A cancelled turn leaves the waiting messages alone as well.
func TestACancelledTurnLeavesTheWaitingMessagesAlone(t *testing.T) {
	e := &recordingEngine{}
	m := busyModel(t, e)
	m = paste(m, "still waiting")
	m = enterCurrent(m)

	updated, _ := m.Update(errorMsg{id: m.streamID, err: context.Canceled})
	m = updated.(Model)

	if m.QueuePending() != 1 {
		t.Fatalf("the cancelled turn cleared the queue: %d pending", m.QueuePending())
	}
	if len(e.sent()) != 0 {
		t.Fatalf("the cancelled turn sent the waiting message: %v", e.sent())
	}
}

// The warning of a truncated reply survives the turn that follows it.
//
// A waiting message starts the next turn as soon as the reply arrives,
// and the notice under the footer is replaced by the status of that
// turn. The warning is therefore a note in the conversation: the
// operator can still see that the reply was cut at max_tokens.
func TestTheOutputLimitWarningSurvivesTheNextQueuedTurn(t *testing.T) {
	e := &recordingEngine{}
	m := busyModel(t, e)
	m = paste(m, "first")
	m = enterCurrent(m)
	m = paste(m, "second")
	m = enterCurrent(m)
	if m.QueuePending() != 2 {
		t.Fatalf("the queue holds %d pending messages", m.QueuePending())
	}

	// The running turn ends with a reply that was cut at the output
	// limit.
	updated, _ := m.Update(streamStatsMsg{id: m.streamID, stats: provider.TurnStats{
		OutputLimitHit: true,
		Status:         provider.StatusDone,
	}})
	m = updated.(Model)
	updated, cmd := m.Update(responseMsg{id: m.streamID, text: "a cut off answer"})
	m = updated.(Model)
	m = draining(m, cmd)

	// The waiting messages started the next turns on their own, in the
	// order they were written.
	if got := e.sent(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("the waiting messages were sent in the wrong order: %v", got)
	}
	if m.QueuePending() != 0 {
		t.Fatalf("the queue still holds %d pending messages", m.QueuePending())
	}
	// The warning is still there: it is a note in the conversation.
	joined := strings.Join(m.Lines(), "\n")
	if !strings.Contains(joined, "cut at max_tokens") {
		t.Errorf("the warning is gone:\n%s", joined)
	}
	// The note is recorded once, and not again by the turns that
	// followed it.
	if n := strings.Count(joined, "cut at max_tokens"); n != 1 {
		t.Errorf("the warning is recorded %d times:\n%s", n, joined)
	}
}

// A steering message is taken up by the running turn: it is not an
// ordinary queued message, and it does not wait for the turn to end.
func TestASteeringMessageIsTakenUpByTheRunningTurn(t *testing.T) {
	m := busyModel(t, &recordingEngine{})
	m = typeText(m, "/steer focus on the error instead")
	m = enterCurrent(m)

	items := m.QueueSnapshot()
	if len(items) != 1 {
		t.Fatalf("the queue holds %d items", len(items))
	}
	if items[0].Kind != queue.KindSteer {
		t.Fatalf("the item is %q, want a steer", items[0].Kind)
	}
	// The running turn takes it at its next request boundary.
	taken, ok := m.queue.TakeSteer()
	if !ok {
		t.Fatal("the running turn found no steering message")
	}
	if taken.Text != "focus on the error instead" {
		t.Fatalf("the turn took %q", taken.Text)
	}
	// It was taken, not sent: it is no longer waiting.
	if m.QueuePending() != 0 {
		t.Fatalf("the queue still holds %d pending items", m.QueuePending())
	}
}

// A steering message with no instruction is refused: HAL would have
// nothing to steer with.
func TestASteeringMessageNeedsAnInstruction(t *testing.T) {
	m := busyModel(t, &recordingEngine{})
	m = typeText(m, "/steer")
	m = enterCurrent(m)

	if m.QueuePending() != 0 {
		t.Fatalf("an empty steer was queued: %d pending", m.QueuePending())
	}
	if !strings.Contains(m.Notice(), "/steer needs an instruction") {
		t.Errorf("the operator was not told: %q", m.Notice())
	}
}

// The operator sees the states: what is waiting, what has been sent
// and what is done.
func TestTheQueuePanelShowsTheStates(t *testing.T) {
	m := busyModel(t, &recordingEngine{})
	m = paste(m, "waiting")
	m = enterCurrent(m)

	m = typeText(m, "/queue")
	m = enterCurrent(m)

	if m.view != viewQueue {
		t.Fatalf("the queue panel did not open: %v", m.view)
	}
	rows := strings.Join(m.queueRows(), "\n")
	if !strings.Contains(rows, "1 waiting") {
		t.Errorf("the panel does not say what is waiting:\n%s", rows)
	}
	if !strings.Contains(rows, "waiting") || !strings.Contains(rows, "message") {
		t.Errorf("the panel does not show the message:\n%s", rows)
	}
}

// The operator is in control: clearing removes what is waiting and
// keeps what is already on its way.
func TestClearingRemovesOnlyTheWaitingMessages(t *testing.T) {
	m := busyModel(t, &recordingEngine{})
	m = paste(m, "waiting one")
	m = enterCurrent(m)
	m = paste(m, "waiting two")
	m = enterCurrent(m)

	// One message is taken by the running turn.
	if _, ok := m.queue.TakeSteer(); ok {
		t.Fatal("an ordinary message was taken as a steer")
	}
	m.queue.Next()

	m = typeText(m, "/queue clear")
	m = enterCurrent(m)

	items := m.QueueSnapshot()
	if m.QueuePending() != 0 {
		t.Fatalf("the queue still holds %d pending items", m.QueuePending())
	}
	// The message the turn had taken is still with the turn.
	accepted := 0
	for _, item := range items {
		if item.State == queue.StateAccepted {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("the clearing took back %d accepted messages", accepted-1)
	}
}

// queueFileEngine records the files that reach it with a turn.
type queueFileEngine struct {
	recordingEngine
	files [][]attachments.Meta
}

// RespondStreamEventsWithAttachments implements AttachmentEngine.
func (e *queueFileEngine) RespondStreamEventsWithAttachments(_ context.Context, input string, atts []attachments.Meta, _ func(engine.Event)) (string, error) {
	e.inputs = append(e.inputs, input)
	e.files = append(e.files, atts)
	return "answer to " + input, nil
}

// A queued message carries the file the operator attached to it: the
// attachment belongs to the message, not to whatever message comes
// next.
func TestAQueuedMessageCarriesItsAttachment(t *testing.T) {
	e := &queueFileEngine{}
	m := busyModel(t, e)
	m.pendingFiles = []attachments.Meta{{ID: "a1", Name: "notes.txt", Path: "notes.txt", Bytes: 12}}
	m = paste(m, "read this")
	m = enterCurrent(m)

	if len(m.pendingFiles) != 0 {
		t.Fatalf("the attachment is still pending: %v", m.pendingFiles)
	}
	found := false
	for _, en := range m.Entries() {
		if en.text == "read this" && len(en.files) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the queued message does not carry its file: %#v", m.Entries())
	}

	// When the turn ends, the message is sent with the file.
	updated, cmd := m.Update(responseMsg{id: m.streamID, text: "done"})
	m = updated.(Model)
	m = draining(m, cmd)
	if len(e.files) != 1 || len(e.files[0]) != 1 {
		t.Fatalf("the queued message was sent without its file: %v", e.files)
	}
}

// The queue belongs to the conversation it was written in: a session
// change moves it with the conversation.
func TestTheQueueMovesWithTheConversation(t *testing.T) {
	q := queue.New()
	m := NewWithHooks(&recordingEngine{}, Hooks{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.SetQueue(q)
	m.sending = true
	m = paste(m, "written here")
	m = enterCurrent(m)

	if m.QueuePending() != 1 {
		t.Fatalf("the message was not queued: %d pending", m.QueuePending())
	}

	// The conversation changes: the queue is emptied for the new one.
	m.queue.Reset()
	if m.QueuePending() != 0 {
		t.Fatalf("the queue followed the operator: %d pending", m.QueuePending())
	}
}

// A message that came from a previous run waits for the operator: a
// turn that ends does not send it, and /queue send does.
func TestARestoredMessageWaitsForTheOperator(t *testing.T) {
	dir := t.TempDir()
	store := queue.FileStore{Path: filepath.Join(dir, "chat.queue.json")}

	// A previous run left a message waiting.
	before := queue.New()
	if _, err := before.Push(queue.KindMessage, "written before", nil); err != nil {
		t.Fatal(err)
	}
	if err := before.Save(store); err != nil {
		t.Fatal(err)
	}

	e := &recordingEngine{}
	m := NewWithHooks(e, Hooks{QueueStore: func() queue.Store { return store }})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	if m.QueuePending() != 1 {
		t.Fatalf("the message from before was not restored: %d pending", m.QueuePending())
	}

	// A turn that ends does not send it.
	m.sending = true
	updated, cmd := m.Update(responseMsg{id: m.streamID, text: "done"})
	m = updated.(Model)
	m = draining(m, cmd)
	if len(e.sent()) != 0 {
		t.Fatalf("a restored message was sent without being asked: %v", e.sent())
	}
	if m.QueuePending() != 1 {
		t.Fatalf("the restored message was lost: %d pending", m.QueuePending())
	}

	// The operator sends it.
	m = typeText(m, "/queue send")
	m = enterCurrent(m)
	sent := e.sent()
	if len(sent) != 1 || sent[0] != "written before" {
		t.Fatalf("the operator's send did not reach the model: %v", sent)
	}
}
