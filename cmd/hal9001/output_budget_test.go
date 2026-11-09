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
	"unicode"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/knowledge"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/skills"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// The output budget on the wire.
//
// HAL's calculated budget and the request the provider receives must
// agree: the output cap on the wire is the room the budget reserved
// for the answer, and the request fits the window the endpoint really
// enforces.
//
// The endpoint in these tests counts the tokens with its own rule —
// deliberately not HAL's estimate — and refuses a request that does not
// fit, the way a provider would. A budget that only HAL's own estimate
// can confirm is not a budget.

// enforcingEndpoint is a recording endpoint that enforces a context
// window with its own token count.
//
// The count is deliberately independent of HAL's: HAL counts four
// characters per token, this counts words and punctuation and never
// fewer than three characters per token. It is the stricter of the two,
// so a request HAL believes fits is a request that really fits.
type enforcingEndpoint struct {
	*httptest.Server
	// window is the total the endpoint accepts, in its own tokens.
	window int
	// advertised is the window the endpoint reports in its catalogue.
	// Zero means it reports nothing at all.
	advertised int
	// anthropic makes the endpoint speak the Messages protocol.
	anthropic bool

	mu sync.Mutex
	// requests are the requests the endpoint accepted.
	requests []map[string]any
	// refused are the requests the endpoint turned away.
	refused []map[string]any
}

// newEnforcingEndpoint starts an endpoint that accepts window tokens in
// all and advertises advertised tokens in its catalogue.
func newEnforcingEndpoint(t *testing.T, window, advertised int, anthropic bool) *enforcingEndpoint {
	t.Helper()
	e := &enforcingEndpoint{window: window, advertised: advertised, anthropic: anthropic}
	e.Server = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.Close)
	return e
}

