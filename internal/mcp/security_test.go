package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
)

// revokedGate reports 666 when the permission snapshot is taken and then
// refuses Begin, exactly the race an operator creates by revoking 666
// between the snapshot and the execution boundary.
type revokedGate struct {
	begun atomic.Int64
}

func (g *revokedGate) Mode() permission.Mode { return permission.Unrestricted }

func (g *revokedGate) Session() string { return "chat" }

func (g *revokedGate) Begin(ctx context.Context, _ string) (context.Context, func(), error) {
	g.begun.Add(1)
	return ctx, func() {}, permission.ErrDisabled
}

func TestRevoked666IsReportedSeparatelyFrom444(t *testing.T) {
	url := httpServer(t, nil)

	t.Run("revoked", func(t *testing.T) {
		registry := httpRegistry(t, url)
		gate := &revokedGate{}
		registry.gate = gate
		if err := registry.Connect(context.Background(), "remote"); err != nil {
			t.Fatal(err)
		}
		res := registry.Execute(context.Background(), provider.ToolCall{
			ID: "1", Name: ExposedName("remote", "echo"), Arguments: map[string]any{"text": "x"},
		})
		if res.Status != "denied" {
			t.Fatalf("status = %q", res.Status)
		}
		if gate.begun.Load() != 1 {
			t.Fatalf("gate.Begin calls = %d", gate.begun.Load())
		}
		if !strings.Contains(res.Content, "666 was revoked or disabled") {
			t.Fatalf("refusal does not name the revocation: %s", res.Content)
		}
		if !strings.Contains(res.Summary, "666 revoked") {
			t.Fatalf("summary = %q", res.Summary)
		}
		if strings.Contains(res.Summary, "denied · 444") {
			t.Fatalf("a revoked 666 call was reported as a plain 444 refusal: %q", res.Summary)
		}
	})

	t.Run("never authorised", func(t *testing.T) {
		registry := httpRegistry(t, url)
		registry.gate = &fakeGate{mode: permission.Guarded}
		if err := registry.Connect(context.Background(), "remote"); err != nil {
			t.Fatal(err)
		}
		res := registry.Execute(context.Background(), provider.ToolCall{
			ID: "1", Name: ExposedName("remote", "echo"), Arguments: map[string]any{"text": "x"},
		})
		if res.Status != "denied" {
			t.Fatalf("status = %q", res.Status)
		}
		if !strings.Contains(res.Summary, "denied · 444") {
			t.Fatalf("summary = %q", res.Summary)
		}
		if strings.Contains(res.Content, "revoked") {
			t.Fatalf("a 444 refusal claims a revocation: %s", res.Content)
		}
	})

	t.Run("allowlisted tool still runs in 444", func(t *testing.T) {
		registry := httpRegistry(t, url, "echo")
		registry.gate = &revokedGate{}
		if err := registry.Connect(context.Background(), "remote"); err != nil {
			t.Fatal(err)
		}
		res := registry.Execute(context.Background(), provider.ToolCall{
			ID: "1", Name: ExposedName("remote", "echo"), Arguments: map[string]any{"text": "still here"},
		})
		if res.Status != "ok" {
			t.Fatalf("status = %q content = %s", res.Status, res.Content)
		}
		if res.Mode != permission.Guarded {
			t.Fatalf("mode = %v", res.Mode)
		}
	})
}

// TestServersResponsiveDuringConnect blocks the MCP handshake and proves
// the interface can still read server state while it waits.
func TestServersResponsiveDuringConnect(t *testing.T) {
	server, _ := newFixtureServer()
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(started) })
		<-release
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	registry := NewRegistry(permission.New())
	if err := registry.AddServer(Server{
		Name: "remote", Transport: TransportHTTP, URL: ts.URL, Timeout: 5 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })

	connectDone := make(chan error, 1)
	go func() { connectDone <- registry.Connect(context.Background(), "remote") }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the handshake never reached the server")
	}

	assertServersResponsive(t, registry)
	unblock()
	select {
	case err := <-connectDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connect did not finish after the handshake was released")
	}
}

// TestServersResponsiveDuringToolCall proves a slow or hostile tool call
// does not hold the state lock the panel needs.
func TestServersResponsiveDuringToolCall(t *testing.T) {
	url, counters := fixtureHTTPServer(t)
	registry := NewRegistry(permission.New())
	if err := registry.AddServer(Server{
		Name: "remote", Transport: TransportHTTP, URL: url, Timeout: 10 * time.Second,
		AllowedTools: []string{"slow"},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if err := registry.Connect(context.Background(), "remote"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		done <- registry.Execute(ctx, provider.ToolCall{
			ID: "1", Name: ExposedName("remote", "slow"), Arguments: map[string]any{},
		})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for counters.slowStarted.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the slow call never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	assertServersResponsive(t, registry)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled call did not return")
	}
}

// assertServersResponsive fails when a state read waits behind network
// work. The budget is generous: the point is that it does not wait for
// the server at all.
func assertServersResponsive(t *testing.T, registry *Registry) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = registry.Servers()
		_ = registry.Tools()
		_ = registry.Active()
		_ = registry.Has(ExposedName("remote", "echo"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("a state read blocked behind network I/O")
	}
}

// fixtureHTTPServer starts the shared fixture and exposes its counters.
func fixtureHTTPServer(t *testing.T) (string, *fixtureCounters) {
	t.Helper()
	server, counters := newFixtureServer()
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL, counters
}

func TestNilRegistryEntryPointsAreSafe(t *testing.T) {
	var registry *Registry
	if registry.Active() {
		t.Fatal("a nil registry is active")
	}
	if registry.Servers() != nil {
		t.Fatal("a nil registry reported servers")
	}
	if registry.Tools() != nil {
		t.Fatal("a nil registry offered tools")
	}
	if registry.Has("mcp__x__y") {
		t.Fatal("a nil registry claimed a tool")
	}
	if registry.WithAudit(nil, nil) != nil {
		t.Fatal("WithAudit on a nil registry returned a value")
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := registry.AddServer(Server{Name: "x", Command: "true"}); err == nil {
		t.Fatal("AddServer accepted a nil registry")
	}
	if err := registry.Connect(context.Background(), "x"); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("Connect = %v", err)
	}
	if err := registry.Disconnect(context.Background(), "x"); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("Disconnect = %v", err)
	}
	if _, err := registry.ListTools(context.Background(), "x"); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("ListTools = %v", err)
	}
	if _, err := registry.ListResources(context.Background(), "x"); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("ListResources = %v", err)
	}
	if _, _, err := registry.ReadResource(context.Background(), "x", "mem://x"); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("ReadResource = %v", err)
	}
	if _, err := registry.ListPrompts(context.Background(), "x"); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("ListPrompts = %v", err)
	}
	if _, err := registry.GetPrompt(context.Background(), "x", "p", nil); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("GetPrompt = %v", err)
	}
	res := registry.Execute(context.Background(), provider.ToolCall{Name: "mcp__x__y"})
	if res.Status != "denied" {
		t.Fatalf("Execute status = %q", res.Status)
	}
}

