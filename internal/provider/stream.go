package provider

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// Turn diagnostics and the safeguards around a single streamed turn.
//
// Everything here is content-free: counts, durations and token totals.
// Reasoning text, prompts, replies and credentials are never recorded.

// Usage is the token accounting a provider reports, when it reports one.
// Zero values mean "not reported"; HasUsage distinguishes that from a
// genuine zero.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
}

// TurnStats describes one streamed turn without carrying any of its text.
type TurnStats struct {
	// StartedAt is when the request began.
	StartedAt time.Time
	// Duration is how long the turn took.
	Duration time.Duration
	// Events counts SSE data events; KeepAlives counts comment lines;
	// Malformed counts data lines that were not valid JSON.
	Events     int
	KeepAlives int
	Malformed  int
	// VisibleChunks and VisibleRunes count user-facing content.
	VisibleChunks int
	VisibleRunes  int
	// ReasoningChunks and ReasoningRunes count reasoning events. The text
	// itself is never kept.
	ReasoningChunks int
	ReasoningRunes  int
	// FirstVisible and FirstReasoning are offsets from StartedAt; zero
	// means the turn never produced one.
	FirstVisible   time.Duration
	FirstReasoning time.Duration
	// MaxTokens, IdleTimeout and TurnTimeout are the limits that applied
	// to this turn, so diagnostics can show the safeguards in force.
	MaxTokens   int
	IdleTimeout time.Duration
	TurnTimeout time.Duration
	// FinishReason is the provider's finish_reason, when sent.
	FinishReason string
	// Usage is the reported token accounting.
	Usage    Usage
	HasUsage bool
	// Status is how the stream ended: done, finished, eof, cancelled,
	// idle, timeout or error.
	Status string
	// OutputLimitHit reports finish_reason "length": the provider stopped
	// generating because the output cap was reached.
	OutputLimitHit bool
}

// Stream status values.
const (
	StatusDone      = "done"      // [DONE] received
	StatusFinished  = "finished"  // finish_reason received, stream ended
	StatusEOF       = "eof"       // connection closed without [DONE]
	StatusCancelled = "cancelled" // the caller cancelled
	StatusIdle      = "idle"      // no data within the idle window
	StatusTimeout   = "timeout"   // the turn exceeded its total ceiling
	StatusError     = "error"     // transport or protocol error
)

// Errors the caller can classify without parsing strings.
var (
	// ErrStreamIdle reports that the provider stopped sending within the
	// configured idle window. The request has already been closed.
	ErrStreamIdle = errors.New("provider: no data within the idle timeout")
	// ErrTurnTimeout reports that a turn exceeded its total time ceiling.
	ErrTurnTimeout = errors.New("provider: turn exceeded the time limit")
)

// Defaults for the safeguards. They are deliberately generous: the point
// is to stop runaway or silent turns, not to cut normal long answers.
const (
	// DefaultMaxTokens caps one response when the model does not set its
	// own limit. Without it the provider default governs, which for a
	// reasoning model can be very large. It is deliberately generous: a
	// normal answer is far shorter, and the cap only bounds a runaway.
	DefaultMaxTokens = 16384
	// DefaultIdleTimeout aborts a stream that sends nothing for this long.
	DefaultIdleTimeout = 120 * time.Second
	// DefaultTurnTimeout bounds one whole turn.
	DefaultTurnTimeout = 15 * time.Minute
	// finishGrace is how long to keep reading after finish_reason for a
	// usage chunk or [DONE] before closing the stream.
	finishGrace = 3 * time.Second
)

func (o *OpenAICompatible) maxTokens() int {
	if o.DefaultMaxTokens > 0 {
		return o.DefaultMaxTokens
	}
	return DefaultMaxTokens
}

// effectiveMaxTokens is the cap actually sent for this model.
func (o *OpenAICompatible) effectiveMaxTokens(model Model) int {
	if model.MaxTokens > 0 {
		return model.MaxTokens
	}
	return o.maxTokens()
}

func (o *OpenAICompatible) idleTimeout() time.Duration {
	if o.IdleTimeout > 0 {
		return o.IdleTimeout
	}
	return DefaultIdleTimeout
}

func (o *OpenAICompatible) turnTimeout() time.Duration {
	if o.TurnTimeout > 0 {
		return o.TurnTimeout
	}
	return DefaultTurnTimeout
}

// turnContext bounds one turn with the total ceiling.
func (o *OpenAICompatible) turnContext(ctx context.Context) (context.Context, context.CancelFunc) {
	total := o.turnTimeout()
	if total <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, total)
}

// idleWatch cancels the request when no bytes arrive within d. Every read
// that returns data re-arms it, so a slow but active stream is never cut.
type idleWatch struct {
	timer  *time.Timer
	d      time.Duration
	cancel context.CancelFunc
	fired  atomic.Bool
}

func newIdleWatch(cancel context.CancelFunc, d time.Duration) *idleWatch {
	w := &idleWatch{d: d, cancel: cancel}
	w.timer = time.AfterFunc(d, func() {
		w.fired.Store(true)
		cancel()
	})
	return w
}

// touch re-arms the watchdog after activity.
func (w *idleWatch) touch() {
	if w == nil {
		return
	}
	w.timer.Reset(w.d)
}

// grace shortens the window once the provider has signalled completion:
// only a usage chunk or [DONE] is still expected.
func (w *idleWatch) grace() {
	if w == nil {
		return
	}
	w.d = finishGrace
	w.timer.Reset(finishGrace)
}

func (w *idleWatch) stop() {
	if w == nil {
		return
	}
	w.timer.Stop()
}

// watchedBody re-arms the idle watchdog whenever the provider sends data.
type watchedBody struct {
	body interface {
		Read([]byte) (int, error)
		Close() error
	}
	watch *idleWatch
}

func (w *watchedBody) Read(p []byte) (int, error) {
	n, err := w.body.Read(p)
	if n > 0 {
		w.watch.touch()
	}
	return n, err
}

func (w *watchedBody) Close() error { return w.body.Close() }

// streamStatus classifies how a stream ended.
func streamStatus(parent, turn context.Context, watch *idleWatch) string {
	switch {
	case parent.Err() == context.Canceled:
		return StatusCancelled
	case parent.Err() == context.DeadlineExceeded:
		return StatusTimeout
	case parent.Err() != nil:
		return StatusCancelled
	case watch != nil && watch.fired.Load():
		return StatusIdle
	case errors.Is(turn.Err(), context.DeadlineExceeded):
		return StatusTimeout
	case turn.Err() != nil:
		return StatusCancelled
	}
	return StatusError
}

// streamError builds the error for a failed stream, classified so the
// caller can tell a cancellation from a stall and a timeout.
func streamError(parent, turn context.Context, watch *idleWatch, readErr error) error {
	switch streamStatus(parent, turn, watch) {
	case StatusCancelled:
		return fmt.Errorf("provider: %w", parent.Err())
	case StatusIdle:
		return fmt.Errorf("%w (%s); the request was closed", ErrStreamIdle, watch.d)
	case StatusTimeout:
		return ErrTurnTimeout
	}
	return fmt.Errorf("provider: stream read: %w", readErr)
}
