package main

import (
	"context"
	"strings"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/config"
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
//
// The record of the active endpoint is the first place the limit is
// looked for: a model belongs to one endpoint, and the limit belongs to
// the model. The flat legacy lists are the fallback for a configuration
// that has not been migrated yet.
func (a *app) operatorContextLimit(id string) int {
	if id == "" {
		return 0
	}
	if endpoint, ok := a.stored.ActiveEndpoint(); ok {
		if record, ok := endpoint.Model(id); ok {
			if limit := operatorLimitFor(record); limit > 0 {
				return limit
			}
		}
	}
	for _, record := range a.cfg.Models {
		if record.ID == id {
			if limit := operatorLimitFor(record); limit > 0 {
				return limit
			}
		}
	}
	for _, record := range a.stored.Models {
		if record.ID == id {
			if limit := operatorLimitFor(record); limit > 0 {
				return limit
			}
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
// The endpoint's own protocol wins: a version-2 configuration knows
// which dialect each endpoint speaks. An unsupported name falls back to
// the OpenAI-compatible protocol, which is what the configuration has
// always meant.
func (a *app) wireProtocol() string {
	target, err := a.resolveTarget(a.stored)
	if err == nil && target.Wire != "" {
		return target.Wire
	}
	wire, err := provider.NormalizeWireProtocol(a.cfg.Provider)
	if err != nil {
		return provider.WireOpenAI
	}
	return wire
}

// targetSettings returns the identity, the address, the credential and
// the wire protocol of the active target.
//
// The credential is returned for the caller's own use and never appears
// in a message, a notice or a log.
func (a *app) targetSettings() (identity contextKey, endpoint, key, wire string) {
	target, err := a.resolveTarget(a.stored)
	if err != nil {
		return contextKey{}, "", "", ""
	}
	return contextKey{endpoint: target.EndpointID, model: target.ModelID},
		target.BaseURL, target.Credential, target.Wire
}

// refreshContextInBackground asks the endpoint what it knows about a
// model's context, without making the interface wait for the answer.
//
// The lookup is bounded, happens once per model and is never retried.
// An endpoint that says nothing leaves the budget provisional, which is
// the conservative default; the operator can always set a limit by
// hand.
//
// It runs in the background: the interface must not wait for a
// metadata lookup, and the budget it resolves is read again on the next
// request, so a late answer is still used. The endpoint settings are
// taken from the resolved target, because the stored configuration is
// written on the interface's own thread and reading it from the lookup
// would be a race.
//
// The answer belongs to one endpoint and one model, and to the
// configuration that endpoint had when the lookup started. An endpoint
// that changed while the lookup was running does not get the answer:
// it describes an address that is no longer in force.
func (a *app) refreshContextInBackground(modelID string) {
	identity, endpoint, key, wire := a.targetSettings()
	if endpoint == "" || wire == "" {
		return
	}
	if !a.discoveryAllowed() {
		return
	}
	identity.model = strings.TrimSpace(modelID)
	generation := a.contextGeneration(identity.endpoint)
	go a.refreshContext(context.Background(), identity, generation, endpoint, key, wire)
}

// discoveryAllowed reports whether the active endpoint's catalogue may
// be probed.
//
// The lookup of a model's context is a request to the operator's
// provider, so it is made only for an endpoint that allows it. The
// endpoint record answers for itself: its discovery flag says whether
// the provider's catalogue may be probed. A configuration that has no
// endpoint record has no flag, and nothing is probed.
func (a *app) discoveryAllowed() bool {
	endpoint, ok := a.stored.ActiveEndpoint()
	if !ok {
		return false
	}
	return endpoint.Discovery.Enabled
}

// contextKey identifies a model's context metadata.
//
// The endpoint is part of the identity, and it has to be: two endpoints
// may serve the same model identifier with very different windows, and
// one endpoint's advertisement must never become the other's. A model on
// endpoint B inherits nothing from endpoint A.
type contextKey struct {
	// endpoint is the endpoint the metadata belongs to.
	endpoint string
	// model is the model identifier inside that endpoint.
	model string
}

// recordContext stores the context metadata an endpoint reported for a
// model.
//
// The metadata is kept as it was reported, including where it came
// from: the resolution decides which number applies, and the provenance
// survives into the budget that is enforced.
//
// generation is the endpoint's configuration generation when the lookup
// started. A lookup that finishes after the endpoint changed does not
// store its answer: the answer describes a configuration that is no
// longer in force, and it would be worse than no answer at all.
//
// The map is written by the background lookup and read by the
// resolution, so it is guarded.
func (a *app) recordContext(key contextKey, generation int, meta provider.ContextMetadata) {
	key.model = strings.TrimSpace(key.model)
	if key.model == "" {
		return
	}
	a.contextMu.Lock()
	defer a.contextMu.Unlock()
	if a.contextEpoch[key.endpoint] != generation {
		// The endpoint changed while the lookup was running.
		return
	}
	if a.contexts == nil {
		a.contexts = map[contextKey]provider.ContextMetadata{}
	}
	a.contexts[key] = meta
}

// invalidateContext forgets what an endpoint reported.
//
// The endpoint's configuration changed — its address, its protocol, its
// credential — so what it said about its models belongs to a
// configuration that is no longer in force. The generation is raised
// as well, so a lookup that is still running does not store its
// answer either.
func (a *app) invalidateContext(endpointID string) {
	a.contextMu.Lock()
	defer a.contextMu.Unlock()
	if a.contextEpoch == nil {
		a.contextEpoch = map[string]int{}
	}
	a.contextEpoch[endpointID]++
	for key := range a.contexts {
		if key.endpoint == endpointID {
			delete(a.contexts, key)
		}
	}
}

// contextGeneration reports the current configuration generation of an
// endpoint.
//
// A lookup captures it before it starts and the answer is only stored
// when the generation is still the same.
func (a *app) contextGeneration(endpointID string) int {
	a.contextMu.Lock()
	defer a.contextMu.Unlock()
	return a.contextEpoch[endpointID]
}

// contextBudget resolves the effective context budget for a model of an
// endpoint.
//
// The order is the one the provider package documents: the operator's
// own limit first, then what a local runtime allocated, then what the
// endpoint advertised, then a record verified earlier, and finally the
// provisional record.
//
// The advertised maximum and the budget in force stay apart: the
// resolution keeps them in separate fields, and only the budget in
// force is enforced.
//
// The window is a TOTAL in every case: the room for the answer is
// reserved from it, and the request may use what is left. The
// provisional record is a conservative total too, so an endpoint that
// says nothing bounds the conversation instead of letting it grow
// without limit; it is never labelled verified, and the operator's own
// limit always wins over it.
//
// operatorLimit is passed in rather than looked up here. The request
// path runs off the interface's own thread, and the stored
// configuration is written on that thread when the operator saves the
// setup: the limit is read there, once, and the number travels with
// the request.
//
// allowance is the room the answer may use. It carries whether the
// operator configured that number: two configured numbers that
// contradict each other are refused, and a default is reduced
// conservatively.
func (a *app) contextBudget(key contextKey, operatorLimit int, allowance provider.OutputAllowance) (provider.ContextBudget, error) {
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
	if meta, ok := a.knownContext(key); ok {
		record := meta
		switch record.Source {
		case provider.ContextSourceLocal:
			inputs.Local = &record
		case provider.ContextSourceProvisional:
			// A provisional record is HAL's own conservative guess,
			// not something the endpoint reported. It must not take
			// the provider's slot: in that slot it would be read as
			// an advertisement — a verified fact about the model —
			// and the provisional policy would be lost with it.
			//
			// The resolution falls back to the provisional record on
			// its own, output cap and provenance included.
		default:
			inputs.Provider = &record
		}
	}
	resolved := provider.ResolveContext(inputs)
	return provider.BudgetFor(resolved, allowance)
}

// knownContext returns the context metadata an endpoint reported for a
// model, if it reported any.
//
// The metadata belongs to one endpoint and one model. A model on
// another endpoint is a different model, and it inherits nothing.
func (a *app) knownContext(key contextKey) (provider.ContextMetadata, bool) {
	a.contextMu.Lock()
	defer a.contextMu.Unlock()
	meta, ok := a.contexts[key]
	if !ok || meta.ContextWindow <= 0 {
		return provider.ContextMetadata{}, false
	}
	return meta, true
}

// budgetForActiveModel resolves the budget of the active model on the
// interface's own thread.
//
// It is the budget the diagnostics panel reports. A budget that cannot
// be resolved is reported as the reason it could not be resolved: the
// panel never shows a number that was guessed.
func (a *app) budgetForActiveModel() (provider.ContextBudget, error) {
	target, err := a.resolveTarget(a.stored)
	if err != nil {
		// The target cannot be resolved. The budget of the active
		// model is still reported, with the endpoint's own protocol,
		// so the operator sees what the numbers would be.
		id := a.activeModelID()
		key := contextKey{model: id}
		wire := provider.WireOpenAI
		limit := 0
		effort := ""
		if endpoint, ok := a.stored.ActiveEndpoint(); ok {
			if resolved, wireErr := endpoint.EffectiveWireProtocol(); wireErr == nil {
				wire = string(resolved)
			}
			if record, ok := endpoint.Model(id); ok {
				limit = operatorLimitFor(record)
				effort = record.Effort
			}
		}
		allowance, allowanceErr := a.outputAllowance(0, wire, id, effort)
		if allowanceErr != nil {
			return provider.ContextBudget{}, allowanceErr
		}
		return a.contextBudget(key, limit, allowance)
	}
	return target.Budget, nil
}

// currentBudget resolves the budget of a model on the request path.
//
// It is called once per request, so it stays cheap: the operator's own
// limit, which never changes, the recorded metadata, and nothing else.
// The model identifier, the endpoint it belongs to and the operator's
// limit are captured when the engine is built, on the interface's own
// thread: the request path runs off it, and the stored configuration is
// written on it.
func (a *app) currentBudget(key contextKey, operatorLimit int, allowance provider.OutputAllowance) (provider.ContextBudget, error) {
	return a.contextBudget(key, operatorLimit, allowance)
}

// contextBudgetDescription describes the budget that is enforced for the
// active model.
//
// It is content-free: the size of the budget, where it came from and
// how much of it is held back for the answer. It never names a model,
// a prompt or a reply.
//
// A budget that cannot be resolved is described by its reason, so the
// operator sees the contradiction instead of a number.
func (a *app) contextBudgetDescription() string {
	budget, err := a.budgetForActiveModel()
	if err != nil {
		return err.Error()
	}
	return provider.DescribeBudget(budget)
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
func (a *app) refreshContext(ctx context.Context, identity contextKey, generation int, endpoint, key, wire string) {
	identity.model = strings.TrimSpace(identity.model)
	if identity.model == "" {
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
	meta, err := discoverer.ContextFor(ctx, identity.model)
	if err != nil || meta.ContextWindow <= 0 {
		return
	}
	// The answer belongs to the endpoint and the model it was asked
	// about, and to the configuration that endpoint had when the
	// lookup started. A newer configuration does not get it.
	a.recordContext(identity, generation, meta)
}

// endpointCategory returns the category the operator declared for the
// endpoint.
//
// An endpoint named "local" is a local runtime: it may report the
// context it allocated, which is usually far smaller than the model's
// own window. Every other name is not a local endpoint, so no local
// runtime is probed.
func (a *app) endpointCategory() string {
	if endpoint, ok := a.stored.ActiveEndpoint(); ok {
		if endpoint.Protocol == config.ProtocolLocal {
			return "local"
		}
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(a.cfg.Provider), "local") {
		return "local"
	}
	return ""
}
