package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The bridges.
//
// The two implementations resolved the runtime in different places: the
// model management resolved the endpoint, the credential and the model
// as one target, and the discovery implementation resolved the context
// budget beside it. These tests hold the four bridges to their promise.

// Bridge 1: the context model.
//
// The model carries both numbers, and they mean different things: the
// operator's own limit and the effective budget that is enforced.
func TestTheModelCarriesTheLimitAndTheBudget(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))

	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{{
		ID: "alpha", Name: "Alpha", Protocol: config.ProtocolOpenAI,
		BaseURL: "https://api.example.invalid/v1",
		Models: []config.ModelRecord{{
			ID: "alpha-model", ContextMode: config.ContextManual,
			ContextWindow: 22585, MaxOutputTokens: 4096,
		}},
	}}
	stored.SetActive("alpha", "alpha-model")
	a := newRuntimeApp(t, stored)

	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.ContextWindow != 22585 {
		t.Errorf("the operator's limit = %d, want 22585", target.ContextWindow)
	}
	if target.MaxTokens != 4096 {
		t.Errorf("the output cap = %d, want 4096", target.MaxTokens)
	}
	model := modelFromTarget(target)
	if model.ContextWindow != 22585 {
		t.Errorf("the model does not carry the operator's limit: %+v", model)
	}
	if model.Budget.Limit != 22585 {
		t.Errorf("the budget does not carry the operator's limit: %+v", model.Budget)
	}
	if model.Budget.Source != provider.ContextSourceOperator {
		t.Errorf("the budget does not name its source: %+v", model.Budget)
	}
	if !model.Budget.Verified {
		t.Errorf("an operator limit is verified: %+v", model.Budget)
	}
	if model.Budget.ReservedOutput != 4096 {
		t.Errorf("the answer's room = %d, want 4096", model.Budget.ReservedOutput)
	}
}

// Bridge 1: a configured manual limit reaches the outgoing request.
//
// The budget is not a number in a panel: the request the provider
// receives fits inside it.
func TestAConfiguredLimitReachesTheOutgoingRequest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	rec, _ := newRecordingProvider(t, "ok")

	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{{
		ID: "alpha", Name: "Alpha", Protocol: config.ProtocolOpenAI,
		BaseURL: rec.URL + "/v1",
		Models: []config.ModelRecord{{
			ID: "alpha-model", ContextMode: config.ContextManual,
			ContextWindow: 1500,
		}},
	}}
	stored.SetActive("alpha", "alpha-model")
	a := newRuntimeApp(t, stored)

	// A conversation long enough that it cannot fit inside the limit.
	for i := 0; i < 20; i++ {
		if _, err := a.store.AppendTurn(session.RoleUser,
			strings.Repeat("question ", 40), session.Turn{}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.store.AppendTurn(session.RoleAssistant,
			strings.Repeat("answer ", 40), session.Turn{}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := a.eng.RespondStream(context.Background(), "and now?", nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	requests := rec.recorded()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	messages, _ := requests[0].Body["messages"].([]any)
	if len(messages) >= 41 {
		t.Errorf("the whole conversation was transmitted: %d messages", len(messages))
	}
	// The current message is never the thing that is dropped.
	last, _ := messages[len(messages)-1].(map[string]any)
	content, _ := last["content"].(string)
	if !strings.Contains(content, "and now?") {
		t.Errorf("the current request was dropped: %+v", last)
	}
}

// Bridge 2: the legacy runtime.
//
// A pre-version-2 configuration keeps the active model's context and
// output settings when the runtime is resolved.
func TestTheLegacyRuntimeKeepsTheModelsLimits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))

	a := &app{
		cfg: config.Config{
			Provider: "openai-compatible",
			Endpoint: "https://api.example.invalid/v1",
			APIKey:   "k",
			Model:    "example-model",
			Models: []config.ModelRecord{{
				ID: "example-model", ContextWindow: 22585, MaxOutputTokens: 4096,
			}},
		},
		stored: config.FileConfig{
			Endpoint: "https://api.example.invalid/v1", APIKey: "k",
			ActiveModel: "example-model",
			Models: []config.ModelRecord{{
				ID: "example-model", ContextWindow: 22585, MaxOutputTokens: 4096,
			}},
		},
	}
	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.ContextWindow != 22585 {
		t.Errorf("the context window = %d, want 22585", target.ContextWindow)
	}
	if target.MaxTokens != 4096 {
		t.Errorf("the output cap = %d, want 4096", target.MaxTokens)
	}
	model := modelFromTarget(target)
	if model.MaxTokens != 4096 {
		t.Errorf("the model does not carry the output cap: %+v", model)
	}
	if model.ContextWindow != 22585 {
		t.Errorf("the model does not carry the context window: %+v", model)
	}
	if model.Budget.Limit != 22585 {
		t.Errorf("the budget does not carry the limit: %+v", model.Budget)
	}
}

