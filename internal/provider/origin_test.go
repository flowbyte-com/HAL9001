package provider

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// The provider origin.
//
// The reasoning a provider sends belongs to that provider. Two
// endpoints that speak the same dialect and serve the same model
// identifier are still two providers, and the thinking of one is not
// the thinking of the other.
//
// The origin is what tells them apart. It is a fingerprint of the
// endpoint: the wire it speaks, the address it answers on and the
// credential profile it uses. It is stable, it is deterministic, it
// names no secret, and two endpoints never share one.

// TestTheOriginIsStableAndSecretFree describes what the origin is.
//
// It is the same for the same endpoint, and it is different for every
// other endpoint. Nothing of a credential reaches it: not the value,
// not a header, not a secret in the address.
func TestTheOriginIsStableAndSecretFree(t *testing.T) {
	first := OriginFor(WireAnthropic, "https://api.example.com/anthropic", "env:DEEPSEEK_API_KEY")
	second := OriginFor(WireAnthropic, "https://api.example.com/anthropic", "env:DEEPSEEK_API_KEY")
	if first != second {
		t.Errorf("the origin of one endpoint changed: %q and %q", first, second)
	}
	if first == "" {
		t.Fatal("the origin is empty")
	}
	// The origin names the endpoint and nothing else. The address, the
	// credential and the secret are all in the inputs, and none of them
	// is in the fingerprint.
	for _, secret := range []string{
		"api.example.com",
		"anthropic",
		"DEEPSEEK_API_KEY",
		"sk-secret-value",
	} {
		if strings.Contains(string(first), secret) {
			t.Errorf("the origin carries %q: %q", secret, first)
		}
	}
	// A secret in the address itself is not part of the identity and is
	// not carried by it.
	withSecret := OriginFor(WireAnthropic,
		"https://user:sk-secret-value@api.example.com/anthropic?token=sk-secret-value", "env:KEY")
	if strings.Contains(string(withSecret), "sk-secret-value") {
		t.Errorf("the origin carries a secret from the address: %q", withSecret)
	}
}

// TestTwoEndpointsNeverShareAnOrigin describes what separates two
// origins.
//
// The wire, the path of the address and the credential profile each
// tell two endpoints apart. The credential profile is what tells two
// accounts at one address apart.
func TestTwoEndpointsNeverShareAnOrigin(t *testing.T) {
	base := OriginFor(WireAnthropic, "https://api.example.com/anthropic", "env:KEY")
	cases := []struct {
		name  string
		other Origin
	}{
		{"another wire", OriginFor(WireOpenAI, "https://api.example.com/anthropic", "env:KEY")},
		{"another host", OriginFor(WireAnthropic, "https://other.example.com/anthropic", "env:KEY")},
		{"another path", OriginFor(WireAnthropic, "https://api.example.com/v1", "env:KEY")},
		{"another port", OriginFor(WireAnthropic, "https://api.example.com:8443/anthropic", "env:KEY")},
		{"another account", OriginFor(WireAnthropic, "https://api.example.com/anthropic", "env:OTHER_KEY")},
	}
	for _, c := range cases {
		if c.other == base {
			t.Errorf("%s shares the origin of the first endpoint: %q", c.name, c.other)
		}
	}
	// The same endpoint, spelled the same way, is the same origin.
	if OriginFor(WireAnthropic, "https://api.example.com/anthropic", "env:KEY") != base {
		t.Error("the origin of one endpoint is not stable")
	}
}

// The two accounts at one address are apart, and the credential that
// names them is never named by the origin.
//
// This is the documented limitation: two accounts at one address are
// two origins only when the configuration can tell them apart. A
// configuration that carries the credential itself cannot, and the
// origin says so by being the same for both.
func TestTwoAccountsAtOneAddressAreApartWhenTheProfileTellsThemApart(t *testing.T) {
	first := OriginFor(WireAnthropic, "https://api.example.com/anthropic", "env:FIRST_KEY")
	second := OriginFor(WireAnthropic, "https://api.example.com/anthropic", "env:SECOND_KEY")
	if first == second {
		t.Errorf("two accounts share one origin: %q", first)
	}
	// A credential the configuration carries itself is one profile: the
	// configuration cannot tell two of them apart, and the origin does
	// not pretend it can.
	inline := OriginFor(WireAnthropic, "https://api.example.com/anthropic", "inline")
	other := OriginFor(WireAnthropic, "https://api.example.com/anthropic", "inline")
	if inline != other {
		t.Errorf("the origin of one carried credential changed: %q and %q", inline, other)
	}
}

