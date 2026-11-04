package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The adapter offers functions and reports the calls a model makes. It
// never executes anything: a call is data on its way to the engine.

func TestChatStreamToolsOffersTheFunction(t *testing.T) {
	var captured capturedRequest
	srv := streamServer(t, []string{
		`{"choices":[{"delta":{"content":"Preparing."}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}, &captured)
	defer srv.Close()

	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	_, err := client.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "save this"}},
		Model{ID: "m"}, []Tool{{
			Name:        "write_file",
			Description: "propose a file",
			Parameters:  `{"type":"object","properties":{"path":{"type":"string"}}}`,
		}}, func(Delta) error { return nil })
	if err != nil {
		t.Fatalf("ChatStreamTools: %v", err)
	}
	tools, ok := captured.body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", captured.body["tools"])
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "write_file" || fn["description"] != "propose a file" {
		t.Errorf("function = %#v", fn)
	}
	if _, ok := fn["parameters"].(map[string]any); !ok {
		t.Errorf("parameters = %#v", fn["parameters"])
	}
}

func TestChatWithoutToolsOmitsTheField(t *testing.T) {
	var captured capturedRequest
	srv := streamServer(t, []string{
		`{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`,
	}, &captured)
	defer srv.Close()
	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	if _, err := client.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, Model{ID: "m"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := captured.body["tools"]; ok {
		t.Error("a conversation without a workspace must not offer tools")
	}
}

func TestChatParsesToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Here it is.","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"note.md\",\"content\":\"hello\"}"}}
		]},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	resp, err := client.Chat(context.Background(),
		[]Message{{Role: "user", Content: "save"}}, Model{ID: "m"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "Here it is." {
		t.Errorf("content = %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("calls = %#v", resp.ToolCalls)
	}
	call := resp.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "write_file" {
		t.Errorf("call = %#v", call)
	}
	if call.Arguments["path"] != "note.md" || call.Arguments["content"] != "hello" {
		t.Errorf("arguments = %#v", call.Arguments)
	}
}

func TestChatStreamAssemblesToolCallDeltas(t *testing.T) {
	srv := streamServer(t, []string{
		`{"choices":[{"delta":{"content":"Preparing.","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write_","arguments":"{\"path\":\"no"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"file","arguments":"te.md\",\"content\":\"hi\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}, nil)
	defer srv.Close()

	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	var emitted int
	resp, err := client.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "save"}}, Model{ID: "m"},
		[]Tool{{Name: "write_file"}}, func(Delta) error { emitted++; return nil })
	if err != nil {
		t.Fatalf("ChatStreamTools: %v", err)
	}
	if emitted == 0 {
		t.Error("no text deltas were emitted")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("calls = %#v", resp.ToolCalls)
	}
	call := resp.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "write_file" {
		t.Errorf("call = %#v", call)
	}
	if call.Arguments["path"] != "note.md" || call.Arguments["content"] != "hi" {
		t.Errorf("arguments = %#v", call.Arguments)
	}
}

func TestToolCallWithBadArgumentsIsStillReported(t *testing.T) {
	srv := streamServer(t, []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"write_file","arguments":"not json"}}]},"finish_reason":"tool_calls"}]}`,
	}, nil)
	defer srv.Close()
	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	resp, err := client.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, nil, func(Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("calls = %#v", resp.ToolCalls)
	}
	if raw, ok := resp.ToolCalls[0].Arguments["_invalid"]; !ok || raw != "not json" {
		t.Errorf("arguments = %#v", resp.ToolCalls[0].Arguments)
	}
}

func TestToolCallSetMergesByIndex(t *testing.T) {
	var set toolCallSet
	one, two := 0, 1
	set.add([]openAIToolCall{{Index: &one, ID: "a", Function: struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}{Name: "write_file", Arguments: `{"a":`}}})
	set.add([]openAIToolCall{{Index: &two, ID: "b"}})
	set.add([]openAIToolCall{{Index: &one, Function: struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}{Arguments: `1}`}}})
	calls := set.list()
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].Function.Arguments != `{"a":1}` || calls[1].ID != "b" {
		t.Errorf("calls = %#v", calls)
	}
}

func TestRequestCarriesAssistantToolCallsAndToolResults(t *testing.T) {
	client := &OpenAICompatible{Endpoint: "http://example.test"}
	body, err := client.requestBodyTools([]Message{
		{Role: "user", Content: "read it"},
		{Role: "assistant", Content: "I'll look.", ToolCalls: []ToolCall{{
			ID: "call_1", Name: "read",
			Arguments: map[string]any{"path": "/tmp/note"},
		}}},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: `{"ok":true}`},
	}, Model{ID: "m"}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Messages) != 3 {
		t.Fatalf("messages = %d", len(parsed.Messages))
	}
	assistant := parsed.Messages[1]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant = %#v", assistant)
	}
	call := assistant.ToolCalls[0]
	if call.ID != "call_1" || call.Type != "function" ||
		call.Function.Name != "read" ||
		call.Function.Arguments != `{"path":"/tmp/note"}` {
		t.Errorf("tool call = %#v", call)
	}
	result := parsed.Messages[2]
	if result.Role != "tool" || result.ToolCallID != "call_1" || result.Content != `{"ok":true}` {
		t.Errorf("tool result = %#v", result)
	}
}

func TestInvalidToolArgumentsArePreservedInWireForm(t *testing.T) {
	raw := ToolCall{Arguments: map[string]any{"_invalid": "not json"}}.ArgumentsJSON()
	if raw != "not json" {
		t.Errorf("invalid arguments = %q", raw)
	}
}

func TestRequestWithAnInvalidSchemaStillMarshals(t *testing.T) {
	client := &OpenAICompatible{Endpoint: "http://example.test"}
	body, err := client.requestBodyTools([]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, true,
		[]Tool{{Name: "write_file", Parameters: `{"type":"object"}`}})
	if err != nil {
		t.Fatalf("requestBodyTools: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"write_file"`) {
		t.Errorf("body = %s", body)
	}
}
