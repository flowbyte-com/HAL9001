package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// DeepSeek on the Anthropic-compatible Messages endpoint.
//
// DeepSeek documents the endpoint at https://api.deepseek.com/anthropic.
// Its own rules for a continued tool call in thinking mode are the ones
// the Messages API documents, with one difference that matters here: the
// thinking blocks are DeepSeek's, not Claude's, and they are kept under
// a protocol of their own.
//
// The mock below enforces those rules on the exact payload and answers
// 400 when they are broken. An answer of 200 from a permissive mock
// proves nothing.

// strictMessages is a mock Messages endpoint that refuses a request
// that breaks the documented replay rules.
//
// It remembers the assistant content array it sent, and it refuses a
// continuation that does not carry it back whole: the thinking blocks
// with their text, in their order, and the tool calls with their
// identities.
type strictMessages struct {
	t        *testing.T
	mu       sync.Mutex
	requests []map[string]any
	// answers are the responses the endpoint sends, one per request.
	answers []string
	// violations counts the requests the endpoint refused.
	violations int
}

// adapter builds the adapter this mock is reached through.
//
// The mock is DeepSeek's Anthropic-compatible endpoint, and that
// endpoint thinks when the request says nothing about it. HAL is told
// so: what HAL knows about an endpoint comes from the endpoint, never
// from the model identifier.
func (s *strictMessages) adapter(ts *httptest.Server) *AnthropicCompatible {
	return &AnthropicCompatible{Endpoint: ts.URL, ThinkingDefault: ThinkingDefaultOn}
}

// ServeHTTP implements the mock endpoint.
func (s *strictMessages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.t.Errorf("the request was not JSON: %v", err)
	}
	s.mu.Lock()
	s.requests = append(s.requests, body)
	answer := deepSeekAnswer
	if len(s.answers) > 0 {
		answer = s.answers[0]
		s.answers = s.answers[1:]
	}
	s.mu.Unlock()

	if problem := s.check(body); problem != "" {
		s.mu.Lock()
		s.violations++
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"` +
			problem + `"}}`))
		return
	}
	if stream, _ := body["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasPrefix(answer, "event: ") {
			// The answer is already the stream the provider sent.
			_, _ = w.Write([]byte(answer))
			return
		}
		_, _ = w.Write([]byte(streamOf(s.t, answer)))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(answer))
}

