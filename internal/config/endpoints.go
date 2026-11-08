package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// EffortLevels are the documented reasoning levels. The list mirrors the
// provider package's; a test in this package keeps the two in step.
var EffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// validEffort reports whether a level is one of the documented levels.
func validEffort(level string) bool {
	level = strings.ToLower(strings.TrimSpace(level))
	for _, l := range EffortLevels {
		if l == level {
			return true
		}
	}
	return false
}

// Protocol is the protocol type an endpoint declares.
//
// The taxonomy mixes two questions: which API family the endpoint belongs
// to, and where it runs. "local" is a deployment kind, not an API family —
// a local server speaks the OpenAI dialect — and "custom" is the catch-all
// that must still name a wire protocol HAL actually speaks.
type Protocol string

const (
	// ProtocolOpenAI is any OpenAI-style /chat/completions service.
	ProtocolOpenAI Protocol = "openai-compatible"
	// ProtocolAnthropic is any Anthropic-style /v1/messages service.
	ProtocolAnthropic Protocol = "anthropic-compatible"
	// ProtocolLocal is a local server that speaks the OpenAI dialect.
	ProtocolLocal Protocol = "local"
	// ProtocolCustom is any other service. It must name a wire protocol.
	ProtocolCustom Protocol = "custom"
)

// WireProtocol is the wire protocol HAL actually speaks. HAL never
// pretends that an arbitrary API is compatible: an endpoint whose wire
// protocol is not listed here is refused when it is selected.
type WireProtocol string

const (
	// WireOpenAI is the OpenAI chat-completions dialect.
	WireOpenAI WireProtocol = "openai-compatible"
	// WireAnthropic is the Anthropic messages dialect.
	WireAnthropic WireProtocol = "anthropic-compatible"
)

// Context modes for a model's context window.
const (
	// ContextAuto uses the context window the provider reported for the
	// model, and no limit when the provider reported none.
	ContextAuto = "auto"
	// ContextManual uses the operator's explicit context-window limit.
	ContextManual = "manual"
)

// Provenance of a model record.
const (
	// SourceManual means the operator supplied the identifier by hand.
	SourceManual = "manual"
	// SourceMigrated means the record came from the pre-version-2 file.
	SourceMigrated = "migrated"
	// SourceDiscovered means the provider's catalogue confirmed it.
	SourceDiscovered = "discovered"
)

// EndpointRecord is one independently configured provider endpoint.
//
// The identifier is stable: it is what the operator types to name this
// endpoint, and it never changes when the endpoint is edited. The models
// underneath it belong to this endpoint and to no other, so a model
// identifier is never shared between endpoints by accident.
type EndpointRecord struct {
	// ID is the stable unique identifier: [a-z0-9_-]{1,64}.
	ID string `json:"id"`
	// Name is the operator-friendly display name.
	Name string `json:"name"`
	// Protocol is the declared protocol type.
	Protocol Protocol `json:"protocol"`
	// WireProtocol is the wire protocol HAL speaks to this endpoint.
	// "custom" endpoints must state it explicitly.
	WireProtocol WireProtocol `json:"wire_protocol,omitempty"`
	// BaseURL is the API base URL.
	BaseURL string `json:"base_url"`
	// CredentialEnv names an environment variable holding the credential.
	// It is the preferred way to hold a secret: the value never enters the
	// configuration file.
	CredentialEnv string `json:"credential_env,omitempty"`
	// Credential is a literal credential, used only when CredentialEnv is
	// empty. It is never written to a log, a notice or a diagnostic.
	Credential string `json:"credential,omitempty"`
	// Discovery describes whether the provider's catalogue may be probed.
	Discovery DiscoveryRecord `json:"discovery"`
	// Models are the models of this endpoint.
	Models []ModelRecord `json:"models,omitempty"`
	// Review is a note for the operator. A non-empty value means the
	// endpoint needs the operator's attention.
	Review string `json:"review,omitempty"`
}

// DiscoveryRecord describes the optional model-catalogue discovery.
type DiscoveryRecord struct {
	// Enabled allows the provider's catalogue to be probed.
	Enabled bool `json:"enabled"`
	// LastChecked is when the catalogue was last probed (RFC 3339).
	LastChecked string `json:"last_checked,omitempty"`
}

// ActiveSelection names the active endpoint and the active model inside
// it. The model is scoped to the endpoint: two endpoints may carry the
// same model identifier without either of them owning the other's.
type ActiveSelection struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
}

// Display returns the endpoint's display name, or its identifier.
func (e EndpointRecord) Display() string {
	if strings.TrimSpace(e.Name) != "" {
		return e.Name
	}
	return e.ID
}