// The thinking default comes from the endpoint.
//
// A known endpoint has the behaviour its own documentation states. An
// unknown one has none: HAL does not claim that a gateway thinks, and
// it does not claim that it does not.
func TestTheThinkingDefaultComesFromTheEndpoint(t *testing.T) {
	if got := ThinkingDefaultFor("https://api.deepseek.com/anthropic"); got != ThinkingDefaultOn {
		t.Errorf("the DeepSeek endpoint thinks by default = %v, want on", got)
	}
	if got := ThinkingDefaultFor("https://api.anthropic.com/v1"); got != ThinkingDefaultOff {
		t.Errorf("the Anthropic endpoint thinks by default = %v, want off", got)
	}
	// A gateway is neither, whatever it is called.
	for _, address := range []string{
		"https://gateway.example.com/anthropic",
		"https://api.deepseek.com.example.net/anthropic",
		"http://127.0.0.1:8080",
		"https://localhost:1234",
	} {
		if got := ThinkingDefaultFor(address); got != ThinkingUnknown {
			t.Errorf("the endpoint %q thinks by default = %v, want unknown", address, got)
		}
	}
}

// The endpoint isolation.
//
// The reasoning of one endpoint is never sent to another. The request
// is refused locally, before anything goes out, and the diagnostic
// names the change.

// The turn that crossed the boundary is refused, and the endpoint that
// was about to receive it receives nothing.
func requireRefused(t *testing.T, err error, srv *anthropicEndpoint) {
	t.Helper()
	if err == nil {
		t.Fatal("the reasoning of another endpoint was sent")
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("the diagnostic does not name the origin: %v", err)
	}
	if len(srv.requests) != 0 {
		t.Errorf("the endpoint received %d requests", len(srv.requests))
	}
}

// The reasoning of one endpoint is not sent to another endpoint that
// serves the same model through the same dialect.
func TestTheReasoningOfOneEndpointIsNotSentToAnother(t *testing.T) {
	first := &anthropicEndpoint{t: t, signed: true, steps: []string{"read"}}
	firstTS := httptest.NewServer(first)
	defer firstTS.Close()
	second := &anthropicEndpoint{t: t, signed: true, steps: []string{"read"}}
	secondTS := httptest.NewServer(second)
	defer secondTS.Close()

	a := first.adapter(firstTS)
	model := Model{ID: "claude-sonnet-4-5", Effort: "high"}
	tools := []Tool{{Name: "read"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}}, model, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 1 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}

	// The operator moves the conversation to the other endpoint.
	b := second.adapter(secondTS)
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks,
			Origin: resp.Origin, Thinking: resp.Thinking},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	_, err = b.ChatStreamTools(context.Background(), follow, model, tools, nil)
	requireRefused(t, err, second)
}

// The reasoning of Claude is not sent to the endpoint that serves the
// Claude name as an alias.
func TestTheReasoningOfClaudeIsNotSentToTheAliasEndpoint(t *testing.T) {
	first := &anthropicEndpoint{t: t, signed: true, steps: []string{"read"}}
	firstTS := httptest.NewServer(first)
	defer firstTS.Close()
	second := &anthropicEndpoint{t: t, deepSeek: true, steps: []string{"read"}}
	secondTS := httptest.NewServer(second)
	defer secondTS.Close()

	a := first.adapter(firstTS)
	model := Model{ID: "claude-sonnet-4-5", Effort: "high"}
	tools := []Tool{{Name: "read"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}}, model, tools, nil)
	if err != nil {
		t.Fatal(err)
	}

	b := second.adapter(secondTS)
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks,
			Origin: resp.Origin, Thinking: resp.Thinking},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	_, err = b.ChatStreamTools(context.Background(), follow, model, tools, nil)
	requireRefused(t, err, second)
}

// The reasoning of the alias is not sent to the genuine endpoint.
//
// The alias and the Claude model share the protocol key, because the
// name is a Claude name. The origin is what tells them apart.
func TestTheReasoningOfTheAliasIsNotSentToClaude(t *testing.T) {
	first := &anthropicEndpoint{t: t, deepSeek: true, steps: []string{"read"}}
	firstTS := httptest.NewServer(first)
	defer firstTS.Close()
	second := &anthropicEndpoint{t: t, signed: true, steps: []string{"read"}}
	secondTS := httptest.NewServer(second)
	defer secondTS.Close()

	a := first.adapter(firstTS)
	model := Model{ID: "claude-sonnet-4-5", Effort: "high"}
	tools := []Tool{{Name: "read"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}}, model, tools, nil)
	if err != nil {
		t.Fatal(err)
	}

	b := second.adapter(secondTS)
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks,
			Origin: resp.Origin, Thinking: resp.Thinking},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	_, err = b.ChatStreamTools(context.Background(), follow, model, tools, nil)
	requireRefused(t, err, second)
}

