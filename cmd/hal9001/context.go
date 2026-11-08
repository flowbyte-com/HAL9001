package main

import (
	"context"
	"strings"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/provider"
)

// The resolved context budget.
//
// The engine owns the enforcement: it fits the fully assembled request
// into the budget immediately before every provider call. The
// application owns the resolution: it collects what is known about the
// active model and hands the result to the engine on the model
// carrier.
//
// Nothing here probes an endpoint on its own initiative. A discovery
// flow records what an endpoint said (recordContext); the resolution
// then decides which of the known numbers applies. A lookup that never
// happened leaves the budget provisional, which is the conservative
// default, and a lookup that fails is never retried.

// contextLookupTimeout bounds one context lookup.
//
// The lookup is a single bounded request to an endpoint the operator
// already configured. It is not a probe loop: HAL never generates
// enormous requests to measure a context length, and it never retries.
const contextLookupTimeout = 3 * time.Second

// operatorContextLimit returns the context limit the operator set for a
// model, in tokens.
//
// Zero means the operator set none, and the provider's own window
// governs.
func (a *app) operatorContextLimit(id string) int {
	if id == "" {
		return 0
	}
	for _, record := range a.cfg.Models {
		if record.ID == id && record.ContextWindow > 0 {
			return record.ContextWindow
		}
	}
	for _, record := range a.stored.Models {
		if record.ID == id && record.ContextWindow > 0 {
			return record.ContextWindow
		}
	}
	return 0
}

// outputTokens returns the answer cap HAL asks for, in tokens.
//
// The configured cap wins; without one the protocol's own default is
// used, because the two protocols default differently.
func (a *app) outputTokens() int {
	if a.cfg.MaxTokens > 0 {
		return a.cfg.MaxTokens
	}
	return provider.DefaultOutputTokens(a.wireProtocol())
}

// wireProtocol returns the wire protocol HAL speaks to the configured
// endpoint.
//
// An unsupported name falls back to the OpenAI-compatible protocol,
// which is what the configuration has always meant.
func (a *app) wireProtocol() string {
	wire, err := provider.NormalizeWireProtocol(a.cfg.Provider)
	if err != nil {
		return provider.WireOpenAI
	}
	return wire
}

// endpointSettings returns the endpoint, the credential and the wire
// protocol HAL speaks to.
//
// The credential is returned for the caller's own use and never appears
// in a message, a notice or a log.
func (a *app) endpointSettings() (endpoint, key, wire string) {
	endpoint, key = a.cfg.Endpoint, a.cfg.APIKey
	if endpoint == "" {
		endpoint = a.stored.Endpoint
	}
	if key == "" {
		key = a.stored.APIKey
	}
	normalized, err := provider.NormalizeWireProtocol(a.cfg.Provider)
	if err != nil {
		// An unsupported protocol name leaves the wire empty: the
		// caller decides what to do about it.
		return endpoint, key, ""
	}
	return endpoint, key, normalized
}

// recordContext stores the context metadata an endpoint reported for a
// model.
//
// The metadata is kept as it was reported, including where it came
// from: the resolution decides which number applies, and the provenance
// survives into the budget that is enforced.
//
// The map is written by the background lookup and read by the
// resolution, so it is guarded.
func (a *app) recordContext(modelID string, meta provider.ContextMetadata) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return
	}
	a.contextMu.Lock()
	defer a.contextMu.Unlock()
	if a.contexts == nil {
		a.contexts = map[string]provider.ContextMetadata{}
	}
	a.contexts[modelID] = meta
}

