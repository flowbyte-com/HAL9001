package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/tui"
)

// The endpoint and model management commands.
//
// Every change follows the same rule: the new configuration is prepared on
// a copy, validated, written to disk, and only then does the running
// application adopt it. A failed write leaves both the file and the
// running interface exactly as they were.
//
// No model-facing tool reaches any of this. A configuration change is an
// operator action, and nothing here can be triggered by a model response
// or by another task as a side effect.

// endpointInfos describes the configured endpoints for the interface.
//
// A credential is described, never shown: the panel says where it comes
// from, not what it is.
func (a *app) endpointInfos() []tui.EndpointInfo {
	out := make([]tui.EndpointInfo, 0, len(a.stored.Endpoints))
	activeEndpoint := a.stored.ActiveEndpointID()
	activeModel := a.stored.ActiveModelID()
	for _, e := range a.stored.Endpoints {
		info := tui.EndpointInfo{
			ID:           e.ID,
			Name:         e.Display(),
			Protocol:     string(e.Protocol),
			WireProtocol: string(e.WireProtocol),
			BaseURL:      e.BaseURL,
			Credential:   credentialLabel(e),
			Active:       e.ID == activeEndpoint,
			Review:       e.Review,
		}
		if info.WireProtocol == "" {
			if wire, err := e.EffectiveWireProtocol(); err == nil {
				info.WireProtocol = string(wire)
			}
		}
		for _, m := range e.Models {
			info.Models = append(info.Models, tui.ModelInfo{
				ID:       m.ID,
				Label:    m.Display(),
				Endpoint: e.ID,
				Active:   e.ID == activeEndpoint && m.ID == activeModel,
				Review:   m.Review,
			})
		}
		out = append(out, info)
	}
	return out
}

// credentialLabel describes a credential without revealing it.
func credentialLabel(e config.EndpointRecord) string {
	switch {
	case strings.TrimSpace(e.CredentialEnv) != "":
		return "env:" + e.CredentialEnv
	case strings.TrimSpace(e.Credential) != "":
		return "set"
	default:
		return "unset"
	}
}

// endpointChanged reports whether the fields that decide what an
// endpoint says about its models changed.
//
// The context metadata an endpoint reported belongs to its address, its
// protocol and its credential: a window advertised by one address
// describes that address, and it says nothing about another. A change
// to any of these fields makes the recorded window stale, and a change
// to the name or to the discovery flag does not.
func endpointChanged(before, after config.EndpointRecord) bool {
	return before.BaseURL != after.BaseURL ||
		before.Protocol != after.Protocol ||
		before.WireProtocol != after.WireProtocol ||
		before.Credential != after.Credential ||
		before.CredentialEnv != after.CredentialEnv
}

// saveEndpoint adds or updates one endpoint.
//
// An update only changes the fields the operator named: an endpoint that
// is edited keeps everything it is not asked to change, including its
// models.
//
// An endpoint that changed its address, its protocol or its credential
// forgets what it reported about its models: the window it advertised
// belonged to the configuration that is no longer in force.
func (a *app) saveEndpoint(spec tui.EndpointSpec) error {
	next := a.stored.Clone()
	if _, found := next.EndpointByID(spec.ID); !found {
		if strings.TrimSpace(spec.ID) == "" {
			return fmt.Errorf("an endpoint needs an id")
		}
		rec := config.EndpointRecord{
			ID:            spec.ID,
			Name:          spec.Name,
			Protocol:      config.Protocol(spec.Protocol),
			WireProtocol:  config.WireProtocol(spec.WireProtocol),
			BaseURL:       spec.BaseURL,
			CredentialEnv: spec.CredentialEnv,
			Credential:    spec.Credential,
			Discovery:     config.DiscoveryRecord{Enabled: spec.Discovery},
		}
		if rec.Name == "" {
			rec.Name = rec.ID
		}
		if rec.Protocol == "" {
			rec.Protocol = config.ProtocolOpenAI
		}
		if err := next.AddEndpoint(rec); err != nil {
			return err
		}
		a.invalidateContext(spec.ID)
		return a.commitTarget(next)
	}
	before, _ := next.EndpointByID(spec.ID)
	err := next.UpdateEndpoint(spec.ID, func(e *config.EndpointRecord) error {
		if spec.Name != "" {
			e.Name = spec.Name
		}
		if spec.Protocol != "" {
			e.Protocol = config.Protocol(spec.Protocol)
		}
		if spec.WireProtocol != "" {
			e.WireProtocol = config.WireProtocol(spec.WireProtocol)
		}
		if spec.BaseURL != "" {
			e.BaseURL = spec.BaseURL
		}
		if spec.CredentialEnv != "" {
			e.CredentialEnv = spec.CredentialEnv
			e.Credential = ""
		}
		if spec.Credential != "" {
			e.Credential = spec.Credential
			e.CredentialEnv = ""
		}
		e.Discovery.Enabled = spec.Discovery
		return nil
	})
	if err != nil {
		return err
	}
	after, _ := next.EndpointByID(spec.ID)
	if endpointChanged(before, after) {
		a.invalidateContext(spec.ID)
	}
	return a.commitTarget(next)
}

