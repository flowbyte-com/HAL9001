package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

func testEngine(t *testing.T, replies []provider.Response) *Engine {
	t.Helper()
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	return New(st, &provider.MockProvider{Replies: replies}, provider.Model{ID: "mock"})
}

func TestRespondStoresBothSides(t *testing.T) {
	eng := testEngine(t, []provider.Response{{Content: "hello there"}})
	reply, err := eng.Respond(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "hello there" {
		t.Errorf("reply = %q", reply)
	}
}

func TestRespondRejectsEmpty(t *testing.T) {
	eng := testEngine(t, nil)
	if _, err := eng.Respond(context.Background(), "  "); err == nil {
		t.Error("empty input must be rejected")
	}
}

// The engine never implements: a provider-requested shell call is refused
// with the 444 refusal, recorded in the conversation.
func TestToolCallDenied(t *testing.T) {
	eng := testEngine(t, []provider.Response{{
		Content:   "on it",
		ToolCalls: []provider.ToolCall{{ID: "1", Name: "exec", Arguments: map[string]any{"cmd": "id"}}},
	}})
	reply, err := eng.Respond(context.Background(), "run something")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "444") {
		t.Errorf("denial must cite the boundary, got %q", reply)
	}
}

func TestAllowedButUnimplementedIsHonest(t *testing.T) {
	eng := testEngine(t, []provider.Response{{
		ToolCalls: []provider.ToolCall{{ID: "1", Name: "web_search"}},
	}})
	reply, err := eng.Respond(context.Background(), "look it up")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "not yet implemented") {
		t.Errorf("allowed-but-missing integrations must say so, got %q", reply)
	}
}

func TestNilProviderStoresWithoutFabricating(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	eng := New(st, nil, provider.Model{ID: "none"})
	reply, err := eng.Respond(context.Background(), "remember this")
	if err == nil {
		t.Fatalf("expected ErrNoProvider, got reply %q", reply)
	}
	if reply != "" {
		t.Errorf("no assistant text may be fabricated, got %q", reply)
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Role != session.RoleUser {
		t.Fatalf("user message must still be stored, got %+v", history)
	}
}

func TestSystemPromptPrependedNotStored(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	var got []provider.Message
	spy := &spyProvider{fn: func(messages []provider.Message) {
		got = append([]provider.Message(nil), messages...)
	}}
	eng := New(st, spy, provider.Model{ID: "m"}).WithSystemPrompt("be helpful")
	if _, err := eng.Respond(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Role != "system" || got[0].Content != "be helpful" {
		t.Fatalf("system prompt must lead the request, got %+v", got)
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range history {
		if msg.Role == session.RoleSystem {
			t.Fatal("system prompt must never be stored in the session")
		}
	}
}

type spyProvider struct {
	fn func([]provider.Message)
}

func (s *spyProvider) Name() string { return "spy" }
func (s *spyProvider) Chat(_ context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	s.fn(messages)
	return provider.Response{Content: "ok"}, nil
}

func TestStreamingEmitsDeltas(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	streamer := &scriptStreamer{chunks: []string{"Hel", "lo"}}
	eng := New(st, streamer, provider.Model{ID: "m"})
	var emitted []string
	reply, err := eng.RespondStream(context.Background(), "hi", func(text string) { emitted = append(emitted, text) })
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Hello" {
		t.Errorf("reply = %q", reply)
	}
	if got := strings.Join(emitted, ""); got != "Hello" {
		t.Errorf("emitted = %v", emitted)
	}
}

type scriptStreamer struct{ chunks []string }

func (s *scriptStreamer) Name() string { return "script-stream" }
func (s *scriptStreamer) Chat(ctx context.Context, m []provider.Message, model provider.Model) (provider.Response, error) {
	return provider.Response{Content: strings.Join(s.chunks, "")}, nil
}
func (s *scriptStreamer) ChatStream(_ context.Context, _ []provider.Message, _ provider.Model, emit func(provider.Delta) error) (provider.Response, error) {
	var content string
	for _, chunk := range s.chunks {
		content += chunk
		if err := emit(provider.Delta{Text: chunk}); err != nil {
			return provider.Response{}, err
		}
	}
	return provider.Response{Content: content}, nil
}

func TestCancelledStreamStoresNothingFromAssistant(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	eng := New(st, &hangingProvider{}, provider.Model{ID: "m"})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := eng.RespondStream(ctx, "hi", func(string) {}); err == nil {
		t.Fatal("expected cancellation to fail the turn")
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Role != session.RoleUser {
		t.Fatalf("only the user message may be stored, got %+v", history)
	}
}

type hangingProvider struct{}

func (h *hangingProvider) Name() string { return "hang" }
func (h *hangingProvider) Chat(ctx context.Context, _ []provider.Message, _ provider.Model) (provider.Response, error) {
	<-ctx.Done()
	return provider.Response{}, ctx.Err()
}

func TestHistoryAccumulatesAcrossTurns(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	var got []provider.Message
	spy := &spyProvider{fn: func(messages []provider.Message) {
		got = append([]provider.Message(nil), messages...)
	}}
	eng := New(st, spy, provider.Model{ID: "m"})
	if _, err := eng.Respond(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Respond(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	// Second request carries both turns: user, assistant, user.
	if len(got) != 3 {
		t.Fatalf("second request should carry 3 messages, got %+v", got)
	}
	if got[0].Content != "first" || got[1].Content != "ok" || got[2].Content != "second" {
		t.Errorf("history wrong: %+v", got)
	}
}
