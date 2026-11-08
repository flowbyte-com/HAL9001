package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The Anthropic Messages protocol.
//
// These tests are the evidence that HAL speaks the documented protocol
// and not the OpenAI one: the path, the headers, the request shape, the
// content blocks, the tool format and the named stream events are all
// checked against what the Messages API documents.

// recordedCall is one request the fixture received.
type recordedCall struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   map[string]any
}

// anthropicFixture is a recording Anthropic-compatible endpoint.
type anthropicFixture struct {
	t        *testing.T
	server   *httptest.Server
	calls    []recordedCall
	respond  func(w http.ResponseWriter, r *http.Request, call recordedCall)
	listing  []map[string]any
	hasMore  bool
	lastID   string
	pageSize int
}

// newAnthropicFixture starts a recording endpoint.
func newAnthropicFixture(t *testing.T) *anthropicFixture {
	t.Helper()
	f := &anthropicFixture{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := recordedCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone()}
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				f.t.Errorf("decoding request: %v", err)
			}
			call.Body = body
		}
		f.calls = append(f.calls, call)
		if f.respond != nil {
			f.respond(w, r, call)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"m","stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// last returns the most recent recorded call.
func (f *anthropicFixture) last() recordedCall {
	f.t.Helper()
	if len(f.calls) == 0 {
		f.t.Fatal("no request was recorded")
	}
	return f.calls[len(f.calls)-1]
}

// blocks returns the content blocks of one message of the request.
func blocksOf(t *testing.T, body map[string]any, index int) []map[string]any {
	t.Helper()
	messages, ok := body["messages"].([]any)
	if !ok || index >= len(messages) {
		t.Fatalf("message %d missing from %#v", index, body["messages"])
	}
	message := messages[index].(map[string]any)
	blocks, ok := message["content"].([]any)
	if !ok {
		t.Fatalf("message %d has no content blocks: %#v", index, message)
	}
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.(map[string]any))
	}
	return out
}

func TestAnthropicUsesTheMessagesProtocol(t *testing.T) {
	f := newAnthropicFixture(t)
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "secret-key"}

	resp, err := client.Chat(context.Background(),
		[]Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "hello"},
		}, Model{ID: "claude-x"})
	if err != nil {
		t.Fatal(err)
	}
	call := f.last()
	if call.Path != "/v1/messages" {
		t.Errorf("path = %q, the Messages API is POST /v1/messages", call.Path)
	}
	if got := call.Header.Get("x-api-key"); got != "secret-key" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := call.Header.Get("anthropic-version"); got != DefaultAnthropicVersion {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := call.Header.Get("Authorization"); got != "" {
		t.Errorf("the Messages API does not use a bearer header, got %q", got)
	}
	if call.Body["model"] != "claude-x" {
		t.Errorf("model = %#v", call.Body["model"])
	}
	if _, ok := call.Body["max_tokens"]; !ok {
		t.Error("max_tokens is required by the Messages API")
	}
	if call.Body["system"] != "be brief" {
		t.Errorf("system = %#v, it must be a top-level field", call.Body["system"])
	}
	if _, ok := call.Body["messages"].([]any)[0].(map[string]any)["role"]; !ok {
		t.Error("the first message must be a user turn")
	}
	blocks := blocksOf(t, call.Body, 0)
	if len(blocks) != 1 || blocks[0]["type"] != "text" || blocks[0]["text"] != "hello" {
		t.Errorf("blocks = %#v, the content must be typed blocks", blocks)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
}

func TestAnthropicBaseWithAndWithoutVersion(t *testing.T) {
	for _, base := range []string{"https://api.example.com", "https://api.example.com/v1", "https://api.example.com/v1/"} {
		client := &AnthropicCompatible{Endpoint: base}
		if got := client.messagesURL(); got != "https://api.example.com/v1/messages" {
			t.Errorf("base %q -> %q", base, got)
		}
	}
}

func TestAnthropicOffersToolsAsInputSchema(t *testing.T) {
	f := newAnthropicFixture(t)
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	_, err := client.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "save this"}}, Model{ID: "m"},
		[]Tool{{Name: "write_file", Description: "propose a file", Parameters: `{"type":"object"}`}},
		func(Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	tools, ok := f.last().Body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", f.last().Body["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "write_file" || tool["description"] != "propose a file" {
		t.Errorf("tool = %#v", tool)
	}
	if _, ok := tool["input_schema"]; !ok {
		t.Errorf("tool = %#v, the Messages API names the schema input_schema", tool)
	}
	if _, ok := tool["function"]; ok {
		t.Errorf("tool = %#v, the OpenAI function wrapper must not be sent", tool)
	}
}

