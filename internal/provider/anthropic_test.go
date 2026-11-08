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

func TestNewForProtocolRefusesAnUnsupportedProtocol(t *testing.T) {
	_, err := NewForProtocol("grpc", "https://example.invalid", "key", Options{})
	if err == nil {
		t.Fatal("an unsupported wire protocol was accepted")
	}
	if !strings.Contains(err.Error(), WireOpenAI) || !strings.Contains(err.Error(), WireAnthropic) {
		t.Errorf("the refusal does not name the supported protocols: %v", err)
	}
}

// TestAnthropicRequestShape checks the request an Anthropic-compatible
// endpoint receives: the version header, the key header, the system
// instruction outside the messages, and the tool definitions in the
// Anthropic shape.
func TestAnthropicRequestShape(t *testing.T) {
	var seen struct {
		header  http.Header
		request map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.header = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&seen.request)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":4}}`))
	}))
	defer server.Close()

	impl := &AnthropicCompatible{Endpoint: server.URL}
	_, err := impl.Chat(context.Background(), []Message{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "hello"},
	}, Model{ID: "claude-test"})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got := seen.header.Get("anthropic-version"); got != DefaultAnthropicVersion {
		t.Errorf("anthropic-version = %q", got)
	}
	if seen.request["system"] != "be brief" {
		t.Errorf("system = %v", seen.request["system"])
	}
	messages, _ := seen.request["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v", seen.request["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("role = %v", first["role"])
	}
	blocks, _ := first["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("content = %v", first["content"])
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "hello" {
		t.Errorf("block = %v", block)
	}
	if _, ok := seen.request["max_tokens"]; !ok {
		t.Error("the request has no max_tokens")
	}
}

// TestAnthropicToolsUseTheInputSchemaName checks that the offered
// functions use the shape Anthropic documents.
func TestAnthropicToolsUseTheInputSchemaName(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"end_turn"}`))
	}))
	defer server.Close()

	impl := &AnthropicCompatible{Endpoint: server.URL}
	_, err := impl.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "go"}},
		Model{ID: "claude-test"},
		[]Tool{{Name: "write_file", Description: "writes", Parameters: `{"type":"object"}`}},
		nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	tools, _ := request["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", request["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "write_file" {
		t.Errorf("tool = %v", tool)
	}
	if _, ok := tool["input_schema"]; !ok {
		t.Errorf("the tool has no input_schema: %v", tool)
	}
}

// TestAnthropicStreamReassemblesTextAndToolCalls checks that a streamed
// turn produces the answer and the tool call the provider sent.
func TestAnthropicStreamReassemblesTextAndToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"write_file","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"a.txt\"}"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
			`{"type":"message_stop"}`,
		}
		for _, event := range events {
			_, _ = w.Write([]byte("data: " + event + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	impl := &AnthropicCompatible{Endpoint: server.URL}
	var text strings.Builder
	resp, err := impl.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "go"}},
		Model{ID: "claude-test"},
		[]Tool{{Name: "write_file"}},
		func(d Delta) error {
			text.WriteString(d.Text)
			return nil
		})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if text.String() != "Hello world" {
		t.Errorf("the streamed text = %q", text.String())
	}
	if resp.Content != "Hello world" {
		t.Errorf("the answer = %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	call := resp.ToolCalls[0]
	if call.Name != "write_file" || call.ID != "call_1" {
		t.Errorf("call = %+v", call)
	}
	if call.Arguments["path"] != "a.txt" {
		t.Errorf("arguments = %+v", call.Arguments)
	}
	if resp.Stats.Usage.PromptTokens != 10 {
		t.Errorf("prompt tokens = %d", resp.Stats.Usage.PromptTokens)
	}
	if resp.Stats.Usage.CompletionTokens != 20 {
		t.Errorf("completion tokens = %d", resp.Stats.Usage.CompletionTokens)
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

// TestAnthropicAuthenticationFailure checks that a rejected key is
// reported without the key.
func TestAnthropicAuthenticationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid key"}}`))
	}))
	defer server.Close()

	impl := &AnthropicCompatible{Endpoint: server.URL, APIKey: "sk-secret"}
	_, err := impl.Chat(context.Background(), []Message{{Role: "user", Content: "go"}}, Model{ID: "claude-test"})
	if err == nil {
		t.Fatal("a rejected key was accepted")
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Errorf("the error carries the credential: %v", err)
	}
}

// TestAnthropicBaseAddsTheVersionPrefix checks the two base URL shapes.
func TestAnthropicBaseAddsTheVersionPrefix(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com":           "https://api.example.com/v1",
		"https://api.example.com/":          "https://api.example.com/v1",
		"https://api.example.com/v1":        "https://api.example.com/v1",
		"https://api.example.com/v1/":       "https://api.example.com/v1",
		"https://api.example.com/anthropic": "https://api.example.com/anthropic/v1",
	}
	for base, want := range cases {
		impl := &AnthropicCompatible{Endpoint: base}
		if got := impl.base(); got != want {
			t.Errorf("%q: base = %q, want %q", base, got, want)
		}
	}
}

// TestAnthropicModelsListsTheCatalogue checks the discovery call.
func TestAnthropicModelsListsTheCatalogue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-a"},{"id":"claude-b"}]}`))
	}))
	defer server.Close()

	impl := &AnthropicCompatible{Endpoint: server.URL}
	ids, err := impl.Models(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(ids) != 2 || ids[0] != "claude-a" {
		t.Errorf("ids = %v", ids)
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

// TestAnthropicToolResultBecomesAUserMessage checks the shape of a tool
// outcome.
func TestAnthropicToolResultBecomesAUserMessage(t *testing.T) {
	_, messages := anthropicMessages([]Message{
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "c1", Name: "write_file"}}},
		{Role: "tool", ToolCallID: "c1", Content: "written"},
	})
	if len(messages) != 2 {
		t.Fatalf("messages = %+v", messages)
	}
	if messages[0].Content[0].Type != "tool_use" {
		t.Errorf("the assistant block = %+v", messages[0].Content[0])
	}
	if messages[1].Role != "user" || messages[1].Content[0].Type != "tool_result" {
		t.Errorf("the tool result = %+v", messages[1])
	}
	if messages[1].Content[0].ToolUseID != "c1" {
		t.Errorf("the tool result has no id: %+v", messages[1].Content[0])
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