// Model returns the endpoint's record for a model identifier.
func (e EndpointRecord) Model(id string) (ModelRecord, bool) {
	for _, m := range e.Models {
		if m.ID == id {
			return m, true
		}
	}
	return ModelRecord{}, false
}

// CredentialValue returns the secret this endpoint uses, or an empty
// string when it has none.
//
// The endpoint's own configuration always wins: the environment variable
// the operator named, then a literal secret. The global HAL9001_API_KEY
// is a backward-compatible fallback and is used only by an endpoint that
// declares neither, so one global variable can never substitute another
// endpoint's credential.
//
// The result is a secret: it never travels into a notice, a log or an
// error.
func (e EndpointRecord) CredentialValue() string {
	if name := strings.TrimSpace(e.CredentialEnv); name != "" {
		// The operator named this endpoint's credential. A variable
		// that is not set means this endpoint has no credential: the
		// global fallback belongs to other endpoints and must not be
		// sent here.
		return strings.TrimSpace(os.Getenv(name))
	}
	if secret := strings.TrimSpace(e.Credential); secret != "" {
		return secret
	}
	return strings.TrimSpace(os.Getenv(EnvAPIKey))
}

// HasCredential reports whether a credential can be resolved for this
// endpoint. It never reveals the credential itself.
func (e EndpointRecord) HasCredential() bool {
	return e.CredentialValue() != ""
}

// EffectiveWireProtocol returns the wire protocol HAL will speak to this
// endpoint, and whether the endpoint names one it supports.
//
// "local" defaults to the OpenAI dialect, because a local server
// (Ollama, llama.cpp, vLLM) speaks it. Every other protocol names its own
// wire protocol. An endpoint that names a wire protocol HAL does not
// implement is refused when it is selected: HAL never pretends that an
// arbitrary API is compatible.
func (e EndpointRecord) EffectiveWireProtocol() (WireProtocol, error) {
	switch e.Protocol {
	case ProtocolOpenAI:
		if e.WireProtocol != "" && e.WireProtocol != WireOpenAI {
			return "", fmt.Errorf("endpoint %q is %s and cannot speak %s", e.ID, e.Protocol, e.WireProtocol)
		}
		return WireOpenAI, nil
	case ProtocolLocal:
		if e.WireProtocol == "" {
			return WireOpenAI, nil
		}
		if e.WireProtocol != WireOpenAI && e.WireProtocol != WireAnthropic {
			return "", fmt.Errorf("endpoint %q names an unsupported wire protocol %q", e.ID, e.WireProtocol)
		}
		return e.WireProtocol, nil
	case ProtocolAnthropic:
		if e.WireProtocol != "" && e.WireProtocol != WireAnthropic {
			return "", fmt.Errorf("endpoint %q is %s and cannot speak %s", e.ID, e.Protocol, e.WireProtocol)
		}
		return WireAnthropic, nil
	case ProtocolCustom:
		switch e.WireProtocol {
		case WireOpenAI, WireAnthropic:
			return e.WireProtocol, nil
		case "":
			return "", fmt.Errorf("endpoint %q is custom and must name a wire protocol (%s or %s)",
				e.ID, WireOpenAI, WireAnthropic)
		default:
			return "", fmt.Errorf("endpoint %q names an unsupported wire protocol %q", e.ID, e.WireProtocol)
		}
	case "":
		return "", fmt.Errorf("endpoint %q has no protocol", e.ID)
	default:
		return "", fmt.Errorf("endpoint %q has an unknown protocol %q", e.ID, e.Protocol)
	}
}

// Validate checks one endpoint record without touching the network or the
// environment.
func (e EndpointRecord) Validate() error {
	if !validEndpointID(e.ID) {
		return fmt.Errorf("endpoint id %q must be 1-64 characters of a-z, 0-9, _ or -", e.ID)
	}
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("endpoint %q needs a display name", e.ID)
	}
	if strings.TrimSpace(e.BaseURL) == "" {
		return fmt.Errorf("endpoint %q needs a base URL", e.ID)
	}
	if _, err := url.Parse(e.BaseURL); err != nil {
		return fmt.Errorf("endpoint %q has an unusable base URL: %w", e.ID, err)
	}
	switch e.Protocol {
	case ProtocolOpenAI, ProtocolAnthropic, ProtocolLocal:
	case ProtocolCustom:
	case "":
		return fmt.Errorf("endpoint %q needs a protocol", e.ID)
	default:
		return fmt.Errorf("endpoint %q has an unknown protocol %q", e.ID, e.Protocol)
	}
	if _, err := e.EffectiveWireProtocol(); err != nil {
		return err
	}
	if e.CredentialEnv != "" && !validEnvName(e.CredentialEnv) {
		return fmt.Errorf("endpoint %q names an unusable environment variable %q", e.ID, e.CredentialEnv)
	}
	seen := map[string]bool{}
	for _, m := range e.Models {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("endpoint %q: %w", e.ID, err)
		}
		if seen[m.ID] {
			return fmt.Errorf("endpoint %q lists the model %q twice", e.ID, m.ID)
		}
		seen[m.ID] = true
	}
	return nil
}

