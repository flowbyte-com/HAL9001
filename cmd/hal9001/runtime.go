package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/provider"
)

// The runtime target.
//
// A provider request needs four things at once: the endpoint, the wire
// protocol, the credential and the model with its settings. They are
// resolved together, in one place, from one configuration. A request can
// therefore never carry the endpoint of one configuration and the
// credential of another, and a change of endpoint takes effect as one
// coherent change.

// providerTarget is everything one provider request needs.
type providerTarget struct {
	// EndpointID is the endpoint the request goes to.
	EndpointID string
	// BaseURL is the endpoint's address.
	BaseURL string
	// Wire is the wire protocol HAL speaks to the endpoint.
	Wire string
	// Credential is the secret the endpoint uses.
	Credential string
	// ModelID is the model the request asks for.
	ModelID string
	// Effort is the reasoning level of that model.
	Effort string
	// MaxTokens is the model's output limit. Zero means the provider's
	// own default.
	MaxTokens int
	// ContextMode is "auto" or "manual".
	ContextMode string
	// ContextWindow is the operator's context limit in tokens. It is
	// enforced only in manual mode.
	ContextWindow int
	// Budget is the effective context budget: the room the request may
	// use, the room held back for the answer, and the provenance of the
	// number that was chosen.
	//
	// It is resolved from every known source — the operator's own
	// limit, the provider's advertisement, the local runtime's
	// allocation, a record verified earlier and the conservative
	// fallback — and it is what the engine enforces. The operator's
	// ContextWindow is the input to that resolution, never a second,
	// competing budget.
	Budget provider.ContextBudget
}

// resolveTarget derives the runtime target from a stored configuration.
//
// The stored configuration is the source of truth: an endpoint's own
// credential and its own models always describe that endpoint. The
// operator's flags and the environment override it only where the
// operator configured nothing explicitly.
//
// A configuration that names an endpoint which does not exist is an
// error, never a fallback to the previous endpoint: a deleted endpoint
// must not come back through a stale mirror.
func (a *app) resolveTarget(stored config.FileConfig) (providerTarget, error) {
	target := providerTarget{}
	endpointOverride, modelOverride, providerOverride := a.overrides()

	if stored.Version >= config.ConfigVersion {
		// A version-2 configuration: the endpoints are the only source
		// of truth. The legacy fields are a mirror and never a
		// fallback, so a removed endpoint cannot come back through
		// them.
		endpoint, ok := stored.ActiveEndpoint()
		if !ok {
			if stored.Selection != nil {
				// The selection names an endpoint that is not there.
				// That is a broken configuration, and it is refused:
				// the legacy fields are never a fallback, so a
				// removed endpoint cannot come back through them.
				return target, fmt.Errorf("the active endpoint %q no longer exists", stored.ActiveEndpointID())
			}
			// No endpoint is selected at all. That is a valid
			// state: the operator removed the last endpoint. The
			// conversation runs without a provider and the interface
			// says so. An explicit override still applies below.
			target.Wire = provider.WireOpenAI
		} else {
			wire, err := endpoint.EffectiveWireProtocol()
			if err != nil {
				return target, err
			}
			target.EndpointID = endpoint.ID
			target.BaseURL = endpoint.BaseURL
			target.Wire = string(wire)
			target.Credential = endpoint.CredentialValue()
			model, _ := stored.ActiveModelRecord()
			target.ModelID = model.ID
			target.Effort = effortForRecord(model)
			target.MaxTokens = model.MaxOutputTokens
			target.ContextMode = model.ContextMode
			target.ContextWindow = operatorLimitFor(model)
		}
	} else {
		// A pre-version-2 configuration: one global endpoint, one
		// credential and a flat model list.
		target.BaseURL = a.cfg.Endpoint
		target.Credential = a.cfg.APIKey
		target.Wire = provider.WireOpenAI
		target.ModelID = a.cfg.Model
		if record, ok := legacyModelRecord(a, stored, target.ModelID); ok {
			target.ModelID = record.ID
			target.Effort = effortForRecord(record)
			// The legacy record carries the operator's own limits
			// too. They are the same limits the version-2 record
			// carries, and they survive the migration: a legacy
			// model with an explicit context window or output cap
			// keeps it when the runtime is resolved.
			target.MaxTokens = record.MaxOutputTokens
			target.ContextMode = record.ContextMode
			target.ContextWindow = operatorLimitFor(record)
		}
	}

	if endpointOverride != "" {
		target.BaseURL = endpointOverride
	}
	if modelOverride != "" {
		// The override names a model, and a model carries its own
		// settings.
		target.ModelID = modelOverride
		if record, ok := stored.ModelFor(target.EndpointID, modelOverride); ok {
			target.Effort = effortForRecord(record)
			target.MaxTokens = record.MaxOutputTokens
			target.ContextMode = record.ContextMode
			target.ContextWindow = operatorLimitFor(record)
		} else {
			target.Effort = ""
			target.MaxTokens = 0
			target.ContextMode = ""
			target.ContextWindow = 0
		}
	}
	if providerOverride != "" {
		wire, err := wireProtocolFor(providerOverride)
		if err != nil {
			return target, err
		}
		target.Wire = wire
	}
	if target.Wire == "" {
		target.Wire = provider.WireOpenAI
	}
	// The budget is resolved last, because it depends on everything the
	// target carries: the operator's own limit, the model identifier
	// the endpoint may have reported a window for, and the room the
	// answer may use.
	target.Budget = a.contextBudget(target.ModelID, target.ContextWindow, a.reservedOutput(target))
	return target, nil
}

