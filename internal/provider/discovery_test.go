package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Model discovery and context discovery.
//
// The rules these tests hold HAL to: a listing is bounded and filtered,
// the identifiers are the endpoint's own, a listed model is not claimed
// to work, and a context number always says where it came from.

// stubCatalog is a ModelCatalog that answers with a fixed list.
type stubCatalog struct {
	models []ModelInfo
	err    error
	calls  int
}

func (s *stubCatalog) ListModels(context.Context) ([]ModelInfo, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.models, nil
}

func TestDiscoverFiltersAndBounds(t *testing.T) {
	catalog := &stubCatalog{models: []ModelInfo{
		{ID: "claude-sonnet-4-5"},
		{ID: "claude-opus-4-1"},
		{ID: "gpt-4o"},
		{ID: "MiniMax-M3.1-Flash"},
	}}
	models, err := Discover(context.Background(), catalog, DiscoveryOptions{Search: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v", models)
	}
	models, err = Discover(context.Background(), catalog, DiscoveryOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("the limit was not applied: %#v", models)
	}
}

func TestDiscoverKeepsTheEndpointIdentifiers(t *testing.T) {
	catalog := &stubCatalog{models: []ModelInfo{
		{ID: "vendor/Model-V2:latest"},
		{ID: "  spaced  "},
		{ID: "MiXeD-Case"},
	}}
	models, err := Discover(context.Background(), catalog, DiscoveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("models = %#v", models)
	}
	if models[0].ID != "vendor/Model-V2:latest" {
		t.Errorf("the identifier was rewritten: %q", models[0].ID)
	}
	if models[1].ID != "  spaced  " {
		t.Errorf("the identifier must be kept exactly as the endpoint wrote it: %q", models[1].ID)
	}
	if models[2].ID != "MiXeD-Case" {
		t.Errorf("the case was changed: %q", models[2].ID)
	}
}

func TestDiscoverSkipsEntriesWithoutAnIdentifier(t *testing.T) {
	catalog := &stubCatalog{models: []ModelInfo{{ID: "  "}, {ID: "real"}}}
	models, err := Discover(context.Background(), catalog, DiscoveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "real" {
		t.Fatalf("models = %#v", models)
	}
}

func TestDiscoverNeverClaimsAListedModelWorks(t *testing.T) {
	catalog := &stubCatalog{models: []ModelInfo{{ID: "m"}}}
	models, err := Discover(context.Background(), catalog, DiscoveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if models[0].Access != AccessUnknown {
		t.Errorf("access = %q, a listed model is not an accessible model", models[0].Access)
	}
}

func TestDiscoverWithoutACatalogue(t *testing.T) {
	_, err := Discover(context.Background(), nil, DiscoveryOptions{})
	if err == nil || !strings.Contains(err.Error(), "does not list its models") {
		t.Fatalf("expected a no-listing error, got %v", err)
	}
}

func TestDiscoverPropagatesTheEndpointError(t *testing.T) {
	catalog := &stubCatalog{err: fmt.Errorf("provider: authentication failed (401): check the API key")}
	_, err := Discover(context.Background(), catalog, DiscoveryOptions{})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveContextPrefersTheOperator(t *testing.T) {
	resolved := ResolveContext(ContextInputs{
		Operator: &ContextMetadata{ContextWindow: 4096},
		Provider: &ContextMetadata{ContextWindow: 200000},
		Local:    &ContextMetadata{ContextWindow: 8192},
		Cached:   &ContextMetadata{ContextWindow: 100000},
	})
	if resolved.ContextWindow != 4096 {
		t.Errorf("window = %d", resolved.ContextWindow)
	}
	if resolved.Source != ContextSourceOperator || resolved.Mode != ContextModeConfigured {
		t.Errorf("source = %q mode = %q", resolved.Source, resolved.Mode)
	}
	if !resolved.Verified {
		t.Error("an operator's own limit is verified")
	}
	if resolved.MaxContextWindow != 200000 {
		t.Errorf("the advertised maximum was lost: %d", resolved.MaxContextWindow)
	}
}

func TestResolveContextPrefersTheLocalAllocation(t *testing.T) {
	resolved := ResolveContext(ContextInputs{
		Provider: &ContextMetadata{ContextWindow: 200000},
		Local:    &ContextMetadata{ContextWindow: 8192},
	})
	if resolved.ContextWindow != 8192 {
		t.Errorf("window = %d, the local allocation is the budget in force", resolved.ContextWindow)
	}
	if resolved.MaxContextWindow != 200000 {
		t.Errorf("the advertised maximum must be kept apart: %d", resolved.MaxContextWindow)
	}
	if resolved.Source != ContextSourceLocal || resolved.Mode != ContextModeLocal {
		t.Errorf("source = %q mode = %q", resolved.Source, resolved.Mode)
	}
}

func TestResolveContextUsesTheAdvertisedMaximum(t *testing.T) {
	resolved := ResolveContext(ContextInputs{
		Provider: &ContextMetadata{ContextWindow: 200000, MaxOutputTokens: 8192},
	})
	if resolved.ContextWindow != 200000 {
		t.Errorf("window = %d", resolved.ContextWindow)
	}
	if resolved.Source != ContextSourceProvider || resolved.Mode != ContextModeMax {
		t.Errorf("source = %q mode = %q", resolved.Source, resolved.Mode)
	}
	if resolved.MaxOutputTokens != 8192 {
		t.Errorf("output = %d", resolved.MaxOutputTokens)
	}
}

func TestResolveContextFallsBackToTheCachedRecord(t *testing.T) {
	resolved := ResolveContext(ContextInputs{
		Cached: &ContextMetadata{ContextWindow: 65536, Verified: true},
	})
	if resolved.ContextWindow != 65536 {
		t.Errorf("window = %d", resolved.ContextWindow)
	}
	if resolved.Source != ContextSourceCached {
		t.Errorf("source = %q", resolved.Source)
	}
	if !resolved.Verified {
		t.Error("a cached record that was verified stays verified")
	}
}

func TestResolveContextFallsBackToTheProvisionalBudget(t *testing.T) {
	resolved := ResolveContext(ContextInputs{})
	if resolved.ContextWindow != ProvisionalContextWindow {
		t.Errorf("window = %d", resolved.ContextWindow)
	}
	if resolved.Source != ContextSourceProvisional {
		t.Errorf("source = %q", resolved.Source)
	}
	if resolved.Verified {
		t.Error("a guessed number must never be labelled verified")
	}
	if resolved.Mode != ContextModeProvisional {
		t.Errorf("mode = %q", resolved.Mode)
	}
	if resolved.Note == "" {
		t.Error("the uncertainty must be explained")
	}
}

func TestResolveContextRoutesBySource(t *testing.T) {
	// A record that arrives in the wrong slot is routed by its source,
	// so a caller cannot get a different answer by accident.
	resolved := ResolveContext(ContextInputs{
		Provider: &ContextMetadata{ContextWindow: 4096, Source: ContextSourceLocal},
	})
	if resolved.Source != ContextSourceLocal || resolved.ContextWindow != 4096 {
		t.Errorf("resolved = %#v", resolved)
	}
}

// --- the discoverer ---------------------------------------------------

func TestDiscovererListsTheEndpointModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"claude-a"},{"id":"claude-b"},{"id":"other"}]}`)
	}))
	defer srv.Close()

	d, err := NewDiscoverer(Endpoint{
		Wire:       WireOpenAI,
		Category:   "openai-compatible",
		BaseURL:    srv.URL,
		Credential: "k",
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	models, err := d.ListModels(context.Background(), DiscoveryOptions{Search: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v", models)
	}
	if models[0].Access != AccessUnknown {
		t.Errorf("access = %q", models[0].Access)
	}
}

func TestDiscovererReportsAnEndpointWithoutListing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	d, err := NewDiscoverer(Endpoint{Wire: WireOpenAI, BaseURL: srv.URL, Credential: "k"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.ListModels(context.Background(), DiscoveryOptions{})
	if err == nil || !strings.Contains(err.Error(), "does not list its models") {
		t.Fatalf("error = %v", err)
	}
}

func TestDiscovererReadsTheAdvertisedContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"m","context_length":200000,"max_output_tokens":8192}]}`)
	}))
	defer srv.Close()

	d, err := NewDiscoverer(Endpoint{Wire: WireOpenAI, BaseURL: srv.URL, Credential: "k"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := d.ContextFor(context.Background(), "m")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ContextWindow != 200000 {
		t.Errorf("window = %d", meta.ContextWindow)
	}
	if meta.MaxOutputTokens != 8192 {
		t.Errorf("output = %d", meta.MaxOutputTokens)
	}
	if meta.Source != ContextSourceProvider || !meta.Verified {
		t.Errorf("meta = %#v", meta)
	}
}

func TestDiscovererFallsBackToTheProvisionalBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
	}))
	defer srv.Close()

	d, err := NewDiscoverer(Endpoint{Wire: WireOpenAI, BaseURL: srv.URL, Credential: "k"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := d.ContextFor(context.Background(), "m")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Source != ContextSourceProvisional || meta.Verified {
		t.Errorf("meta = %#v", meta)
	}
	if meta.ContextWindow != ProvisionalContextWindow {
		t.Errorf("window = %d", meta.ContextWindow)
	}
	if meta.Note == "" {
		t.Error("the uncertainty must be explained")
	}
}

func TestDiscovererRefusesAnEmptyModelID(t *testing.T) {
	d, err := NewDiscoverer(Endpoint{Wire: WireOpenAI, BaseURL: "http://127.0.0.1:1", Credential: "k"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ContextFor(context.Background(), "  "); err == nil {
		t.Fatal("expected an error for an empty model id")
	}
}

func TestDiscovererRejectsAnUnknownWireProtocol(t *testing.T) {
	_, err := NewDiscoverer(Endpoint{Wire: "telepathy", BaseURL: "http://127.0.0.1:1"}, Options{})
	if err == nil || !strings.Contains(err.Error(), "unsupported wire protocol") {
		t.Fatalf("error = %v", err)
	}
}

// The OpenAI-compatible listing keeps working, and it now reports what
// the endpoint said about each model.
func TestOpenAICatalogueReportsTheAdvertisedContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"m","context_length":32768,"max_output_tokens":4096}]}`)
	}))
	defer srv.Close()
	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %#v", models)
	}
	if models[0].ContextWindow != 32768 || models[0].MaxOutputTokens != 4096 {
		t.Errorf("model = %#v", models[0])
	}
}
