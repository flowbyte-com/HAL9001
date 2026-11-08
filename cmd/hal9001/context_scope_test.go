package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/tui"
)

// The context window is a total.
//
// The number the operator writes down — and the number an endpoint
// advertises — is the whole context the model accepts. The room for the
// answer is reserved from it, and the request may use what is left.
//
// These tests watch the request that actually leaves the machine.

// contextFixture is a recording endpoint that advertises a context
// window for the models it serves.
//
// The advertisement is what an endpoint says about its own models: the
// same identifier may be advertised with a very different window by a
// different endpoint.
type contextFixture struct {
	*httptest.Server
	// window is the context window the fixture advertises.
	window int

	mu sync.Mutex
	// requests records every request the fixture received.
	requests []map[string]any
}

// newContextFixture starts a recording endpoint that advertises a
// context window for the model named "shared".
func newContextFixture(t *testing.T, window int) *contextFixture {
	t.Helper()
	f := &contextFixture{window: window}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.requests = append(f.requests, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			fmt.Fprintf(w, `{"data":[{"id":"shared","context_window":%d}]}`, f.window)
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// conversations returns the requests the fixture received.
func (f *contextFixture) conversations() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.requests...)
}

// estimateOf returns the estimated size of a recorded OpenAI request.
//
// The request is what leaves the machine, so the estimate is made on
// the wire form: the messages it carries and the function definitions
// that travel with every request.
func estimateOf(t *testing.T, body map[string]any) int {
	t.Helper()
	raw, _ := body["messages"]
	list, _ := raw.([]any)
	messages := make([]provider.Message, 0, len(list))
	for _, item := range list {
		entry, _ := item.(map[string]any)
		role, _ := entry["role"].(string)
		content, _ := entry["content"].(string)
		messages = append(messages, provider.Message{Role: role, Content: content})
	}
	rawTools, _ := body["tools"].([]any)
	tools := make([]provider.Tool, 0, len(rawTools))
	for _, item := range rawTools {
		entry, _ := item.(map[string]any)
		name, _ := entry["name"].(string)
		description, _ := entry["description"].(string)
		schema, _ := entry["parameters"].(map[string]any)
		encoded, _ := json.Marshal(schema)
		tools = append(tools, provider.Tool{
			Name:        name,
			Description: description,
			Parameters:  string(encoded),
		})
	}
	return provider.EstimateTokens(messages, tools)
}

// contextApp builds an application with one endpoint that serves the
// model "shared", and a conversation long enough to need a trim.
func contextApp(t *testing.T, fixture *contextFixture, window, output int) *app {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))

	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{{
		ID: "alpha", Name: "Alpha", Protocol: config.ProtocolOpenAI,
		BaseURL: fixture.URL + "/v1",
		Models: []config.ModelRecord{{
			ID: "shared", ContextMode: config.ContextManual,
			ContextWindow: window, MaxOutputTokens: output,
		}},
	}}
	stored.SetActive("alpha", "shared")
	a := newRuntimeApp(t, stored)
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
	return a
}