// Validate checks one model record.
func (m ModelRecord) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("a model needs an identifier")
	}
	switch m.ContextMode {
	case "", ContextAuto, ContextManual:
	default:
		return fmt.Errorf("model %q has an unknown context mode %q", m.ID, m.ContextMode)
	}
	if m.ContextWindow < 0 {
		return fmt.Errorf("model %q has a negative context window", m.ID)
	}
	if m.MaxOutputTokens < 0 {
		return fmt.Errorf("model %q has a negative output limit", m.ID)
	}
	// The context window is a TOTAL: the room for the answer comes out
	// of it. An output limit that fills the whole window leaves no
	// room for the request at all, so the two numbers contradict each
	// other and the record is refused with both of them named.
	if m.ContextWindow > 0 && m.MaxOutputTokens >= m.ContextWindow {
		return fmt.Errorf(
			"model %q allows an answer of %d tokens but its context window is only %d tokens: "+
				"the answer does not fit inside the window",
			m.ID, m.MaxOutputTokens, m.ContextWindow)
	}
	if m.Effort != "" && !validEffort(m.Effort) {
		return fmt.Errorf("model %q has an unknown effort level %q", m.ID, m.Effort)
	}
	return nil
}

// EndpointByID returns the endpoint with this identifier.
func (f FileConfig) EndpointByID(id string) (EndpointRecord, bool) {
	for _, e := range f.Endpoints {
		if e.ID == id {
			return e, true
		}
	}
	return EndpointRecord{}, false
}

// ActiveEndpoint returns the active endpoint record.
func (f FileConfig) ActiveEndpoint() (EndpointRecord, bool) {
	if f.Selection == nil {
		return EndpointRecord{}, false
	}
	return f.EndpointByID(f.Selection.Endpoint)
}

// ActiveEndpointID returns the identifier of the active endpoint.
func (f FileConfig) ActiveEndpointID() string {
	if f.Selection == nil {
		return ""
	}
	return f.Selection.Endpoint
}

// ActiveModelID returns the identifier of the active model inside the
// active endpoint.
func (f FileConfig) ActiveModelID() string {
	if f.Selection == nil {
		return ""
	}
	return f.Selection.Model
}

// ActiveModelRecord returns the active model record.
func (f FileConfig) ActiveModelRecord() (ModelRecord, bool) {
	ep, ok := f.ActiveEndpoint()
	if !ok {
		return ModelRecord{}, false
	}
	return ep.Model(f.ActiveModelID())
}

// SetActive records the active endpoint and model. The selection is
// replaced as a whole, never field by field, so a copy of the
// configuration can never share half a selection with its original.
func (f *FileConfig) SetActive(endpoint, model string) {
	f.Selection = &ActiveSelection{Endpoint: endpoint, Model: model}
}

// AddEndpoint stores a new endpoint. It refuses a duplicate identifier,
// because the identifier is what every later command names the endpoint by.
func (f *FileConfig) AddEndpoint(rec EndpointRecord) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	if _, ok := f.EndpointByID(rec.ID); ok {
		return fmt.Errorf("an endpoint with the id %q already exists", rec.ID)
	}
	f.Endpoints = append(f.Endpoints, rec)
	return nil
}

// UpdateEndpoint applies a change to one endpoint.
func (f *FileConfig) UpdateEndpoint(id string, change func(*EndpointRecord) error) error {
	for i := range f.Endpoints {
		if f.Endpoints[i].ID != id {
			continue
		}
		previous := f.Endpoints[i]
		if err := change(&f.Endpoints[i]); err != nil {
			return err
		}
		if err := f.Endpoints[i].Validate(); err != nil {
			f.Endpoints[i] = previous
			return err
		}
		return nil
	}
	return fmt.Errorf("no endpoint with the id %q", id)
}

// RemoveEndpoint removes one endpoint and its models.
func (f *FileConfig) RemoveEndpoint(id string) error {
	for i := range f.Endpoints {
		if f.Endpoints[i].ID != id {
			continue
		}
		f.Endpoints = append(f.Endpoints[:i], f.Endpoints[i+1:]...)
		return nil
	}
	return fmt.Errorf("no endpoint with the id %q", id)
}

