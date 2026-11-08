package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/tui"
)

// The recording provider.
//
// Two endpoints, two servers, two credentials. Every request is recorded
// with the path it arrived on and the credential it carried, so a test
// can prove which endpoint a request went to and which key it used.

// recordedRequest is one request a recording provider received.
type recordedRequest struct {
	Path   string
	Auth   string
	APIKey string
	Body   map[string]any
}

// recordingProvider is a local endpoint that records what it receives.
type recordingProvider struct {
	mu       sync.Mutex
	requests []recordedRequest
	// URL is the address of the recording endpoint.
	URL string
	// answer is what the provider replies with.
	answer string
}

// newRecordingProvider starts a local endpoint that records its requests.
//
// The endpoint answers in the shape the wire protocol expects: the
// Anthropic messages dialect and the OpenAI chat-completions dialect
// are different documents, and a recording endpoint that answered both
// in one shape would prove nothing about the protocol HAL speaks.
func newRecordingProvider(t *testing.T, answer string) (*recordingProvider, *httptest.Server) {
	t.Helper()
	rec := &recordingProvider{answer: answer}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			APIKey: r.Header.Get("x-api-key"),
			Body:   body,
		})
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			fmt.Fprintf(w, `{"content":[{"type":"text","text":%q}],"stop_reason":"end_turn"}`, rec.answer)
		case strings.HasSuffix(r.URL.Path, "/models"):
			fmt.Fprint(w, `{"data":[{"id":"recorded-model"}]}`)
		default:
			fmt.Fprintf(w, `{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, rec.answer)
		}
	}))
	t.Cleanup(server.Close)
	rec.URL = server.URL
	return rec, server
}

// recorded returns the requests the provider received.
func (r *recordingProvider) recorded() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.requests...)
}

// count returns the number of requests the provider received.
func (r *recordingProvider) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// reset forgets the recorded requests.
func (r *recordingProvider) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = nil
}

// twoEndpointApp builds an application with two configured endpoints, each
// with its own credential reference, and its own recording provider.
//
// The active endpoint is the first one.
func twoEndpointApp(t *testing.T) (*app, *recordingProvider, *recordingProvider) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("HAL_TEST_KEY_ALPHA", "KEY_ALPHA")
	t.Setenv("HAL_TEST_KEY_BETA", "KEY_BETA")

	alpha, _ := newRecordingProvider(t, "alpha-answer")
	beta, _ := newRecordingProvider(t, "beta-answer")

	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{
		{
			ID:            "alpha",
			Name:          "Alpha",
			Protocol:      config.ProtocolOpenAI,
			BaseURL:       alpha.URL + "/v1",
			CredentialEnv: "HAL_TEST_KEY_ALPHA",
			Models: []config.ModelRecord{
				{ID: "alpha-model", ContextMode: config.ContextAuto},
			},
		},
		{
			ID:            "beta",
			Name:          "Beta",
			Protocol:      config.ProtocolOpenAI,
			BaseURL:       beta.URL + "/v1",
			CredentialEnv: "HAL_TEST_KEY_BETA",
			Models: []config.ModelRecord{
				{ID: "beta-model", ContextMode: config.ContextAuto},
			},
		},
	}
	stored.SetActive("alpha", "alpha-model")

	a := newRuntimeApp(t, stored)
	return a, alpha, beta
}

// newRuntimeApp builds an application around a stored configuration, the
// way a launch does.
func newRuntimeApp(t *testing.T, stored config.FileConfig) *app {
	t.Helper()
	store, err := session.Open(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{
		cfg: config.Config{
			Provider:     "openai-compatible",
			StoreDir:     t.TempDir(),
			WorkspaceDir: t.TempDir(),
		},
		stored:  stored,
		store:   store,
		session: "test",
	}
	a.rebuildEngine()
	return a
}

// tuiEndpointSpec describes an endpoint for the management commands.
func tuiEndpointSpec(id, name, baseURL, credentialEnv string) tui.EndpointSpec {
	return tui.EndpointSpec{
		ID:            id,
		Name:          name,
		Protocol:      string(config.ProtocolOpenAI),
		BaseURL:       baseURL,
		CredentialEnv: credentialEnv,
		Discovery:     true,
	}
}

// onboardData describes an onboarding answer.
func onboardData(endpoint, model, key string) tui.OnboardData {
	return tui.OnboardData{Endpoint: endpoint, Model: model, Key: key}
}

// ask sends one turn through the application and returns the answer.
func ask(t *testing.T, a *app, text string) string {
	t.Helper()
	answer, err := a.eng.RespondStream(context.Background(), text, nil)
	if err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	return answer
}

// A switch to another endpoint must change the endpoint, the credential
// and the model together. A request meant for beta must never reach
// alpha.
func TestSwitchingEndpointChangesTheRuntime(t *testing.T) {
	a, alpha, beta := twoEndpointApp(t)
	ask(t, a, "hello alpha")
	if alpha.count() != 1 {
		t.Fatalf("alpha received %d requests, want 1", alpha.count())
	}
	alpha.reset()
	beta.reset()

	if _, err := a.switchModel("beta", "beta-model"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	ask(t, a, "hello beta")

	if alpha.count() != 0 {
		t.Errorf("the request intended for beta reached alpha %d time(s)", alpha.count())
	}
	if beta.count() != 1 {
		t.Fatalf("beta received %d requests, want 1", beta.count())
	}
	request := beta.recorded()[0]
	if request.Auth != "Bearer KEY_BETA" {
		t.Errorf("the credential = %q, want beta's", request.Auth)
	}
	if request.Body["model"] != "beta-model" {
		t.Errorf("the model = %v, want beta-model", request.Body["model"])
	}
	if !strings.HasSuffix(request.Path, "/chat/completions") {
		t.Errorf("the path = %q", request.Path)
	}
}

// The credential of an endpoint is its own. The global variable must
// never substitute it.
func TestEachEndpointKeepsItsOwnCredential(t *testing.T) {
	t.Setenv("HAL9001_API_KEY", "THE_GLOBAL_KEY")
	a, alpha, beta := twoEndpointApp(t)
	ask(t, a, "hello alpha")
	ask(t, a, "still alpha")
	for _, request := range alpha.recorded() {
		if request.Auth != "Bearer KEY_ALPHA" {
			t.Errorf("alpha received the credential %q, want its own", request.Auth)
		}
	}
	if _, err := a.switchModel("beta", "beta-model"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	ask(t, a, "hello beta")
	for _, request := range beta.recorded() {
		if request.Auth != "Bearer KEY_BETA" {
			t.Errorf("beta received the credential %q, want its own", request.Auth)
		}
	}
}

// The global variable is the fallback for an endpoint that has no
// credential of its own, and never for one that has.
func TestTheGlobalCredentialIsOnlyAFallback(t *testing.T) {
	t.Setenv("HAL9001_API_KEY", "THE_GLOBAL_KEY")
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	rec, _ := newRecordingProvider(t, "ok")

	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{
		{
			ID: "bare", Name: "Bare", Protocol: config.ProtocolOpenAI,
			BaseURL: rec.URL + "/v1",
			Models:  []config.ModelRecord{{ID: "m", ContextMode: config.ContextAuto}},
		},
	}
	stored.SetActive("bare", "m")
	a := newRuntimeApp(t, stored)
	ask(t, a, "hello")
	requests := rec.recorded()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	if requests[0].Auth != "Bearer THE_GLOBAL_KEY" {
		t.Errorf("the credential = %q, want the global fallback", requests[0].Auth)
	}
}

// Editing the active endpoint must change the runtime: the address and
// the credential the next request uses.
func TestEditingTheActiveEndpointChangesTheRuntime(t *testing.T) {
	a, alpha, beta := twoEndpointApp(t)
	ask(t, a, "hello")
	alpha.reset()
	beta.reset()

	// The endpoint moves to the other server and takes another key.
	if err := a.saveEndpoint(tuiEndpointSpec("alpha", "Alpha", beta.URL+"/v1", "HAL_TEST_KEY_BETA")); err != nil {
		t.Fatalf("edit endpoint: %v", err)
	}
	ask(t, a, "hello again")

	if alpha.count() != 0 {
		t.Errorf("the request reached the old endpoint %d time(s)", alpha.count())
	}
	if beta.count() != 1 {
		t.Fatalf("the new endpoint received %d requests, want 1", beta.count())
	}
	request := beta.recorded()[0]
	if request.Auth != "Bearer KEY_BETA" {
		t.Errorf("the credential = %q, want the edited one", request.Auth)
	}
}

// A switch that cannot be used is refused: the last working runtime and
// the stored selection both stay.
func TestAFailedSwitchKeepsTheLastRuntime(t *testing.T) {
	a, alpha, _ := twoEndpointApp(t)
	ask(t, a, "hello")
	alpha.reset()

	// An endpoint whose protocol HAL does not implement cannot be used.
	// The record is edited directly, the way a hand-edited file would
	// carry it: the point is that a configuration that cannot be used
	// never becomes the running one.
	for i := range a.stored.Endpoints {
		if a.stored.Endpoints[i].ID == "beta" {
			a.stored.Endpoints[i].Protocol = "grpc-protocol"
		}
	}
	if _, err := a.switchModel("beta", "beta-model"); err == nil {
		t.Fatal("a switch to an unusable endpoint was accepted")
	}
	if a.stored.ActiveEndpointID() != "alpha" || a.stored.ActiveModelID() != "alpha-model" {
		t.Errorf("the selection changed to %s/%s", a.stored.ActiveEndpointID(), a.stored.ActiveModelID())
	}
	// The last working runtime still works.
	ask(t, a, "still here")
	if alpha.count() != 1 {
		t.Errorf("the last working runtime sent %d requests, want 1", alpha.count())
	}
}

// Deleting an endpoint must not let it come back at the next launch.
func TestADeletedEndpointDoesNotComeBack(t *testing.T) {
	a, alpha, beta := twoEndpointApp(t)
	ask(t, a, "hello")
	alpha.reset()
	beta.reset()

	if err := a.deleteEndpoint("alpha", "beta/beta-model"); err != nil {
		t.Fatalf("delete endpoint: %v", err)
	}
	if _, ok := a.stored.EndpointByID("alpha"); ok {
		t.Fatal("the endpoint was not deleted")
	}
	// A fresh launch reads the file and must not resolve the deleted
	// endpoint.
	reloaded := reloadApp(t)
	if reloaded.stored.ActiveEndpointID() == "alpha" {
		t.Error("the deleted endpoint is active again")
	}
	if _, ok := reloaded.stored.EndpointByID("alpha"); ok {
		t.Error("the deleted endpoint is back in the configuration")
	}
	ask(t, reloaded, "hello after the restart")
	if alpha.count() != 0 {
		t.Errorf("a request reached the deleted endpoint %d time(s)", alpha.count())
	}
	if beta.count() != 1 {
		t.Errorf("the new endpoint received %d requests, want 1", beta.count())
	}
}

// A replacement that lives inside the endpoint being deleted is refused:
// it would leave a selection that points at nothing.
func TestDeleteEndpointRefusesAReplacementInsideIt(t *testing.T) {
	a, _, _ := twoEndpointApp(t)
	err := a.deleteEndpoint("alpha", "alpha/alpha-model")
	if err == nil {
		t.Fatal("a replacement inside the deleted endpoint was accepted")
	}
	if !strings.Contains(err.Error(), "inside") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
	if _, ok := a.stored.EndpointByID("alpha"); !ok {
		t.Error("the endpoint was deleted anyway")
	}
}

// Deleting the last endpoint clears the selection explicitly.
func TestDeletingTheLastEndpointClearsTheSelection(t *testing.T) {
	a, _, _ := twoEndpointApp(t)
	if err := a.deleteEndpoint("beta", ""); err != nil {
		t.Fatalf("delete endpoint: %v", err)
	}
	if err := a.deleteEndpoint("alpha", ""); err != nil {
		t.Fatalf("delete endpoint: %v", err)
	}
	if a.stored.Selection != nil {
		t.Errorf("the selection is still %+v", a.stored.Selection)
	}
	// The mirror does not keep the deleted endpoint's address.
	if a.stored.Endpoint != "" {
		t.Errorf("the mirror still holds %q", a.stored.Endpoint)
	}
	reloaded := reloadApp(t)
	if reloaded.stored.Selection != nil {
		t.Errorf("the selection came back after the restart: %+v", reloaded.stored.Selection)
	}
	target, err := reloaded.resolveTarget(reloaded.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.BaseURL != "" {
		t.Errorf("a configuration without an endpoint resolves %q", target.BaseURL)
	}
}

// The onboarding keeps the protocol the operator selected.
func TestOnboardingKeepsTheAnthropicProtocol(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv(config.EnvProvider, "anthropic-compatible")
	rec, _ := newRecordingProvider(t, "ok")

	a := newRuntimeApp(t, config.FileConfig{})
	if _, err := a.saveOnboard(onboardData(rec.URL+"/anthropic", "claude-test", "ANTHROPIC_KEY")); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	endpoint, ok := a.stored.ActiveEndpoint()
	if !ok {
		t.Fatal("no endpoint was created")
	}
	if endpoint.Protocol != config.ProtocolAnthropic {
		t.Errorf("the protocol = %q, want anthropic-compatible", endpoint.Protocol)
	}
	wire, err := endpoint.EffectiveWireProtocol()
	if err != nil {
		t.Fatalf("wire protocol: %v", err)
	}
	if wire != "anthropic-compatible" {
		t.Errorf("the wire protocol = %q", wire)
	}
	ask(t, a, "hello")
	requests := rec.recorded()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	if !strings.HasSuffix(requests[0].Path, "/messages") {
		t.Errorf("the path = %q, want the Anthropic messages path", requests[0].Path)
	}
	if requests[0].APIKey != "ANTHROPIC_KEY" {
		t.Errorf("the credential = %q", requests[0].APIKey)
	}
}

// An endpoint whose protocol contradicts its wire protocol is refused.
func TestAnInconsistentProtocolIsRefused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv(config.EnvProvider, "anthropic-compatible")

	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{{
		ID: "x", Name: "X", Protocol: config.ProtocolAnthropic,
		WireProtocol: "grpc", BaseURL: "https://example.invalid/v1",
		Models: []config.ModelRecord{{ID: "m"}},
	}}
	stored.SetActive("x", "m")
	a := newRuntimeApp(t, stored)

	if _, err := a.saveOnboard(onboardData("https://example.invalid/v1", "m", "k")); err == nil {
		t.Fatal("an inconsistent endpoint was accepted")
	}
}

// The legacy configuration is still migrated, and the migrated endpoint
// keeps its credential reference.
func TestTheMigratedConfigurationKeepsItsCredentialReference(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv("HAL9001_CONFIG", path)
	t.Setenv("HAL_TEST_KEY_LEGACY", "KEY_LEGACY")
	body := `{"provider":"openai-compatible","endpoint":"https://api.example.invalid/v1",` +
		`"api_key":"$HAL_TEST_KEY_LEGACY","models":[{"id":"legacy-model"}],"active_model":"legacy-model"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := config.Migrate(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !report.Migrated {
		t.Fatal("the configuration was not migrated")
	}
	fc, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(fc.Endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(fc.Endpoints))
	}
	if fc.Endpoints[0].CredentialEnv != "HAL_TEST_KEY_LEGACY" {
		t.Errorf("the credential reference = %q", fc.Endpoints[0].CredentialEnv)
	}
	if fc.Endpoints[0].Credential != "" {
		t.Error("the reference became a literal secret")
	}
	if fc.ActiveModelID() != "legacy-model" {
		t.Errorf("the active model = %q", fc.ActiveModelID())
	}
	// The migrated configuration drives the runtime: the endpoint's own
	// credential is used, not a global one.
	t.Setenv("HAL9001_API_KEY", "THE_GLOBAL_KEY")
	a := newRuntimeApp(t, fc)
	target, err := a.resolveTarget(fc)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Credential != "KEY_LEGACY" {
		t.Errorf("the credential = %q, want the migrated endpoint's own", target.Credential)
	}
}

// reloadApp reads the configuration file again and builds a fresh
// application, exactly as a restart would.
func reloadApp(t *testing.T) *app {
	t.Helper()
	stored, err := config.LoadFile(config.ConfigPath())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return newRuntimeApp(t, stored)
}