// C1: a configured 8192-token window with 4096 reserved for the answer
// leaves 4096 tokens for the request.
func TestAn8192WindowWith4096ReservedLeaves4096ForTheRequest(t *testing.T) {
	fixture := newContextFixture(t, 8192)
	a := contextApp(t, fixture, 8192, 4096)

	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Budget.Limit != 4096 {
		t.Fatalf("the limit = %d, want 4096: the window is a total", target.Budget.Limit)
	}
	if target.Budget.ReservedOutput != 4096 {
		t.Fatalf("the reservation = %d, want 4096", target.Budget.ReservedOutput)
	}

	if _, err := a.eng.RespondStream(context.Background(), "and now?", nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	requests := fixture.conversations()
	if len(requests) != 1 {
		t.Fatalf("the endpoint received %d requests", len(requests))
	}
	if got := estimateOf(t, requests[0]); got > 4096 {
		t.Errorf("the request is %d tokens, over the 4096 that are left of the window", got)
	}
	// The conversation is longer than the request: something was left
	// out on purpose.
	messages, _ := requests[0]["messages"].([]any)
	if len(messages) >= 41 {
		t.Errorf("the whole conversation was transmitted: %d messages", len(messages))
	}
}

// C1: the same window with the default output allowance. The default is
// reduced conservatively, so the request still has room.
func TestAn8192WindowWithTheDefaultOutputLeavesRoomForTheRequest(t *testing.T) {
	fixture := newContextFixture(t, 8192)
	a := contextApp(t, fixture, 8192, 0)

	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Budget.Limit <= 0 {
		t.Fatalf("the window left no room for the request: %+v", target.Budget)
	}
	if target.Budget.ReservedOutput > 8192/2 {
		t.Errorf("more than half the window was reserved: %+v", target.Budget)
	}
	if _, err := a.eng.RespondStream(context.Background(), "and now?", nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	requests := fixture.conversations()
	if len(requests) != 1 {
		t.Fatalf("the endpoint received %d requests", len(requests))
	}
	if got := estimateOf(t, requests[0]); got > target.Budget.Limit {
		t.Errorf("the request is %d tokens, over the limit of %d", got, target.Budget.Limit)
	}
}

// C1: a tiny window. The request that cannot be reduced is refused
// before it is transmitted, and the operator's message is not lost.
func TestATinyContextWindowRefusesTheRequestBeforeItIsSent(t *testing.T) {
	fixture := newContextFixture(t, 128)
	a := contextApp(t, fixture, 128, 0)

	_, err := a.eng.RespondStream(context.Background(), "and now?", nil)
	if err == nil {
		t.Fatal("a request that cannot fit was sent anyway")
	}
	if !strings.Contains(err.Error(), "context budget") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
	if len(fixture.conversations()) != 0 {
		t.Errorf("the endpoint received %d requests with a request that does not fit",
			len(fixture.conversations()))
	}
	history, err := a.store.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 || history[len(history)-1].Role != session.RoleUser {
		t.Errorf("the operator's message was dropped: %+v", history)
	}
}

// C1: an impossible output allowance. The two configured numbers
// contradict each other, and the contradiction is reported instead of
// being guessed at.
func TestAnImpossibleOutputAllowanceIsRefused(t *testing.T) {
	fixture := newContextFixture(t, 8192)
	a := contextApp(t, fixture, 8192, 8192)

	_, err := a.resolveTarget(a.stored)
	if err == nil {
		t.Fatal("a contradiction was accepted")
	}
	if !strings.Contains(err.Error(), "does not fit") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
	if len(fixture.conversations()) != 0 {
		t.Errorf("the endpoint received %d requests", len(fixture.conversations()))
	}
}

// C1: the current request and the system instructions survive the trim,
// and a tool exchange is never cut in half.
func TestTheTrimKeepsTheCurrentRequestAndTheToolExchange(t *testing.T) {
	fixture := newContextFixture(t, 8192)
	a := contextApp(t, fixture, 8192, 512)
	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Budget.Limit != 8192-512 {
		t.Fatalf("the limit = %d, want %d", target.Budget.Limit, 8192-512)
	}

	messages := []provider.Message{
		{Role: "system", Content: "the safety instructions"},
		{Role: "user", Content: "read the file please"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{
			ID: "call-1", Name: "read_file", Arguments: map[string]any{"path": "a.txt"},
		}}},
		{Role: "tool", ToolCallID: "call-1", Name: "read_file", Content: "the file says hello"},
		{Role: "user", Content: "and now the second one"},
	}
	trimmed, _, err := provider.TrimToBudget(messages, nil, provider.ContextBudget{
		Limit: 40, ReservedOutput: 20, Source: provider.ContextSourceOperator,
	})
	if err != nil {
		t.Fatalf("the request should fit: %v", err)
	}
	for _, m := range trimmed {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("a tool call survived without its result: %#v", m)
		}
		if m.Role == "tool" {
			t.Fatalf("a tool result survived without its call: %#v", m)
		}
	}
	if trimmed[0].Role != "system" {
		t.Fatalf("the safety instructions were dropped: %#v", trimmed)
	}
	last := trimmed[len(trimmed)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "second one") {
		t.Fatalf("the current request was dropped: %#v", trimmed)
	}
}

// C2: the context metadata belongs to the endpoint.
//
// Two endpoints may serve the same model identifier with very different
// windows. One endpoint's advertisement must never become the other's.
func TestTheContextMetadataBelongsToTheEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))

	big := newContextFixture(t, 200000)
	small := newContextFixture(t, 8192)

	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{
		{
			ID: "big", Name: "Big", Protocol: config.ProtocolOpenAI,
			BaseURL: big.URL + "/v1",
			Models:  []config.ModelRecord{{ID: "shared", ContextMode: config.ContextAuto}},
		},
		{
			ID: "small", Name: "Small", Protocol: config.ProtocolOpenAI,
			BaseURL: small.URL + "/v1",
			Models:  []config.ModelRecord{{ID: "shared", ContextMode: config.ContextAuto}},
		},
	}
	stored.SetActive("big", "shared")
	a := newRuntimeApp(t, stored)

	// Both endpoints are asked what they serve.
	for _, endpoint := range []struct {
		id  string
		url string
	}{{"big", big.URL}, {"small", small.URL}} {
		a.refreshContext(context.Background(),
			contextKey{endpoint: endpoint.id, model: "shared"},
			a.contextGeneration(endpoint.id), endpoint.url+"/v1", "k", provider.WireOpenAI)
	}

	// Each endpoint's own window is recorded under its own identity.
	bigBudget, err := a.contextBudget(contextKey{endpoint: "big", model: "shared"}, 0,
		provider.OutputAllowance{Tokens: 4096})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if bigBudget.Limit != 200000-4096 {
		t.Errorf("the big endpoint's budget = %d, want %d", bigBudget.Limit, 200000-4096)
	}
	smallBudget, err := a.contextBudget(contextKey{endpoint: "small", model: "shared"}, 0,
		provider.OutputAllowance{Tokens: 4096})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if smallBudget.Limit != 8192-4096 {
		t.Errorf("the small endpoint's budget = %d, want %d: the other endpoint's window leaked in",
			smallBudget.Limit, 8192-4096)
	}

	// Switching to the small endpoint uses its own budget.
	if _, err := a.switchModel("small", "shared"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Budget.Limit != 8192-4096 {
		t.Errorf("after the switch the budget = %d, want %d", target.Budget.Limit, 8192-4096)
	}
	if target.Budget.Source != provider.ContextSourceProvider {
		t.Errorf("the provenance was lost: %+v", target.Budget)
	}
	if !target.Budget.Verified {
		t.Errorf("an advertised window is verified: %+v", target.Budget)
	}
}