// AddModelTo stores a model under an endpoint.
//
// The model identifier is scoped to its endpoint: the same identifier may
// exist under two endpoints without either of them owning the other's,
// and it is never attached to an endpoint the operator did not name.
func (f *FileConfig) AddModelTo(endpointID string, model ModelRecord) error {
	if err := model.Validate(); err != nil {
		return err
	}
	for i := range f.Endpoints {
		if f.Endpoints[i].ID != endpointID {
			continue
		}
		if _, ok := f.Endpoints[i].Model(model.ID); ok {
			return fmt.Errorf("endpoint %q already has a model %q", endpointID, model.ID)
		}
		if model.Source == "" {
			model.Source = SourceManual
		}
		if model.ContextMode == "" {
			model.ContextMode = ContextAuto
		}
		f.Endpoints[i].Models = append(f.Endpoints[i].Models, model)
		return nil
	}
	return fmt.Errorf("no endpoint with the id %q", endpointID)
}

// UpdateModel applies a change to one model of one endpoint.
func (f *FileConfig) UpdateModel(endpointID, modelID string, change func(*ModelRecord) error) error {
	for i := range f.Endpoints {
		if f.Endpoints[i].ID != endpointID {
			continue
		}
		for j := range f.Endpoints[i].Models {
			if f.Endpoints[i].Models[j].ID != modelID {
				continue
			}
			previous := f.Endpoints[i].Models[j]
			if err := change(&f.Endpoints[i].Models[j]); err != nil {
				return err
			}
			if err := f.Endpoints[i].Models[j].Validate(); err != nil {
				f.Endpoints[i].Models[j] = previous
				return err
			}
			return nil
		}
		return fmt.Errorf("endpoint %q has no model %q", endpointID, modelID)
	}
	return fmt.Errorf("no endpoint with the id %q", endpointID)
}

// RemoveModel removes one model from one endpoint.
func (f *FileConfig) RemoveModel(endpointID, modelID string) error {
	for i := range f.Endpoints {
		if f.Endpoints[i].ID != endpointID {
			continue
		}
		for j := range f.Endpoints[i].Models {
			if f.Endpoints[i].Models[j].ID != modelID {
				continue
			}
			f.Endpoints[i].Models = append(f.Endpoints[i].Models[:j], f.Endpoints[i].Models[j+1:]...)
			return nil
		}
		return fmt.Errorf("endpoint %q has no model %q", endpointID, modelID)
	}
	return fmt.Errorf("no endpoint with the id %q", endpointID)
}

// ModelFor resolves a model identifier inside one endpoint.
func (f FileConfig) ModelFor(endpointID, modelID string) (ModelRecord, bool) {
	ep, ok := f.EndpointByID(endpointID)
	if !ok {
		return ModelRecord{}, false
	}
	return ep.Model(modelID)
}

// EndpointsNeedingReview lists the models that carry an operator-review
// note, with the endpoint they belong to.
func (f FileConfig) EndpointsNeedingReview() []ReviewItem {
	var out []ReviewItem
	for _, e := range f.Endpoints {
		for _, m := range e.Models {
			if strings.TrimSpace(m.Review) == "" {
				continue
			}
			out = append(out, ReviewItem{Endpoint: e.ID, Model: m.ID, Note: m.Review})
		}
	}
	return out
}

// ReviewItem is one model that needs the operator's attention.
type ReviewItem struct {
	Endpoint string
	Model    string
	Note     string
}

// validEndpointID reports whether an identifier is usable as a stable
// endpoint name.
func validEndpointID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// validEnvName reports whether a name can be an environment variable.
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// EffortForModel returns the recorded reasoning level of one model inside
// one endpoint, or an empty string when none is recorded.
func (f FileConfig) EffortForModel(endpointID, modelID string) string {
	m, ok := f.ModelFor(endpointID, modelID)
	if !ok {
		return ""
	}
	return m.Effort
}

// SetModelEffort stores the reasoning level of one model inside one
// endpoint. It reports whether anything changed.
func (f *FileConfig) SetModelEffort(endpointID, modelID, effort string) (bool, error) {
	changed := false
	err := f.UpdateModel(endpointID, modelID, func(m *ModelRecord) error {
		if m.Effort == effort {
			return nil
		}
		m.Effort = effort
		changed = true
		return nil
	})
	return changed, err
}

// Clone returns a deep copy of the configuration.
//
// A change is prepared on a copy and only replaces the original once it
// has been written to disk, so a failed write can never leave the running
// interface and the file disagreeing.
func (f FileConfig) Clone() FileConfig {
	out := f
	out.Endpoints = make([]EndpointRecord, len(f.Endpoints))
	for i, e := range f.Endpoints {
		copied := e
		copied.Models = append([]ModelRecord(nil), e.Models...)
		out.Endpoints[i] = copied
	}
	out.Models = append([]ModelRecord(nil), f.Models...)
	out.MCPServers = append([]MCPServerRecord(nil), f.MCPServers...)
	if f.Selection != nil {
		selection := *f.Selection
		out.Selection = &selection
	}
	return out
}
