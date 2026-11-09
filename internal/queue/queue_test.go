package queue

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The queue.
//
// The queue holds the operator's messages while HAL is working. These
// tests hold it to the rules that make that safe: ordinary messages
// keep their order, a steering message takes effect before them, an
// item is never sent twice, nothing is dropped without being said, and
// the operator can clear what is waiting.

func push(t *testing.T, q *Queue, kind Kind, text string) Item {
	t.Helper()
	item, err := q.Push(kind, text, nil)
	if err != nil {
		t.Fatalf("Push(%q): %v", text, err)
	}
	return item
}

func TestOrdinaryMessagesKeepTheirOrder(t *testing.T) {
	q := New()
	for _, text := range []string{"first", "second", "third"} {
		push(t, q, KindMessage, text)
	}
	for _, want := range []string{"first", "second", "third"} {
		item, ok := q.Next()
		if !ok {
			t.Fatalf("the queue ran dry before %q", want)
		}
		if item.Text != want {
			t.Fatalf("Next = %q, want %q", item.Text, want)
		}
		if item.State != StateAccepted {
			t.Fatalf("the item was not accepted: %#v", item)
		}
	}
	if _, ok := q.Next(); ok {
		t.Fatal("the queue handed out more than it held")
	}
}

func TestASteeringMessageTakesEffectBeforeTheOrdinaryOnes(t *testing.T) {
	// The operator queued two messages and then changed direction. The
	// steer is about the work in progress, so it goes first.
	q := New()
	push(t, q, KindMessage, "and check the logs")
	push(t, q, KindMessage, "and check the config")
	push(t, q, KindSteer, "focus on the error instead")

	item, ok := q.Next()
	if !ok {
		t.Fatal("the queue was empty")
	}
	if item.Kind != KindSteer {
		t.Fatalf("the first item was %q, want the steer", item.Kind)
	}
	if item.Text != "focus on the error instead" {
		t.Fatalf("the first item was %q", item.Text)
	}
	// The ordinary messages keep their own order behind it.
	item, _ = q.Next()
	if item.Text != "and check the logs" {
		t.Fatalf("the second item was %q", item.Text)
	}
	item, _ = q.Next()
	if item.Text != "and check the config" {
		t.Fatalf("the third item was %q", item.Text)
	}
}

func TestARunningTurnTakesOnlySteeringMessages(t *testing.T) {
	// An ordinary message is a turn of its own. A running turn takes a
	// steering message into the request it is about to build, and
	// nothing else.
	q := New()
	push(t, q, KindMessage, "an ordinary message")
	push(t, q, KindSteer, "a change of direction")

	item, ok := q.TakeSteer()
	if !ok {
		t.Fatal("the running turn took nothing")
	}
	if item.Kind != KindSteer {
		t.Fatalf("the turn took %q", item.Kind)
	}
	if _, ok := q.TakeSteer(); ok {
		t.Fatal("the turn took a second message")
	}
	// The ordinary message is still waiting for its own turn.
	next, ok := q.Next()
	if !ok {
		t.Fatal("the ordinary message was lost")
	}
	if next.Text != "an ordinary message" {
		t.Fatalf("Next = %q", next.Text)
	}
}

func TestAnItemIsNeverHandedOutTwice(t *testing.T) {
	q := New()
	push(t, q, KindMessage, "only once")
	if _, ok := q.Next(); !ok {
		t.Fatal("the queue was empty")
	}
	if _, ok := q.Next(); ok {
		t.Fatal("the queue handed out the same message twice")
	}
	if _, ok := q.TakeSteer(); ok {
		t.Fatal("the queue handed out the same message twice")
	}
}

func TestAnAcceptedItemIsConsumedWhenItsTurnEnds(t *testing.T) {
	q := New()
	push(t, q, KindMessage, "the message")
	item, _ := q.Next()

	done := q.ConsumeAccepted()
	if len(done) != 1 {
		t.Fatalf("the turn ended %d items", len(done))
	}
	if done[0].ID != item.ID {
		t.Fatalf("the wrong item was ended: %#v", done[0])
	}
	// A consumed item is not waiting and is not sent again.
	if q.Pending() != 0 {
		t.Fatalf("the queue still holds %d pending items", q.Pending())
	}
	if _, ok := q.Next(); ok {
		t.Fatal("a consumed message was sent again")
	}
}

