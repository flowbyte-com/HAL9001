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
	"time"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/lifecycle"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/skills"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// The lifecycle, end to end.
//
// The application wires the engine and the interface together. These
// tests hold that wiring to its promise: the engine owns the state of
// the exchange, a stopped exchange can be continued, and the
// conversation that is left behind is a conversation, not a wreck.

// holdingProvider answers every request, and holds the first one when
// it is asked to.
//
// A held request waits for the client to go away: that is what an
// operator's /stop looks like from the provider's side.
type holdingProvider struct {
	mu       sync.Mutex
	requests int
	// holdFirst makes the first request wait for the client to go
	// away instead of being answered.
	holdFirst bool
	// answer is the text of the answer.
	answer string
	// arrived is closed when the first request arrives.
	arrived chan struct{}
	once    sync.Once
}

func (p *holdingProvider) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)
	p.mu.Lock()
	p.requests++
	n := p.requests
	p.mu.Unlock()
	if n == 1 {
		p.once.Do(func() { close(p.arrived) })
		if p.holdFirst {
			// The operator stops the turn: the request is aborted.
			<-r.Context().Done()
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"`+p.answer+`"},"index":0}]}`)
	if flusher != nil {
		flusher.Flush()
	}
	fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	if flusher != nil {
		flusher.Flush()
	}
}

// lifecycleApp builds an application that talks to the holding
// provider.
func lifecycleApp(t *testing.T, provider *holdingProvider) *app {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	server := httptest.NewServer(http.HandlerFunc(provider.serve))
	t.Cleanup(server.Close)

	ws, err := workspace.Open(filepath.Join(dir, "HAL"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.Open(filepath.Join(dir, "sessions"), "test")
	if err != nil {
		t.Fatal(err)
	}
	gate := permission.New()
	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{{
		ID: "alpha", Name: "Alpha", Protocol: config.ProtocolOpenAI,
		BaseURL: server.URL + "/v1",
		Models:  []config.ModelRecord{{ID: "shared"}},
	}}
	stored.SetActive("alpha", "shared")
	a := &app{
		cfg: config.Config{
			Provider:     string(config.ProtocolOpenAI),
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
	a.rebuildEngine()
	return a
}

// The engine of the application owns the lifecycle, and the interface
// can ask it about the exchange.
func TestTheApplicationEngineOwnsTheLifecycle(t *testing.T) {
	a := lifecycleApp(t, &holdingProvider{answer: "the answer", arrived: make(chan struct{})})
	var lifecycleAware interface {
		Lifecycle() lifecycle.Operation
	}
	lifecycleAware = a.eng
	if lifecycleAware == nil {
		t.Fatal("the application has no engine")
	}
	op := lifecycleAware.Lifecycle()
	if op.State != "" && op.State != lifecycle.StateIdle {
		t.Fatalf("state = %q, want nothing to have run", op.State)
	}
}

// A stopped exchange can be continued, and the conversation that is left
// behind holds the operator's message exactly once.
func TestTheApplicationContinuesAStoppedExchange(t *testing.T) {
	provider := &holdingProvider{answer: "the continued answer", arrived: make(chan struct{}), holdFirst: true}
	a := lifecycleApp(t, provider)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.eng.RespondStreamEvents(ctx, "what is the state of the build?", nil)
		done <- err
	}()
	select {
	case <-provider.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the provider never saw the request")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("the stopped turn reported no error")
	}
	op := a.eng.Lifecycle()
	if op.State != lifecycle.StateStopped {
		t.Fatalf("state = %q, want stopped", op.State)
	}
	if !op.Resumable {
		t.Fatal("the stopped exchange must be continuable")
	}
	reply, err := a.eng.Resume(context.Background(), nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if reply != "the continued answer" {
		t.Errorf("reply = %q", reply)
	}
	history, err := a.store.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history = %d messages, want the question and the answer", len(history))
	}
	if history[0].Role != session.RoleUser ||
		history[0].Content != "what is the state of the build?" {
		t.Errorf("the operator's message = %#v", history[0])
	}
	if history[1].Role != session.RoleAssistant {
		t.Errorf("the answer = %#v", history[1])
	}
	if history[0].TurnID != history[1].TurnID {
		t.Error("the answer belongs to another exchange")
	}
}

// A finished exchange is never continued: the provider is asked once.
func TestTheApplicationNeverReplaysAFinishedExchange(t *testing.T) {
	provider := &holdingProvider{answer: "the answer", arrived: make(chan struct{})}
	a := lifecycleApp(t, provider)
	if _, err := a.eng.RespondStreamEvents(context.Background(), "hello there", nil); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	requests := provider.requests
	provider.mu.Unlock()
	if requests != 1 {
		t.Fatalf("the provider saw %d requests, want one", requests)
	}
	if _, err := a.eng.Resume(context.Background(), nil); err == nil {
		t.Fatal("a finished exchange must not be continued")
	}
	provider.mu.Lock()
	requests = provider.requests
	provider.mu.Unlock()
	if requests != 1 {
		t.Fatalf("the provider saw %d requests, want one", requests)
	}
}

// The engine the interface holds is the one that owns the lifecycle,
// so the interface can ask it.
func TestTheInterfaceCanAskTheEngineAboutTheExchange(t *testing.T) {
	provider := &holdingProvider{answer: "the answer", arrived: make(chan struct{}), holdFirst: true}
	a := lifecycleApp(t, provider)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.eng.RespondStreamEvents(ctx, "hold the line", nil)
		done <- err
	}()
	select {
	case <-provider.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the provider never saw the request")
	}
	cancel()
	<-done
	// The engine the interface would hold answers the question.
	var asked interface {
		Lifecycle() lifecycle.Operation
	} = a.eng
	op := asked.Lifecycle()
	if op.State != lifecycle.StateStopped {
		t.Fatalf("state = %q, want stopped", op.State)
	}
	if !strings.Contains(op.Status(), "stopped") {
		t.Errorf("status = %q", op.Status())
	}
	_ = engine.ErrNoProvider
}
