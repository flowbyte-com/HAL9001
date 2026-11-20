package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The endpoint's own facts reach the provider.
//
// The credential profile and the thinking default are facts about the
// endpoint, and they are what the provider layer needs: the profile
// tells two accounts at one address apart, and the default tells the
// provider how the endpoint thinks when the request says nothing.

// originApp builds an application over one endpoint.
func originApp(t *testing.T, baseURL string, record config.EndpointRecord,
	model config.ModelRecord) *app {
	t.Helper()
	return originAppIn(t, t.TempDir(), baseURL, record, model)
}

// originAppIn builds an application over one endpoint and one store.
func originAppIn(t *testing.T, storeDir, baseURL string, record config.EndpointRecord,
	model config.ModelRecord) *app {
	t.Helper()
	store, err := session.Open(storeDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	stored := config.FileConfig{Version: config.ConfigVersion}
	record.Models = []config.ModelRecord{model}
	stored.Endpoints = []config.EndpointRecord{record}
	stored.SetActive(record.ID, model.ID)
	a := &app{
		cfg:     config.Config{StoreDir: t.TempDir()},
		stored:  stored,
		store:   store,
		session: "test",
	}
	a.rebuildEngine()
	return a
}

// The endpoint's credential profile and thinking default reach the
// provider that serves it.
func TestTheEndpointFactsReachTheProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant",` +
			`"content":[{"type":"thinking","thinking":"the model weighed it"},` +
			`{"type":"text","text":"the answer"}],"model":"claude-sonnet-4-5",` +
			`"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	record := config.EndpointRecord{
		ID: "ep", Name: "Endpoint", Protocol: config.ProtocolAnthropic,
		WireProtocol: config.WireAnthropic, BaseURL: srv.URL,
		CredentialEnv: "TEST_API_KEY", ThinkingDefault: config.ThinkingDefaultOn,
	}
	model := config.ModelRecord{ID: "claude-sonnet-4-5"}
	a := originApp(t, srv.URL, record, model)

	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatal(err)
	}
	if target.CredentialProfile != "env:TEST_API_KEY" {
		t.Errorf("the credential profile = %q", target.CredentialProfile)
	}
	if target.ThinkingDefault != provider.ThinkingDefaultOn {
		t.Errorf("the thinking default = %v, want on", target.ThinkingDefault)
	}
	p, err := a.providerForTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	anthropic, ok := p.(*provider.AnthropicCompatible)
	if !ok {
		t.Fatalf("the provider is %T", p)
	}
	if anthropic.CredentialProfile != "env:TEST_API_KEY" {
		t.Errorf("the provider's credential profile = %q", anthropic.CredentialProfile)
	}
	if anthropic.ThinkingDefault != provider.ThinkingDefaultOn {
		t.Errorf("the provider's thinking default = %v, want on", anthropic.ThinkingDefault)
	}
	// The credential itself never reaches the profile.
	if strings.Contains(anthropic.CredentialProfile, anthropic.APIKey) &&
		anthropic.APIKey != "" {
		t.Errorf("the profile carries the credential: %q", anthropic.CredentialProfile)
	}
}

// An endpoint that says nothing keeps what HAL resolves from its
// address, and an address HAL does not know keeps its own answer.
func TestTheThinkingDefaultFallsBackToTheAddress(t *testing.T) {
	record := config.EndpointRecord{
		ID: "ep", Name: "Endpoint", Protocol: config.ProtocolAnthropic,
		WireProtocol: config.WireAnthropic, BaseURL: "https://api.deepseek.com/anthropic",
	}
	a := originApp(t, record.BaseURL, record, config.ModelRecord{ID: "claude-sonnet-4-5"})
	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatal(err)
	}
	if target.ThinkingDefault != provider.ThinkingUnknown {
		t.Errorf("the thinking default = %v, want unknown", target.ThinkingDefault)
	}
	p, err := a.providerForTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	anthropic, ok := p.(*provider.AnthropicCompatible)
	if !ok {
		t.Fatalf("the provider is %T", p)
	}
	// The address is what settles it.
	if got := provider.ThinkingDefaultFor(anthropic.Endpoint); got != provider.ThinkingDefaultOn {
		t.Errorf("the endpoint thinks by default = %v, want on", got)
	}
}

// The endpoint's own answer decides the replay, and the answer is the
// endpoint's.
//
// An endpoint that thinks by default replays the reasoning of an
// earlier turn even when the request says nothing about thinking. The
// model identifier does not decide it: the same Claude name is a Claude
// model on one endpoint and an alias on another.
func TestTheEndpointDecidesTheReplayAndNotTheModelName(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant",` +
			`"content":[{"type":"thinking","thinking":"the model weighed it"},` +
			`{"type":"tool_use","id":"call_1","name":"read","input":{}}],` +
			`"model":"claude-sonnet-4-5","stop_reason":"tool_use"}`))
	}))
	defer srv.Close()

	record := config.EndpointRecord{
		ID: "ep", Name: "Endpoint", Protocol: config.ProtocolAnthropic,
		WireProtocol: config.WireAnthropic, BaseURL: srv.URL,
		CredentialEnv: "TEST_API_KEY", ThinkingDefault: config.ThinkingDefaultOn,
	}
	a := originApp(t, srv.URL, record, config.ModelRecord{ID: "claude-sonnet-4-5"})
	if _, err := a.eng.RespondStream(context.Background(), "read the file", nil); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 {
		t.Fatalf("the provider was asked %d times", len(bodies))
	}
	// The request itself says nothing about thinking: the endpoint's
	// answer is what brought the blocks.
	if _, ok := bodies[0]["thinking"]; ok {
		t.Errorf("the request asked for thinking: %v", bodies[0]["thinking"])
	}
	// The turn is in the record, with the origin of the endpoint.
	history, err := a.store.History()
	if err != nil {
		t.Fatal(err)
	}
	var stored *session.Protocol
	for i := range history {
		if history[i].Role == session.RoleAssistant {
			stored = history[i].Protocol
		}
	}
	if stored == nil {
		t.Fatal("the answer carries no continuity data")
	}
	continuity, ok := provider.DecodeReasoning(stored.Payload)
	if !ok {
		t.Fatal("the record was not read back")
	}
	if continuity.Origin == "" {
		t.Error("the record names no origin")
	}
	if continuity.Thinking != provider.ThinkingUnset {
		t.Errorf("the thinking of the producing request = %q", continuity.Thinking)
	}
}

