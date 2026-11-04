// Package provider defines HAL9001's model-provider contracts. Onboarding is
// deliberately simple, inspired by MPM: an API key, an endpoint, and a model
// identifier. The conversation engine talks only to these interfaces, so the
// discussion stays model-independent and the operator can switch providers
// without discarding history.
//
// This milestone establishes the interfaces and configuration contracts.
// Only a mock provider ships with the foundation; real provider adapters
// arrive with the provider milestone. Thinking controls are per-model
// capabilities declared by each adapter — never invented globals.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Message is one turn of provider input.
//
// ToolCalls is set on an assistant message that requested functions, and
// ToolCallID is set on the tool message that carries their results. The
// conversation store keeps only visible text; these fields exist inside a
// single turn's provider transcript so a multi-step tool sequence can be
// resolved without inventing a second history format.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a provider-requested operation. The engine enforces the 444
// capability boundary on every call; unlisted operations are refused.
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ArgumentsJSON returns the arguments in the wire form the provider expects.
// An unparseable argument payload is preserved verbatim so the executor can
// refuse it instead of silently turning it into an empty object.
func (c ToolCall) ArgumentsJSON() string {
	if raw, ok := c.Arguments["_invalid"].(string); ok {
		return raw
	}
	if c.Arguments == nil {
		return "{}"
	}
	b, err := json.Marshal(c.Arguments)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Tool is one function offered to the model. A tool call carries no
// authority: the engine decides whether the operation may be prepared, and
// the permission gate is checked again at the operation itself.
type Tool struct {
	Name        string
	Description string
	// Parameters is the JSON schema of the arguments.
	Parameters string
}

// Response is one provider turn. Reasoning carries provider thinking
// metadata when the API returns it (for example a reasoning_content
// field). It is never displayed as the answer and never stored as
// conversation content: user-facing text comes only from Content.
type Response struct {
	Content      string     `json:"content"`
	Reasoning    string     `json:"reasoning,omitempty"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	FinishReason string     `json:"finish_reason,omitempty"`
	// Stats is content-free telemetry for the turn: counts, durations and
	// token totals. It never contains prompt or reply text.
	Stats TurnStats `json:"-"`
}

// Provider is the minimal contract every model adapter implements.
type Provider interface {
	// Name identifies the adapter (e.g. "openai-compatible", "mock").
	Name() string
	// Chat sends the conversation and returns the model's turn.
	Chat(ctx context.Context, messages []Message, model Model) (Response, error)
}

// Model describes one configured model. Thinking controls live here per
// model; adapters must only send parameters their API actually supports.
type Model struct {
	// ID is the provider-side model identifier.
	ID string `json:"id"`
	// Effort is the reasoning level for models whose API accepts
	// reasoning_effort (see EffortCapable). Empty means the adapter
	// default, or no effort control at all.
	Effort string `json:"effort,omitempty"`
	// MaxTokens caps the response length. Zero means provider default.
	MaxTokens int `json:"max_tokens,omitempty"`
}

// Config binds credentials to models. Secrets are never stored here: the
// key comes from the environment at call time.
type Config struct {
	// Provider selects the adapter (e.g. "openai-compatible").
	Provider string `json:"provider"`
	// Endpoint is the API base URL.
	Endpoint string `json:"endpoint"`
	// APIKeyEnv names the environment variable holding the key.
	APIKeyEnv string `json:"api_key_env"`
	// Models available under this provider.
	Models []Model `json:"models"`
}

// Validate checks the contract without touching credentials or network.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Provider) == "" {
		return fmt.Errorf("provider: missing provider name")
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return fmt.Errorf("provider: missing endpoint")
	}
	if strings.TrimSpace(c.APIKeyEnv) == "" {
		return fmt.Errorf("provider: missing api_key_env")
	}
	if len(c.Models) == 0 {
		return fmt.Errorf("provider: at least one model is required")
	}
	for i, m := range c.Models {
		if strings.TrimSpace(m.ID) == "" {
			return fmt.Errorf("provider: models[%d] is missing its id", i)
		}
		if m.MaxTokens < 0 {
			return fmt.Errorf("provider: models[%d] has a negative max_tokens", i)
		}
	}
	return nil
}

// MockProvider is a scripted test double. It is not a model and never
// leaves the test process.
type MockProvider struct {
	Replies []Response
	Calls   int
}

// Name implements Provider.
func (m *MockProvider) Name() string { return "mock" }

// Chat implements Provider, replaying scripted replies.
func (m *MockProvider) Chat(_ context.Context, _ []Message, _ Model) (Response, error) {
	m.Calls++
	if len(m.Replies) == 0 {
		return Response{Content: "mock: script exhausted"}, nil
	}
	resp := m.Replies[0]
	if len(m.Replies) > 1 {
		m.Replies = m.Replies[1:]
	}
	return resp, nil
}