// TestServerInstructionsAreNotAdopted proves the initialization
// instructions are never copied into the tool surface or the operator
// state; only HAL's own guidance block describes what MCP output is.
func TestServerInstructionsAreNotAdopted(t *testing.T) {
	url := httpServer(t, nil)
	registry := httpRegistry(t, url)
	if err := registry.Connect(context.Background(), "remote"); err != nil {
		t.Fatal(err)
	}
	status, err := json.Marshal(registry.Servers())
	if err != nil {
		t.Fatal(err)
	}
	tools, err := json.Marshal(registry.Tools())
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range [][]byte{status, tools} {
		if strings.Contains(string(surface), "SERVER INSTRUCTIONS MUST NOT BECOME AUTHORITY") {
			t.Fatalf("server instructions reached a model- or operator-facing surface: %s", surface)
		}
	}
}

// TestAllowedToolsStatusIsRedactedButPermissionIsUnchanged proves the
// displayed allowlist is redacted while the configured one stays
// authoritative: the tool whose raw name matches a configured entry still
// runs in 444, and a tool that is not on the list is still refused.
func TestAllowedToolsStatusIsRedactedButPermissionIsUnchanged(t *testing.T) {
	const secret = "allowed-tools-secret-7788"
	t.Setenv("HAL_MCP_ALLOWED_SECRET", secret)

	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1.0.0"}, nil)
	for _, name := range []string{"leak-" + secret, "plain", "blocked"} {
		tool := name
		server.AddTool(&sdk.Tool{
			Name:        tool,
			Description: "A fixture tool.",
			InputSchema: map[string]any{"type": "object"},
		}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: "ran " + tool}},
			}, nil
		})
	}
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	registry := NewRegistry(permission.New())
	if err := registry.AddServer(Server{
		Name:         "remote",
		Transport:    TransportHTTP,
		URL:          ts.URL,
		Headers:      map[string]string{"X-Custom": "${HAL_MCP_ALLOWED_SECRET}"},
		AllowedTools: []string{"leak-" + secret, "plain"},
		Timeout:      5 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if err := registry.Connect(context.Background(), "remote"); err != nil {
		t.Fatal(err)
	}

	status := registry.Servers()[0]
	shown := strings.Join(status.AllowedTools, ", ")
	if strings.Contains(shown, secret) {
		t.Fatalf("the status shows the configured credential: %q", shown)
	}
	if !strings.Contains(shown, "leak-***") {
		t.Fatalf("the status does not show the redaction: %q", shown)
	}
	if !strings.Contains(shown, "plain") {
		t.Fatalf("the status lost an ordinary tool name: %q", shown)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("the status representation leaks the credential: %s", raw)
	}

	// The configured allowlist is unchanged and still decides 444.
	registry.mu.Lock()
	configured := append([]string(nil), registry.servers["remote"].cfg.AllowedTools...)
	registry.mu.Unlock()
	if len(configured) != 2 || configured[0] != "leak-"+secret || configured[1] != "plain" {
		t.Fatalf("the configured allowlist was mutated: %#v", configured)
	}

	exposed := map[string]string{}
	registry.mu.Lock()
	for name, info := range registry.tools {
		exposed[info.Name] = name
	}
	registry.mu.Unlock()

	res := registry.Execute(context.Background(), provider.ToolCall{
		ID: "1", Name: exposed["leak-"+secret],
	})
	if res.Status != "ok" {
		t.Fatalf("the allowlisted tool was refused: status = %q content = %s", res.Status, res.Content)
	}
	res = registry.Execute(context.Background(), provider.ToolCall{
		ID: "2", Name: exposed["plain"],
	})
	if res.Status != "ok" {
		t.Fatalf("the allowlisted tool was refused: status = %q content = %s", res.Status, res.Content)
	}
	res = registry.Execute(context.Background(), provider.ToolCall{
		ID: "3", Name: exposed["blocked"],
	})
	if res.Status != "denied" || !strings.Contains(res.Content, "needs 666") {
		t.Fatalf("a tool outside the allowlist was not refused: status = %q content = %s", res.Status, res.Content)
	}
}
