// Package queue holds the operator's messages that HAL has not acted on
// yet.
//
// A message may be submitted while HAL is working. It waits here until
// the running turn is done, and it is then sent in the order it was
// submitted.
//
// Two kinds of message live in the queue:
//
//   - an ordinary MESSAGE is an addition to the conversation. It is
//     sent as its own turn, and ordinary messages keep the order they
//     were submitted in.
//
//   - a STEER is a change of direction. It is not an addition to the
//     conversation: it is an instruction about the work in progress,
//     and it takes effect before the ordinary messages queued around
//     it. A steer never rewrites a request that was already sent.
//
// Every item moves through three states and never backwards:
//
//	pending   the operator's words are waiting;
//	accepted  the engine has them and will act on them;
//	consumed  the turn that carried them has ended.
//
// An item that came from a previous run is restored as pending, and it
// waits for the operator: the drain does not send it. It was written
// for a turn that never happened, and sending it without being asked
// would be a replay nobody requested.
//
// Nothing here talks to a provider, a session or a terminal. The queue
// is a state machine with one rule per transition, it is safe to use
// from several goroutines, and it never invents, repeats or silently
// drops a message.
package queue

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/attachments"
)

// Kind says what a queued item is.
type Kind string

const (
	// KindMessage is an ordinary queued message: an addition to the
	// conversation, sent as its own turn.
	KindMessage Kind = "message"
	// KindSteer is a change of direction. It is taken up by the running
	// turn at its next request boundary, and it takes effect before the
	// ordinary messages.
	KindSteer Kind = "steer"
)

// State says where an item is in its life.
type State string

const (
	// StatePending means the item is waiting.
	StatePending State = "pending"
	// StateAccepted means the engine has taken the item: it was
	// dispatched as a turn, or it was taken up by a running turn.
	StateAccepted State = "accepted"
	// StateConsumed means the turn that carried the item has ended.
	StateConsumed State = "consumed"
)

// MaxItems bounds the queue.
//
// The bound is a guard against a runaway input, not a product limit: a
// message the operator typed is theirs, and the queue refuses the
// overflow loudly rather than dropping it.
const MaxItems = 32

// ErrFull reports that the queue holds as many items as it may.
var ErrFull = errors.New("queue: the queue is full")

// ErrEmpty reports that an item carries no text.
var ErrEmpty = errors.New("queue: a queued message must have text")

// Item is one operator message in the queue.
type Item struct {
	// ID identifies the item for the rest of its life.
	ID string `json:"id"`
	// Kind says what the item is.
	Kind Kind `json:"kind"`
	// State says where the item is.
	State State `json:"state"`
	// Text is the operator's own words.
	Text string `json:"text"`
	// QueuedAt is when the operator submitted it.
	QueuedAt time.Time `json:"queued_at"`
	// TakenAt is when the engine took it.
	TakenAt time.Time `json:"taken_at,omitempty"`
	// DoneAt is when the turn that carried it ended.
	DoneAt time.Time `json:"done_at,omitempty"`
	// Restored reports that the item came from a previous run.
	//
	// A restored message is waiting for the operator: the drain does
	// not send it. It was written for a turn that never happened, and
	// sending it without being asked would be a replay the operator
	// did not ask for.
	Restored bool `json:"restored,omitempty"`
	// Files are the files the operator attached to this message. They
	// belong to the message and travel with it, even when the message
	// waits for the turn to end.
	Files []attachments.Meta `json:"files,omitempty"`
}

// Queue is the operator's outbox.
//
// The zero value is not usable; call New.
type Queue struct {
	mu    sync.Mutex
	items []*Item
	seq   uint64
	now   func() time.Time
}

// New returns an empty queue.
func New() *Queue {
	return &Queue{now: time.Now}
}

// Push adds an item and returns it in the pending state.
//
// The text is stored as the operator wrote it, minus the surrounding
// whitespace. An item with no text is refused: HAL never sends an
// empty message, and a queue that accepted one would look like a lost
// message later.
//
// The files are the ones the operator attached to this message. They
// travel with it, so a message that waits for the turn to end still
// carries the file it was written with.
func (q *Queue) Push(kind Kind, text string, files []attachments.Meta) (Item, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Item{}, ErrEmpty
	}
	if kind != KindMessage && kind != KindSteer {
		kind = KindMessage
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= MaxItems {
		return Item{}, fmt.Errorf("%w (%d items); clear it before queueing more", ErrFull, MaxItems)
	}
	q.seq++
	item := &Item{
		ID:       fmt.Sprintf("q-%d", q.seq),
		Kind:     kind,
		State:    StatePending,
		Text:     text,
		QueuedAt: q.now(),
		Files:    append([]attachments.Meta(nil), files...),
	}
	q.items = append(q.items, item)
	return *item, nil
}

// TakeSteer takes the oldest pending steering message.
//
// This is what a running turn calls at a request boundary: a message
// it takes is one it will carry into the next request it builds. An
// ordinary message is never taken this way — an ordinary message is a
// turn of its own, not an addition to the turn in flight.
//
// It reports false when no steering message is waiting.
func (q *Queue) TakeSteer() (Item, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, item := range q.items {
		if item.Kind == KindSteer && item.State == StatePending {
			q.accept(item)
			return *item, true
		}
	}
	return Item{}, false
}