// deleteEndpoint removes one endpoint and its models.
//
// An endpoint that holds the active model is never removed silently: the
// operator names a replacement, and the removal and the switch happen
// together.
func (a *app) deleteEndpoint(id, replacement string) error {
	next := a.stored.Clone()
	if _, ok := next.EndpointByID(id); !ok {
		return fmt.Errorf("no endpoint with the id %q", id)
	}
	if next.ActiveEndpointID() == id {
		if strings.TrimSpace(replacement) == "" {
			if len(next.Endpoints) > 1 {
				return fmt.Errorf("endpoint %q holds the active model %q — name a replacement with --use <endpoint>/<model>",
					id, next.ActiveModelID())
			}
			// The last endpoint is going away: there is nothing left
			// to select. The selection is cleared explicitly, so no
			// dangling selection can point at a removed endpoint.
			next.Selection = nil
		} else {
			endpoint, model, err := splitReplacement(replacement)
			if err != nil {
				return err
			}
			if endpoint == id {
				// The replacement lives inside the endpoint that is
				// being removed. Accepting it would leave a selection
				// that points at nothing, and the next launch would
				// resolve the removed endpoint from the mirror.
				return fmt.Errorf("the replacement %q is inside endpoint %q, which is being removed", replacement, id)
			}
			if _, ok := next.ModelFor(endpoint, model); !ok {
				return fmt.Errorf("no model %q under endpoint %q", model, endpoint)
			}
			next.SetActive(endpoint, model)
		}
	}
	if err := next.RemoveEndpoint(id); err != nil {
		return err
	}
	// The endpoint is gone: what it reported about its models goes with
	// it.
	a.invalidateContext(id)
	return a.commitTarget(next)
}

// addModel stores a new model under one endpoint.
//
// The active model is never changed by an addition: the operator selects a
// model explicitly.
func (a *app) addModel(endpointID string, spec tui.ModelSpec) error {
	next := a.stored.Clone()
	if _, ok := next.EndpointByID(endpointID); !ok {
		return fmt.Errorf("no endpoint with the id %q", endpointID)
	}
	rec := config.ModelRecord{
		ID:              strings.TrimSpace(spec.ID),
		Label:           spec.Label,
		Effort:          spec.Effort,
		ContextMode:     spec.ContextMode,
		ContextWindow:   spec.ContextWindow,
		MaxOutputTokens: spec.MaxOutputTokens,
		Source:          config.SourceManual,
	}
	if rec.ContextMode == "" {
		rec.ContextMode = config.ContextAuto
	}
	if err := next.AddModelTo(endpointID, rec); err != nil {
		return err
	}
	return a.commitTarget(next)
}

// editModel changes one model's settings.
//
// Only the settings the operator named are changed. The model's identity
// and its endpoint never change.
func (a *app) editModel(endpointID, modelID string, spec tui.ModelSpec) error {
	next := a.stored.Clone()
	err := next.UpdateModel(endpointID, modelID, func(m *config.ModelRecord) error {
		if spec.Label != "" {
			m.Label = spec.Label
		}
		if spec.Effort != "" {
			m.Effort = spec.Effort
		}
		if spec.ContextMode != "" {
			m.ContextMode = spec.ContextMode
			if m.ContextMode == config.ContextAuto {
				m.ContextWindow = 0
			}
		}
		if spec.ContextWindow > 0 {
			m.ContextWindow = spec.ContextWindow
			if m.ContextMode == "" {
				m.ContextMode = config.ContextManual
			}
		}
		if spec.MaxOutputTokens > 0 {
			m.MaxOutputTokens = spec.MaxOutputTokens
		}
		return nil
	})
	if err != nil {
		return err
	}
	return a.commitTarget(next)
}