// reservedOutput returns the room the answer may use.
//
// The model's own cap wins; without one the configured cap is used;
// without that the protocol's default, because the two protocols default
// differently.
//
// It is the same number the adapter asks the provider for, so the room
// that is held back from the request is the room the answer really
// takes.
func (a *app) reservedOutput(target providerTarget) int {
	if target.MaxTokens > 0 {
		return target.MaxTokens
	}
	if a.cfg.MaxTokens > 0 {
		return a.cfg.MaxTokens
	}
	return provider.DefaultOutputTokens(target.Wire)
}

// legacyModelRecord finds the record of a model in a pre-version-2
// configuration.
//
// The stored file comes first: it is the file the operator wrote. The
// resolved runtime configuration is the fallback, because it is the
// same flat list and it carries the operator's own limits.
func legacyModelRecord(a *app, stored config.FileConfig, id string) (config.ModelRecord, bool) {
	if record, ok := stored.Active(); ok {
		return record, true
	}
	if id == "" {
		return config.ModelRecord{}, false
	}
	for _, record := range stored.Models {
		if record.ID == id {
			return record, true
		}
	}
	for _, record := range a.cfg.Models {
		if record.ID == id {
			return record, true
		}
	}
	return config.ModelRecord{}, false
}

// operatorLimitFor reports the context limit the operator configured for
// one model record.
//
// The record carries two different numbers, and they are never confused:
// in manual mode the window is the operator's own limit, in automatic
// mode it is what the provider advertised. A record with a window and no
// mode was written by hand, so the number is honoured as the operator's
// own limit rather than treated as an advertisement.
func operatorLimitFor(record config.ModelRecord) int {
	if record.ContextWindow <= 0 {
		return 0
	}
	switch record.ContextMode {
	case config.ContextManual:
		return record.ContextWindow
	case config.ContextAuto:
		return 0
	default:
		return record.ContextWindow
	}
}

// overrides reports the operator's explicit overrides: the flags they
// gave and the environment they set.
//
// A flag that was given wins. A flag that was not given does not, so a
// stored endpoint is never replaced by a default.
func (a *app) overrides() (endpoint, model, providerName string) {
	if a.cfg.FlagGiven("endpoint") {
		endpoint = a.cfg.Endpoint
	} else if value := strings.TrimSpace(os.Getenv(config.EnvEndpoint)); value != "" {
		endpoint = value
	}
	if a.cfg.FlagGiven("model") {
		model = a.cfg.Model
	} else if value := strings.TrimSpace(os.Getenv(config.EnvModel)); value != "" {
		model = value
	}
	if a.cfg.FlagGiven("provider") {
		providerName = a.cfg.Provider
	}
	return endpoint, model, providerName
}

