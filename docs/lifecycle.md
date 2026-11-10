# The lifecycle of an operation

HAL9001 runs work that takes time: a provider request, a tool call, a
command. Every such piece of work has a **lifecycle**, and this document
is the contract for it. It exists so that two parts of the application
can agree on what is happening without one of them reaching into the
other.

The contract is deliberately small. It is the *vocabulary* and the
*ownership*, not a framework.

## The states

An operation is in exactly one of five states:

| State | Meaning |
|---|---|
| `idle` | nothing has run yet |
| `running` | the work is in flight |
| `completed` | the work finished |
| `stopped` | the work was cancelled before it finished |
| `failed` | the work ended in an error |

Three rules hold:

- **A stop is not a failure.** A cancelled operation is `stopped`, not
  `failed`: the operator asked for it.
- **A stop is not a completion.** An operation that was stopped did not
  finish, and nothing may be presented as if it had.
- **Still running is still running.** A cancellation is cooperative: the
  operation is `running` until it has actually unwound. An interface
  that says "stopped" while the request is still open would be lying.

## The package

`internal/lifecycle` owns the vocabulary and the state machine. It
knows nothing about providers, tools or the terminal.

```go
type State string

const (
	StateIdle      State = "idle"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateStopped   State = "stopped"
	StateFailed    State = "failed"
)

type Operation struct {
	ID        string
	Kind      string    // what it is, for example "turn"
	State     State
	Started   time.Time
	Ended     time.Time
	Reason    string    // why it stopped or failed
	Steps     int       // the operations that ran inside it
	Resumable bool      // a checkpoint is waiting to be continued
}

type Manager struct{ /* ... */ }

func New() *Manager
func (m *Manager) Begin(kind string) Operation
func (m *Manager) Complete(id string)
func (m *Manager) Stop(id, reason string)
func (m *Manager) Fail(id, reason string)
func (m *Manager) Current() Operation
func (m *Manager) Last() Operation
func (m *Manager) Status() string
```

`Manager` is safe for concurrent use: the request path and the
interface's own thread both touch it.

## Who owns what

| Concern | Owner |
|---|---|
| the state of an operation | the engine (`internal/engine`) |
| the checkpoint of an interrupted exchange | the engine |
| the cancel function of the running turn | the interface (`internal/tui`) |
| the operator-visible status | the interface, from the engine's state |

The engine owns the execution state because the engine is what runs.
The interface owns the cancel function because the interface is what
starts the turn. Neither reaches into the other: the interface asks,
the engine answers.

## The interface between them

The interface asks the engine two things, through an optional interface
that any engine may implement:

```go
// LifecycleEngine is implemented by an engine that owns the lifecycle of
// the exchange it runs.
type LifecycleEngine interface {
	// Lifecycle reports the state of the exchange.
	Lifecycle() lifecycle.Operation
	// Resume continues the exchange that was interrupted, from its
	// checkpoint. It fails when there is no checkpoint to continue from.
	Resume(ctx context.Context, emit func(engine.Event)) (string, error)
}
```

An engine without it behaves exactly as before: nothing is stopped, and
nothing is resumed. The interface asks; it never assumes.

## Stopping

`/stop` asks the running turn to stop. The cancellation is cooperative
and travels through the request's context:

- the provider's HTTP request is aborted;
- a command that is running is terminated **together with its process
  group**, so no child is left behind;
- an MCP tool call is cancelled with a `notifications/cancelled`
  notification — a server that does not implement cancellation may
  still finish the work it had already accepted, and the operator is
  told so.

Stopping never leaves the interface stuck: the composer is usable again
at once, and the turn's outcome arrives when it unwinds.

## Continuing

`/resume` continues the exchange that was interrupted. It is refused
unless there is a checkpoint:

| Last state | `/resume` |
|---|---|
| `running` | refused — stop the turn first |
| `completed` | refused — the turn finished; it is never replayed |
| `stopped` | the exchange continues |
| `failed` | the exchange continues |

An exchange that did not finish leaves a checkpoint: one that was
stopped, one that failed, and one that produced no visible answer all
leave the operator's message unanswered, and all three can be
continued. A turn that finished leaves none.

The checkpoint holds the identity of the exchange, the operator's
message and **the operations that already ran inside it**. The resume
never replays an external side effect: an operation that already ran in
the interrupted exchange is not executed again. The recorded result is
handed to the model instead, so the model knows the work is done.

The record is honest about the outcome. `ok` reflects what happened: an
operation that failed is not reported as a success, and an operation
that was cancelled or timed out says that its side effect is unknown.
The model is never told that work succeeded when it did not.

An operation is recognised by the tool's name and a fingerprint of its
arguments. The arguments are hashed, never kept: the fingerprint says
"the same call", not what the call was. The same tool with different
arguments is a different operation, and it runs again.

The ledger of the continued exchange carries what the interrupted attempt
did as well: the record of what really ran is never lost, and it holds one
entry per execution, not one per attempt. A continuation that is
interrupted as well keeps what it did, so a second continuation does not
run it again.

## Where it is proved

| Claim | Where |
|---|---|
| the vocabulary and the state machine | `internal/lifecycle/lifecycle_test.go` |
| the checkpoint, the guard and the refusal | `internal/engine/lifecycle_test.go` |
| the commands, the notices and the status | `internal/tui/lifecycle_test.go` |
| the wiring through the real application | `cmd/hal9001/lifecycle_test.go` |
| the whole lifecycle in a real terminal | `tools/ptycheck/stop_resume_checks.py` |

The terminal suite drives **one** HAL process through the whole scenario:
the safe point lives in the running process, so a scenario spread over two
processes would resume nothing at all.

## What the operator sees

- the notice line names the state: `running`, `completed`, `stopped`,
  `failed`;
- a stopped or failed turn says that the exchange can be continued with
  `/resume`, so the operator knows that the question was not lost;
- `/diag` carries the same state, with the number of operations that
  ran and whether the exchange can be continued;
- `/stop` and `/resume` say what they did, or why they did nothing.