func TestAnthropicToolResultIsAUserBlock(t *testing.T) {
	f := newAnthropicFixture(t)
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	_, err := client.Chat(context.Background(), []Message{
		{Role: "user", Content: "save this"},
		{Role: "assistant", Content: "working", ToolCalls: []ToolCall{{ID: "toolu_1", Name: "write_file", Arguments: map[string]any{"path": "a.txt"}}}},
		{Role: "tool", ToolCallID: "toolu_1", Name: "write_file", Content: "written"},
	}, Model{ID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	body := f.last().Body
	messages := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3: %#v", len(messages), messages)
	}
	assistant := blocksOf(t, body, 1)
	if len(assistant) != 2 {
		t.Fatalf("assistant blocks = %#v", assistant)
	}
	if assistant[1]["type"] != "tool_use" || assistant[1]["id"] != "toolu_1" {
		t.Errorf("tool_use block = %#v", assistant[1])
	}
	input, ok := assistant[1]["input"].(map[string]any)
	if !ok || input["path"] != "a.txt" {
		t.Errorf("input = %#v", assistant[1]["input"])
	}
	result := blocksOf(t, body, 2)
	if len(result) != 1 || result[0]["type"] != "tool_result" || result[0]["tool_use_id"] != "toolu_1" {
		t.Errorf("tool_result block = %#v", result)
	}
	if result[0]["content"] != "written" {
		t.Errorf("tool_result content = %#v", result[0]["content"])
	}
}

func TestAnthropicReadsTheAnswerAndItsCalls(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","content":[
			{"type":"thinking","thinking":"considering"},
			{"type":"text","text":"I will write it."},
			{"type":"tool_use","id":"toolu_1","name":"write_file","input":{"path":"a.txt"}}],
			"stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":5,"cache_read_input_tokens":2}}`)
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	resp, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "I will write it." {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.Reasoning != "considering" {
		t.Errorf("reasoning = %q, the thinking text must not be lost", resp.Reasoning)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].ID != "toolu_1" || resp.ToolCalls[0].Name != "write_file" {
		t.Errorf("tool call = %#v", resp.ToolCalls[0])
	}
	if resp.ToolCalls[0].Arguments["path"] != "a.txt" {
		t.Errorf("arguments = %#v", resp.ToolCalls[0].Arguments)
	}
	if resp.FinishReason != "tool_use" {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if !resp.Stats.HasUsage || resp.Stats.Usage.PromptTokens != 11 || resp.Stats.Usage.CompletionTokens != 5 {
		t.Errorf("usage = %#v", resp.Stats.Usage)
	}
	if resp.Stats.Usage.CachedTokens != 2 {
		t.Errorf("cached tokens = %d", resp.Stats.Usage.CachedTokens)
	}
}

func TestAnthropicStreamAssemblesNamedEvents(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello \"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"there\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		for _, e := range events {
			fmt.Fprint(w, e)
		}
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	var deltas []string
	resp, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"},
		func(d Delta) error { deltas = append(deltas, d.Text); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello there" {
		t.Errorf("content = %q", resp.Content)
	}
	if strings.Join(deltas, "") != "Hello there" {
		t.Errorf("deltas = %v", deltas)
	}
	if resp.FinishReason != "end_turn" {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if !resp.Stats.HasUsage || resp.Stats.Usage.PromptTokens != 7 || resp.Stats.Usage.CompletionTokens != 9 {
		t.Errorf("usage = %#v", resp.Stats.Usage)
	}
	if resp.Stats.Usage.TotalTokens != 16 {
		t.Errorf("total = %d, the two halves must add up", resp.Stats.Usage.TotalTokens)
	}
}

func TestAnthropicStreamCollectsToolCallsInOrder(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"first\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"a\\\":\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"1}\"}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_2\",\"name\":\"second\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"b\\\":2}\"}}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		for _, e := range events {
			fmt.Fprint(w, e)
		}
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	resp, err := client.ChatStreamTools(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"},
		[]Tool{{Name: "first", Parameters: `{"type":"object"}`}}, func(Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Name != "first" || resp.ToolCalls[1].Name != "second" {
		t.Errorf("the calls must keep the order the model produced them: %#v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Arguments["a"] != float64(1) {
		t.Errorf("first arguments = %#v", resp.ToolCalls[0].Arguments)
	}
	if resp.ToolCalls[1].Arguments["b"] != float64(2) {
		t.Errorf("second arguments = %#v", resp.ToolCalls[1].Arguments)
	}
}

func TestAnthropicAuthenticationFailure(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "wrong"}
	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("expected an authentication error, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Errorf("the credential must never appear in an error: %v", err)
	}
}

func TestAnthropicNeverLeaksTheCredential(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 999999 is too large"}}`)
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "top-secret-value"}
	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "top-secret-value") {
		t.Errorf("the credential leaked into the error: %v", err)
	}
	if !strings.Contains(err.Error(), "max_tokens") {
		t.Errorf("the endpoint's own explanation was lost: %v", err)
	}
}

