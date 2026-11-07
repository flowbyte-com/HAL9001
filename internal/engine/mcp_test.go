package engine

import (
	"context"
	"strings"
	"testing"
	"time"

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