// streamOf turns a whole answer into the stream the provider would
// send it as.
//
// The same answer, in both shapes, is what the capture has to agree
// with: the block boundaries, their order and their text are the same
// either way.
func streamOf(t *testing.T, answer string) string {
	t.Helper()
	var message struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type      string          `json:"type"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal([]byte(answer), &message); err != nil {
		t.Fatalf("the answer was not JSON: %v", err)
	}
	var out strings.Builder
	write := func(event, data string) {
		out.WriteString("event: " + event + "\n")
		out.WriteString("data: " + data + "\n\n")
	}
	start, _ := json.Marshal(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": message.ID, "role": "assistant", "content": []any{},
			"model": message.Model, "stop_reason": nil,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 1},
		},
	})
	write("message_start", string(start))
	for i, block := range message.Content {
		open := map[string]any{"type": "content_block_start", "index": i}
		var delta map[string]any
		switch block.Type {
		case "thinking":
			open["content_block"] = map[string]any{"type": "thinking", "thinking": ""}
			if block.Thinking != "" {
				delta = map[string]any{"type": "thinking_delta", "thinking": block.Thinking}
			}
		case "text":
			open["content_block"] = map[string]any{"type": "text", "text": ""}
			if block.Text != "" {
				delta = map[string]any{"type": "text_delta", "text": block.Text}
			}
		case "tool_use":
			open["content_block"] = map[string]any{
				"type": "tool_use", "id": block.ID, "name": block.Name, "input": map[string]any{},
			}
			if len(block.Input) > 0 {
				delta = map[string]any{"type": "input_json_delta", "partial_json": string(block.Input)}
			}
		}
		openBytes, _ := json.Marshal(open)
		write("content_block_start", string(openBytes))
		if delta != nil {
			deltaBytes, _ := json.Marshal(map[string]any{
				"type": "content_block_delta", "index": i, "delta": delta,
			})
			write("content_block_delta", string(deltaBytes))
		}
		if block.Signature != "" {
			// The signature of a thinking block arrives in its own
			// delta, the way the Messages API sends it.
			signature, _ := json.Marshal(map[string]any{
				"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "signature_delta", "signature": block.Signature},
			})
			write("content_block_delta", string(signature))
		}
		stop, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": i})
		write("content_block_stop", string(stop))
	}
	final, _ := json.Marshal(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": message.StopReason},
		"usage": map[string]any{"output_tokens": 5},
	})
	write("message_delta", string(final))
	write("message_stop", `{"type":"message_stop"}`)
	return out.String()
}

// check reports what is wrong with a request, or the empty string when
// the request breaks no rule.
func (s *strictMessages) check(body map[string]any) string {
	messages, _ := body["messages"].([]any)
	for i, raw := range messages {
		msg, _ := raw.(map[string]any)
		if msg["role"] != "assistant" {
			continue
		}
		blocks, _ := msg["content"].([]any)
		var (
			thinking  []any
			toolUses  []any
			texts     int
			sawText   bool
			sawReason bool
		)
		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]any)
			switch block["type"] {
			case "thinking":
				thinking = append(thinking, block)
				if sawText {
					return "a thinking block came after the text"
				}
				sawReason = true
			case "redacted_thinking":
				return "a redacted thinking block is not supported on this endpoint"
			case "text":
				texts++
				sawText = true
			case "tool_use":
				toolUses = append(toolUses, block)
			}
		}
		_ = sawReason
		if len(toolUses) == 0 {
			continue
		}
		// A tool turn must carry its thinking blocks: the endpoint
		// may recover them, but it may not, and a request that leaves
		// them out is refused.
		if len(thinking) == 0 {
			return "the tool turn carries no thinking block"
		}
		// The thinking blocks come first, then the text, then the
		// calls.
		if texts > 0 && thinking[0] == nil {
			return "the thinking block came after the tool call"
		}
		// The next message is the result of the calls.
		if i+1 >= len(messages) {
			return "the tool turn is not followed by its results"
		}
		next, _ := messages[i+1].(map[string]any)
		if next["role"] != "user" {
			return "the results of a tool turn are not a user message"
		}
		nextBlocks, _ := next["content"].([]any)
		results := 0
		for _, rawBlock := range nextBlocks {
			block, _ := rawBlock.(map[string]any)
			if block["type"] == "tool_result" {
				results++
				found := false
				for _, rawUse := range toolUses {
					use, _ := rawUse.(map[string]any)
					if use["id"] == block["tool_use_id"] {
						found = true
						break
					}
				}
				if !found {
					return "a tool result names no call of the turn"
				}
			}
		}
		if results != len(toolUses) {
			return "a tool turn is not followed by a result for every call"
		}
	}
	return ""
}

// last returns the request the endpoint received last.
func (s *strictMessages) last() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return nil
	}
	return s.requests[len(s.requests)-1]
}

// assistantBlocks returns the content blocks of the last assistant turn
// of the last request.
func (s *strictMessages) assistantBlocks() []map[string]any {
	messages, _ := s.last()["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		msg, _ := messages[i].(map[string]any)
		if msg["role"] != "assistant" {
			continue
		}
		raw, _ := msg["content"].([]any)
		out := make([]map[string]any, 0, len(raw))
		for _, b := range raw {
			block, _ := b.(map[string]any)
			out = append(out, block)
		}
		return out
	}
	return nil
}

// deepSeekThinkingAnswer is a DeepSeek answer on the Messages wire: a
// thinking block and a tool call.
const deepSeekThinkingAnswer = `{"id":"msg_1","type":"message","role":"assistant",` +
	`"content":[{"type":"thinking","thinking":"the model weighed it"},{"type":"tool_use","id":"call_1","name":"read","input":{"path":"a.md"}}],` +
	`"model":"deepseek-flash","stop_reason":"tool_use"}`

// deepSeekAnswer is a DeepSeek answer on the Messages wire with a
// thinking block and an answer.
const deepSeekAnswer = `{"id":"msg_2","type":"message","role":"assistant",` +
	`"content":[{"type":"thinking","thinking":"the model weighed it"},{"type":"text","text":"the answer"}],` +
	`"model":"deepseek-flash","stop_reason":"end_turn"}`

// deepSeekModel is the model DeepSeek serves on its Anthropic-compatible
// endpoint.
var deepSeekModel = Model{ID: "deepseek-flash", Effort: "high"}

// The DeepSeek Anthropic wire keeps continuity of its own.
//
// The thinking blocks the provider sent come back with the tool turn,
// whole and in their order, and the request that follows them is
// accepted. Before the fix the blocks were never kept: the protocol key
// of DeepSeek on this wire was empty, so the turn went out without them
// and the endpoint refused it.
func TestDeepSeekOnTheAnthropicWireReplaysItsThinkingBlocks(t *testing.T) {
	srv := &strictMessages{t: t, answers: []string{deepSeekThinkingAnswer}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		deepSeekModel, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ReasoningKey == "" {
		t.Fatal("the turn was not named with its protocol")
	}
	if resp.ReasoningKey == ReasoningKey("claude-sonnet-4-5", WireAnthropic) {
		t.Error("the DeepSeek wire shares the Claude protocol")
	}
	if len(resp.ReasoningBlocks) != 1 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}
	if resp.ReasoningBlocks[0].Text != "the model weighed it" {
		t.Errorf("the thinking = %q", resp.ReasoningBlocks[0].Text)
	}
	if resp.ReasoningBlocks[0].Signature != "" {
		t.Errorf("a signature was invented: %q", resp.ReasoningBlocks[0].Signature)
	}

	// The continuation carries the whole assistant content array.
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow, deepSeekModel, tools, nil); err != nil {
		t.Fatal(err)
	}
	blocks := srv.assistantBlocks()
	if len(blocks) != 2 {
		t.Fatalf("the replayed assistant turn = %+v", blocks)
	}
	if blocks[0]["type"] != "thinking" || blocks[0]["thinking"] != "the model weighed it" {
		t.Errorf("the thinking block = %+v", blocks[0])
	}
	if blocks[1]["type"] != "tool_use" || blocks[1]["id"] != "call_1" {
		t.Errorf("the tool call = %+v", blocks[1])
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// The thinking blocks come back whole, in their order, and the tool
// calls keep their identities: the endpoint compares the array it sent
// with the array it gets.
func TestDeepSeekOnTheAnthropicWireKeepsEveryBlock(t *testing.T) {
	answer := `{"id":"msg_1","type":"message","role":"assistant",` +
		`"content":[{"type":"thinking","thinking":"first thought"},` +
		`{"type":"thinking","thinking":"second thought"},` +
		`{"type":"text","text":"reading it"},` +
		`{"type":"tool_use","id":"call_1","name":"read","input":{"path":"a.md"}},` +
		`{"type":"tool_use","id":"call_2","name":"list","input":{}}],` +
		`"model":"deepseek-flash","stop_reason":"tool_use"}`
	srv := &strictMessages{t: t, answers: []string{answer}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}, {Name: "list"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		deepSeekModel, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 2 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}
	if resp.ReasoningBlocks[0].Text != "first thought" ||
		resp.ReasoningBlocks[1].Text != "second thought" {
		t.Errorf("the blocks are out of order: %+v", resp.ReasoningBlocks)
	}

	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
		{Role: "tool", ToolCallID: "call_2", Name: "list", Content: "the listing"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow, deepSeekModel, tools, nil); err != nil {
		t.Fatal(err)
	}
	blocks := srv.assistantBlocks()
	if len(blocks) != 5 {
		t.Fatalf("the replayed assistant turn = %+v", blocks)
	}
	want := []struct{ kind, field, text, id string }{
		{"thinking", "thinking", "first thought", ""},
		{"thinking", "thinking", "second thought", ""},
		{"text", "text", "reading it", ""},
		{"tool_use", "", "", "call_1"},
		{"tool_use", "", "", "call_2"},
	}
	for i, w := range want {
		if blocks[i]["type"] != w.kind {
			t.Errorf("block %d = %+v, want a %s block", i, blocks[i], w.kind)
			continue
		}
		if w.field != "" && blocks[i][w.field] != w.text {
			t.Errorf("block %d = %+v", i, blocks[i])
		}
		if w.id != "" && blocks[i]["id"] != w.id {
			t.Errorf("block %d = %+v", i, blocks[i])
		}
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// Several continuations in a row keep their blocks: every one of them
// carries the thinking of the turn it continues.
func TestDeepSeekOnTheAnthropicWireSurvivesSeveralContinuations(t *testing.T) {
	second := `{"id":"msg_2","type":"message","role":"assistant",` +
		`"content":[{"type":"thinking","thinking":"and then the listing"},` +
		`{"type":"tool_use","id":"call_2","name":"list","input":{}}],` +
		`"model":"deepseek-flash","stop_reason":"tool_use"}`
	srv := &strictMessages{t: t, answers: []string{
		deepSeekThinkingAnswer, second, deepSeekAnswer,
	}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}, {Name: "list"}}
	messages := []Message{{Role: "user", Content: "read the file"}}
	for step := 0; step < 3; step++ {
		resp, err := a.ChatStreamTools(context.Background(), messages, deepSeekModel, tools, nil)
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		messages = append(messages,
			Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
				ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin})
		for _, call := range resp.ToolCalls {
			messages = append(messages, Message{
				Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: "the result",
			})
		}
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// The stream carries the same blocks as the whole answer.
func TestDeepSeekOnTheAnthropicWireCapturesTheStreamedBlocks(t *testing.T) {
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[],"model":"deepseek-flash","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"the model weighed it"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"read","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	srv := &strictMessages{t: t, answers: []string{stream}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		deepSeekModel, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 1 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}
	if resp.ReasoningBlocks[0].Text != "the model weighed it" {
		t.Errorf("the thinking = %q", resp.ReasoningBlocks[0].Text)
	}
	if resp.ReasoningKey != ReasoningKey("deepseek-flash", WireAnthropic) {
		t.Errorf("the turn was not named with its protocol: %q", resp.ReasoningKey)
	}

	// The continuation carries the streamed blocks.
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow, deepSeekModel, tools, nil); err != nil {
		t.Fatal(err)
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// A response with no thinking block at all keeps nothing: there is
// nothing to replay, and the request is not refused for it.
func TestDeepSeekOnTheAnthropicWireReplaysNothingWithoutThinking(t *testing.T) {
	answer := `{"id":"msg_1","type":"message","role":"assistant",` +
		`"content":[{"type":"tool_use","id":"call_1","name":"read","input":{}}],` +
		`"model":"deepseek-flash","stop_reason":"tool_use"}`
	srv := &strictMessages{t: t, answers: []string{answer}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		deepSeekModel, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 0 {
		t.Fatalf("blocks were invented: %+v", resp.ReasoningBlocks)
	}
	if resp.ReasoningKey != "" {
		t.Errorf("the turn was named with a protocol it does not speak: %q", resp.ReasoningKey)
	}
}

// A thinking block with no text is still a block: it is kept as a block
// of its own, and it is not filled with anything.
func TestDeepSeekOnTheAnthropicWireKeepsAnEmptyThinkingBlock(t *testing.T) {
	answer := `{"id":"msg_1","type":"message","role":"assistant",` +
		`"content":[{"type":"thinking","thinking":""},` +
		`{"type":"tool_use","id":"call_1","name":"read","input":{}}],` +
		`"model":"deepseek-flash","stop_reason":"tool_use"}`
	srv := &strictMessages{t: t, answers: []string{answer}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		deepSeekModel, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 1 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}
	if resp.ReasoningBlocks[0].Type != ReasoningThinking {
		t.Errorf("the block type = %q", resp.ReasoningBlocks[0].Type)
	}
	if resp.ReasoningBlocks[0].Text != "" {
		t.Errorf("the block was filled with something: %q", resp.ReasoningBlocks[0].Text)
	}
}

// A record whose data could not be kept is refused, not sent without.
func TestDeepSeekOnTheAnthropicWireRefusesAnUnreadableRecord(t *testing.T) {
	srv := &strictMessages{t: t}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	messages := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: "reading it",
			ReasoningKey:        ReasoningKey("deepseek-flash", WireAnthropic),
			ReasoningIncomplete: true},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
	}
	_, err := a.ChatStreamTools(context.Background(), messages, deepSeekModel, tools, nil)
	if err == nil {
		t.Fatal("the request was sent without the blocks it needed")
	}
	if !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("error = %v", err)
	}
	if len(srv.requests) != 0 {
		t.Errorf("the request reached the provider: %v", srv.requests)
	}
}

// The protocols stay apart.
//
// A Claude thinking block is never replayed as DeepSeek thinking, a
// DeepSeek block on the OpenAI dialect is never replayed on the
// Messages wire, and the reasoning_content of the OpenAI dialect is
// never sent to the Messages API.
func TestTheProtocolsStayApart(t *testing.T) {
	claudeKey := ReasoningKey("claude-sonnet-4-5", WireAnthropic)
	deepSeekKey := ReasoningKey("deepseek-flash", WireAnthropic)
	openAIKey := ReasoningKey("deepseek-flash", WireOpenAI)
	if claudeKey == "" || deepSeekKey == "" || openAIKey == "" {
		t.Fatalf("the keys are missing: %q %q %q", claudeKey, deepSeekKey, openAIKey)
	}
	if claudeKey == deepSeekKey {
		t.Error("DeepSeek and Claude share a protocol on the same wire")
	}
	if deepSeekKey == openAIKey {
		t.Error("DeepSeek shares a protocol across two wires")
	}

	// A Claude block on the DeepSeek wire is not replayed.
	srv := &strictMessages{t: t, answers: []string{deepSeekAnswer}}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	a := srv.adapter(ts)
	messages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello",
			ReasoningKey: claudeKey,
			Origin:       string(a.origin()),
			Reasoning:    []ReasoningBlock{{Type: ReasoningThinking, Text: "signed", Signature: "sig"}}},
	}
	if _, err := a.ChatStreamTools(context.Background(), messages, deepSeekModel, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, block := range srv.assistantBlocks() {
		if block["type"] == "thinking" {
			t.Errorf("a Claude thinking block was replayed to DeepSeek: %+v", block)
		}
	}

	// A DeepSeek block on the OpenAI dialect is not sent as reasoning_content.
	openai := &wireServer{t: t, answers: []string{`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`}}
	ots := httptest.NewServer(openai)
	defer ots.Close()
	p := &OpenAICompatible{Endpoint: ots.URL}
	openaiMessages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello",
			ReasoningKey: deepSeekKey,
			Origin:       string(p.origin()),
			Reasoning:    []ReasoningBlock{{Type: ReasoningThinking, Text: "signed", Signature: "sig"}}},
	}
	if _, err := p.ChatStreamTools(context.Background(), openaiMessages,
		Model{ID: "deepseek-flash"}, []Tool{{Name: "read"}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, msg := range openai.messages() {
		if _, ok := msg["reasoning_content"]; ok {
			t.Errorf("a Messages-wire block was sent as reasoning_content: %v", msg)
		}
	}
}

// The whole answer carries the same blocks as the stream.
//
// A provider that does not stream sends the content array in one piece;
// the blocks are kept from it exactly as they are kept from the stream.
func TestDeepSeekOnTheAnthropicWireCapturesTheWholeAnswer(t *testing.T) {
	srv := &strictMessages{t: t, answers: []string{deepSeekThinkingAnswer}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	resp, err := a.Chat(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		deepSeekModel)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 1 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}
	if resp.ReasoningBlocks[0].Text != "the model weighed it" {
		t.Errorf("the thinking = %q", resp.ReasoningBlocks[0].Text)
	}
	if resp.ReasoningKey != ReasoningKey("deepseek-flash", WireAnthropic) {
		t.Errorf("the turn was not named with its protocol: %q", resp.ReasoningKey)
	}

	// The continuation carries the blocks of the whole answer.
	tools := []Tool{{Name: "read"}}
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow, deepSeekModel, tools, nil); err != nil {
		t.Fatal(err)
	}
	blocks := srv.assistantBlocks()
	if len(blocks) != 2 || blocks[0]["type"] != "thinking" {
		t.Fatalf("the replayed assistant turn = %+v", blocks)
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// DeepSeek's thinking is on by default: the endpoint enables it when the
// request says nothing about it.
//
// A turn that continues must therefore carry its blocks back even when
// HAL sends no thinking toggle of its own — which is what happens when
// the operator configured no effort level. The blocks are the ones the
// endpoint sent.
func TestDeepSeekOnTheAnthropicWireReplaysWithoutAConfiguredEffort(t *testing.T) {
	srv := &strictMessages{t: t, answers: []string{deepSeekThinkingAnswer}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	// No effort: the request carries no thinking toggle at all.
	model := Model{ID: "deepseek-flash"}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		model, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 1 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}

	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow, model, tools, nil); err != nil {
		t.Fatal(err)
	}
	blocks := srv.assistantBlocks()
	if len(blocks) != 2 {
		t.Fatalf("the replayed assistant turn = %+v", blocks)
	}
	if blocks[0]["type"] != "thinking" {
		t.Errorf("the thinking block was left out: %+v", blocks)
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// The endpoint gave no signature, so none is sent.
//
// An empty signature is not the same as an invented one: the field is
// left out of the block entirely.
func TestDeepSeekOnTheAnthropicWireSendsNoSignatureItWasNotGiven(t *testing.T) {
	srv := &strictMessages{t: t, answers: []string{deepSeekThinkingAnswer}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		deepSeekModel, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow, deepSeekModel, tools, nil); err != nil {
		t.Fatal(err)
	}
	block := srv.assistantBlocks()[0]
	if _, ok := block["signature"]; ok {
		t.Errorf("a signature was sent that the endpoint never gave: %+v", block)
	}
}

// The endpoint refuses a turn that leaves its thinking out.
//
// This is the negative control: the mock is strict, so a request that
// breaks the documented rule is answered with 400 and the whole point
// of the replay is proved rather than assumed.
func TestTheStrictEndpointRefusesATurnWithoutItsThinkingBlocks(t *testing.T) {
	srv := &strictMessages{t: t, answers: []string{deepSeekThinkingAnswer}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	// The turn goes out without the blocks it should carry.
	messages := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: "reading it",
			ToolCalls: []ToolCall{{ID: "call_1", Name: "read"}}},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "the file"},
	}
	_, err := a.ChatStreamTools(context.Background(), messages, deepSeekModel, tools, nil)
	if err == nil {
		t.Fatal("the endpoint accepted a turn without its thinking blocks")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %v", err)
	}
	if srv.violations != 1 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}