// C2: editing an endpoint invalidates what it reported.
func TestEditingAnEndpointInvalidatesItsMetadata(t *testing.T) {
	fixture := newContextFixture(t, 8192)
	a := contextApp(t, fixture, 8192, 0)

	a.refreshContext(context.Background(), contextKey{endpoint: "alpha", model: "shared"},
		a.contextGeneration("alpha"), fixture.URL+"/v1", "k", provider.WireOpenAI)
	if _, ok := a.knownContext(contextKey{endpoint: "alpha", model: "shared"}); !ok {
		t.Fatal("the metadata was not recorded")
	}
	// The endpoint is edited: its address, its protocol or its
	// credential changed, so what it said belongs to a configuration
	// that is no longer in force.
	if err := a.saveEndpoint(tui.EndpointSpec{
		ID: "alpha", Name: "Alpha", Protocol: string(config.ProtocolOpenAI),
		BaseURL: fixture.URL + "/v2",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, ok := a.knownContext(contextKey{endpoint: "alpha", model: "shared"}); ok {
		t.Fatal("the stale metadata survived an endpoint change")
	}
}

// C2: renaming an endpoint does not throw away what it reported.
func TestRenamingAnEndpointKeepsItsMetadata(t *testing.T) {
	fixture := newContextFixture(t, 8192)
	a := contextApp(t, fixture, 8192, 0)

	a.refreshContext(context.Background(), contextKey{endpoint: "alpha", model: "shared"},
		a.contextGeneration("alpha"), fixture.URL+"/v1", "k", provider.WireOpenAI)
	if err := a.saveEndpoint(tui.EndpointSpec{
		ID: "alpha", Name: "Renamed", Protocol: string(config.ProtocolOpenAI),
		BaseURL: fixture.URL + "/v1",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, ok := a.knownContext(contextKey{endpoint: "alpha", model: "shared"}); !ok {
		t.Fatal("the metadata was thrown away by a rename")
	}
}

// C2: deleting an endpoint takes its metadata with it.
func TestDeletingAnEndpointTakesItsMetadataWithIt(t *testing.T) {
	fixture := newContextFixture(t, 8192)
	a := contextApp(t, fixture, 8192, 0)

	a.refreshContext(context.Background(), contextKey{endpoint: "alpha", model: "shared"},
		a.contextGeneration("alpha"), fixture.URL+"/v1", "k", provider.WireOpenAI)
	if _, ok := a.knownContext(contextKey{endpoint: "alpha", model: "shared"}); !ok {
		t.Fatal("the metadata was not recorded")
	}
	if err := a.deleteEndpoint("alpha", ""); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := a.knownContext(contextKey{endpoint: "alpha", model: "shared"}); ok {
		t.Fatal("the metadata of a deleted endpoint survived")
	}
}

// C2: a lookup that finishes after the endpoint changed does not store
// its answer.
func TestAStaleLookupDoesNotOverwriteANewerConfiguration(t *testing.T) {
	fixture := newContextFixture(t, 8192)
	a := contextApp(t, fixture, 8192, 0)

	generation := a.contextGeneration("alpha")
	// The endpoint changes while the lookup is running.
	a.invalidateContext("alpha")
	// The answer arrives late.
	a.recordContext(contextKey{endpoint: "alpha", model: "shared"}, generation,
		provider.ContextMetadata{
			ContextWindow: 8192,
			Source:        provider.ContextSourceProvider,
			Mode:          provider.ContextModeMax,
			Verified:      true,
		})
	if _, ok := a.knownContext(contextKey{endpoint: "alpha", model: "shared"}); ok {
		t.Fatal("a stale answer was stored")
	}
}

// C2: the operator's own limit stays authoritative over what an
// endpoint advertised.
func TestTheOperatorLimitStaysAuthoritative(t *testing.T) {
	fixture := newContextFixture(t, 200000)
	a := contextApp(t, fixture, 4096, 1024)

	a.refreshContext(context.Background(), contextKey{endpoint: "alpha", model: "shared"},
		a.contextGeneration("alpha"), fixture.URL+"/v1", "k", provider.WireOpenAI)

	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Budget.Source != provider.ContextSourceOperator {
		t.Errorf("the source = %q, want the operator's", target.Budget.Source)
	}
	if target.Budget.Limit != 4096-1024 {
		t.Errorf("the limit = %d, want %d", target.Budget.Limit, 4096-1024)
	}
}