func TestAnthropicMalformedResponse(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		fmt.Fprint(w, `{"content": not json`)
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"})
	if err == nil || !strings.Contains(err.Error(), "bad response") {
		t.Fatalf("expected a bad-response error, got %v", err)
	}
}

func TestAnthropicStreamSkipsMalformedEvents(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {not json}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"kept\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	resp, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "kept" {
		t.Errorf("content = %q, a malformed event must not end the stream", resp.Content)
	}
	if resp.Stats.Malformed != 1 {
		t.Errorf("malformed = %d", resp.Stats.Malformed)
	}
}

func TestAnthropicStreamErrorEvent(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	_, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("expected the endpoint's error, got %v", err)
	}
}

func TestAnthropicModelListingFollowsPagination(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		if call.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if call.Query == "" || !strings.Contains(call.Query, "after_id=model-b") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"model-a","display_name":"Model A"},{"id":"model-b"}],"has_more":true,"last_id":"model-b"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"model-c"}],"has_more":false,"last_id":"model-c"}`)
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	ids, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "model-a" || ids[2] != "model-c" {
		t.Errorf("ids = %v", ids)
	}
	if len(f.calls) != 2 {
		t.Fatalf("the listing must follow the cursor, calls = %d", len(f.calls))
	}
	if got := f.calls[0].Header.Get("anthropic-version"); got != DefaultAnthropicVersion {
		t.Errorf("the listing must announce the API version, got %q", got)
	}
}

func TestAnthropicModelListingSendsTheVersionWithoutAKey(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL}
	if _, err := client.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Header.Get("anthropic-version"); got != DefaultAnthropicVersion {
		t.Errorf("anthropic-version = %q", got)
	}
}

func TestAnthropicEndpointWithoutModelListing(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		http.NotFound(w, r)
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	_, err := client.Models(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not list its models") {
		t.Fatalf("expected a no-listing error, got %v", err)
	}
}

func TestAnthropicBearerGatewayIsExplicit(t *testing.T) {
	f := newAnthropicFixture(t)
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k", KeyHeader: "Authorization"}
	if _, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"}); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Header.Get("Authorization"); got != "Bearer k" {
		t.Errorf("authorization = %q", got)
	}
	if got := f.last().Header.Get("x-api-key"); got != "" {
		t.Errorf("x-api-key = %q", got)
	}
}

func TestAnthropicTurnTimeout(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(2 * time.Second)
	}
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k", TurnTimeout: 200 * time.Millisecond}
	_, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"}, nil)
	if err == nil {
		t.Fatal("expected the turn ceiling to fire")
	}
	if !strings.Contains(err.Error(), "time limit") && !strings.Contains(err.Error(), "idle") {
		t.Errorf("error = %v", err)
	}
}

// The reproduction of the reported failure: an endpoint that speaks the
// Messages API cannot be reached with the OpenAI adapter. This is the
// defect the operator hit, kept as a regression guard.
func TestOpenAIAdapterCannotSpeakAnAnthropicEndpoint(t *testing.T) {
	f := newAnthropicFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request, call recordedCall) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}
	client := &OpenAICompatible{Endpoint: f.server.URL, APIKey: "k"}
	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"})
	if err == nil {
		t.Fatal("the OpenAI adapter must not be able to talk to a Messages endpoint")
	}
	call := f.last()
	if call.Path != "/chat/completions" {
		t.Errorf("the OpenAI adapter posted to %q", call.Path)
	}
	if got := call.Header.Get("Authorization"); got != "Bearer k" {
		t.Errorf("the OpenAI adapter used %q", got)
	}
	if got := call.Header.Get("x-api-key"); got != "" {
		t.Errorf("the OpenAI adapter must not pretend to be the Messages API: %q", got)
	}
}
