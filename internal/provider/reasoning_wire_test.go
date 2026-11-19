package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Reasoning continuity on the wire.
//
// Some providers require their own reasoning to be replayed when the
// conversation continues. DeepSeek's Thinking Mode refuses a
// tool-enabled request whose assistant turn lost its reasoning_content.
// The Anthropic Messages API refuses a continued thinking turn whose
// thinking blocks are missing, and refuses a block whose signature was
// changed.
//
// The tests here use mock servers that enforce those rules on the exact
// payload: an answer of 200 from a permissive server proves nothing.

// wireServer is a mock provider that records the requests it received
// and answers with what the test tells it to.
type wireServer struct {
	t        *testing.T
	requests []map[string]any
	// answers are the bodies the server sends, one per request.
	answers []string
	// check is run on every request. It reports what is wrong with it.
	check func(body map[string]any) string
}

func (s *wireServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.t.Errorf("the request was not JSON: %v", err)
	}
	s.requests = append(s.requests, body)
	if s.check != nil {
		if problem := s.check(body); problem != "" {
			s.t.Errorf("the request breaks the protocol: %s", problem)
		}
	}
	answer := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
	if len(s.answers) > 0 {
		answer = s.answers[0]
		s.answers = s.answers[1:]
	}
	if stream, _ := body["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + answer + "\n\ndata: [DONE]\n\n"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(answer))
}

// last returns the request the server received last.
func (s *wireServer) last() map[string]any {
	if len(s.requests) == 0 {
		return nil
	}
	return s.requests[len(s.requests)-1]
}

// messages returns the messages of the last request.
func (s *wireServer) messages() []map[string]any {
	raw, _ := s.last()["messages"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		msg, _ := m.(map[string]any)
		out = append(out, msg)
	}
	return out
}

// deepSeekToolAnswer is a DeepSeek answer that reasons and asks for a
// tool, in the shape a stream sends it.
const deepSeekToolAnswer = `{"choices":[{"delta":{"content":"","reasoning_content":"the model weighed it","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`

