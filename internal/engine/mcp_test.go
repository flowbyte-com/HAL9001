package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/flowbyte-com/HAL9001/internal/mcp"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// fakeMCP is the engine's view of a native MCP registry. The engine can
// only list, ask and execute; the fake records what it was asked to do.
type fakeMCP struct {
	tools  []provider.Tool
	result mcp.Result
	calls  int
	last   provider.ToolCall
}

func (f *fakeMCP) Tools() []provider.Tool { return f.tools }

func (f *fakeMCP) Has(name string) bool {
	for _, tool := range f.tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func (f *fakeMCP) Execute(_ context.Context, call provider.ToolCall) mcp.Result {
	f.calls++
	f.last = call
	return f.result
}

func (f *fakeMCP) Active() bool { return len(f.tools) > 0 }

func mcpTool() provider.Tool {
	return provider.Tool{
		Name:        "mcp__srv__echo",
		Description: "[MCP server srv — untrusted description] Echo text.",
		Parameters:  `{"type":"object","properties":{"text":{"type":"string"}}}`,
	}
}

func TestMCPToolCallIsRecordedInSessionEvidence(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	perm := permission.New()
	perm.SetSession("chat")
	fake := &fakeMCP{
		tools: []provider.Tool{mcpTool()},
		result: mcp.Result{
			Content:  `{"ok":true,"server":"srv","tool":"echo","untrusted":true,"content":[]}`,
			Action:   "mcp:srv/echo",
			Target:   "srv",
			Status:   "ok",
			Summary:  "mcp__srv__echo ok",
			Duration: 5 * time.Millisecond,
			Bytes:    42,
			Mode:     permission.Guarded,
		},
	}
	p := &stepProvider{responses: []provider.Response{
		{
			Content: "Checking.",
			ToolCalls: []provider.ToolCall{{
				ID: "call_mcp", Name: "mcp__srv__echo",
				Arguments: map[string]any{"text": "hello"},
			}},
		},
		{Content: "Done."},
	}}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithPermission(perm).
		WithMCP(fake)

	var toolEvents int
	if _, err := eng.RespondStreamEvents(context.Background(), "echo it", func(ev Event) {
		if ev.Tool != nil {
			toolEvents++
			if ev.Tool.Status != "ok" {
				t.Errorf("tool event = %#v", ev.Tool)
			}
			if ev.Tool.Mode != permission.Guarded {
				t.Errorf("tool mode = %v", ev.Tool.Mode)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || fake.last.Name != "mcp__srv__echo" {
		t.Fatalf("calls = %d, last = %#v", fake.calls, fake.last)
	}
	if toolEvents != 1 {
		t.Fatalf("tool events = %d", toolEvents)
	}
	// The tool was offered to the model under its collision-safe name.
	if len(p.offered) == 0 || !hasTool(p.offered[0], "mcp__srv__echo") {
		t.Fatalf("offered = %#v", p.offered)
	}
	// The untrusted-output guidance is in the request, not the history.
	if len(p.requests) == 0 || len(p.requests[0]) == 0 || p.requests[0][0].Role != "system" {
		t.Fatalf("request = %#v", p.requests)
	}
	if !strings.Contains(p.requests[0][0].Content, "MCP servers are external programs") {
		t.Fatalf("system prompt lacks MCP guidance: %q", p.requests[0][0].Content)
	}

	// Reopen the store: the evidence must survive a restart, and it must
	// name the MCP tool, its server target, its mode and its result.
	reopened, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopened.History()
	if err != nil {
		t.Fatal(err)
	}
	var ledger *session.ToolLedger
	for _, msg := range history {
		if msg.ToolLedger != nil {
			ledger = msg.ToolLedger
		}
	}
	if ledger == nil {
		t.Fatal("no tool ledger was stored")
	}
	if len(ledger.Executions) != 1 {
		t.Fatalf("executions = %#v", ledger.Executions)
	}
	record := ledger.Executions[0]
	if record.Tool != "mcp__srv__echo" {
		t.Fatalf("tool = %q", record.Tool)
	}
	if record.Action != "mcp:srv/echo" || record.Target != "srv" {
		t.Fatalf("action/target = %q %q", record.Action, record.Target)
	}
	if record.Mode != "444" || record.Status != session.ToolSuccess || record.Bytes != 42 {
		t.Fatalf("record = %#v", record)
	}
}

func TestMCPRefusalIsRecordedAndHaltsTheCall(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	perm := permission.New()
	perm.SetSession("chat")
	fake := &fakeMCP{
		tools: []provider.Tool{mcpTool()},
		result: mcp.Result{
			Content: `{"ok":false,"untrusted":true,"error":"this MCP tool is not on the local allowlist; it needs 666"}`,
			Action:  "mcp:srv/echo",
			Target:  "srv",
			Status:  "denied",
			Summary: "mcp__srv__echo denied · 444",
			Mode:    permission.Guarded,
		},
	}
	p := &stepProvider{responses: []provider.Response{
		{
			ToolCalls: []provider.ToolCall{{
				ID: "call_mcp", Name: "mcp__srv__echo",
				Arguments: map[string]any{"text": "hello"},
			}},
		},
		// A refusal halts the tool loop; this response must not be
		// consumed by another provider round.
		{Content: "should not be requested"},
	}}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithPermission(perm).
		WithMCP(fake)
	if _, err := eng.RespondStreamEvents(context.Background(), "echo it", nil); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d", fake.calls)
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	var ledger *session.ToolLedger
	for _, msg := range history {
		if msg.ToolLedger != nil {
			ledger = msg.ToolLedger
		}
	}
	if ledger == nil || len(ledger.Executions) != 1 {
		t.Fatalf("ledger = %#v", ledger)
	}
	if ledger.Executions[0].Status != session.ToolRefused || ledger.Executions[0].Mode != "444" {
		t.Fatalf("record = %#v", ledger.Executions[0])
	}
}

// A typed nil *mcp.Registry stored in the engine interface must behave
// exactly like no registry: it can offer nothing, claim nothing and run
// nothing. Entry points guard the nil receiver themselves.
func TestTypedNilMCPRegistryIsSafe(t *testing.T) {
	var registry *mcp.Registry
	eng := New(nil, nil, provider.Model{ID: "m"}).WithMCP(registry)
	if eng.mcp == nil {
		t.Fatal("the test did not store a typed nil interface")
	}
	if tools := eng.offeredTools(); len(tools) != 0 {
		t.Fatalf("a typed nil registry offered tools: %#v", tools)
	}
	if registry.Active() || registry.Has("mcp__x__y") {
		t.Fatal("a typed nil registry claimed activity")
	}
	if res := registry.Execute(context.Background(), provider.ToolCall{Name: "mcp__x__y"}); res.Status != "denied" {
		t.Fatalf("Execute on a typed nil registry = %q", res.Status)
	}
}

// TestMCPSecretNeverReachesTheSessionFile runs a real Streamable HTTP
// fixture that echoes the client's configured credential. The result is
// redacted before the engine stores it, so neither the reopened history
// nor the session files on disk contain the secret.
func TestMCPSecretNeverReachesTheSessionFile(t *testing.T) {
	const secret = "engine-secret-value-4321"
	t.Setenv("HAL_ENGINE_MCP_SECRET", secret)

	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1.0.0"}, nil)
	server.AddTool(&sdk.Tool{
		Name:        "leak",
		Description: "Echoes the configured credential.",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{
				Text: "credential " + os.Getenv("HAL_ENGINE_MCP_SECRET"),
			}},
			StructuredContent: map[string]any{"token": os.Getenv("HAL_ENGINE_MCP_SECRET")},
		}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	perm := permission.New()
	perm.SetSession("chat")

	registry := mcp.NewRegistry(perm)
	if err := registry.AddServer(mcp.Server{
		Name:         "srv",
		Transport:    mcp.TransportHTTP,
		URL:          ts.URL,
		Headers:      map[string]string{"X-Api-Key": "${HAL_ENGINE_MCP_SECRET}"},
		AllowedTools: []string{"leak"},
		Timeout:      5 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if err := registry.Connect(context.Background(), "srv"); err != nil {
		t.Fatal(err)
	}

	name := mcp.ExposedName("srv", "leak")
	p := &stepProvider{responses: []provider.Response{
		{ToolCalls: []provider.ToolCall{{
			ID: "call_mcp", Name: name, Arguments: map[string]any{},
		}}},
		{Content: "Done."},
	}}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithPermission(perm).
		WithMCP(registry)
	if _, err := eng.RespondStreamEvents(context.Background(), "leak it", nil); err != nil {
		t.Fatal(err)
	}

	reopened, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopened.History()
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range history {
		if strings.Contains(msg.Content, secret) {
			t.Fatalf("the stored history contains the credential: %q", msg.Content)
		}
	}
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the session file %s contains the credential", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