func TestACancelledTurnLeavesTheWaitingMessagesAlone(t *testing.T) {
	// The operator cancels. The messages that were waiting keep
	// waiting: a cancellation is the operator's decision, and it does
	// not discard what they had already written.
	q := New()
	push(t, q, KindMessage, "first")
	push(t, q, KindMessage, "second")
	taken, _ := q.Next()

	q.ConsumeAccepted()

	if q.Pending() != 1 {
		t.Fatalf("the queue holds %d pending items after a cancellation", q.Pending())
	}
	remaining, ok := q.Next()
	if !ok {
		t.Fatal("the waiting message was lost")
	}
	if remaining.Text != "second" {
		t.Fatalf("Next = %q, want the message that was still waiting", remaining.Text)
	}
	// The message the cancelled turn carried is not sent again.
	if remaining.ID == taken.ID {
		t.Fatal("the cancelled message was sent again")
	}
}

func TestAFailedTurnDoesNotReplayWhatItAlreadyTook(t *testing.T) {
	// The turn took the message and then failed. The message is in the
	// session already, so it must not be offered again.
	q := New()
	push(t, q, KindMessage, "the message")
	taken, _ := q.Next()

	// The turn failed: the engine ends the items it took, exactly as
	// it does for a turn that was cancelled.
	q.ConsumeAccepted()

	if q.Pending() != 0 {
		t.Fatalf("the queue holds %d pending items", q.Pending())
	}
	for _, item := range q.Snapshot() {
		if item.ID == taken.ID && item.State == StatePending {
			t.Fatal("the failed message is waiting to be sent again")
		}
	}
}

func TestClearingRemovesTheWaitingItemsAndKeepsTheAcceptedOnes(t *testing.T) {
	// An item the engine has taken is on its way to the model.
	// Clearing the queue must not rewrite it.
	q := New()
	push(t, q, KindMessage, "waiting one")
	push(t, q, KindMessage, "waiting two")
	accepted, _ := q.Next()

	removed := q.ClearPending()
	if len(removed) != 1 {
		t.Fatalf("clearing removed %d items", len(removed))
	}
	if removed[0].Text != "waiting two" {
		t.Fatalf("clearing removed %q", removed[0].Text)
	}
	if q.Pending() != 0 {
		t.Fatalf("the queue still holds %d pending items", q.Pending())
	}
	// The accepted item is untouched: it is still with the running
	// turn.
	found := false
	for _, item := range q.Snapshot() {
		if item.ID == accepted.ID {
			found = true
			if item.State != StateAccepted {
				t.Fatalf("the accepted item is %q", item.State)
			}
		}
	}
	if !found {
		t.Fatal("clearing removed the accepted item")
	}
}

func TestAnEmptyMessageIsRefused(t *testing.T) {
	q := New()
	if _, err := q.Push(KindMessage, "   ", nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("the error was %v, want ErrEmpty", err)
	}
	if q.Pending() != 0 {
		t.Fatal("an empty message was queued")
	}
}

func TestTheQueueRefusesToGrowWithoutBound(t *testing.T) {
	q := New()
	for i := 0; i < MaxItems; i++ {
		push(t, q, KindMessage, "message")
	}
	_, err := q.Push(KindMessage, "one too many", nil)
	if !errors.Is(err, ErrFull) {
		t.Fatalf("the error was %v, want ErrFull", err)
	}
	// The queue kept everything it had.
	if q.Pending() != MaxItems {
		t.Fatalf("the queue holds %d items", q.Pending())
	}
}

// Simultaneous input: the operator types while HAL is working, and the
// interface reads the queue at the same time.
func TestTheQueueIsSafeUnderSimultaneousInput(t *testing.T) {
	q := New()
	const writers = 8
	// The queue holds at most MaxItems, so the writers stay inside the
	// bound and every message is accepted.
	const each = MaxItems / writers
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := q.Push(KindMessage, "message", nil); err != nil {
					t.Errorf("Push: %v", err)
					return
				}
			}
		}(w)
	}
	// The interface reads the queue while it is being written.
	stop := make(chan struct{})
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			q.Snapshot()
			q.Counts()
			q.Pending()
		}
	}()
	wg.Wait()
	close(stop)
	reader.Wait()

	total := 0
	for {
		_, ok := q.Next()
		if !ok {
			break
		}
		total++
		q.ConsumeAccepted()
	}
	if total != writers*each {
		t.Fatalf("the queue handed out %d of %d messages", total, writers*each)
	}
}

