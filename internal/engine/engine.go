// Package engine is HAL9001's conversation core: session continuity plus a
// provider turn, with the 444 capability boundary enforced on every
// provider-requested operation. Presentation lives in internal/tui; this
// package has no terminal code. No implementation-capable tool is
// registered here: the engine knows text turns, and anything else the
// provider asks for is refused with the 444 refusal.
package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// ErrNoProvider reports that no model provider is configured. The
// conversation is still stored; nothing is fabricated in its place.
var ErrNoProvider = errors.New("no provider configured")

// ErrNoVisibleAnswer reports that the provider finished without any
// visible answer text — for example it exhausted the output budget while
// reasoning. Nothing is stored for the assistant side: a note is not an
// answer, and storing one would pollute the next request's context.
var ErrNoVisibleAnswer = errors.New("the model produced no visible answer")

// Engine runs one conversation: it stores both sides of every turn and
// asks the provider for the next assistant message.
type Engine struct {
	sessions     *session.Store
	provider     provider.Provider
	model        provider.Model
	systemPrompt string
	// workspace prepares write proposals. Nil means the turn offers no
	// write capability at all.
	workspace *workspace.Store
	// toolUnsupported records a provider that refused function
	// definitions, so the conversation continues without them.
	toolUnsupported bool
}

// New binds a session store to a provider and model.
func New(sessions *session.Store, p provider.Provider, model provider.Model) *Engine {
	return &Engine{sessions: sessions, provider: p, model: model}
}

// WithWorkspace enables the single write proposal: a model may ask to save
// one file, and only the interface can approve it. Without a workspace the
// engine offers no tools and every model-requested write is refused.
func (e *Engine) WithWorkspace(w *workspace.Store) *Engine {
	e.workspace = w
	return e
}

// WithSystemPrompt sets the system instruction prepended to every request.
// It is never stored in the session: history stays provider-independent
// and the instruction foundation stays byte-stable across turns.
func (e *Engine) WithSystemPrompt(prompt string) *Engine {
	e.systemPrompt = prompt
	return e
}

// UseStore swaps the conversation store (for example when starting a new
// conversation). History already written stays where it was.
func (e *Engine) UseStore(sessions *session.Store) {
	e.sessions = sessions
}

func (e *Engine) requestMessages(history []session.Message) []provider.Message {
	messages := make([]provider.Message, 0, len(history)+1)
	if e.systemPrompt != "" {
		messages = append(messages, provider.Message{Role: "system", Content: e.systemPrompt})
	}
	for _, msg := range history {
		messages = append(messages, provider.Message{
			Role:    string(msg.Role),
			Content: msg.Content,
		})
	}
	return messages
}

// Respond answers without streaming (kept for tests and non-interactive
// use). See RespondStream for the interactive path.
func (e *Engine) Respond(ctx context.Context, input string) (string, error) {
	return e.RespondStream(ctx, input, nil)
}

// Event is one streaming update from a turn.
//
// Text is user-facing content. Reasoning reports that the provider sent a
// reasoning event without exposing any of it: the private reasoning text
// never leaves the provider package. Stats is set on the final event.
type Event struct {
	Text      string
	Reasoning bool
	Stats     *provider.TurnStats
	// Proposal is one prepared workspace write, awaiting the operator's
	// decision. It carries no authority: the interface approves or
	// rejects it, and only then may the filesystem be touched.
	Proposal *workspace.Proposal
}

// RespondStream answers with live text deltas forwarded to emit (nil
// disables streaming and uses the non-streaming path).
func (e *Engine) RespondStream(ctx context.Context, input string, emit func(string)) (string, error) {
	if emit == nil {
		return e.RespondStreamEvents(ctx, input, nil)
	}
	return e.RespondStreamEvents(ctx, input, func(ev Event) {
		if ev.Text != "" {
			emit(ev.Text)
		}
	})
}