// effortForRecord returns the reasoning level of one model record.
//
// Only a model whose API documents a reasoning parameter has one; the
// provider layer owns that table, and everything outside it gets no
// effort control at all.
func effortForRecord(record config.ModelRecord) string {
	if !provider.EffortCapable(record.ID) {
		return ""
	}
	if level := provider.NormalizeEffort(record.Effort); level != "" {
		return level
	}
	return provider.DefaultEffortFor(record.ID)
}

// wireProtocolFor maps a configured protocol name onto a wire protocol
// HAL speaks.
//
// The name is never guessed from the address: an endpoint that names a
// protocol HAL does not implement is refused, and the refusal names the
// protocols that are supported.
func wireProtocolFor(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "openai-compatible", "openai":
		return provider.WireOpenAI, nil
	case "anthropic-compatible", "anthropic":
		return provider.WireAnthropic, nil
	case "local", "ollama", "llamacpp", "llama.cpp", "vllm":
		// A local runtime speaks the OpenAI dialect unless it names
		// another one. The endpoint record owns that choice; this
		// mapping is only the fallback for a name that carries none.
		return provider.WireOpenAI, nil
	default:
		return "", fmt.Errorf("unsupported wire protocol %q (supported: %s, %s)",
			name, provider.WireOpenAI, provider.WireAnthropic)
	}
}

// providerForTarget builds the provider a target describes.
//
// A target with no address has no provider: the conversation runs
// without one and the interface says so.
func (a *app) providerForTarget(target providerTarget) (provider.Provider, error) {
	if strings.TrimSpace(target.BaseURL) == "" {
		return nil, nil
	}
	wire := target.Wire
	if wire == "" {
		wire = provider.WireOpenAI
	}
	return provider.NewForProtocol(wire, target.BaseURL, target.Credential, provider.Options{
		DefaultMaxTokens: a.cfg.MaxTokens,
		IdleTimeout:      seconds(a.cfg.StreamIdleSeconds),
		TurnTimeout:      seconds(a.cfg.TurnTimeoutSeconds),
	})
}

// providerFor builds the provider of the current configuration.
func (a *app) providerFor() (provider.Provider, error) {
	return a.providerForConfig(a.stored)
}

// providerForConfig builds the provider a stored configuration would
// use.
//
// It is what a change is checked against before the change is written:
// a configuration whose provider cannot be built is refused.
func (a *app) providerForConfig(stored config.FileConfig) (provider.Provider, error) {
	target, err := a.resolveTarget(stored)
	if err != nil {
		return nil, err
	}
	return a.providerForTarget(target)
}

// modelFor returns the model of the current configuration.
func (a *app) modelFor() provider.Model {
	target, err := a.resolveTarget(a.stored)
	if err != nil {
		// The configuration cannot be used. The model name is still
		// shown, so the operator sees what the interface thinks it is
		// talking to.
		return provider.Model{ID: a.cfg.Model}
	}
	return modelFromTarget(target)
}

// modelFromTarget builds the provider's model from a target.
//
// The two context numbers are both carried, and they mean different
// things:
//
//   - ContextWindow is the operator's own limit. It is the input to the
//     budget resolution, not the budget itself;
//   - Budget is the effective budget: the room the request may use, the
//     room held back for the answer, and the provenance of the number
//     that was chosen.
//
// The engine enforces the budget. Keeping both numbers here is what
// makes a configured manual limit reach the outgoing request: the limit
// is resolved into the budget, and the budget is what the engine
// applies.
func modelFromTarget(target providerTarget) provider.Model {
	model := provider.Model{
		ID:     target.ModelID,
		Effort: target.Effort,
	}
	// The output limit is a per-model setting: it belongs to the model,
	// not to the endpoint and not to the whole installation.
	if target.MaxTokens > 0 {
		model.MaxTokens = target.MaxTokens
	}
	// The operator's own context limit. It is zero when the operator
	// configured none, or when the model is in automatic mode and the
	// number in the record is the provider's advertisement.
	model.ContextWindow = target.ContextWindow
	// The effective budget, with its provenance.
	model.Budget = target.Budget
	return model
}