// The DeepSeek protocol: a tool-enabled request must carry the
// reasoning_content of the assistant turn that asked for the tool.
//
// The check is the provider's own rule, not HAL's: the mock refuses the
// request when the field is missing, exactly as DeepSeek does.
func TestDeepSeekReplaysItsReasoningOnTheToolRequest(t *testing.T) {
	srv := &wireServer{t: t, answers: []string{deepSeekToolAnswer}}
	srv.check = func(body map[string]any) string {
		messages, _ := body["messages"].([]any)
		for _, m := range messages {
			msg, _ := m.(map[string]any)
			if msg["role"] != "assistant" {
				continue
			}
			reasoning, ok := msg["reasoning_content"]
			if !ok {
				return "the assistant turn carries no reasoning_content"
			}
			if reasoning != "the model weighed it" {
				return "the reasoning_content was changed"
			}
		}
		return ""
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	p := &OpenAICompatible{Endpoint: ts.URL}
	tools := []Tool{{Name: "read"}}
	messages := []Message{{Role: "user", Content: "read the file"}}

	resp, err := p.ChatStreamTools(context.Background(), messages, Model{ID: "deepseek-flash"}, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ReasoningKey == "" {
		t.Fatal("the turn was not named with its protocol")
	}
	if len(resp.ReasoningBlocks) != 1 || resp.ReasoningBlocks[0].Text != "the model weighed it" {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}

	// The follow-up request carries the assistant turn and its
	// reasoning.
	follow := append(append([]Message(nil), messages...),
		Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks},
		Message{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
	)
	if _, err := p.ChatStreamTools(context.Background(), follow, Model{ID: "deepseek-flash"}, tools, nil); err != nil {
		t.Fatal(err)
	}
	if len(srv.requests) != 2 {
		t.Fatalf("the server saw %d requests", len(srv.requests))
	}
}

// The reasoning of a turn is replayed across user turns as well: the
// provider needs it in every later request that carries tools.
func TestDeepSeekReplaysItsReasoningAcrossUserTurns(t *testing.T) {
	srv := &wireServer{t: t, answers: []string{deepSeekToolAnswer}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	p := &OpenAICompatible{Endpoint: ts.URL}
	tools := []Tool{{Name: "read"}}
	resp, err := p.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		Model{ID: "deepseek-flash"}, tools, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A later turn: the assistant turn of the first one is in the
	// history, and the request carries tools.
	later := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
		{Role: "user", Content: "and now the other one"},
	}
	if _, err := p.ChatStreamTools(context.Background(), later, Model{ID: "deepseek-flash"}, tools, nil); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, msg := range srv.messages() {
		if msg["role"] != "assistant" {
			continue
		}
		if reasoning, ok := msg["reasoning_content"].(string); ok && reasoning == "the model weighed it" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the reasoning was not replayed across the user turn: %v", srv.messages())
	}
}

// An absent field and an empty one are not the same thing to the
// provider: a turn with no reasoning carries no field at all.
func TestDeepSeekOmitsTheReasoningFieldWhenThereIsNone(t *testing.T) {
	srv := &wireServer{t: t, answers: []string{`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	p := &OpenAICompatible{Endpoint: ts.URL}
	tools := []Tool{{Name: "read"}}
	messages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	if _, err := p.ChatStreamTools(context.Background(), messages, Model{ID: "deepseek-flash"}, tools, nil); err != nil {
		t.Fatal(err)
	}
	for _, msg := range srv.messages() {
		if msg["role"] != "assistant" {
			continue
		}
		if _, ok := msg["reasoning_content"]; ok {
			t.Fatalf("the assistant turn carries a reasoning_content it never had: %v", msg)
		}
	}
}

// The requirement is a property of the tool-enabled request: a request
// without tools carries no reasoning.
func TestDeepSeekReplaysNoReasoningWithoutTools(t *testing.T) {
	srv := &wireServer{t: t, answers: []string{`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	p := &OpenAICompatible{Endpoint: ts.URL}
	messages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello",
			ReasoningKey: ReasoningKey("deepseek-flash", WireOpenAI),
			Reasoning:    []ReasoningBlock{{Type: ReasoningPlain, Text: "the model weighed it"}}},
	}
	if _, err := p.ChatStream(context.Background(), messages, Model{ID: "deepseek-flash"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, msg := range srv.messages() {
		if _, ok := msg["reasoning_content"]; ok {
			t.Fatalf("a request without tools carried reasoning: %v", msg)
		}
	}
}

// The reasoning of one protocol is never sent to another: a Claude
// thinking block means nothing to the OpenAI dialect.
func TestReasoningOfAnotherProtocolIsNeverReplayed(t *testing.T) {
	srv := &wireServer{t: t, answers: []string{`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	p := &OpenAICompatible{Endpoint: ts.URL}
	tools := []Tool{{Name: "read"}}
	messages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello",
			ReasoningKey: "anthropic/thinking",
			Reasoning:    []ReasoningBlock{{Type: ReasoningThinking, Text: "signed", Signature: "sig"}}},
	}
	if _, err := p.ChatStreamTools(context.Background(), messages, Model{ID: "deepseek-flash"}, tools, nil); err != nil {
		t.Fatal(err)
	}
	for _, msg := range srv.messages() {
		if _, ok := msg["reasoning_content"]; ok {
			t.Fatalf("the reasoning of another protocol was replayed: %v", msg)
		}
	}
}

// The reasoning of a model is not sent to another model of the same
// wire either: the continuity belongs to the protocol that produced
// it, and a model that speaks another one gets none.
func TestTheReasoningOfOneModelIsNotSentToAnother(t *testing.T) {
	srv := &wireServer{t: t, answers: []string{`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	p := &OpenAICompatible{Endpoint: ts.URL}
	tools := []Tool{{Name: "read"}}
	messages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello",
			ReasoningKey: ReasoningKey("deepseek-flash", WireOpenAI),
			Reasoning:    []ReasoningBlock{{Type: ReasoningPlain, Text: "the model weighed it"}}},
	}
	// Another model of the same wire, and another protocol.
	for _, model := range []string{"MiniMax-M3.1-Flash", "gpt-4o"} {
		srv.requests = nil
		if _, err := p.ChatStreamTools(context.Background(), messages, Model{ID: model}, tools, nil); err != nil {
			t.Fatal(err)
		}
		for _, msg := range srv.messages() {
			if _, ok := msg["reasoning_content"]; ok {
				t.Fatalf("the reasoning was replayed to %q: %v", model, msg)
			}
		}
	}
}

// A turn that asked for several tools at once keeps its reasoning, and
// the follow-up request carries every result with it.
func TestTheReasoningSurvivesATurnWithSeveralTools(t *testing.T) {
	answer := `{"choices":[{"delta":{"content":"","reasoning_content":"the model weighed it",` +
		`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}},` +
		`{"id":"call_2","type":"function","function":{"name":"list","arguments":"{}"}}]},` +
		`"finish_reason":"tool_calls"}]}`
	srv := &wireServer{t: t, answers: []string{answer}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	p := &OpenAICompatible{Endpoint: ts.URL}
	tools := []Tool{{Name: "read"}, {Name: "list"}}
	resp, err := p.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		Model{ID: "deepseek-flash"}, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("the turn asked for %d tools", len(resp.ToolCalls))
	}
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
		{Role: "tool", ToolCallID: "call_2", Name: "list", Content: "the listing"},
	}
	if _, err := p.ChatStreamTools(context.Background(), follow, Model{ID: "deepseek-flash"}, tools, nil); err != nil {
		t.Fatal(err)
	}
	messages := srv.messages()
	var assistant map[string]any
	results := 0
	for _, msg := range messages {
		switch msg["role"] {
		case "assistant":
			assistant = msg
		case "tool":
			results++
		}
	}
	if results != 2 {
		t.Errorf("the follow-up request carries %d results, want 2", results)
	}
	if assistant == nil {
		t.Fatal("the follow-up request carries no assistant turn")
	}
	if assistant["reasoning_content"] != "the model weighed it" {
		t.Errorf("the reasoning was lost: %v", assistant)
	}
}

// A turn whose reasoning was too large to keep cannot be sent without
// it: the request is refused, with the reason.
func TestATurnWithoutItsReasoningIsRefused(t *testing.T) {
	srv := &wireServer{t: t}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	p := &OpenAICompatible{Endpoint: ts.URL}
	tools := []Tool{{Name: "read"}}
	messages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello",
			ReasoningKey:        ReasoningKey("deepseek-flash", WireOpenAI),
			ReasoningIncomplete: true},
	}
	_, err := p.ChatStreamTools(context.Background(), messages, Model{ID: "deepseek-flash"}, tools, nil)
	if err == nil {
		t.Fatal("the request was sent without the reasoning it needed")
	}
	if !strings.Contains(err.Error(), "too large to keep") {
		t.Errorf("error = %v", err)
	}
	if len(srv.requests) != 0 {
		t.Errorf("the request reached the provider: %v", srv.requests)
	}
}

// The Anthropic protocol: a continued thinking turn sends its thinking
// blocks back, with the provider's own signature, before the text and
// the tool call.
func TestClaudeReplaysItsSignedThinkingWithTheToolResult(t *testing.T) {
	srv := &wireServer{t: t}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := &AnthropicCompatible{Endpoint: ts.URL}
	model := Model{ID: "claude-sonnet-4-5", Effort: "high"}
	tools := []Tool{{Name: "read"}}
	messages := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: "reading it",
			ReasoningKey: ReasoningKey(model.ID, WireAnthropic),
			Reasoning: []ReasoningBlock{
				{Type: ReasoningThinking, Text: "the model weighed it", Signature: "sig-abc"},
			}},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
	}
	body, err := a.requestBody(messages, model, false, tools)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	conversation, _ := payload["messages"].([]any)
	if len(conversation) < 2 {
		t.Fatalf("the conversation = %v", payload)
	}
	assistant, _ := conversation[1].(map[string]any)
	blocks, _ := assistant["content"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("the assistant turn carries no blocks: %v", assistant)
	}
	first, _ := blocks[0].(map[string]any)
	if first["type"] != "thinking" {
		t.Fatalf("the first block is %v, want the thinking block", first)
	}
	if first["thinking"] != "the model weighed it" {
		t.Errorf("the thinking was changed: %v", first["thinking"])
	}
	if first["signature"] != "sig-abc" {
		t.Errorf("the signature was changed: %v", first["signature"])
	}
	// The tool result follows the assistant turn, as a user message
	// with a tool_result block.
	user, _ := conversation[2].(map[string]any)
	if user["role"] != "user" {
		t.Fatalf("the tool result is not a user message: %v", user)
	}
	result, _ := user["content"].([]any)
	if len(result) == 0 {
		t.Fatalf("the tool result carries no block: %v", user)
	}
	if block, _ := result[0].(map[string]any); block["type"] != "tool_result" {
		t.Errorf("the tool result block = %v", result[0])
	}
}

