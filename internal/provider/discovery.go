package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Model discovery and context discovery.
//
// Discovery asks an endpoint what it offers. It never maintains a
// catalogue of providers or models: the answer comes from the endpoint
// the operator configured, and a model that the endpoint does not list
// simply does not exist as far as HAL is concerned.
//
// Two rules shape everything here:
//
//   - discovery never changes the active model. Adding a model and
//     choosing one are separate, explicit acts;
//   - a listed model is not an accessible model. A listing says the
//     endpoint knows the identifier, not that a request to it will
//     succeed, so a listed model carries Access unknown until a request
//     to that model has actually worked.

// ModelAccess says what is known about a model's reachability.
type ModelAccess string

const (
	// AccessUnknown means the model was listed but never exercised. HAL
	// does not claim a listed model works.
	AccessUnknown ModelAccess = "unknown"
	// AccessVerified means HAL made a successful request to this model.
	AccessVerified ModelAccess = "verified"
)

// ModelInfo is one model as an endpoint reports it.
//
// The identifier is exactly what the endpoint returned. HAL never
// rewrites, truncates, lower-cases or otherwise "tidies" a model
// identifier: the endpoint is the authority on its own names.
type ModelInfo struct {
	// ID is the provider-side model identifier.
	ID string `json:"id"`
	// Label is the endpoint's display name, when it has one.
	Label string `json:"label,omitempty"`
	// ContextWindow is the context the endpoint advertises for this
	// model. Zero means the endpoint said nothing about it.
	ContextWindow int `json:"context_window,omitempty"`
	// MaxOutputTokens is the output cap the endpoint advertises. Zero
	// means the endpoint said nothing about it.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// Access says whether the model is known to work.
	Access ModelAccess `json:"access,omitempty"`
}

// Display returns the label or the identifier.
func (m ModelInfo) Display() string {
	if strings.TrimSpace(m.Label) != "" {
		return m.Label
	}
	return m.ID
}

