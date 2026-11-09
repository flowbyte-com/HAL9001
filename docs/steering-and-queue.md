# Steering and the message queue

HAL works one turn at a time, and a turn can take a while. The operator
is not idle in the meantime: they can write the next message, or change
the direction of the work that is running.

This document says exactly what happens to those messages, so that
nothing is lost, nothing is sent twice, and nothing is sent that the
operator did not ask for.

## The two kinds of message

**A queued message** is an addition to the conversation. It is sent as
a turn of its own. Ordinary queued messages keep the order they were
submitted in.

**A steering message** (`/steer`) is a change of direction. It is not an
addition to the conversation: it is an instruction about the work in
progress. It is taken up by the running turn at its next request
boundary, and it takes effect before the ordinary messages queued
around it.

A message is written while HAL is working by pressing `Enter`. Before
this workstream, `Enter` while a turn was in flight did nothing at all:
the operator's words were dropped on the floor with no feedback. They
now wait in the queue, and the operator can see them.

## When a steering message takes effect

**A steering message takes effect at the next request boundary of the
running turn** — the first moment the turn is about to send a request
it has not sent yet.

A request that was already dispatched is never rewritten. The operator
is told what happened, and the instruction applies to the work that has
not been sent yet.

This is the boundary the engine uses: at the top of every round of the
tool loop, before the request is built, the turn asks its steering
source for a message. The message is then carried into that request, so
the model sees it, and it is written to the session, so the operator's
own words are in the record of the conversation.

If the running turn has already sent its last request, the steering
message is not seen by that turn. It waits, and it leads the next turn:
it becomes the instruction of the next request HAL sends. The operator
is told that.

A steering message is never lost and never sent twice. If the turn that
took it up fails or is cancelled, the message is ended with that turn —
it is in the session, and offering it again would send it twice.

## The states

Every queued message is in one of three states, and never goes back:

| State | What it means |
|---|---|
| `pending` | the operator's words are waiting |
| `accepted` | the engine has them and will act on them |
| `consumed` | the turn that carried them has ended |

The conversation shows the state under the message:

- `queued` — waiting;
- `steering` — a steering message, waiting for its boundary;
- `sent` — the engine has it;
- `steering · taken up` — the running turn has it;
- `done` — the turn that carried it has ended.

`/queue` lists every message with its state and says how many are
waiting, how many have been sent and how many are done.

## When the queue drains

The queue drains **only after a turn that produced an answer**.

A turn that failed, or that the operator cancelled, leaves the waiting
messages alone. That is the operator's decision to make, not the
interface's: what they had already written keeps waiting until they
send it themselves or clear it.

A message the engine has taken is ended when its turn ends, whatever
the outcome. It is never offered again.

## The order

A steering message comes first. It is about the work in progress, and it
takes effect before the ordinary messages queued around it.

Ordinary messages keep the order they were submitted in among themselves.

## Clearing

`/queue clear` removes the messages that are still waiting.

A message the engine has already taken is not taken back: it is on its
way to the model, and clearing it would rewrite work that is already in
flight. The operator is told how many messages went and how many stayed.

## What a restart does to the queue

The waiting messages belong to the operator, not to the process. They
are kept in `<store>/<session>.queue.json`, beside the session's own
file, written atomically with mode `0600`.

Only the messages that are still waiting are written. A message the
engine has taken is in the session already, and writing it here as well
would be a second copy that could be sent twice.

A message that comes from a previous run **waits for the operator**: it
is shown in `/queue`, it is marked `waiting from before`, and it is not
sent on its own. Sending it without being asked would be a replay
nobody requested — it was written for a turn that never happened. The
operator sends it with `/queue send`, in the order it was written, or
removes it with `/queue clear`.

A change of conversation moves the queue with it: the messages that were
waiting are written to the session they belong to, and the messages
waiting for the session that is opened are read back. A message never
follows the operator into another conversation.

## The interface

| Command | What it does |
|---|---|
| `/steer <instruction>` | change the direction of the running turn |
| `/queue` | show the messages that are waiting, with their states |
| `/queue clear` | remove the messages that are still waiting |
| `/queue send` | send the messages that came from a previous run |

Writing a message while HAL is working queues it. The composer is
cleared, the message appears in the conversation marked `queued`, and
the activity line counts what is waiting.

A file the operator attached belongs to the message it was attached to,
so a queued message carries its file and sends it when the turn ends.

`Enter` while HAL is idle sends the message as usual. Nothing about the
composer, the session format or the permission gate changed.

## The engine seam

The engine asks its steering source once per round of the tool loop, at
the top of the round, before the request is built:

```go
// A SteerSource supplies change-of-direction messages to a running turn.
type SteerSource interface {
	// Steer returns the next steering message, if one is waiting.
	Steer() (text string, ok bool)
}

// WithSteerSource attaches the source of steering messages.
func (e *Engine) WithSteerSource(s SteerSource) *Engine
```

It is a pull, so the engine never needs to know whether a turn is
running and the queue never needs a handle on the engine. It adds no
turn-lifecycle concept to the engine: no current-turn handle, no turn
registry. The context the caller hands to `Respond*` stays the only way
to stop a turn.

The queue lives in `internal/queue` and depends on neither the engine
nor the interface. The application owns one queue and shares it with
both.