// The queue on disk.
func TestTheQueueSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.queue.json")
	store := FileStore{Path: path}

	q := New()
	push(t, q, KindMessage, "still waiting")
	push(t, q, KindSteer, "still steering")
	// A steer is taken first: it is the instruction about the work in
	// progress.
	taken, _ := q.Next()
	if taken.Kind != KindSteer {
		t.Fatalf("the first item was %q", taken.Kind)
	}
	if err := q.Save(store); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// A new process reads the queue back.
	restored, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("the queue restored %d items", len(restored))
	}
	if restored[0].Text != "still waiting" {
		t.Fatalf("the restored item was %q", restored[0].Text)
	}
	// The item the engine had taken is not restored: it is in the
	// session, and restoring it would send it twice.
	for _, item := range restored {
		if item.ID == taken.ID {
			t.Fatal("a message that was already taken was restored")
		}
	}

	q2 := New()
	q2.Restore(restored)
	if q2.Pending() != 1 {
		t.Fatalf("the restored queue holds %d pending items", q2.Pending())
	}
	// The drain does not send it: it was written for a turn that never
	// happened, and sending it on its own would be a replay nobody
	// asked for.
	if _, ok := q2.Next(); ok {
		t.Fatal("a restored message was sent without the operator asking")
	}
	if q2.RestoredPending() != 1 {
		t.Fatalf("the restored queue holds %d restored items", q2.RestoredPending())
	}
	// The operator sends it.
	item, ok := q2.NextRestored()
	if !ok {
		t.Fatal("the operator could not send the restored message")
	}
	if item.Text != "still waiting" {
		t.Fatalf("the restored queue handed out %q", item.Text)
	}
}

// A message that came from a previous run waits for the operator, and
// the messages of this run keep their own order around it.
func TestARestoredMessageWaitsForTheOperator(t *testing.T) {
	q := New()
	q.Restore([]Item{{ID: "q-1", Kind: KindMessage, Text: "from before"}})
	push(t, q, KindMessage, "from this run")

	// The drain takes the message of this run and leaves the restored
	// one alone.
	item, ok := q.Next()
	if !ok {
		t.Fatal("the queue was empty")
	}
	if item.Text != "from this run" {
		t.Fatalf("the drain handed out %q", item.Text)
	}
	if q.Pending() != 1 {
		t.Fatalf("the queue holds %d pending items", q.Pending())
	}
	restored, ok := q.NextRestored()
	if !ok {
		t.Fatal("the restored message was lost")
	}
	if restored.Text != "from before" {
		t.Fatalf("the restored message was %q", restored.Text)
	}
}

func TestClearingTheQueueRemovesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.queue.json")
	store := FileStore{Path: path}

	q := New()
	push(t, q, KindMessage, "waiting")
	if err := q.Save(store); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the queue file was not written: %v", err)
	}
	q.ClearPending()
	if err := q.Save(store); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the queue file survived the clearing: %v", err)
	}
	items, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("the store returned %d items", len(items))
	}
}

func TestTheStoreRefusesADamagedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.queue.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := FileStore{Path: path}
	if _, err := store.Load(); err == nil {
		t.Fatal("a damaged queue file was read as an empty queue")
	}
}

func TestTheStoreKeepsTheOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.queue.json")
	store := FileStore{Path: path}

	q := New()
	for _, text := range []string{"one", "two", "three"} {
		push(t, q, KindMessage, text)
	}
	if err := q.Save(store); err != nil {
		t.Fatalf("Save: %v", err)
	}
	items, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var texts []string
	for _, item := range items {
		texts = append(texts, item.Text)
	}
	if strings.Join(texts, ",") != "one,two,three" {
		t.Fatalf("the restored order was %v", texts)
	}
}

func TestTheItemCarriesItsTimes(t *testing.T) {
	q := New()
	q.now = func() time.Time { return time.Unix(1700000000, 0).UTC() }
	push(t, q, KindMessage, "a message")
	items := q.Snapshot()
	if len(items) != 1 {
		t.Fatalf("the queue holds %d items", len(items))
	}
	if items[0].QueuedAt.IsZero() {
		t.Fatal("the item has no queue time")
	}
	if !items[0].TakenAt.IsZero() {
		t.Fatal("a pending item has a taken time")
	}
	_, _ = q.Next()
	items = q.Snapshot()
	if items[0].TakenAt.IsZero() {
		t.Fatal("the accepted item has no taken time")
	}
	q.ConsumeAccepted()
	items = q.Snapshot()
	if items[0].DoneAt.IsZero() {
		t.Fatal("the consumed item has no end time")
	}
}
