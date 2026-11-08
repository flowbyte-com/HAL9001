package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// The wire protocols HAL9001 actually speaks.
//
// A configuration endpoint names one of them. HAL never pretends that an
// arbitrary API is compatible: an endpoint whose wire protocol is not
// listed here is refused when it is selected, and the refusal names the
// protocols that are supported.
const (
	// WireOpenAI is the OpenAI chat-completions dialect.
	WireOpenAI = "openai-compatible"
	// WireAnthropic is the Anthropic messages dialect.
	WireAnthropic = "anthropic-compatible"
)

// Options are the runtime safeguards shared by the adapters.
type Options struct {
	// DefaultMaxTokens caps one response when the model sets no limit.
	DefaultMaxTokens int
	// IdleTimeout aborts a stream that sends nothing for this long.
	IdleTimeout time.Duration
	// TurnTimeout bounds one whole turn.
	TurnTimeout time.Duration
	// Client is the HTTP client. Nil lets the adapter build one.
	Client *http.Client
}

// NewForProtocol builds the adapter that speaks a wire protocol.
//
// The endpoint is the API base URL and the key is the credential. Neither
// is ever logged or reported back: a failure names the protocol and the
// problem, never the secret.
func NewForProtocol(wire, endpoint, apiKey string, opts Options) (Provider, error) {
	switch wire {
	case "", WireOpenAI:
		return &OpenAICompatible{
			Endpoint:         endpoint,
			APIKey:           apiKey,
			Client:           opts.Client,
			DefaultMaxTokens: opts.DefaultMaxTokens,
			IdleTimeout:      opts.IdleTimeout,
			TurnTimeout:      opts.TurnTimeout,
		}, nil
	case WireAnthropic:
		return &AnthropicCompatible{
			Endpoint:         endpoint,
			APIKey:           apiKey,
			Client:           opts.Client,
			DefaultMaxTokens: opts.DefaultMaxTokens,
			IdleTimeout:      opts.IdleTimeout,
			TurnTimeout:      opts.TurnTimeout,
		}, nil
	default:
		return nil, fmt.Errorf("provider: unsupported wire protocol %q (supported: %s, %s)",
			wire, WireOpenAI, WireAnthropic)
	}
}

// ModelLister is implemented by adapters that can list the provider's
// catalogue of model identifiers.
//
// The catalogue is used for discovery: it confirms which identifiers a
// provider really serves. It never adds a model on its own, and it never
// moves a model between endpoints.
type ModelLister interface {
	Models(ctx context.Context) ([]string, error)
}

// SupportedWireProtocols lists the wire protocols HAL speaks, for a
// message that has to name them.
func SupportedWireProtocols() []string {
	return []string{WireOpenAI, WireAnthropic}
}
