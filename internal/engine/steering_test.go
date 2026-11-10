package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// Steering.
//
// A steering message is a change of direction. The running turn takes
// it up at the next boundary where it has not sent a request yet, so
// the operator's direction applies to the work that has not been sent
// and the request that was already sent stands as it was.
//
// These tests use a provider that reads a file and then answers, so the
// turn really has two requests, and a source that only releases the
// steering message once the first request has gone out.

// steerProbe holds one steering message and releases it only after the
// first request has been sent.
type steerProbe struct {
	mu      sync.Mutex
	message string
	sent    bool
	taken   bool
}

// Steer implements SteerSource.
func (p *steerProbe) Steer() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.sent || p.taken {
		return "", false
	}
	p.taken = true
	return p.message, true
}

// markSent records that a request has gone out.
func (p *steerProbe) markSent() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = true
}

// wasTaken reports whether the message was handed to the turn.
func (p *steerProbe) wasTaken() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.taken
}

// steerProvider answers with a tool call and then with text, and
// records every request it was handed.
type steerProvider struct {
	probe     *steerProbe
	responses []provider.Response
	requests  [][]provider.Message
}

func (p *steerProvider) Name() string { return "steer-test" }

func (p *steerProvider) Chat(_ context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	p.requests = append(p.requests, append([]provider.Message(nil), messages...))
	p.probe.markSent()
	if len(p.responses) == 0 {
		return provider.Response{Content: "script exhausted"}, nil
	}
	resp := p.responses[0]
	p.responses = p.responses[1:]
	return resp, nil
}

func (p *steerProvider) ChatStream(_ context.Context, messages []provider.Message, model provider.Model, _ func(provider.Delta) error) (provider.Response, error) {
	return p.Chat(context.Background(), messages, model)
}

func (p *steerProvider) ChatStreamTools(_ context.Context, messages []provider.Message, model provider.Model, _ []provider.Tool, _ func(provider.Delta) error) (provider.Response, error) {
	return p.Chat(context.Background(), messages, model)
}

// steerEngine builds an engine in 666 with a working read tool, so a
// turn really has a second request.
func steerEngine(t *testing.T, p provider.Provider, source SteerSource) (*Engine, *session.Store) {
	t.Helper()
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	perm := permission.New()
	perm.SetSession("chat")
	if err := perm.Enable("chat"); err != nil {
		t.Fatal(err)
	}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithSystemPrompt("You are HAL9001.").
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	if source != nil {
		eng.WithSteerSource(source)
	}
	return eng, st
}

// readFile writes a file the engine's read tool can open.
func readFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(path, []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// requestHas reports whether a request carries the text.
func requestHas(messages []provider.Message, text string) bool {
	for _, m := range messages {
		if strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}

func TestASteeringMessageJoinsTheNextRequestAndNotTheOneThatWasSent(t *testing.T) {
	path := readFile(t)
	probe := &steerProbe{message: "focus on the error instead"}
	p := &steerProvider{
		probe: probe,
		responses: []provider.Response{
			{Content: "I'll look.", ToolCalls: []provider.ToolCall{{
				ID: "call_read", Name: "read",
				Arguments: map[string]any{"path": path},
			}}},
			{Content: "Done."},
		},
	}
	eng, st := steerEngine(t, p, probe)

	var steers []string
	var stored []bool
	if _, err := eng.RespondStreamEvents(context.Background(), "read it", func(ev Event) {
		if ev.Steer != "" {
			steers = append(steers, ev.Steer)
			stored = append(stored, ev.SteerStored)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 2 {
		t.Fatalf("the turn made %d requests", len(p.requests))
	}
	// The request that was already sent is not rewritten.
	if requestHas(p.requests[0], probe.message) {
		t.Fatalf("the first request was rewritten: %#v", p.requests[0])
	}
	// The steer is in the request the turn built next.
	if !requestHas(p.requests[1], probe.message) {
		t.Fatalf("the steer did not reach the second request: %#v", p.requests[1])
	}
	// The operator is told, and the message is in the record.
	if len(steers) != 1 || steers[0] != probe.message {
		t.Fatalf("the interface was told %v", steers)
	}
	if len(stored) != 1 || !stored[0] {
		t.Fatalf("the steer was not stored: %v", stored)
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range history {
		if m.Role == session.RoleUser && strings.Contains(m.Content, probe.message) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the steer is not in the session: %#v", history)
	}
}

func TestASteeringMessageIsNotTakenWhenTheTurnHasNoRequestLeft(t *testing.T) {
	// The turn sends one request and answers. The steer arrives after
	// that request went out, so the turn never sees it: the caller
	// keeps it and decides what to do with it.
	probe := &steerProbe{message: "focus on the error instead"}
	p := &steerProvider{
		probe:     probe,
		responses: []provider.Response{{Content: "Done."}},
	}
	eng, st := steerEngine(t, p, probe)

	if _, err := eng.RespondStreamEvents(context.Background(), "just answer", nil); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("the turn made %d requests", len(p.requests))
	}
	if probe.wasTaken() {
		t.Fatal("the turn took a message it could not use")
	}
	// The message was not written to the session either: it never
	// reached the model.
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range history {
		if strings.Contains(m.Content, probe.message) {
			t.Fatalf("a message the model never saw is in the session: %#v", m)
		}
	}
}

func TestATurnWithoutASourceIsNeverSteered(t *testing.T) {
	path := readFile(t)
	p := &steerProvider{
		probe: &steerProbe{},
		responses: []provider.Response{
			{Content: "I'll look.", ToolCalls: []provider.ToolCall{{
				ID: "call_read", Name: "read",
				Arguments: map[string]any{"path": path},
			}}},
			{Content: "Done."},
		},
	}
	eng, _ := steerEngine(t, p, nil)
	if _, err := eng.RespondStreamEvents(context.Background(), "read it", nil); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 2 {
		t.Fatalf("the turn made %d requests", len(p.requests))
	}
}
