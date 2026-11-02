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
	"fmt"
	"strings"
)

// Message is one turn of provider input.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ToolCall is a provider-requested operation. The engine enforces the 444
// capability boundary on every call; unlisted operations are refused.
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Response is one provider turn.
type Response struct {
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	FinishReason string     `json:"finish_reason,omitempty"`
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
	// Thinking is an optional per-model reasoning control. Empty means
	// "provider default". Adapters translate it only when the API has a
	// matching parameter; otherwise they leave it unset.
	Thinking string `json:"thinking,omitempty"`
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