// ModelCatalog is implemented by adapters whose endpoint can report the
// models it offers, with whatever the endpoint said about them.
//
// It is the richer form of ModelLister: the simple listing answers
// "which identifiers exist", the catalogue also answers "what did the
// endpoint say about them".
type ModelCatalog interface {
	// ListModels returns the models the endpoint offers. The
	// implementation bounds the response it reads and the number of
	// models it returns.
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ErrNoModelListing reports that an endpoint does not offer a model
// listing. The operator can still configure a model identifier by hand;
// HAL never invents one.
var ErrNoModelListing = errors.New("provider: this endpoint does not list its models; add the model id by hand")

// DiscoveryOptions bound one discovery run.
type DiscoveryOptions struct {
	// Search filters the listed models: an empty search returns
	// everything. The filter is a case-insensitive substring match on
	// the identifier and the label.
	Search string
	// Limit bounds the number of models returned. Zero uses
	// DefaultDiscoveryLimit.
	Limit int
	// Timeout bounds the whole run. Zero uses DefaultDiscoveryTimeout.
	Timeout time.Duration
}

// Discovery defaults.
const (
	// DefaultDiscoveryLimit bounds how many models one run returns.
	DefaultDiscoveryLimit = 200
	// DefaultDiscoveryTimeout bounds one discovery run.
	DefaultDiscoveryTimeout = 20 * time.Second
)

// Discover lists the models an endpoint offers and applies the
// operator's filter.
//
// The listing is bounded twice: the catalogue bounds what it reads from
// the endpoint, and Discover bounds what it hands back.
func Discover(ctx context.Context, catalog ModelCatalog, opts DiscoveryOptions) ([]ModelInfo, error) {
	if catalog == nil {
		return nil, ErrNoModelListing
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultDiscoveryTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	models, err := catalog.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	return applyDiscoveryOptions(models, opts), nil
}

// applyDiscoveryOptions filters and bounds a listing.
func applyDiscoveryOptions(models []ModelInfo, opts DiscoveryOptions) []ModelInfo {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultDiscoveryLimit
	}
	search := strings.ToLower(strings.TrimSpace(opts.Search))
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		if strings.TrimSpace(m.ID) == "" {
			// An entry without an identifier is not a model. It is
			// skipped rather than shown as an empty name.
			continue
		}
		if search != "" &&
			!strings.Contains(strings.ToLower(m.ID), search) &&
			!strings.Contains(strings.ToLower(m.Label), search) {
			continue
		}
		if m.Access == "" {
			m.Access = AccessUnknown
		}
		out = append(out, m)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// --- context ----------------------------------------------------------

// ContextSource says where a context number came from. The sources are
// never conflated: a number the operator typed, a number the provider
// advertised, a number a local runtime allocated, a number HAL verified
// earlier and a number HAL guessed are five different things.
type ContextSource string

const (
	// ContextSourceOperator is a limit the operator configured.
	ContextSourceOperator ContextSource = "operator"
	// ContextSourceProvider is a limit the provider advertised.
	ContextSourceProvider ContextSource = "provider"
	// ContextSourceLocal is a limit a local runtime allocated.
	ContextSourceLocal ContextSource = "local-runtime"
	// ContextSourceCached is a limit verified earlier and stored.
	ContextSourceCached ContextSource = "cached"
	// ContextSourceProvisional is a conservative guess HAL made because
	// nothing was known.
	ContextSourceProvisional ContextSource = "provisional"
)

// ContextMode says how the budget in force was chosen.
type ContextMode string

const (
	// ContextModeMax means the budget is the model's advertised maximum
	// context.
	ContextModeMax ContextMode = "max"
	// ContextModeLocal means the budget is a local runtime's own
	// allocation, which is usually far smaller than the model's
	// maximum.
	ContextModeLocal ContextMode = "local"
	// ContextModeProvisional means the budget is a conservative guess.
	ContextModeProvisional ContextMode = "provisional"
	// ContextModeConfigured means the operator set the limit.
	ContextModeConfigured ContextMode = "configured"
)

// ContextMetadata is one context record.
//
// ContextWindow is the budget HAL must respect. MaxContextWindow is the
// largest context the model advertises, when that is known. The two are
// different numbers and are kept apart: a local runtime may allocate
// far less than the model could accept, and the advertised maximum must
// never silently become the budget.
type ContextMetadata struct {
	// ContextWindow is the budget in force.
	ContextWindow int `json:"context_window,omitempty"`
	// MaxContextWindow is the advertised maximum, when the endpoint
	// reports one. It is informational.
	MaxContextWindow int `json:"max_context_window,omitempty"`
	// MaxOutputTokens is the output cap, when known.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// Source says where the numbers came from.
	Source ContextSource `json:"source,omitempty"`
	// Mode says how the budget was chosen.
	Mode ContextMode `json:"context_mode,omitempty"`
	// Verified is true only for a number that came from the operator,
	// the provider or a local runtime. A guessed number is never
	// verified.
	Verified bool `json:"verified,omitempty"`
	// FetchedAt is when the record was produced.
	FetchedAt time.Time `json:"fetched_at,omitempty"`
	// Note explains the record to the operator.
	Note string `json:"note,omitempty"`
}

// Provisional context defaults.
//
// When nothing is known, HAL keeps working with a small conservative
// budget and says so. The number is deliberately modest: a turn that
// exceeds it is far more likely than a turn that is rejected for asking
// for too much.
const (
	// ProvisionalContextWindow is the context budget used when no
	// authoritative number is known.
	ProvisionalContextWindow = 8192
	// ProvisionalMaxOutputTokens is the output cap used with it.
	ProvisionalMaxOutputTokens = 4096
)

// ProvisionalContext returns the conservative record used when nothing
// is known. It is never labelled verified.
func ProvisionalContext() ContextMetadata {
	return ContextMetadata{
		ContextWindow:   ProvisionalContextWindow,
		MaxOutputTokens: ProvisionalMaxOutputTokens,
		Source:          ContextSourceProvisional,
		Mode:            ContextModeProvisional,
		Verified:        false,
		Note: "the endpoint did not report a context window; using a conservative " +
			"budget until the operator sets one",
	}
}

// ContextInputs collects the candidate context records.
//
// Each slot holds one record; a nil slot means "nothing known from this
// source". The operator's limit, the provider's advertisement, the local
// runtime's allocation and the cached record are four separate things
// and are never merged into one number before they are resolved.
type ContextInputs struct {
	// Operator is the limit the operator configured.
	Operator *ContextMetadata
	// Provider is what the endpoint advertised.
	Provider *ContextMetadata
	// Local is what a local runtime allocated.
	Local *ContextMetadata
	// Cached is what was verified earlier and stored.
	Cached *ContextMetadata
}

// ResolveContext decides the budget HAL must respect.
//
// The order is deliberate:
//
//  1. the operator's own limit always wins;
//  2. a local runtime's allocation is the budget in force for a local
//     endpoint, because that is what the runtime can actually accept;
//  3. the provider's advertised maximum is the budget when there is no
//     local allocation;
//  4. a cached, previously verified record is used next;
//  5. otherwise the conservative provisional budget is used, and it is
//     never labelled verified.
//
// The advertised maximum is carried along in MaxContextWindow whenever it
// is known, even when a smaller budget is in force.
func ResolveContext(in ContextInputs) ContextMetadata {
	in = normalizeContextInputs(in)
	var advertised *ContextMetadata
	if in.Provider != nil && in.Provider.ContextWindow > 0 {
		advertised = in.Provider
	}
	switch {
	case in.Operator != nil && in.Operator.ContextWindow > 0:
		out := *in.Operator
		out.Source = ContextSourceOperator
		out.Mode = ContextModeConfigured
		out.Verified = true
		if advertised != nil {
			out.MaxContextWindow = advertised.ContextWindow
			if out.MaxOutputTokens == 0 {
				out.MaxOutputTokens = advertised.MaxOutputTokens
			}
		}
		return out
	case in.Local != nil && in.Local.ContextWindow > 0:
		out := *in.Local
		out.Source = ContextSourceLocal
		out.Mode = ContextModeLocal
		out.Verified = true
		if advertised != nil {
			out.MaxContextWindow = advertised.ContextWindow
			if out.MaxOutputTokens == 0 {
				out.MaxOutputTokens = advertised.MaxOutputTokens
			}
		}
		return out
	case advertised != nil:
		out := *advertised
		out.Source = ContextSourceProvider
		out.Mode = ContextModeMax
		out.Verified = true
		return out
	case in.Cached != nil && in.Cached.ContextWindow > 0:
		out := *in.Cached
		out.Source = ContextSourceCached
		out.Mode = ContextModeMax
		out.Verified = in.Cached.Verified
		if out.Note == "" {
			out.Note = "reusing the context metadata verified earlier"
		}
		return out
	}
	return ProvisionalContext()
}

// normalizeContextInputs moves a record into the slot its source
// describes, so a caller cannot place a local allocation in the
// provider slot and get a different answer by accident.
func normalizeContextInputs(in ContextInputs) ContextInputs {
	out := in
	if in.Provider != nil && in.Provider.Source == ContextSourceLocal {
		out.Local = in.Provider
		out.Provider = nil
	}
	if in.Local != nil && in.Local.Source == ContextSourceProvider {
		out.Provider = in.Local
		out.Local = nil
	}
	return out
}

// --- discoverer -------------------------------------------------------

// NormalizeWireProtocol maps the protocol names an operator may write
// onto the wire protocols HAL speaks.
//
// The mapping is explicit. HAL never infers a protocol from a URL, and
// it never guesses one from a model identifier either: an unknown name
// is an error that names the protocols HAL does speak.
func NormalizeWireProtocol(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", WireOpenAI, "openai", "local":
		return WireOpenAI, nil
	case WireAnthropic, "anthropic":
		return WireAnthropic, nil
	}
	return "", fmt.Errorf("provider: unsupported wire protocol %q (supported: %s, %s)",
		strings.TrimSpace(raw), WireOpenAI, WireAnthropic)
}

// Endpoint is the provider-side view of one configured endpoint.
//
// The operator-facing categories live in the configuration package,
// which owns them. What the provider side needs to know is which wire
// protocol HAL speaks to the endpoint, and whether the endpoint is a
// local runtime that may report the context it allocated.
type Endpoint struct {
	// Wire is the wire protocol HAL speaks: openai-compatible or
	// anthropic-compatible.
	Wire string
	// Category is the declared category: openai-compatible,
	// anthropic-compatible, local or custom. A local endpoint may
	// report the context it allocated.
	Category string
	// BaseURL is the API root.
	BaseURL string
	// Credential is the secret. It travels in a header and never
	// appears in an error, a notice or a log.
	Credential string
	// KeyHeader names the header the credential travels in. Empty uses
	// the protocol's own header.
	KeyHeader string
}

// Discoverer answers the operator's questions about one endpoint.
type Discoverer interface {
	// ListModels returns the models the endpoint offers. It never
	// changes the active model.
	ListModels(ctx context.Context, opts DiscoveryOptions) ([]ModelInfo, error)
	// ContextFor returns the context record the endpoint itself can
	// produce for a model: what it advertised, or what a local runtime
	// allocated. When it knows nothing, the conservative provisional
	// record is returned.
	ContextFor(ctx context.Context, modelID string) (ContextMetadata, error)
}

// endpointDiscoverer is the Discoverer for one configured endpoint.
type endpointDiscoverer struct {
	endpoint Endpoint
	provider Provider
	catalog  ModelCatalog
	local    *localRuntime
}

// NewDiscoverer builds the discovery service for one endpoint.
func NewDiscoverer(e Endpoint, opts Options) (Discoverer, error) {
	wire := strings.TrimSpace(e.Wire)
	if wire == "" {
		wire = WireOpenAI
	}
	impl, err := NewForProtocol(wire, e.BaseURL, e.Credential, opts)
	if err != nil {
		return nil, err
	}
	d := &endpointDiscoverer{endpoint: e, provider: impl}
	if catalog, ok := impl.(ModelCatalog); ok {
		d.catalog = catalog
	}
	// A gateway may want the credential in a different header. The
	// protocol's own header is the default; this is set explicitly and
	// never inferred from the URL.
	if header := strings.TrimSpace(e.KeyHeader); header != "" {
		if a, ok := impl.(*AnthropicCompatible); ok {
			a.KeyHeader = header
		}
	}
	if strings.EqualFold(strings.TrimSpace(e.Category), "local") {
		d.local = newLocalRuntime(e, opts)
	}
	return d, nil
}

// ListModels implements Discoverer.
func (d *endpointDiscoverer) ListModels(ctx context.Context, opts DiscoveryOptions) ([]ModelInfo, error) {
	if d.local != nil {
		models, err := d.local.ListModels(ctx)
		if err == nil && len(models) > 0 {
			return applyDiscoveryOptions(models, opts), nil
		}
		if err != nil && !errors.Is(err, ErrNoModelListing) {
			return nil, err
		}
	}
	if d.catalog == nil {
		return nil, ErrNoModelListing
	}
	return Discover(ctx, d.catalog, opts)
}

// ContextFor implements Discoverer.
func (d *endpointDiscoverer) ContextFor(ctx context.Context, modelID string) (ContextMetadata, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return ContextMetadata{}, fmt.Errorf("provider: a model id is required")
	}
	advertised := ContextMetadata{}
	if d.catalog != nil {
		models, err := Discover(ctx, d.catalog, DiscoveryOptions{Search: modelID})
		if err != nil && !errors.Is(err, ErrNoModelListing) {
			return ContextMetadata{}, err
		}
		for _, m := range models {
			if m.ID != modelID {
				continue
			}
			if m.ContextWindow > 0 {
				advertised = ContextMetadata{
					ContextWindow:   m.ContextWindow,
					MaxOutputTokens: m.MaxOutputTokens,
					Source:          ContextSourceProvider,
					Mode:            ContextModeMax,
					Verified:        true,
					FetchedAt:       time.Now(),
					Note:            "advertised by the endpoint",
				}
			}
			break
		}
	}
	if d.local != nil {
		local, err := d.local.ContextFor(ctx, modelID)
		if err != nil && !errors.Is(err, ErrNoModelListing) {
			return ContextMetadata{}, err
		}
		if err == nil && local.ContextWindow > 0 {
			local.MaxContextWindow = advertised.ContextWindow
			if local.MaxOutputTokens == 0 {
				local.MaxOutputTokens = advertised.MaxOutputTokens
			}
			return local, nil
		}
	}
	if advertised.ContextWindow > 0 {
		return advertised, nil
	}
	return ProvisionalContext(), nil
}