// Bridge 4: the discovery onboarding.
//
// The connection test uses the sanitised catalogue, speaks the protocol
// the operator chose, and refuses a hostile identifier.
func TestTheConnectionTestUsesTheSanitisedCatalogue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv(config.EnvProvider, "anthropic-compatible")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/models") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// A hostile catalogue: an identifier that would move the
		// cursor, a display name that would forge a row, and one
		// usable identifier.
		_, _ = w.Write([]byte(`{"data":[` +
			`{"id":"\u001b[31mred","display_name":"\u001b[2Jforged"},` +
			`{"id":"claude-test","display_name":"Claude Test"}]}`))
	}))
	defer server.Close()

	a := &app{cfg: config.Config{Provider: "anthropic-compatible"}}
	summary, err := a.testOnboard(onboardData(server.URL+"/anthropic", "claude-test", "k"))
	if err != nil {
		t.Fatalf("test onboard: %v", err)
	}
	if !strings.Contains(summary, "Claude Test") {
		t.Errorf("the summary does not name the model: %q", summary)
	}
	// The hostile identifier and label never reach the summary.
	if strings.Contains(summary, "\x1b") {
		t.Errorf("the summary carries an escape sequence: %q", summary)
	}
}

// Bridge 4: the protocol is the operator's own.
//
// An Anthropic-compatible endpoint is tested with the Anthropic
// Messages protocol, and the catalogue request carries the credential
// the way the Messages API expects it.
func TestTheConnectionTestSpeaksTheOperatorsProtocol(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv(config.EnvProvider, "anthropic-compatible")

	var seen struct {
		path  string
		key   string
		ver   string
		count int
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.path = r.URL.Path
		seen.key = r.Header.Get("x-api-key")
		seen.ver = r.Header.Get("anthropic-version")
		seen.count++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-test"}]}`))
	}))
	defer server.Close()

	a := &app{cfg: config.Config{Provider: "anthropic-compatible"}}
	if _, err := a.testOnboard(onboardData(server.URL, "claude-test", "THE_KEY")); err != nil {
		t.Fatalf("test onboard: %v", err)
	}
	if !strings.HasSuffix(seen.path, "/v1/models") {
		t.Errorf("the path = %q, want the Anthropic catalogue path", seen.path)
	}
	if seen.key != "THE_KEY" {
		t.Errorf("the credential header = %q", seen.key)
	}
	if seen.ver == "" {
		t.Error("the Anthropic version header was not sent")
	}
	if seen.count != 1 {
		t.Errorf("the endpoint was asked %d times", seen.count)
	}
}

// The endpoint that cannot be used is reported, not hidden.
func TestTheConnectionTestReportsAnEndpointWithoutACatalogue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	a := &app{cfg: config.Config{Provider: "openai-compatible"}}
	summary, err := a.testOnboard(onboardData(server.URL, "m", "k"))
	if err != nil {
		t.Fatalf("test onboard: %v", err)
	}
	if !strings.Contains(summary, "does not list") {
		t.Errorf("the summary does not report the missing catalogue: %q", summary)
	}
}
