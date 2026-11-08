package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The regression tests the model-management work brought with it.
//
// The Anthropic adapter is the discovery implementation's one. These
// tests cover what the two implementations each know: the wire
// protocol selection, the extended-thinking block, the idle watchdog
// and the rejection of a key. The discovery implementation's own tests
// cover the request shape, the catalogue and the stream events.

// TestNewForProtocolBuildsTheRightAdapter checks that the wire
// protocol selects the adapter that speaks it.
func TestNewForProtocolBuildsTheRightAdapter(t *testing.T) {
	cases := []struct {
		wire string
		name string
	}{
		{"", "openai-compatible"},
		{WireOpenAI, "openai-compatible"},
		{WireAnthropic, "anthropic-compatible"},
	}
	for _, tc := range cases {
		impl, err := NewForProtocol(tc.wire, "https://example.invalid/v1", "key", Options{})
		if err != nil {
			t.Fatalf("%q: %v", tc.wire, err)
		}
		if impl.Name() != tc.name {
			t.Errorf("%q: adapter = %q, want %q", tc.wire, impl.Name(), tc.name)
		}
	}
}

// TestNewForProtocolRefusesAnUnsupportedProtocol checks that HAL never
// pretends that an arbitrary API is compatible.
func TestNewForProtocolRefusesAnUnsupportedProtocol(t *testing.T) {
	_, err := NewForProtocol("grpc", "https://example.invalid", "key", Options{})
	if err == nil {
		t.Fatal("an unsupported wire protocol was accepted")
	}
	if !strings.Contains(err.Error(), WireOpenAI) || !strings.Contains(err.Error(), WireAnthropic) {
		t.Errorf("the refusal does not name the supported protocols: %v", err)
	}
}

// TestAnthropicThinkingIsSentOnlyForCapableModels checks that the
// extended-thinking block is not sent to a model that does not take one.
func TestAnthropicThinkingIsSentOnlyForCapableModels(t *testing.T) {
	requests := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests[body["model"].(string)] = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer server.Close()

	impl := &AnthropicCompatible{Endpoint: server.URL}
	for _, id := range []string{"claude-sonnet-4-5", "claude-3-7-sonnet", "some-other-model"} {
		if _, err := impl.Chat(context.Background(),
			[]Message{{Role: "user", Content: "go"}},
			Model{ID: id, Effort: "high"}); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if _, ok := requests["claude-sonnet-4-5"]["thinking"]; !ok {
		t.Error("a capable model did not get the thinking block")
	}
	if _, ok := requests["claude-3-7-sonnet"]["thinking"]; !ok {
		t.Error("a capable model did not get the thinking block")
	}
	if _, ok := requests["some-other-model"]["thinking"]; ok {
		t.Error("an unknown model got the thinking block")
	}
}

// TestAnthropicThinkingBudgetStaysBelowMaxTokens checks the rule the API
// documents: the output cap must exceed the thinking budget.
func TestAnthropicThinkingBudgetStaysBelowMaxTokens(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer server.Close()

	impl := &AnthropicCompatible{Endpoint: server.URL}
	if _, err := impl.Chat(context.Background(),
		[]Message{{Role: "user", Content: "go"}},
		Model{ID: "claude-sonnet-4-5", Effort: "max", MaxTokens: 100}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	thinking, _ := request["thinking"].(map[string]any)
	budget, _ := thinking["budget_tokens"].(float64)
	maxTokens, _ := request["max_tokens"].(float64)
	if int(maxTokens) <= int(budget) {
		t.Errorf("max_tokens = %v, budget = %v", maxTokens, budget)
	}
}

// TestAnthropicIdleTimeoutClosesTheRequest checks that a provider that
// never answers is cut loose instead of hanging the turn.
func TestAnthropicIdleTimeoutClosesTheRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The headers arrive, and then the provider goes quiet.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()

	impl := &AnthropicCompatible{Endpoint: server.URL, IdleTimeout: 100 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := impl.ChatStream(ctx, []Message{{Role: "user", Content: "go"}}, Model{ID: "claude-test"}, nil)
	if err == nil {
		t.Fatal("a silent provider was waited for")
	}
}

// TestAnthropicMergesConsecutiveMessages checks that two user messages
// become one, because the API expects alternating roles.
func TestAnthropicMergesConsecutiveMessages(t *testing.T) {
	system, messages := anthropicMessages([]Message{
		{Role: "user", Content: "one"},
		{Role: "user", Content: "two"},
		{Role: "assistant", Content: "answer"},
	})
	if system != "" {
		t.Errorf("system = %q", system)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %+v", messages)
	}
	if len(messages[0].Content) != 2 {
		t.Errorf("the two user messages were not merged: %+v", messages[0])
	}
}