// A redacted thinking block is kept exactly as the provider sent it: it
// is the only thing the provider will accept back.
func TestClaudeKeepsRedactedThinking(t *testing.T) {
	srv := &wireServer{t: t}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := &AnthropicCompatible{Endpoint: ts.URL}
	model := Model{ID: "claude-sonnet-4-5", Effort: "high"}
	messages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello",
			ReasoningKey: ReasoningKey(model.ID, WireAnthropic),
			Reasoning: []ReasoningBlock{
				{Type: ReasoningRedacted, Data: "encrypted-payload"},
			}},
	}
	body, err := a.requestBody(messages, model, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	conversation, _ := payload["messages"].([]any)
	assistant, _ := conversation[1].(map[string]any)
	blocks, _ := assistant["content"].([]any)
	first, _ := blocks[0].(map[string]any)
	if first["type"] != "redacted_thinking" {
		t.Fatalf("the first block = %v", first)
	}
	if first["data"] != "encrypted-payload" {
		t.Errorf("the redacted payload was changed: %v", first["data"])
	}
}

// The thinking blocks are replayed only when the request asks for
// thinking: the Messages API refuses an unexpected block when thinking
// is off.
func TestClaudeReplaysNoThinkingWhenThinkingIsOff(t *testing.T) {
	srv := &wireServer{t: t}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := &AnthropicCompatible{Endpoint: ts.URL}
	model := Model{ID: "claude-sonnet-4-5"}
	messages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello",
			ReasoningKey: ReasoningKey(model.ID, WireAnthropic),
			Reasoning: []ReasoningBlock{
				{Type: ReasoningThinking, Text: "the model weighed it", Signature: "sig-abc"},
			}},
	}
	body, err := a.requestBody(messages, model, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["thinking"]; ok {
		t.Errorf("a model with no level asked for thinking: %v", payload)
	}
	conversation, _ := payload["messages"].([]any)
	assistant, _ := conversation[1].(map[string]any)
	blocks, _ := assistant["content"].([]any)
	for _, b := range blocks {
		block, _ := b.(map[string]any)
		if block["type"] == "thinking" || block["type"] == "redacted_thinking" {
			t.Errorf("a thinking block was replayed without thinking: %v", block)
		}
	}
}