// deleteModel removes one model.
//
// The active model is never removed silently: the operator names a
// replacement, and the removal and the switch happen together.
func (a *app) deleteModel(endpointID, modelID, replacement string) error {
	next := a.stored.Clone()
	if _, ok := next.EndpointByID(endpointID); !ok {
		return fmt.Errorf("no endpoint with the id %q", endpointID)
	}
	if _, ok := next.ModelFor(endpointID, modelID); !ok {
		return fmt.Errorf("endpoint %q has no model %q", endpointID, modelID)
	}
	if next.ActiveEndpointID() == endpointID && next.ActiveModelID() == modelID {
		if strings.TrimSpace(replacement) == "" {
			return fmt.Errorf("%q is the active model — name a replacement with --use <endpoint>/<model>", modelID)
		}
		endpoint, model, err := splitReplacement(replacement)
		if err != nil {
			return err
		}
		if endpoint == endpointID && model == modelID {
			return fmt.Errorf("the replacement is the model that is being removed")
		}
		if _, ok := next.ModelFor(endpoint, model); !ok {
			return fmt.Errorf("no model %q under endpoint %q", model, endpoint)
		}
		next.SetActive(endpoint, model)
	}
	if err := next.RemoveModel(endpointID, modelID); err != nil {
		return err
	}
	return a.commitTarget(next)
}

// modelDetails describes one model's settings, one line per setting.
//
// No credential appears here: the panel describes the endpoint, not its
// secret.
func (a *app) modelDetails(endpointID, modelID string) ([]string, error) {
	endpoint, ok := a.stored.EndpointByID(endpointID)
	if !ok {
		return nil, fmt.Errorf("no endpoint with the id %q", endpointID)
	}
	model, ok := endpoint.Model(modelID)
	if !ok {
		return nil, fmt.Errorf("endpoint %q has no model %q", endpointID, modelID)
	}
	contextWindow := "automatic"
	if model.ContextMode == config.ContextManual {
		contextWindow = fmt.Sprintf("manual, %d tokens", model.ContextWindow)
	}
	output := "provider default"
	if model.MaxOutputTokens > 0 {
		output = fmt.Sprintf("%d tokens", model.MaxOutputTokens)
	}
	effort := "none"
	if model.Effort != "" {
		effort = model.Effort
	}
	lines := []string{
		fmt.Sprintf("  id              %s", model.ID),
		fmt.Sprintf("  endpoint        %s (%s)", endpoint.Display(), endpoint.ID),
		fmt.Sprintf("  protocol        %s · wire %s", endpoint.Protocol, wireOf(endpoint)),
		fmt.Sprintf("  base url        %s", endpoint.BaseURL),
		fmt.Sprintf("  credential      %s", credentialLabel(endpoint)),
		fmt.Sprintf("  label           %s", orDefault(model.Label, "(none)")),
		fmt.Sprintf("  effort          %s", effort),
		fmt.Sprintf("  context         %s", contextWindow),
		fmt.Sprintf("  output limit    %s", output),
		fmt.Sprintf("  source          %s", orDefault(model.Source, config.SourceManual)),
	}
	// The effective budget: the whole window, the room held back for the
	// answer, the room the request may use and where the numbers came
	// from. It is what the request is held to, so the operator sees it.
	if budget, err := a.budgetForModel(endpoint, model); err == nil {
		lines = append(lines, fmt.Sprintf("  context budget  %s", provider.DescribeBudget(budget)))
	}
	if model.FetchedAt != "" {
		lines = append(lines, fmt.Sprintf("  confirmed at    %s", model.FetchedAt))
	}
	if model.Review != "" {
		lines = append(lines, fmt.Sprintf("  needs review    %s", model.Review))
	}
	return lines, nil
}

// budgetForModel resolves the effective budget of one model of one
// endpoint.
//
// The numbers are the ones the request is held to: the whole window the
// model accepts, the room held back for the answer and the room the
// request may use. The provenance travels with them, so a provisional
// budget is recognisable as one.
func (a *app) budgetForModel(endpoint config.EndpointRecord, model config.ModelRecord) (provider.ContextBudget, error) {
	allowance, err := a.outputAllowance(
		model.MaxOutputTokens, wireOf(endpoint), model.ID, model.Effort)
	if err != nil {
		return provider.ContextBudget{}, err
	}
	key := contextKey{endpoint: endpoint.ID, model: model.ID}
	return a.contextBudget(key, operatorLimitFor(model), allowance)
}

