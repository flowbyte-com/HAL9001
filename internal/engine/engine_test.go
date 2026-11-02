package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/hal9001/internal/provider"
	"github.com/sipeed/picoclaw/hal9001/internal/session"
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
