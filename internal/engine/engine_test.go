package engine

import (
	"context"
	"errors"
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

// progressStreamer sends reasoning, then content, then stats, imitating a
// MiniMax-style reasoning model.
type progressStreamer struct {
	reasoning     []string
	chunks        []string
	content       string
	reasoningText string
	stats         provider.TurnStats
}

func (s *progressStreamer) Name() string { return "progress" }
func (s *progressStreamer) Chat(context.Context, []provider.Message, provider.Model) (provider.Response, error) {
	return provider.Response{Content: s.content}, nil
}
func (s *progressStreamer) ChatStream(_ context.Context, _ []provider.Message, _ provider.Model, emit func(provider.Delta) error) (provider.Response, error) {
	for _, r := range s.reasoning {
		if err := emit(provider.Delta{Reasoning: r}); err != nil {
			return provider.Response{}, err
		}
		s.reasoningText += r
	}
	var content string
	for _, chunk := range s.chunks {
		content += chunk
		if err := emit(provider.Delta{Text: chunk}); err != nil {
			return provider.Response{}, err
		}
	}
	return provider.Response{Content: content, Reasoning: s.reasoningText, Stats: s.stats}, nil
}

func TestProgressEventsReportReasoningWithoutText(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	streamer := &progressStreamer{
		reasoning: []string{"private thought"},
		chunks:    []string{"Public ", "answer"},
		stats:     provider.TurnStats{Status: provider.StatusDone, Events: 3, ReasoningChunks: 1, VisibleChunks: 2},
	}
	eng := New(st, streamer, provider.Model{ID: "m"})
	var events []Event
	reply, err := eng.RespondStreamEvents(context.Background(), "hi", func(ev Event) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Public answer" {
		t.Errorf("reply = %q", reply)
	}
	var reasoningSignals, texts, stats int
	for _, ev := range events {
		if ev.Reasoning {
			reasoningSignals++
			if ev.Text != "" {
				t.Error("a reasoning event must not carry text")
			}
		}
		if ev.Text != "" {
			texts++
			if strings.Contains(ev.Text, "private thought") {
				t.Errorf("reasoning text leaked into the answer: %q", ev.Text)
			}
		}
		if ev.Stats != nil {
			stats++
		}
	}
	if reasoningSignals != 1 {
		t.Errorf("reasoning signals = %d, want 1", reasoningSignals)
	}
	if texts != 2 {
		t.Errorf("text events = %d, want 2", texts)
	}
	if stats != 1 {
		t.Errorf("stats events = %d, want 1", stats)
	}
	history, _ := st.History()
	if len(history) != 2 || history[1].Content != "Public answer" {
		t.Errorf("stored history = %+v", history)
	}
	for _, msg := range history {
		if strings.Contains(msg.Content, "private thought") {
			t.Error("reasoning text must never be stored")
		}
	}
}

// A reply that is entirely reasoning must not be reported as an empty
// answer: the operator is told what happened, without the reasoning text.
// A reply that is entirely reasoning is not an answer: the turn fails
// with a typed error, stores nothing from the assistant side, and never
// leaks the reasoning text.
func TestReasoningOnlyTurnIsReportedNotStored(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	streamer := &progressStreamer{
		reasoning: []string{"thinking"},
		content:   "",
		stats:     provider.TurnStats{Status: provider.StatusFinished, FinishReason: "length", OutputLimitHit: true},
	}
	eng := New(st, streamer, provider.Model{ID: "m"})
	reply, err := eng.RespondStream(context.Background(), "hi", func(string) {})
	if !errors.Is(err, ErrNoVisibleAnswer) {
		t.Fatalf("err = %v, want ErrNoVisibleAnswer", err)
	}
	if reply != "" {
		t.Errorf("reply = %q, want no fabricated answer", reply)
	}
	if strings.Contains(err.Error(), "thinking") {
		t.Errorf("reasoning text leaked: %q", err)
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Role != session.RoleUser {
		t.Fatalf("only the user message may be stored, got %+v", history)
	}
}

// The diagnostics for an empty answer are emitted before the error, so
// /diag can explain the finish reason and the token accounting.
func TestReasoningOnlyTurnEmitsStats(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	streamer := &progressStreamer{
		reasoning: []string{"thinking"},
		content:   "",
		stats: provider.TurnStats{
			Status: provider.StatusFinished, FinishReason: "length", OutputLimitHit: true,
			HasUsage: true, Usage: provider.Usage{PromptTokens: 10, CompletionTokens: 8192, TotalTokens: 8202},
		},
	}
	eng := New(st, streamer, provider.Model{ID: "m"})
	var stats *provider.TurnStats
	_, err = eng.RespondStreamEvents(context.Background(), "hi", func(ev Event) {
		if ev.Stats != nil {
			stats = ev.Stats
		}
	})
	if !errors.Is(err, ErrNoVisibleAnswer) {
		t.Fatalf("err = %v", err)
	}
	if stats == nil {
		t.Fatal("stats must be reported even when the answer is empty")
	}
	if !stats.OutputLimitHit || stats.FinishReason != "length" {
		t.Errorf("stats = %+v", stats)
	}
	if stats.Usage.CompletionTokens != 8192 {
		t.Errorf("usage = %+v", stats.Usage)
	}
}

// Long prompts reach the provider in full: collapsing is a display
// concern only.
func TestLongPromptReachesProviderUnchanged(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("museum thought experiment line\n", 60)
	var got []provider.Message
	spy := &spyProvider{fn: func(messages []provider.Message) {
		got = append([]provider.Message(nil), messages...)
	}}
	eng := New(st, spy, provider.Model{ID: "m"})
	if _, err := eng.RespondStream(context.Background(), long, nil); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range got {
		if m.Content == long {
			found = true
		}
	}
	if !found {
		t.Error("the full prompt must reach the provider")
	}
	history, _ := st.History()
	if len(history) != 2 || history[0].Content != long {
		t.Error("the full prompt must be stored in the session")
	}
}