// discover probes the provider's catalogue of one endpoint.
//
// Discovery confirms the identifiers that are already configured and
// reports the ones that are not. It never adds a model, and it never
// moves a model between endpoints.
func (a *app) discover(endpointID string) ([]string, error) {
	endpoint, ok := a.stored.EndpointByID(endpointID)
	if !ok {
		return nil, fmt.Errorf("no endpoint with the id %q", endpointID)
	}
	wire, err := endpoint.EffectiveWireProtocol()
	if err != nil {
		return nil, err
	}
	impl, err := provider.NewForProtocol(string(wire), endpoint.BaseURL, endpoint.CredentialValue(), provider.Options{
		DefaultMaxTokens: a.cfg.MaxTokens,
		IdleTimeout:      seconds(a.cfg.StreamIdleSeconds),
		TurnTimeout:      seconds(a.cfg.TurnTimeoutSeconds),
	})
	if err != nil {
		return nil, err
	}
	lister, ok := impl.(provider.ModelLister)
	if !ok {
		return nil, fmt.Errorf("the %s wire protocol cannot list a catalogue", wire)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ids, err := lister.Models(ctx)
	if err != nil {
		return nil, err
	}
	// A catalogue that lists an identifier confirms it. The confirmation
	// is recorded, and a review note that only said the identifier was
	// never confirmed is cleared.
	next := a.stored.Clone()
	confirmed := map[string]bool{}
	for _, id := range ids {
		confirmed[id] = true
	}
	err = next.UpdateEndpoint(endpointID, func(e *config.EndpointRecord) error {
		e.Discovery.LastChecked = time.Now().UTC().Format(time.RFC3339)
		for i := range e.Models {
			model := &e.Models[i]
			if !confirmed[model.ID] {
				continue
			}
			model.Source = config.SourceDiscovered
			model.FetchedAt = time.Now().UTC().Format(time.RFC3339)
			model.Review = ""
		}
		return nil
	})
	if err != nil {
		return ids, nil
	}
	// The catalogue is reported even when the confirmation could not be
	// written: the operator still learns what the provider serves.
	_ = a.commitConfig(next)
	sort.Strings(ids)
	return ids, nil
}

// commitConfig writes the prepared configuration and adopts it.
//
// The file is written first. Only a successful write replaces the running
// configuration, so the interface and the file never disagree.
func (a *app) commitConfig(next config.FileConfig) error {
	next = withCurrentSchema(next)
	next.SyncLegacyMirror()
	if err := config.SaveFile(config.ConfigPath(), next); err != nil {
		return err
	}
	a.stored = next
	return nil
}

// withCurrentSchema adopts the current schema version when the
// configuration describes endpoints.
//
// A configuration that holds endpoints is a version-2 file. Writing it
// with an older version would make the next launch read it as a legacy
// file and ignore the endpoints entirely: the operator's first endpoint
// would exist in the file and nowhere else.
func withCurrentSchema(next config.FileConfig) config.FileConfig {
	if len(next.Endpoints) > 0 && next.Version < config.ConfigVersion {
		next.Version = config.ConfigVersion
	}
	return next
}

// commitTarget writes a prepared configuration and rebuilds the runtime
// from it.
//
// The runtime that would use the new configuration is built first. A
// configuration that cannot be used is refused: the last working runtime
// stays, and the active selection is never committed for a
// configuration that does not work.
func (a *app) commitTarget(next config.FileConfig) error {
	next = withCurrentSchema(next)
	if _, err := a.providerForConfig(next); err != nil {
		return err
	}
	if err := a.commitConfig(next); err != nil {
		return err
	}
	a.rebuildEngine()
	return nil
}

// splitReplacement reads an "endpoint/model" replacement.
func splitReplacement(value string) (endpoint, model string, err error) {
	endpoint, model, ok := strings.Cut(strings.TrimSpace(value), "/")
	if !ok || endpoint == "" || model == "" {
		return "", "", fmt.Errorf("the replacement must name a model as \"endpoint/model\"")
	}
	return endpoint, model, nil
}

// wireOf names the wire protocol of an endpoint.
func wireOf(endpoint config.EndpointRecord) string {
	wire, err := endpoint.EffectiveWireProtocol()
	if err != nil {
		return "unsupported"
	}
	return string(wire)
}

// orDefault returns a value or a fallback.
func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
