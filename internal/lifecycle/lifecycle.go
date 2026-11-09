// Package lifecycle owns the state of the operations HAL9001 runs.
//
// An operation is one piece of work with a beginning and an end: a
// provider turn, a tool call, a command, a background job. This package
// knows nothing about providers, tools or the terminal. It owns the
// vocabulary and the state machine, so that every part of the
// application can report what it is doing in the same terms.
//
// The states are the ones an operator can act on:
//
//	running   the work is in flight
//	completed the work finished
//	stopped   the work was cancelled before it finished
//	failed    the work ended in an error
//
// Three rules hold, and they are the reason the package exists:
//
//   - a stop is not a failure. The operator asked for it, and an
//     operation that was stopped did not fail.
//   - a stop is not a completion. Nothing may be presented as finished
//     when it was cancelled.
//   - still running is still running. A cancellation is cooperative:
//     the operation is running until it has actually unwound. An
//     interface that says "stopped" while the request is still open
//     would be lying.
//
// The Manager is safe for concurrent use: the request path and the
// interface's own thread both touch it.
package lifecycle

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// State is the state of one operation.
type State string

const (
	// StateIdle means nothing has run yet.
	StateIdle State = "idle"
	// StateRunning means the work is in flight.
	StateRunning State = "running"
	// StateCompleted means the work finished.
	StateCompleted State = "completed"
	// StateStopped means the work was cancelled before it finished.
	StateStopped State = "stopped"
	// StateFailed means the work ended in an error.
	StateFailed State = "failed"
)

// Finished reports whether the operation has ended, one way or another.
//
// A finished operation is not a successful one: stopped and failed are
// finished too.
func (s State) Finished() bool {
	switch s {
	case StateCompleted, StateStopped, StateFailed:
		return true
	}
	return false
}

// Running reports whether the operation is still in flight.
//
// A cancellation does not change this: the work unwinds on its own
// terms, and until it has, it is still running.
func (s State) Running() bool {
	return s == StateRunning
}

// Operation is one unit of work with a lifecycle.
//
// The record carries no conversation text, no file contents and no
// credentials: it describes what happened, never what was said.
type Operation struct {
	// ID is the identity of the operation.
	ID string
	// Kind says what the operation is, for example "turn".
	Kind string
	// State is where the operation is.
	State State
	// Started is when the operation began.
	Started time.Time
	// Ended is when the operation finished. It is zero while the
	// operation is still running.
	Ended time.Time
	// Reason says why the operation stopped or failed. It is empty
	// for a completed operation.
	Reason string
	// Steps is the number of operations that ran inside this one.
	Steps int
	// Resumable reports that a checkpoint is waiting to be continued.
	Resumable bool
}

// Status renders the operation for the operator.
//
// The rendering names the state in words and never dresses a stopped
// operation up as a finished one.
func (o Operation) Status() string {
	if o.State == "" {
		return string(StateIdle)
	}
	var parts []string
	switch o.State {
	case StateRunning:
		parts = append(parts, "running")
	case StateCompleted:
		parts = append(parts, "completed")
	case StateStopped:
		parts = append(parts, "stopped")
	case StateFailed:
		parts = append(parts, "failed")
	default:
		parts = append(parts, string(o.State))
	}
	if o.Steps > 0 {
		parts = append(parts, fmt.Sprintf("%d operation(s)", o.Steps))
	}
	if o.Resumable {
		parts = append(parts, "can be continued")
	}
	if o.Reason != "" {
		parts = append(parts, o.Reason)
	}
	return strings.Join(parts, " · ")
}

// Manager owns the state of the operations an application runs.
//
// At most one operation is current at a time: HAL9001 runs one
// exchange at a time, and the interface asks about the current one and
// about the last finished one.
type Manager struct {
	mu      sync.Mutex
	seq     int
	current Operation
	last    Operation
	now     func() time.Time
}

// New returns a manager with no operation in it.
func New() *Manager {
	return &Manager{now: time.Now}
}

// Begin starts an operation and returns it.
//
// The operation is current until it is completed, stopped or failed.
func (m *Manager) Begin(kind string) Operation {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	op := Operation{
		ID:      fmt.Sprintf("%s-%d", kind, m.seq),
		Kind:    kind,
		State:   StateRunning,
		Started: m.now(),
	}
	m.current = op
	return op
}

// Steps records how many operations ran inside the current operation.
//
// It is the count of the work that was really done: the tool calls of
// a turn, for example. A count of zero means nothing ran inside.
func (m *Manager) Steps(id string, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current.ID == id {
		m.current.Steps = n
	}
}

// Complete records that an operation finished.
func (m *Manager) Complete(id string) {
	m.finish(id, StateCompleted, "")
}

// Stop records that an operation was cancelled before it finished.
//
// The reason says who asked for it and why, in the operator's terms.
func (m *Manager) Stop(id, reason string) {
	m.finish(id, StateStopped, reason)
}

// Fail records that an operation ended in an error.
func (m *Manager) Fail(id, reason string) {
	m.finish(id, StateFailed, reason)
}

// finish moves an operation out of the current slot and into the last
// one.
//
// An operation that is not current is left alone: a late report about
// a turn that was already replaced must not overwrite the state of the
// turn that is running now.
func (m *Manager) finish(id string, state State, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current.ID == "" || m.current.ID != id {
		return
	}
	m.current.State = state
	m.current.Reason = reason
	m.current.Ended = m.now()
	m.last = m.current
	m.current = Operation{}
}

// Current returns the operation that is in flight.
//
// The zero operation means nothing is running.
func (m *Manager) Current() Operation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// Last returns the operation that finished last.
//
// The zero operation means nothing has finished yet.
func (m *Manager) Last() Operation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

// Status renders the state of the operations for the operator.
//
// The current operation wins over the last one: while a turn is
// running, the operator is told that it is running.
func (m *Manager) Status() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current.ID != "" {
		return m.current.Status()
	}
	if m.last.ID != "" {
		return m.last.Status()
	}
	return string(StateIdle)
}
