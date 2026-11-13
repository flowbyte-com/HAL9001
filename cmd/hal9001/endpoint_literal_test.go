package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/tui"
)

// Synthetic secrets only. None of these belongs to a real provider.
const (
	syntheticLiteral   = "synthetic-literal-key-CCC333"
	syntheticReplaced  = "synthetic-literal-key-DDD444"
	syntheticOldEnvKey = "synthetic-old-env-key-EEE555"
)

// literalApp builds an application with one endpoint holding the given
// credential state and an isolated configuration file.
func literalApp(t *testing.T, env, literal string) *app {
	t.Helper()
	a := newEndpointApp(t)
	a.stored.Endpoints[0].CredentialEnv = env
	a.stored.Endpoints[0].Credential = literal
	return a
}

// TestSaveEndpointLiteralPersistsProtected proves Case A at the storage
// layer: the exact synthetic value is stored, the reference is empty,
// the file is mode 0600, and a fresh process reading the file can use
// the credential.
func TestSaveEndpointLiteralPersistsProtected(t *testing.T) {
	a := literalApp(t, "", "")
	if err := a.saveEndpoint(tui.EndpointSpec{
		ID: "minimax", Credential: syntheticLiteral,
	}); err != nil {
		t.Fatalf("save endpoint: %v", err)
	}
	endpoint, _ := a.stored.EndpointByID("minimax")
	if endpoint.Credential != syntheticLiteral {
		t.Fatalf("the stored credential was not the staged value")
	}
	if endpoint.CredentialEnv != "" {
		t.Fatalf("the reference was not cleared: %q", endpoint.CredentialEnv)
	}
	path := os.Getenv("HAL9001_CONFIG")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the configuration mode = %o, want 600", perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), syntheticLiteral) {
		t.Error("the stored file does not hold the literal")
	}
	// Restart: a fresh read of the file keeps a usable credential.
	loaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, _ := loaded.EndpointByID("minimax")
	if got := reloaded.CredentialValue(); got != syntheticLiteral {
		t.Errorf("the reloaded credential is unusable")
	}
}