// RespondStreamEvents answers with live events: text for the answer,
// reasoning presence for progress, and content-free stats at the end.
// Cancellation aborts the provider call and stores nothing from the
// assistant side: an interrupted stream never becomes a complete answer
// in the session.
//
// When a workspace is configured, the turn also offers the single write
// proposal. A prepared proposal is emitted as an event and the interface
// decides what happens to it; the engine never writes anything itself.
func (e *Engine) RespondStreamEvents(ctx context.Context, input string, emit func(Event)) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("engine: empty input")
	}
	if _, err := e.sessions.Append(session.RoleUser, input); err != nil {
		return "", err
	}
	if e.provider == nil {
		return "", ErrNoProvider
	}
	history, err := e.sessions.History()
	if err != nil {
		return "", err
	}
	messages := e.requestMessages(history)
	turn := userTurns(history)
	tools := e.offeredTools()
	resp, err := e.chat(ctx, messages, tools, emit)
	if err != nil && len(tools) > 0 && isToolRejection(err) {
		// A provider that cannot take function definitions must not lose
		// the conversation: retry the same turn without them and stop
		// offering tools for this engine.
		e.toolUnsupported = true
		resp, err = e.chat(ctx, messages, nil, emit)
	}
	if err != nil {
		// A failed turn still reports its content-free diagnostics: the
		// elapsed time and the event counts are how a stall is explained.
		if emit != nil && !resp.Stats.StartedAt.IsZero() {
			stats := resp.Stats
			emit(Event{Stats: &stats})
		}
		return "", err
	}
	if emit != nil {
		stats := resp.Stats
		if stats.Status == "" {
			stats.Status = provider.StatusFinished
		}
		emit(Event{Stats: &stats})
	}
	reply := strings.TrimSpace(resp.Content)
	notes, proposal := e.handleToolCalls(resp.ToolCalls, e.sessionName(), turn)
	if notes != "" {
		if reply == "" {
			reply = notes
		} else {
			reply += "\n\n" + notes
		}
	}
	if reply == "" {
		// The provider completed without visible answer text. Report it
		// honestly and store nothing: an explanation is not an assistant
		// message, and silently retrying or raising the cap would hide
		// the cost instead of explaining it.
		finish := resp.FinishReason
		if finish == "" {
			finish = "unknown"
		}
		if resp.Reasoning != "" {
			return "", fmt.Errorf("%w: the output budget was spent on reasoning (finish_reason %s)", ErrNoVisibleAnswer, finish)
		}
		return "", fmt.Errorf("%w (finish_reason %s)", ErrNoVisibleAnswer, finish)
	}
	if _, err := e.sessions.Append(session.RoleAssistant, reply); err != nil {
		return "", err
	}
	if proposal != nil && emit != nil {
		// The last event of the turn: one prepared write for the operator
		// to review. It carries no authority, only a request.
		emit(Event{Proposal: proposal})
	}
	return reply, nil
}

// chat runs one provider turn, offering tools when there are any.
//
// The streaming path is preferred; a provider that cannot stream with
// functions falls back to the ordinary stream, and a provider that cannot
// stream at all uses the blocking call.
func (e *Engine) chat(ctx context.Context, messages []provider.Message, tools []provider.Tool, emit func(Event)) (provider.Response, error) {
	deltaFn := func(d provider.Delta) error {
		ev := Event{Text: d.Text, Reasoning: d.Reasoning != ""}
		if ev.Text == "" && !ev.Reasoning {
			return nil
		}
		if emit != nil {
			emit(ev)
		}
		return nil
	}
	if len(tools) > 0 {
		if streamer, ok := e.provider.(provider.ToolStreamer); ok {
			return streamer.ChatStreamTools(ctx, messages, e.model, tools, deltaFn)
		}
	}
	if streamer, ok := e.provider.(provider.Streamer); ok && emit != nil {
		return streamer.ChatStream(ctx, messages, e.model, deltaFn)
	}
	return e.provider.Chat(ctx, messages, e.model)
}

// userTurns counts the user messages in the history: the turn number the
// review and the audit refer to, stable across restarts.
func userTurns(history []session.Message) int {
	n := 0
	for _, m := range history {
		if m.Role == session.RoleUser {
			n++
		}
	}
	return n
}

// sessionName is the stored conversation's identifier, derived from the
// store's own path. It never leaves the application.
func (e *Engine) sessionName() string {
	if e.sessions == nil {
		return ""
	}
	base := filepath.Base(e.sessions.Path())
	return strings.TrimSuffix(base, ".jsonl")
}
