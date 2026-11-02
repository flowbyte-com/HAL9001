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

	"github.com/sipeed/picoclaw/hal9001/internal/capability"
	"github.com/sipeed/picoclaw/hal9001/internal/provider"
	"github.com/sipeed/picoclaw/hal9001/internal/session"
)

// ErrNoProvider reports that no model provider is configured. The
// conversation is still stored; nothing is fabricated in its place.
var ErrNoProvider = errors.New("no provider configured")

// Engine runs one conversation: it stores both sides of every turn and
// asks the provider for the next assistant message.
type Engine struct {
	sessions *session.Store
	provider provider.Provider
	model    provider.Model
}

// New binds a session store to a provider and model.
func New(sessions *session.Store, p provider.Provider, model provider.Model) *Engine {
	return &Engine{sessions: sessions, provider: p, model: model}
}

// Respond records the user message, asks the provider, enforces the 444
// boundary on any requested operations, records the reply, and returns it.
// Provider-requested tool calls are denied unless explicitly allowed; the
// denial itself becomes the reply content so the refusal is visible in the
// conversation instead of failing silently.
func (e *Engine) Respond(ctx context.Context, input string) (string, error) {
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
	messages := make([]provider.Message, 0, len(history))
	for _, msg := range history {
		messages = append(messages, provider.Message{
			Role:    string(msg.Role),
			Content: msg.Content,
		})
	}
	resp, err := e.provider.Chat(ctx, messages, e.model)
	if err != nil {
		return "", fmt.Errorf("engine: %w", err)
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
		reply = "(no response)"
	}
	if _, err := e.sessions.Append(session.RoleAssistant, reply); err != nil {
		return "", err
	}
	return reply, nil
}