// Next takes the next item to be sent as a turn.
//
// A steering message comes first: it is an instruction about the work
// in progress, and it takes effect before the ordinary messages queued
// around it. Ordinary messages keep the order they were submitted in.
//
// A restored item is not returned. It came from a previous run and
// waits for the operator; NextRestored is how they send it.
//
// It reports false when nothing is waiting.
func (q *Queue) Next() (Item, bool) {
	return q.next(false)
}

// NextRestored takes the next item that came from a previous run.
//
// It is the operator's own send: the drain never takes these, so a
// message the operator wrote before HAL was restarted is only sent
// when the operator asks for it.
func (q *Queue) NextRestored() (Item, bool) {
	return q.next(true)
}

// next takes the next item of the kind the caller asked for.
func (q *Queue) next(restored bool) (Item, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, item := range q.items {
		if item.Kind == KindSteer && item.State == StatePending && item.Restored == restored {
			q.accept(item)
			return *item, true
		}
	}
	for _, item := range q.items {
		if item.Kind == KindMessage && item.State == StatePending && item.Restored == restored {
			q.accept(item)
			return *item, true
		}
	}
	return Item{}, false
}

// ConsumeAccepted ends the items the engine has taken.
//
// It is called when the turn that carried them ends, whatever the
// outcome: a turn that was cancelled or that failed still ends, and
// the operator's words are not sent again. The items that were pending
// are untouched and keep waiting.
//
// It returns the items it ended.
func (q *Queue) ConsumeAccepted() []Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []Item
	for _, item := range q.items {
		if item.State != StateAccepted {
			continue
		}
		item.State = StateConsumed
		item.DoneAt = q.now()
		out = append(out, *item)
	}
	return out
}

// ClearPending removes the items that are still waiting.
//
// An item the engine has already taken is not cleared: it is on its way
// to the model, and dropping it here would rewrite work that is already
// in flight. The operator is told how many items went and how many
// stayed.
//
// It returns the items it removed.
func (q *Queue) ClearPending() []Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []Item
	kept := q.items[:0]
	for _, item := range q.items {
		if item.State == StatePending {
			item.State = StateConsumed
			item.DoneAt = q.now()
			out = append(out, *item)
			continue
		}
		kept = append(kept, item)
	}
	q.items = kept
	return out
}

// Reset empties the queue.
//
// It is used when the conversation changes: the queue belongs to the
// session it was written in, and its messages must not follow the
// operator into another conversation.
func (q *Queue) Reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = nil
}

// Snapshot returns every item, oldest first.
func (q *Queue) Snapshot() []Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Item, 0, len(q.items))
	for _, item := range q.items {
		out = append(out, *item)
	}
	return out
}

// Restore puts items back into the queue as pending.
//
// It is used when a session is opened again and the items that were
// waiting are read back from disk. The items keep their order and
// their identity, so nothing that was waiting is sent twice.
//
// A restored item waits for the operator: the drain does not send it,
// because it was written for a turn that never happened.
func (q *Queue) Restore(items []Item) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, item := range items {
		if strings.TrimSpace(item.Text) == "" {
			continue
		}
		if item.Kind != KindMessage && item.Kind != KindSteer {
			item.Kind = KindMessage
		}
		item.State = StatePending
		item.TakenAt = time.Time{}
		item.DoneAt = time.Time{}
		item.Restored = true
		if item.ID == "" {
			q.seq++
			item.ID = fmt.Sprintf("q-%d", q.seq)
		}
		q.items = append(q.items, &item)
	}
}

// Save writes the pending items to a store.
//
// A store that cannot be written is reported, not swallowed: the
// operator is told that a queued message is not safe against a
// restart.
func (q *Queue) Save(store Store) error {
	if store == nil {
		return nil
	}
	return store.Save(q.Snapshot())
}

// Counts reports how many items are in each state.
func (q *Queue) Counts() (pending, accepted, consumed int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, item := range q.items {
		switch item.State {
		case StatePending:
			pending++
		case StateAccepted:
			accepted++
		case StateConsumed:
			consumed++
		}
	}
	return pending, accepted, consumed
}

// Pending reports how many items are still waiting.
func (q *Queue) Pending() int {
	pending, _, _ := q.Counts()
	return pending
}

// RestoredPending reports how many waiting items came from a previous
// run.
//
// They wait for the operator: the drain does not send them.
func (q *Queue) RestoredPending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, item := range q.items {
		if item.State == StatePending && item.Restored {
			n++
		}
	}
	return n
}

// Empty reports whether nothing is waiting.
//
// An item the engine has taken is not waiting, but it is also not
// gone: the turn that carries it is still running.
func (q *Queue) Empty() bool {
	return q.Pending() == 0
}

// accept moves an item into the accepted state. The caller holds the
// lock.
func (q *Queue) accept(item *Item) {
	item.State = StateAccepted
	item.TakenAt = q.now()
}
