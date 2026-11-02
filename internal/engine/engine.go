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
	"strings"

	"github.com/flowbyte-com/HAL9001/internal/capability"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// ErrNoProvider reports that no model provider is configured. The
// conversation is still stored; nothing is fabricated in its place.
var ErrNoProvider = errors.New("no provider configured")

// Engine runs one conversation: it stores both sides of every turn and
// asks the provider for the next assistant message.
type Engine struct {
	sessions     *session.Store
	provider     provider.Provider
	model        provider.Model
	systemPrompt string
}

// New binds a session store to a provider and model.
func New(sessions *session.Store, p provider.Provider, model provider.Model) *Engine {
	return &Engine{sessions: sessions, provider: p, model: model}
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
	var resp provider.Response
	if streamer, ok := e.provider.(provider.Streamer); ok && emit != nil {
		resp, err = streamer.ChatStream(ctx, messages, e.model, func(d provider.Delta) error {
			ev := Event{Text: d.Text, Reasoning: d.Reasoning != ""}
			if ev.Text == "" && !ev.Reasoning {
				return nil
			}
			emit(ev)
			return nil
		})
	} else {
		resp, err = e.provider.Chat(ctx, messages, e.model)
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
	for _, call := range resp.ToolCalls {
		if err := capability.Check(call.Name); err != nil {
			denied := fmt.Sprintf("Requested operation %q refused: %s", call.Name, capability.Refusal())
			if reply == "" {
				reply = denied
			} else {
				reply += "\n\n" + denied
			}
			continue
		}
		// Allowed capabilities have no executors in this milestone: the
		// foundation establishes the boundary, not the integrations.
		pending := fmt.Sprintf("Operation %q is allowed but not yet implemented in this foundation.", call.Name)
		if reply == "" {
			reply = pending
		} else {
			reply += "\n\n" + pending
		}
	}
	if reply == "" {
		if resp.Reasoning != "" {
			// The provider spent its turn thinking and returned no
			// answer. Say so plainly; never surface the reasoning text.
			reply = "(the model produced reasoning but no answer)"
		} else {
			reply = "(no response)"
		}
	}
	if _, err := e.sessions.Append(session.RoleAssistant, reply); err != nil {
		return "", err
	}
	return reply, nil
}
