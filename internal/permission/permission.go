// Package permission owns HAL9001's session-scoped execution permission.
//
// The default is 444: guarded, read-only authority. 666 is an explicit,
// operator-controlled exception for one conversation. Nothing in a model
// response, tool result, skill document, MPM record or stored transcript can
// reach the controller's Enable method: the interface calls it only after a
// deliberate operator chord.
//
// The engine receives the narrow Gate interface, never the controller. That
// keeps the mode switch out of the tool surface and makes the execution
// boundary a fresh check rather than a decision made when tools were offered.
package permission

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Mode is the authority a conversation currently has.
type Mode int

const (
	// Guarded is HAL's default. The model may converse and use explicitly
	// safe knowledge tools, but it cannot run commands or edit OS files.
	Guarded Mode = 444
	// Unrestricted is the operator-enabled 666 mode. Its tools run with the
	// permissions of the OS user running HAL.
	Unrestricted Mode = 666
)

// String returns the numeric label shown in the interface.
func (m Mode) String() string {
	if m == Unrestricted {
		return "666"
	}
	return "444"
}

// ErrDisabled reports that a privileged operation was requested while the
// conversation was not in 666.
var ErrDisabled = errors.New("permission: privileged operation requires 666")

// ErrSessionChanged reports that the mode was enabled for another conversation.
var ErrSessionChanged = errors.New("permission: the conversation changed")

// ErrNoSession reports that 666 was requested before a conversation was
// bound. A mode without a session would not be session-scoped.
var ErrNoSession = errors.New("permission: no conversation is bound")

// Gate is the narrow, read-only view the engine and tool executors receive.
// It cannot enable or disable anything.
type Gate interface {
	// Mode returns the current mode. It is a snapshot: every privileged
	// operation must still call Begin at its execution boundary.
	Mode() Mode
	// Session returns the conversation the current mode belongs to.
	Session() string
	// Begin rechecks the permission and returns a context that is cancelled
	// when 666 is revoked. The returned function must be called when the
	// operation ends. A non-nil error means the operation must not run.
	Begin(ctx context.Context, operation string) (context.Context, func(), error)
}

// Controller is the trusted, operator-facing permission authority. The
// interface holds it and the engine only ever receives it as a Gate.
type Controller struct {
	mu      sync.Mutex
	mode    Mode
	session string
	next    uint64
	active  map[uint64]context.CancelFunc
}

// New returns a controller in the safe 444 state.
func New() *Controller {
	return &Controller{mode: Guarded, active: make(map[uint64]context.CancelFunc)}
}

// Mode implements Gate.
func (c *Controller) Mode() Mode {
	if c == nil {
		return Guarded
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

// Session implements Gate.
func (c *Controller) Session() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// SetSession binds the mode to a conversation. A change always returns to
// 444, including when the controller was already 444: the new conversation
// must never inherit an old window, even one that was disabled a moment ago.
func (c *Controller) SetSession(session string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == session {
		return
	}
	c.session = session
	c.resetLocked()
}

// Enable switches one conversation to 666. The operation is refused when
// the controller has no bound conversation or when another conversation is
// bound. The interface calls this only from the permission overlay after a
// deliberate operator key.
func (c *Controller) Enable(session string) error {
	if c == nil {
		return ErrDisabled
	}
	if session == "" {
		return ErrNoSession
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != "" && c.session != session {
		return ErrSessionChanged
	}
	c.session = session
	c.mode = Unrestricted
	// A new enable starts a new authority generation. No operation from a
	// previous window can continue under the new one.
	c.cancelActiveLocked()
	return nil
}

// Disable returns to 444 and cancels every operation that began under 666.
// It is idempotent. The reason is deliberately not stored: the mode is
// never persisted anywhere.
func (c *Controller) Disable() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked()
}

// Active reports how many privileged operations are currently running.
func (c *Controller) Active() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.active)
}

func (c *Controller) resetLocked() {
	c.mode = Guarded
	c.cancelActiveLocked()
}

func (c *Controller) cancelActiveLocked() {
	for id, cancel := range c.active {
		cancel()
		delete(c.active, id)
	}
}

// Begin implements Gate. The permission check and the registration of the
// operation's cancellation happen under the same lock, so a Disable cannot
// slip between "allowed" and "running".
func (c *Controller) Begin(ctx context.Context, operation string) (context.Context, func(), error) {
	if c == nil {
		return ctx, func() {}, ErrDisabled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.mode != Unrestricted {
		c.mu.Unlock()
		return ctx, func() {}, fmt.Errorf("%w: %s", ErrDisabled, operation)
	}
	opCtx, cancel := context.WithCancel(ctx)
	c.next++
	id := c.next
	if c.active == nil {
		c.active = make(map[uint64]context.CancelFunc)
	}
	c.active[id] = cancel
	c.mu.Unlock()

	var once sync.Once
	done := func() {
		once.Do(func() {
			cancel()
			c.mu.Lock()
			delete(c.active, id)
			c.mu.Unlock()
		})
	}
	return opCtx, done, nil
}

// Valid reports whether a context returned by Begin is still allowed to
// perform a non-cancellable final step, such as a file rename. It does not
// replace Begin: callers must have begun the operation first.
func (c *Controller) Valid(ctx context.Context) bool {
	if c == nil || ctx == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode == Unrestricted && ctx.Err() == nil
}