// Two gateways that serve the same model identifier are two providers.
//
// The protocol key is the same and the model is the same. The origin
// is not, and that is what keeps the reasoning of one away from the
// other.
func TestTwoGatewaysWithTheSameModelAreApart(t *testing.T) {
	first := &anthropicEndpoint{t: t, signed: true, steps: []string{"read"}}
	firstTS := httptest.NewServer(first)
	defer firstTS.Close()
	second := &anthropicEndpoint{t: t, signed: true, steps: []string{"read"}}
	secondTS := httptest.NewServer(second)
	defer secondTS.Close()

	a := first.adapter(firstTS)
	model := Model{ID: "claude-sonnet-4-5", Effort: "high"}
	tools := []Tool{{Name: "read"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}}, model, tools, nil)
	if err != nil {
		t.Fatal(err)
	}

	b := second.adapter(secondTS)
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks,
			Origin: resp.Origin, Thinking: resp.Thinking},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	_, err = b.ChatStreamTools(context.Background(), follow, model, tools, nil)
	requireRefused(t, err, second)
}

// The same endpoint after a reload is the same provider.
//
// HAL reads its configuration again and builds its adapters again. The
// endpoint did not change, so the origin did not change, and the
// reasoning of the conversation is still the endpoint's own.
func TestTheSameEndpointAfterAReloadKeepsItsReasoning(t *testing.T) {
	srv := &anthropicEndpoint{t: t, signed: true, steps: []string{"read", ""}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	first := srv.adapter(ts)
	model := Model{ID: "claude-sonnet-4-5", Effort: "high"}
	tools := []Tool{{Name: "read"}}
	resp, err := first.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}}, model, tools, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The reload: a new adapter over the same endpoint and the same
	// credential profile.
	second := srv.adapter(ts)
	if first.origin() != second.origin() {
		t.Fatalf("the origin changed across the reload: %q and %q",
			first.origin(), second.origin())
	}
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks,
			Origin: resp.Origin, Thinking: resp.Thinking},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	if _, err := second.ChatStreamTools(context.Background(), follow, model, tools, nil); err != nil {
		t.Fatalf("the continuation was refused: %v", err)
	}
	if len(srv.thinkingBlocks()) == 0 {
		t.Errorf("the blocks were not replayed: %+v", srv.assistantBlocks())
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// A compatible model change on the same endpoint keeps the reasoning.
//
// The operator moves from one Claude model to another on the same
// endpoint. The provider is the same one, so its reasoning is the same
// one's, and the turn comes back whole.
func TestTheSameEndpointWithACompatibleModelChangeKeepsItsReasoning(t *testing.T) {
	srv := &anthropicEndpoint{t: t, signed: true, steps: []string{"read", ""}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		Model{ID: "claude-sonnet-4-5", Effort: "high"}, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks,
			Origin: resp.Origin, Thinking: resp.Thinking},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow,
		Model{ID: "claude-opus-4-1", Effort: "high"}, tools, nil); err != nil {
		t.Fatalf("the continuation was refused: %v", err)
	}
	if len(srv.thinkingBlocks()) == 0 {
		t.Errorf("the blocks were not replayed: %+v", srv.assistantBlocks())
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// A record that names no endpoint is never sent to any endpoint.
//
// The record was written before HAL kept the origin. It holds the
// blocks the provider sent, and it does not say which provider sent
// them. Nothing is invented for it: the request is refused, with the
// reason.
func TestARecordWithoutAnOriginIsRefused(t *testing.T) {
	srv := &anthropicEndpoint{t: t, signed: true, steps: []string{"read"}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	model := Model{ID: "claude-sonnet-4-5", Effort: "high"}
	tools := []Tool{{Name: "read"}}
	messages := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: "reading it",
			ReasoningKey: ReasoningKey(model.ID, WireAnthropic),
			Thinking:     ThinkingAsked,
			Reasoning: []ReasoningBlock{
				{Type: ReasoningThinking, Text: "the model weighed it", Signature: "sig-abc"},
			}},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	_, err := a.ChatStreamTools(context.Background(), messages, model, tools, nil)
	if err == nil {
		t.Fatal("a record without an endpoint identity was sent")
	}
	if !strings.Contains(err.Error(), "no endpoint identity") {
		t.Errorf("the diagnostic does not name the reason: %v", err)
	}
	if len(srv.requests) != 0 {
		t.Errorf("the endpoint received %d requests", len(srv.requests))
	}
}

// A conversation without reasoning keeps working.
//
// Most conversations say nothing about thinking. They have no
// continuity data, so there is nothing to bind to an origin, and the
// endpoint may be changed freely.
func TestAConversationWithoutReasoningKeepsWorking(t *testing.T) {
	first := &anthropicEndpoint{t: t, steps: []string{""}}
	firstTS := httptest.NewServer(first)
	defer firstTS.Close()
	second := &anthropicEndpoint{t: t, steps: []string{""}}
	secondTS := httptest.NewServer(second)
	defer secondTS.Close()

	a := first.adapter(firstTS)
	model := Model{ID: "claude-sonnet-4-5"}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "hello"}}, model, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 0 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}

	// The operator moves the conversation to the other endpoint: there
	// is no reasoning to keep, so nothing is refused.
	b := second.adapter(secondTS)
	follow := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: resp.Content},
	}
	if _, err := b.ChatStreamTools(context.Background(), follow, model, nil, nil); err != nil {
		t.Fatalf("the continuation was refused: %v", err)
	}
	if len(second.requests) != 1 {
		t.Errorf("the endpoint received %d requests", len(second.requests))
	}
}