// The Anthropic protocol, on the stream: the signature arrives in
// pieces and belongs to the block it came for.
func TestClaudeCapturesTheSignatureFromTheStream(t *testing.T) {
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[],"model":"claude-sonnet-4-5","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"the model weighed it"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"abc"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"the answer"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	defer ts.Close()

	a := &AnthropicCompatible{Endpoint: ts.URL}
	resp, err := a.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		Model{ID: "claude-sonnet-4-5", Effort: "high"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 1 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}
	block := resp.ReasoningBlocks[0]
	if block.Type != ReasoningThinking {
		t.Errorf("the block type = %q", block.Type)
	}
	if block.Text != "the model weighed it" {
		t.Errorf("the thinking = %q", block.Text)
	}
	if block.Signature != "sig-abc" {
		t.Errorf("the signature = %q, want the provider's own", block.Signature)
	}
	if resp.ReasoningKey != ReasoningKey("claude-sonnet-4-5", WireAnthropic) {
		t.Errorf("the turn was not named with its protocol: %q", resp.ReasoningKey)
	}
}

// The reasoning of a turn that does not stream is captured as well: the
// provider's own, not a summary of it.
func TestClaudeCapturesItsThinkingWithoutStreaming(t *testing.T) {
	answer := `{"id":"msg_1","type":"message","role":"assistant","content":[` +
		`{"type":"thinking","thinking":"the model weighed it","signature":"sig-abc"},` +
		`{"type":"text","text":"the answer"}],"model":"claude-sonnet-4-5","stop_reason":"end_turn"}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	defer ts.Close()

	a := &AnthropicCompatible{Endpoint: ts.URL}
	resp, err := a.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		Model{ID: "claude-sonnet-4-5", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 1 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}
	block := resp.ReasoningBlocks[0]
	if block.Text != "the model weighed it" || block.Signature != "sig-abc" {
		t.Errorf("the block = %+v", block)
	}
}

// The DeepSeek reasoning of a turn that does not stream is captured as
// well.
func TestDeepSeekCapturesItsReasoningWithoutStreaming(t *testing.T) {
	answer := `{"choices":[{"message":{"content":"the answer","reasoning_content":"the model weighed it"},"finish_reason":"stop"}]}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	defer ts.Close()

	p := &OpenAICompatible{Endpoint: ts.URL}
	resp, err := p.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		Model{ID: "deepseek-flash"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reasoning != "the model weighed it" {
		t.Errorf("the reasoning = %q", resp.Reasoning)
	}
	if len(resp.ReasoningBlocks) != 1 || resp.ReasoningBlocks[0].Text != "the model weighed it" {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}
	if resp.ReasoningKey != ReasoningKey("deepseek-flash", WireOpenAI) {
		t.Errorf("the turn was not named with its protocol: %q", resp.ReasoningKey)
	}
}

// The encoded form of the continuity data is opaque to the record: it
// names its protocol and carries the provider's own blocks.
func TestTheContinuityRecordCarriesTheProvidersOwnBlocks(t *testing.T) {
	blocks := []ReasoningBlock{
		{Type: ReasoningThinking, Text: "the model weighed it", Signature: "sig-abc"},
		{Type: ReasoningRedacted, Data: "encrypted"},
	}
	payload := EncodeReasoning("anthropic/thinking", "claude-sonnet-4-5", blocks)
	if len(payload) == 0 {
		t.Fatal("nothing was encoded")
	}
	key, model, decoded, ok := DecodeReasoning(payload)
	if !ok {
		t.Fatal("the payload was not read back")
	}
	if key != "anthropic/thinking" {
		t.Errorf("the key = %q", key)
	}
	if model != "claude-sonnet-4-5" {
		t.Errorf("the model = %q", model)
	}
	if len(decoded) != 2 {
		t.Fatalf("the blocks = %+v", decoded)
	}
	if decoded[0].Signature != "sig-abc" || decoded[1].Data != "encrypted" {
		t.Errorf("the blocks were changed: %+v", decoded)
	}
}

// A payload that is not one of ours is dropped, never guessed at.
func TestAnUnknownPayloadIsDropped(t *testing.T) {
	if _, _, _, ok := DecodeReasoning([]byte("not json")); ok {
		t.Error("a malformed payload was read")
	}
	if _, _, _, ok := DecodeReasoning(nil); ok {
		t.Error("an empty payload was read")
	}
	if _, _, _, ok := DecodeReasoning([]byte(`{"key":""}`)); ok {
		t.Error("a payload with no key was read")
	}
}

// A payload that kept its key but not its data says so: the request
// that needs it is refused rather than sent without it.
func TestAKeptKeyWithoutDataIsAnIncompleteRecord(t *testing.T) {
	payload := EncodeReasoningKey("openai/reasoning-content")
	key, _, blocks, ok := DecodeReasoning(payload)
	if !ok {
		t.Fatal("the payload was not read back")
	}
	if key != "openai/reasoning-content" {
		t.Errorf("the key = %q", key)
	}
	if len(blocks) != 0 {
		t.Errorf("the record carries data it does not have: %+v", blocks)
	}
}