// serve answers one request.
func (e *enforcingEndpoint) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)
	if strings.HasSuffix(r.URL.Path, "/models") {
		w.Header().Set("Content-Type", "application/json")
		if e.advertised > 0 {
			fmt.Fprintf(w, `{"data":[{"id":"shared","context_window":%d}]}`, e.advertised)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"shared"}]}`)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/messages") &&
		!strings.HasSuffix(r.URL.Path, "/chat/completions") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		return
	}
	prompt := requestTokens(body)
	answer := 0
	switch cap := body["max_tokens"].(type) {
	case float64:
		answer = int(cap)
	case int:
		answer = cap
	}
	e.mu.Lock()
	e.requests = append(e.requests, body)
	e.mu.Unlock()
	if prompt+answer > e.window {
		// The endpoint refuses the request the way a provider does.
		e.mu.Lock()
		e.refused = append(e.refused, body)
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w,
			`{"error":{"message":"the request needs about %d tokens but the model accepts %d",`+
				`"type":"invalid_request_error"}}`, prompt+answer, e.window)
		return
	}
	streaming, _ := body["stream"].(bool)
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		e.streamAnswer(w, flusher)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if e.anthropic {
		fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
		return
	}
	fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
}

// streamAnswer streams one short answer in the protocol's own form.
func (e *enforcingEndpoint) streamAnswer(w http.ResponseWriter, flusher http.Flusher) {
	event := func(payload string) {
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
	if e.anthropic {
		event(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		event(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)
	} else {
		event(`{"choices":[{"delta":{"content":"ok"},"index":0}]}`)
		event(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	}
	event(`[DONE]`)
}

// received returns the requests the endpoint saw.
func (e *enforcingEndpoint) received() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]map[string]any(nil), e.requests...)
}

// turnedAway returns the requests the endpoint refused.
func (e *enforcingEndpoint) turnedAway() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]map[string]any(nil), e.refused...)
}

// requestTokens counts the tokens of a request with the endpoint's own
// rule.
//
// It understands both wire forms: the OpenAI one, with the messages and
// the function definitions, and the Anthropic one, with the system
// block, the content blocks and the input schemas.
func requestTokens(body map[string]any) int {
	total := 0
	if system, _ := body["system"].(string); system != "" {
		total += countText(system) + 4
	}
	messages, _ := body["messages"].([]any)
	for _, item := range messages {
		entry, _ := item.(map[string]any)
		// The role and the framing around the message.
		total += 4
		switch content := entry["content"].(type) {
		case string:
			total += countText(content)
		case []any:
			for _, block := range content {
				part, _ := block.(map[string]any)
				text, _ := part["text"].(string)
				total += countText(text)
			}
		}
		if calls, ok := entry["tool_calls"].([]any); ok {
			for _, item := range calls {
				call, _ := item.(map[string]any)
				function, _ := call["function"].(map[string]any)
				arguments, _ := function["arguments"].(string)
				total += countText(arguments)
			}
		}
	}
	tools, _ := body["tools"].([]any)
	for _, item := range tools {
		entry, _ := item.(map[string]any)
		function, _ := entry["function"].(map[string]any)
		name, _ := function["name"].(string)
		description, _ := function["description"].(string)
		schema, _ := function["parameters"]
		if name == "" {
			name, _ = entry["name"].(string)
			description, _ = entry["description"].(string)
			schema, _ = entry["input_schema"]
		}
		encoded, _ := json.Marshal(schema)
		total += countText(name) + countText(description) + countText(string(encoded)) + 10
	}
	return total
}

// countText counts the tokens of one text with the endpoint's rule.
//
// Words and punctuation are counted one by one, and text that has no
// word boundaries — dense code, a long run of one character — is
// counted at three characters per token.
func countText(text string) int {
	if text == "" {
		return 0
	}
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	byCharacters := len([]rune(text)) / 3
	if byCharacters > len(words) {
		return byCharacters
	}
	return len(words)
}

// wireCap reads the output cap the endpoint received.
func wireCap(t *testing.T, body map[string]any) int {
	t.Helper()
	switch cap := body["max_tokens"].(type) {
	case float64:
		return int(cap)
	case int:
		return cap
	default:
		t.Fatalf("the request carries no output cap: %+v", body)
		return 0
	}
}

// wireApp builds an application that talks to one endpoint with HAL's
// own system instruction.
//
// The workspace is real, so the guarded write proposal is offered, and
// the permission controller is real, so the request carries the same
// instruction the operator's interface would send.
//
// tools decides whether MPM's typed knowledge tools are offered as
// well. A lean application carries the instruction and the write
// proposal only, which is what the cases with a small budget need: the
// cap on the wire can only be observed when the request fits.
func wireApp(t *testing.T, endpoint *enforcingEndpoint, model config.ModelRecord, anthropic, tools bool) *app {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))

	ws, err := workspace.Open(filepath.Join(dir, "HAL"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.Open(filepath.Join(dir, "sessions"), "test")
	if err != nil {
		t.Fatal(err)
	}
	gate := permission.New()
	protocol := config.ProtocolOpenAI
	if anthropic {
		protocol = config.ProtocolAnthropic
	}
	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{{
		ID: "alpha", Name: "Alpha", Protocol: protocol,
		BaseURL: endpoint.URL + "/v1",
		Models:  []config.ModelRecord{model},
	}}
	stored.SetActive("alpha", model.ID)
	a := &app{
		cfg: config.Config{
			Provider:     string(protocol),
			StoreDir:     dir,
			WorkspaceDir: ws.Root(),
		},
		stored:     stored,
		store:      store,
		session:    "test",
		workspace:  ws,
		permission: gate,
		tools:      agenttools.New(gate, nil),
		skills:     skills.NewSelector(&skills.Client{}),
	}
	if tools {
		a.knowledge = knowledge.NewRegistry(&knowledge.Client{})
	}
	a.rebuildEngine()
	return a
}

// The wire cap is the reservation.
//
// A known 8192-token window with the default output allowance resolves
// to 4096 reserved, and 4096 is what the provider is asked for: the
// window is a total and the answer's room comes out of it.
func TestAKnownWindowWithTheDefaultOutputReachesTheWire(t *testing.T) {
	endpoint := newEnforcingEndpoint(t, 8192, 0, false)
	a := wireApp(t, endpoint, config.ModelRecord{
		ID: "shared", ContextMode: config.ContextManual, ContextWindow: 8192,
	}, false, false)

	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Budget.ReservedOutput != 4096 {
		t.Fatalf("the reservation = %d, want 4096", target.Budget.ReservedOutput)
	}
	if target.Budget.Limit != 4096 {
		t.Fatalf("the limit = %d, want 4096", target.Budget.Limit)
	}
	if _, err := a.eng.RespondStream(context.Background(), "hello there", nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	accepted := endpoint.received()
	if len(accepted) != 1 {
		t.Fatalf("the endpoint accepted %d requests", len(accepted))
	}
	if got := wireCap(t, accepted[0]); got != 4096 {
		t.Fatalf("the wire cap = %d, want the reservation 4096", got)
	}
}

// A configured output cap that fits is the cap on the wire.
func TestAKnownWindowWithAConfiguredOutputReachesTheWire(t *testing.T) {
	endpoint := newEnforcingEndpoint(t, 8192, 0, false)
	a := wireApp(t, endpoint, config.ModelRecord{
		ID: "shared", ContextMode: config.ContextManual,
		ContextWindow: 8192, MaxOutputTokens: 4096,
	}, false, false)

	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Budget.ReservedOutput != 4096 {
		t.Fatalf("the reservation = %d, want 4096", target.Budget.ReservedOutput)
	}
	if _, err := a.eng.RespondStream(context.Background(), "hello there", nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	accepted := endpoint.received()
	if len(accepted) != 1 {
		t.Fatalf("the endpoint accepted %d requests", len(accepted))
	}
	if got := wireCap(t, accepted[0]); got != 4096 {
		t.Fatalf("the wire cap = %d, want 4096", got)
	}
}

// A configured output cap that does not fit inside the window is a
// contradiction, and it is refused before the request is transmitted.
func TestAnOutputCapThatDoesNotFitIsRefusedBeforeTransmission(t *testing.T) {
	endpoint := newEnforcingEndpoint(t, 8192, 0, false)
	a := wireApp(t, endpoint, config.ModelRecord{
		ID: "shared", ContextMode: config.ContextManual,
		ContextWindow: 8192, MaxOutputTokens: 9000,
	}, false, false)

	_, err := a.resolveTarget(a.stored)
	if err == nil {
		t.Fatal("a contradiction was accepted")
	}
	if !strings.Contains(err.Error(), "does not fit inside the window") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
	if len(endpoint.received()) != 0 || len(endpoint.turnedAway()) != 0 {
		t.Errorf("the endpoint saw %d requests", len(endpoint.received()))
	}
}

// The provisional policy is operational.
//
// A model with no context metadata gets the provisional total of 8192
// and the provisional output cap of 1024, and both reach the wire.
func TestTheProvisionalPolicyReachesTheWire(t *testing.T) {
	endpoint := newEnforcingEndpoint(t, 8192, 0, false)
	a := wireApp(t, endpoint, config.ModelRecord{
		ID: "shared", ContextMode: config.ContextAuto,
	}, false, true)

	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Budget.Source != provider.ContextSourceProvisional {
		t.Fatalf("the source = %q, want the provisional one", target.Budget.Source)
	}
	if target.Budget.Verified {
		t.Error("a provisional budget is never verified")
	}
	if target.Budget.Total != provider.ProvisionalContextWindow {
		t.Fatalf("the total = %d, want %d", target.Budget.Total, provider.ProvisionalContextWindow)
	}
	if target.Budget.ReservedOutput != provider.ProvisionalMaxOutputTokens {
		t.Fatalf("the reservation = %d, want %d",
			target.Budget.ReservedOutput, provider.ProvisionalMaxOutputTokens)
	}
	if target.Budget.Limit != provider.ProvisionalContextWindow-provider.ProvisionalMaxOutputTokens {
		t.Fatalf("the limit = %d, want %d", target.Budget.Limit,
			provider.ProvisionalContextWindow-provider.ProvisionalMaxOutputTokens)
	}
	if _, err := a.eng.RespondStream(context.Background(), "hello there", nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	accepted := endpoint.received()
	if len(accepted) != 1 {
		t.Fatalf("the endpoint accepted %d requests", len(accepted))
	}
	if got := wireCap(t, accepted[0]); got != provider.ProvisionalMaxOutputTokens {
		t.Fatalf("the wire cap = %d, want %d", got, provider.ProvisionalMaxOutputTokens)
	}
}

// The completion criterion: an unknown 8K model conducts an ordinary
// conversation, with HAL's own instructions and its own tool
// definitions, and the endpoint accepts every request.
//
// The endpoint counts the tokens with its own rule, so this is not
// HAL's estimate confirming HAL's estimate.
func TestAnUnknownEightKModelHoldsAnOrdinaryConversation(t *testing.T) {
	endpoint := newEnforcingEndpoint(t, 8192, 0, false)
	a := wireApp(t, endpoint, config.ModelRecord{
		ID: "shared", ContextMode: config.ContextAuto,
	}, false, true)

	for turn := 0; turn < 3; turn++ {
		if _, err := a.eng.RespondStream(context.Background(),
			fmt.Sprintf("question %d: what is the state of the build?", turn), nil); err != nil {
			t.Fatalf("turn %d failed: %v", turn, err)
		}
	}
	accepted := endpoint.received()
	if len(accepted) != 3 {
		t.Fatalf("the endpoint accepted %d requests", len(accepted))
	}
	if len(endpoint.turnedAway()) != 0 {
		t.Fatalf("the endpoint turned %d requests away", len(endpoint.turnedAway()))
	}
	// The request carries HAL's own instruction and its own tool
	// definitions: this is an ordinary request, not an empty one.
	body := accepted[0]
	messages, _ := body["messages"].([]any)
	if len(messages) == 0 {
		t.Fatal("the request carries no message")
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("the request carries no system instruction: %+v", first)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) == 0 {
		t.Error("the request offers no tool definition")
	}
	// The endpoint's own count leaves room for the answer.
	for i, request := range accepted {
		if count := requestTokens(request); count+provider.ProvisionalMaxOutputTokens > 8192 {
			t.Errorf("request %d counts %d tokens, over the window", i, count)
		}
	}
}

// A longer conversation on the same endpoint is trimmed: whole exchanges
// go, the request stays whole, and the endpoint accepts it.
func TestALongerConversationIsTrimmedAndStillFits(t *testing.T) {
	endpoint := newEnforcingEndpoint(t, 8192, 0, false)
	a := wireApp(t, endpoint, config.ModelRecord{
		ID: "shared", ContextMode: config.ContextAuto,
	}, false, true)

	// A conversation that cannot fit inside the provisional window.
	for i := 0; i < 12; i++ {
		if _, err := a.store.AppendTurn(session.RoleUser,
			strings.Repeat("question ", 60), session.Turn{}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.store.AppendTurn(session.RoleAssistant,
			strings.Repeat("answer ", 60), session.Turn{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.eng.RespondStream(context.Background(), "and now?", nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	accepted := endpoint.received()
	if len(accepted) != 1 {
		t.Fatalf("the endpoint accepted %d requests", len(accepted))
	}
	if len(endpoint.turnedAway()) != 0 {
		t.Fatalf("the endpoint turned %d requests away", len(endpoint.turnedAway()))
	}
	messages, _ := accepted[0]["messages"].([]any)
	if len(messages) >= 26 {
		t.Errorf("the whole conversation was transmitted: %d messages", len(messages))
	}
	// The current request is the last message, and it is there.
	last, _ := messages[len(messages)-1].(map[string]any)
	content, _ := last["content"].(string)
	if !strings.Contains(content, "and now?") {
		t.Errorf("the current request was dropped: %+v", last)
	}
	// The instruction is never dropped.
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("the instruction was dropped: %+v", first)
	}
}

// A tool exchange is never split: the call and its result go together.
//
// The rule is the provider's own, and it is exercised where the budget
// is enforced: see the trim tests beside it.
func TestATrimmedRequestKeepsTheToolExchangeWhole(t *testing.T) {
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
}

// An endpoint that advertises a window is trusted over the provisional
// default: the provisional restrictions do not apply.
func TestAnAdvertisedWindowReplacesTheProvisionalRestrictions(t *testing.T) {
	endpoint := newEnforcingEndpoint(t, 32768, 32768, false)
	a := wireApp(t, endpoint, config.ModelRecord{
		ID: "shared", ContextMode: config.ContextAuto,
	}, false, true)

	// The endpoint reports its window.
	a.refreshContext(context.Background(), contextKey{endpoint: "alpha", model: "shared"},
		a.contextGeneration("alpha"), endpoint.URL+"/v1", "k", provider.WireOpenAI)

	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Budget.Source != provider.ContextSourceProvider {
		t.Fatalf("the source = %q, want the provider's", target.Budget.Source)
	}
	if target.Budget.Total != 32768 {
		t.Fatalf("the total = %d, want 32768", target.Budget.Total)
	}
	if target.Budget.ReservedOutput != provider.DefaultMaxTokens {
		t.Fatalf("the reservation = %d, want the protocol default %d",
			target.Budget.ReservedOutput, provider.DefaultMaxTokens)
	}
	if _, err := a.eng.RespondStream(context.Background(), "hello there", nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	accepted := endpoint.received()
	if len(accepted) != 1 {
		t.Fatalf("the endpoint accepted %d requests", len(accepted))
	}
	if got := wireCap(t, accepted[0]); got != provider.DefaultMaxTokens {
		t.Fatalf("the wire cap = %d, want the protocol default %d", got, provider.DefaultMaxTokens)
	}
}

// The operator's own window stays authoritative over everything the
// endpoint says.
func TestTheOperatorWindowStaysAuthoritative(t *testing.T) {
	endpoint := newEnforcingEndpoint(t, 32768, 32768, false)
	a := wireApp(t, endpoint, config.ModelRecord{
		ID: "shared", ContextMode: config.ContextManual,
		ContextWindow: 8192, MaxOutputTokens: 1024,
	}, false, true)

	a.refreshContext(context.Background(), contextKey{endpoint: "alpha", model: "shared"},
		a.contextGeneration("alpha"), endpoint.URL+"/v1", "k", provider.WireOpenAI)

	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Budget.Source != provider.ContextSourceOperator {
		t.Fatalf("the source = %q, want the operator's", target.Budget.Source)
	}
	if target.Budget.Total != 8192 {
		t.Fatalf("the total = %d, want the operator's 8192", target.Budget.Total)
	}
	if target.Budget.ReservedOutput != 1024 {
		t.Fatalf("the reservation = %d, want 1024", target.Budget.ReservedOutput)
	}
	if _, err := a.eng.RespondStream(context.Background(), "hello there", nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	accepted := endpoint.received()
	if len(accepted) != 1 {
		t.Fatalf("the endpoint accepted %d requests", len(accepted))
	}
	if got := wireCap(t, accepted[0]); got != 1024 {
		t.Fatalf("the wire cap = %d, want 1024", got)
	}
}

// The Anthropic Messages request obeys the same rule.
func TestTheAnthropicRequestObeysTheSameOutputBudget(t *testing.T) {
	endpoint := newEnforcingEndpoint(t, 8192, 0, true)
	a := wireApp(t, endpoint, config.ModelRecord{
		ID: "shared", ContextMode: config.ContextManual, ContextWindow: 8192,
	}, true, false)

	target, err := a.resolveTarget(a.stored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Budget.ReservedOutput != 4096 {
		t.Fatalf("the reservation = %d, want 4096", target.Budget.ReservedOutput)
	}
	if _, err := a.eng.RespondStream(context.Background(), "hello there", nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	accepted := endpoint.received()
	if len(accepted) != 1 {
		t.Fatalf("the endpoint accepted %d requests", len(accepted))
	}
	if got := wireCap(t, accepted[0]); got != 4096 {
		t.Fatalf("the wire cap = %d, want the reservation 4096", got)
	}
}

// The Anthropic request of a model with no metadata obeys the
// provisional policy as well.
func TestTheAnthropicProvisionalRequestObeysTheSameOutputBudget(t *testing.T) {
	endpoint := newEnforcingEndpoint(t, 8192, 0, true)
	a := wireApp(t, endpoint, config.ModelRecord{
		ID: "shared", ContextMode: config.ContextAuto,
	}, true, true)

	if _, err := a.eng.RespondStream(context.Background(), "hello there", nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	accepted := endpoint.received()
	if len(accepted) != 1 {
		t.Fatalf("the endpoint accepted %d requests", len(accepted))
	}
	if got := wireCap(t, accepted[0]); got != provider.ProvisionalMaxOutputTokens {
		t.Fatalf("the wire cap = %d, want %d", got, provider.ProvisionalMaxOutputTokens)
	}
}

// A request that cannot be reduced fails locally, with an explanation,
// and nothing is transmitted.
func TestAnIrreducibleRequestFailsLocally(t *testing.T) {
	endpoint := newEnforcingEndpoint(t, 8192, 0, false)
	a := wireApp(t, endpoint, config.ModelRecord{
		ID: "shared", ContextMode: config.ContextManual, ContextWindow: 1024,
	}, false, false)

	_, err := a.eng.RespondStream(context.Background(), "and now?", nil)
	if err == nil {
		t.Fatal("a request that cannot fit was sent anyway")
	}
	if !strings.Contains(err.Error(), "context budget") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
	if !strings.Contains(err.Error(), "estimated") {
		t.Errorf("the refusal does not say the number is an estimate: %v", err)
	}
	if len(endpoint.received()) != 0 || len(endpoint.turnedAway()) != 0 {
		t.Errorf("the endpoint saw %d requests", len(endpoint.received()))
	}
	// The operator's message is still in the conversation.
	history, err := a.store.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 || history[len(history)-1].Role != session.RoleUser {
		t.Errorf("the operator's message was dropped: %+v", history)
	}
}