// The reasoning of the OpenAI dialect is bound to its endpoint too.
//
// DeepSeek's Thinking Mode wants the reasoning_content of an earlier
// turn back with every request that carries tools. That reasoning
// belongs to the endpoint that produced it, and it is never sent to
// another one.
func TestTheReasoningOfTheOpenAIDialectIsNotSentToAnotherEndpoint(t *testing.T) {
	srv := &wireServer{t: t, answers: []string{
		`{"choices":[{"delta":{"content":"ok","reasoning_content":"the model weighed it"},` +
			`"finish_reason":"stop"}]}`}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	p := &OpenAICompatible{Endpoint: ts.URL}
	model := Model{ID: "deepseek-reasoner"}
	resp, err := p.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, model, []Tool{{Name: "read"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 1 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}

	// The operator moves the conversation to another endpoint.
	other := &wireServer{t: t, answers: []string{
		`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`}}
	ots := httptest.NewServer(other)
	defer ots.Close()
	q := &OpenAICompatible{Endpoint: ots.URL}
	follow := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: resp.Content,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks,
			Origin: resp.Origin, Thinking: resp.Thinking},
	}
	_, err = q.ChatStreamTools(context.Background(), follow, model, []Tool{{Name: "read"}}, nil)
	if err == nil {
		t.Fatal("the reasoning of another endpoint was sent")
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("the diagnostic does not name the origin: %v", err)
	}
	if len(other.requests) != 0 {
		t.Errorf("the endpoint received %d requests", len(other.requests))
	}
}

// A record of another protocol is refused as well when it names
// another endpoint.
//
// The protocol says how the reasoning is written; the origin says
// whose it is. A record that names another endpoint is refused whatever
// its protocol is, because nothing of it may cross the boundary.
func TestARecordOfAnotherProtocolAndAnotherEndpointIsRefused(t *testing.T) {
	srv := &anthropicEndpoint{t: t, signed: true, steps: []string{"read"}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	model := Model{ID: "claude-sonnet-4-5", Effort: "high"}
	tools := []Tool{{Name: "read"}}
	messages := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: "reading it",
			ReasoningKey: ReasoningKey("deepseek-flash", WireOpenAI),
			Origin:       string(providerOriginOf("https://other.example.com/anthropic")),
			Thinking:     ThinkingAsked,
			Reasoning: []ReasoningBlock{
				{Type: ReasoningPlain, Text: "the model weighed it"},
			}},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	_, err := a.ChatStreamTools(context.Background(), messages, model, tools, nil)
	if err == nil {
		t.Fatal("the reasoning of another endpoint was sent")
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("the diagnostic does not name the origin: %v", err)
	}
	if len(srv.requests) != 0 {
		t.Errorf("the endpoint received %d requests", len(srv.requests))
	}
}

// providerOriginOf is the origin of an endpoint that is not the one
// under test.
func providerOriginOf(address string) Origin {
	return OriginFor(WireAnthropic, address, "env:KEY")
}