// contextBudget resolves the effective context budget for a model.
//
// The order is the one the provider package documents: the operator's
// own limit first, then what a local runtime allocated, then what the
// endpoint advertised, then a record verified earlier, and finally the
// provisional record.
//
// The provisional record is reported but not enforced: nothing is known
// about the window, and a guess must not discard the operator's
// context.
//
// The advertised maximum and the budget in force stay apart: the
// resolution keeps them in separate fields, and only the budget in
// force is enforced.
//
// operatorLimit is passed in rather than looked up here. The request
// path runs off the interface's own thread, and the stored
// configuration is written on that thread when the operator saves the
// setup: the limit is read there, once, and the number travels with
// the request.
func (a *app) contextBudget(id string, operatorLimit int) provider.ContextBudget {
	var inputs provider.ContextInputs
	if operatorLimit > 0 {
		inputs.Operator = &provider.ContextMetadata{
			ContextWindow: operatorLimit,
			Source:        provider.ContextSourceOperator,
			Mode:          provider.ContextModeConfigured,
			Verified:      true,
			Note:          "the limit the operator set for this model",
		}
	}
	if meta, ok := a.knownContext(id); ok {
		record := meta
		switch record.Source {
		case provider.ContextSourceLocal:
			inputs.Local = &record
		default:
			inputs.Provider = &record
		}
	}
	resolved := provider.ResolveContext(inputs)
	return provider.BudgetFor(resolved, a.outputTokens())
}

// knownContext returns the context metadata an endpoint reported for a
// model, if it reported any.
func (a *app) knownContext(id string) (provider.ContextMetadata, bool) {
	a.contextMu.Lock()
	defer a.contextMu.Unlock()
	meta, ok := a.contexts[id]
	if !ok || meta.ContextWindow <= 0 {
		return provider.ContextMetadata{}, false
	}
	return meta, true
}

// budgetForActiveModel resolves the budget of the active model on the
// interface's own thread.
//
// It is the budget the diagnostics panel reports.
func (a *app) budgetForActiveModel() provider.ContextBudget {
	id := a.activeModelID()
	return a.contextBudget(id, a.operatorContextLimit(id))
}

// currentBudget resolves the budget of a model on the request path.
//
// It is called once per request, so it stays cheap: the operator's own
// limit, which never changes, the recorded metadata, and nothing else.
// The model identifier and the operator's limit are captured when the
// engine is built, on the interface's own thread: the request path
// runs off it, and the stored configuration is written on it.
func (a *app) currentBudget(id string, operatorLimit int) provider.ContextBudget {
	return a.contextBudget(id, operatorLimit)
}

// contextBudgetDescription describes the budget that is enforced for the
// active model.
//
// It is content-free: the size of the budget, where it came from and
// how much of it is held back for the answer. It never names a model,
// a prompt or a reply.
func (a *app) contextBudgetDescription() string {
	return provider.DescribeBudget(a.budgetForActiveModel())
}

// refreshContext asks the endpoint what it knows about a model's
// context and records the answer.
//
// The lookup is bounded, happens once per model and is never retried.
// An endpoint that says nothing leaves the budget provisional, which is
// the conservative default; the operator can always set a limit by
// hand.
//
// It runs in the background: the interface must not wait for a
// metadata lookup, and the budget it resolves is read again on the next
// request, so a late answer is still used. The endpoint settings are
// passed in because the stored configuration is written on the
// interface's own thread, and reading it from the lookup would be a
// race.
func (a *app) refreshContext(ctx context.Context, modelID, endpoint, key, wire string) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return
	}
	if endpoint == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, contextLookupTimeout)
	defer cancel()
	discoverer, err := provider.NewDiscoverer(provider.Endpoint{
		Wire:       wire,
		Category:   a.endpointCategory(),
		BaseURL:    endpoint,
		Credential: key,
	}, provider.Options{
		DefaultMaxTokens: a.cfg.MaxTokens,
		IdleTimeout:      seconds(a.cfg.StreamIdleSeconds),
		TurnTimeout:      seconds(a.cfg.TurnTimeoutSeconds),
	})
	if err != nil {
		return
	}
	meta, err := discoverer.ContextFor(ctx, modelID)
	if err != nil || meta.ContextWindow <= 0 {
		return
	}
	a.recordContext(modelID, meta)
}

// endpointCategory returns the category the operator declared for the
// endpoint.
//
// An endpoint named "local" is a local runtime: it may report the
// context it allocated, which is usually far smaller than the model's
// own window. Every other name is not a local endpoint, so no local
// runtime is probed.
func (a *app) endpointCategory() string {
	if strings.EqualFold(strings.TrimSpace(a.cfg.Provider), "local") {
		return "local"
	}
	return ""
}