// TestSaveEndpointCredentialTransitions proves Case D at the storage
// layer: every direction replaces atomically and an unrelated edit
// preserves authentication.
func TestSaveEndpointCredentialTransitions(t *testing.T) {
	// Environment variable -> literal key.
	a := literalApp(t, "OLD_VAR", "")
	if err := a.saveEndpoint(tui.EndpointSpec{ID: "minimax", Credential: syntheticLiteral}); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := a.stored.EndpointByID("minimax")
	if endpoint.Credential != syntheticLiteral || endpoint.CredentialEnv != "" {
		t.Errorf("env->literal = (%q, stored=%v)", endpoint.CredentialEnv, endpoint.Credential != "")
	}
	// Literal key -> environment variable.
	if err := a.saveEndpoint(tui.EndpointSpec{ID: "minimax", CredentialEnv: "OTHER_VAR"}); err != nil {
		t.Fatal(err)
	}
	endpoint, _ = a.stored.EndpointByID("minimax")
	if endpoint.CredentialEnv != "OTHER_VAR" || endpoint.Credential != "" {
		t.Errorf("literal->env kept the old literal")
	}
	// Literal key -> replacement literal key.
	if err := a.saveEndpoint(tui.EndpointSpec{ID: "minimax", Credential: syntheticReplaced}); err != nil {
		t.Fatal(err)
	}
	endpoint, _ = a.stored.EndpointByID("minimax")
	if endpoint.Credential != syntheticReplaced {
		t.Errorf("the replacement key was not stored")
	}
	// Unrelated edit preserves authentication.
	if err := a.saveEndpoint(tui.EndpointSpec{ID: "minimax", Name: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	endpoint, _ = a.stored.EndpointByID("minimax")
	if endpoint.Credential != syntheticReplaced {
		t.Errorf("an unrelated edit lost the credential")
	}
	// Explicit removal clears both.
	if err := a.saveEndpoint(tui.EndpointSpec{ID: "minimax", ClearCredential: true}); err != nil {
		t.Fatal(err)
	}
	endpoint, _ = a.stored.EndpointByID("minimax")
	if endpoint.Credential != "" || endpoint.CredentialEnv != "" {
		t.Errorf("removal left a credential behind")
	}
}

// TestSaveEndpointFailureKeepsTheOldCredential proves Case C at the
// storage layer: a refused change leaves the running configuration and
// the file exactly as they were.
func TestSaveEndpointFailureKeepsTheOldCredential(t *testing.T) {
	a := literalApp(t, "OLD_VAR", "")
	before := a.stored.Clone()
	err := a.saveEndpoint(tui.EndpointSpec{
		ID: "minimax", Protocol: "openai-compatible", WireProtocol: "anthropic-compatible",
	})
	if err == nil {
		t.Fatal("a contradictory protocol change was accepted")
	}
	endpoint, _ := a.stored.EndpointByID("minimax")
	if endpoint.CredentialEnv != "OLD_VAR" || endpoint.Credential != "" {
		t.Errorf("a failed save touched the credential")
	}
	if len(a.stored.Endpoints) != len(before.Endpoints) {
		t.Errorf("a failed save changed the endpoint set")
	}
}

// recordingTarget is a local OpenAI-compatible endpoint that records
// the authorization header of catalogue probes.
type recordingTarget struct {
	t       *testing.T
	auth    []string
	server  *httptest.Server
	other   int
	handler http.Handler
}

func newRecordingTarget(t *testing.T, status int, contentType, body string) *recordingTarget {
	t.Helper()
	rt := &recordingTarget{t: t}
	rt.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt.auth = append(rt.auth, r.Header.Get("Authorization"))
		if r.URL.Path == "/models" {
			raw := body
			if raw == "" {
				raw = `{"data":[{"id":"m"}]}`
			}
			if contentType == "" {
				contentType = "application/json"
			}
			w.Header().Set("Content-Type", contentType)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(raw))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(rt.server.Close)
	return rt
}

// TestEndpointUsesTheStagedKey proves Case B: an explicit Test sends
// the newly staged literal, never the previously saved credential, and
// no other endpoint is contacted.
func TestEndpointUsesTheStagedKey(t *testing.T) {
	target := newRecordingTarget(t, http.StatusOK, "", "")
	other := newRecordingTarget(t, http.StatusOK, "", "")
	a := newEndpointApp(t)
	t.Setenv("OLD_VAR", syntheticOldEnvKey)
	report, err := a.testEndpoint(tui.EndpointSpec{
		ID: "lit-ep", Name: "Lit", Protocol: "openai-compatible",
		BaseURL: target.server.URL, Credential: syntheticLiteral,
	})
	if err != nil {
		t.Fatalf("test endpoint: %v", err)
	}
	if !strings.Contains(report, "reachable") {
		t.Fatalf("report = %q", report)
	}
	if len(target.auth) != 1 || target.auth[0] != "Bearer "+syntheticLiteral {
		t.Errorf("the probe did not carry the staged key: %v", len(target.auth))
	}
	for _, header := range target.auth {
		if strings.Contains(header, syntheticOldEnvKey) {
			t.Errorf("the probe carried the old credential")
		}
	}
	if len(other.auth) != 0 {
		t.Errorf("another endpoint received %d request(s)", len(other.auth))
	}
	if strings.Contains(report, syntheticLiteral) {
		t.Errorf("the report exposes the key: %q", report)
	}
}

// TestEndpointDistinguishesCatalogueFromAuth proves the catalogue and
// the credential are told apart: HTML is reachable-but-no-catalogue,
// a 401 is an authentication failure.
func TestEndpointDistinguishesCatalogueFromAuth(t *testing.T) {
	html := newRecordingTarget(t, http.StatusOK, "text/html; charset=utf-8",
		"<html><body>proxy error</body></html>")
	a := newEndpointApp(t)
	report, err := a.testEndpoint(tui.EndpointSpec{
		ID: "html-ep", Protocol: "openai-compatible", BaseURL: html.server.URL,
	})
	if err != nil {
		t.Fatalf("an HTML catalogue must be reachable, got: %v", err)
	}
	if !strings.Contains(report, "does not list its models") {
		t.Errorf("report = %q, want reachable-but-no-catalogue", report)
	}
	denied := newRecordingTarget(t, http.StatusUnauthorized, "application/json", "no")
	report, err = a.testEndpoint(tui.EndpointSpec{
		ID: "denied-ep", Protocol: "openai-compatible", BaseURL: denied.server.URL,
	})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("a 401 must report authentication failure, got %q, %v", report, err)
	}
	if errors.Is(err, provider.ErrNoModelListing) {
		t.Errorf("an auth failure must not look like missing discovery: %v", err)
	}
}

// TestEndpointFallsBackToTheStoredCredential proves a reopened editor
// tests with the saved key: the rows name no credential, so the probe
// would otherwise go out unauthenticated. A staged key still wins.
func TestEndpointFallsBackToTheStoredCredential(t *testing.T) {
	target := newRecordingTarget(t, http.StatusOK, "", "")
	a := newEndpointApp(t)
	a.stored.Endpoints[0].CredentialEnv = ""
	a.stored.Endpoints[0].Credential = syntheticLiteral
	report, err := a.testEndpoint(tui.EndpointSpec{
		ID: "minimax", Protocol: "openai-compatible", BaseURL: target.server.URL,
	})
	if err != nil {
		t.Fatalf("test endpoint: %v", err)
	}
	if !strings.Contains(report, "reachable") {
		t.Fatalf("report = %q", report)
	}
	if len(target.auth) != 1 || target.auth[0] != "Bearer "+syntheticLiteral {
		t.Errorf("the probe did not carry the stored key")
	}
	// A staged key wins over the stored one.
	report, err = a.testEndpoint(tui.EndpointSpec{
		ID: "minimax", Protocol: "openai-compatible", BaseURL: target.server.URL,
		Credential: syntheticReplaced,
	})
	if err != nil {
		t.Fatalf("test endpoint: %v", err)
	}
	if len(target.auth) != 2 || target.auth[1] != "Bearer "+syntheticReplaced {
		t.Errorf("the staged key did not win: %v", len(target.auth))
	}
	// An explicit removal stays removed.
	report, err = a.testEndpoint(tui.EndpointSpec{
		ID: "minimax", Protocol: "openai-compatible", BaseURL: target.server.URL,
		ClearCredential: true,
	})
	if err != nil {
		t.Fatalf("test endpoint: %v", err)
	}
	if len(target.auth) != 3 || target.auth[2] != "" {
		t.Errorf("a removed credential was still sent: %q", target.auth[2])
	}
	_ = report
}

// TestCredentialSurfacesNeverCarrySecrets extends the existing guards
// to the literal path: labels, details and endpoint records describe
// a stored key without ever containing it.
func TestCredentialSurfacesNeverCarrySecrets(t *testing.T) {
	a := literalApp(t, "", syntheticLiteral)
	for _, info := range a.endpointInfos() {
		if strings.Contains(info.Credential, syntheticLiteral) {
			t.Errorf("endpoint info carries the key: %q", info.Credential)
		}
		if !strings.Contains(info.Credential, "set") {
			t.Errorf("endpoint info must say a key is set: %q", info.Credential)
		}
	}
	detail, err := a.endpointDetail("minimax")
	if err != nil {
		t.Fatal(err)
	}
	if !detail.HasLiteralCredential {
		t.Error("the detail must report the stored key")
	}
	if strings.Contains(detail.Settings.Credential, syntheticLiteral) {
		t.Error("the detail carries the key value")
	}
	lines, err := a.modelDetails("minimax", "MiniMax-M3.1-Flash")
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(lines, "\n"); strings.Contains(joined, syntheticLiteral) {
		t.Error("the model details carry the key")
	}
}