// The operator's own journey: the endpoint changes under a
// conversation.
//
// The reasoning of the first turn belongs to the endpoint that produced
// it. The continuation is refused before anything goes out, the new
// endpoint receives nothing, and the operator is told what happened.
func TestTheEndpointChangeUnderAConversationIsRefused(t *testing.T) {
	var firstRequests, secondRequests int
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant",` +
			`"content":[{"type":"thinking","thinking":"the model weighed it"},` +
			`{"type":"text","text":"the answer"}],"model":"claude-sonnet-4-5",` +
			`"stop_reason":"end_turn"}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant",` +
			`"content":[{"type":"text","text":"the answer"}],"model":"claude-sonnet-4-5",` +
			`"stop_reason":"end_turn"}`))
	}))
	defer second.Close()

	record := config.EndpointRecord{
		ID: "ep", Name: "Endpoint", Protocol: config.ProtocolAnthropic,
		WireProtocol: config.WireAnthropic, BaseURL: first.URL,
		CredentialEnv: "TEST_API_KEY",
	}
	model := config.ModelRecord{ID: "claude-sonnet-4-5"}
	storeDir := t.TempDir()
	a := originAppIn(t, storeDir, first.URL, record, model)
	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if firstRequests != 1 {
		t.Fatalf("the first endpoint was asked %d times", firstRequests)
	}

	// The operator moves the conversation to another endpoint.
	other := record
	other.BaseURL = second.URL
	b := originAppIn(t, storeDir, second.URL, other, model)
	_, err := b.eng.RespondStream(context.Background(), "and again", nil)
	if err == nil {
		t.Fatal("the reasoning of another endpoint was sent")
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("the diagnostic does not name the origin: %v", err)
	}
	if secondRequests != 0 {
		t.Errorf("the new endpoint received %d requests", secondRequests)
	}
}
