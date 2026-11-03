package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// toolProvider reports the tools it was offered and returns a scripted
// turn. It is the smallest provider that can make a call.
type toolProvider struct {
	offered [][]provider.Tool
	reply   provider.Response
	err     error
}

func (p *toolProvider) Name() string { return "tool-test" }

func (p *toolProvider) Chat(_ context.Context, _ []provider.Message, _ provider.Model) (provider.Response, error) {
	p.offered = append(p.offered, nil)
	return p.reply, p.err
}

func (p *toolProvider) ChatStream(_ context.Context, _ []provider.Message, _ provider.Model, emit func(provider.Delta) error) (provider.Response, error) {
	p.offered = append(p.offered, nil)
	return p.reply, p.err
}

func (p *toolProvider) ChatStreamTools(_ context.Context, _ []provider.Message, _ provider.Model, tools []provider.Tool, emit func(provider.Delta) error) (provider.Response, error) {
	p.offered = append(p.offered, tools)
	if p.err == nil && p.reply.Content != "" {
		if err := emit(provider.Delta{Text: p.reply.Content}); err != nil {
			return provider.Response{}, err
		}
	}
	return p.reply, p.err
}

// plainProvider streams but cannot take function definitions.
type plainProvider struct {
	calls int
	reply provider.Response
}

func (p *plainProvider) Name() string { return "plain" }
func (p *plainProvider) Chat(_ context.Context, _ []provider.Message, _ provider.Model) (provider.Response, error) {
	p.calls++
	return p.reply, nil
}
func (p *plainProvider) ChatStream(_ context.Context, _ []provider.Message, _ provider.Model, _ func(provider.Delta) error) (provider.Response, error) {
	p.calls++
	return p.reply, nil
}

// rejectingProvider refuses function definitions, as an API without tool
// support does, and works when asked without them.
type rejectingProvider struct {
	withTools int
	without   int
}

func (p *rejectingProvider) Name() string { return "rejecting" }
func (p *rejectingProvider) Chat(_ context.Context, _ []provider.Message, _ provider.Model) (provider.Response, error) {
	p.without++
	return provider.Response{Content: "plain answer"}, nil
}
func (p *rejectingProvider) ChatStream(_ context.Context, _ []provider.Message, _ provider.Model, emit func(provider.Delta) error) (provider.Response, error) {
	p.without++
	return provider.Response{Content: "plain answer"}, nil
}
func (p *rejectingProvider) ChatStreamTools(_ context.Context, _ []provider.Message, _ provider.Model, _ []provider.Tool, _ func(provider.Delta) error) (provider.Response, error) {
	p.withTools++
	return provider.Response{}, errors.New(`provider: HTTP 400: {"error":{"message":"tools are not supported by this model"}}`)
}

// toolEngine builds an engine with a workspace rooted in a temporary
// directory, and returns it with that root.
func toolEngine(t *testing.T, p provider.Provider, reply provider.Response) (*Engine, string) {
	t.Helper()
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "HAL")
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if tp, ok := p.(*toolProvider); ok {
		tp.reply = reply
	}
	return New(st, p, provider.Model{ID: "m"}).WithWorkspace(ws), ws.Root()
}

// collect runs one turn, recording the proposal and text events.
func collect(t *testing.T, e *Engine, input string) (string, *workspace.Proposal, []string) {
	t.Helper()
	var (
		proposal *workspace.Proposal
		text     []string
	)
	reply, err := e.RespondStreamEvents(context.Background(), input, func(ev Event) {
		if ev.Proposal != nil {
			proposal = ev.Proposal
		}
		if ev.Text != "" {
			text = append(text, ev.Text)
		}
	})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	return reply, proposal, text
}

// TestWriteProposalIsPreparedNotExecuted checks the central promise: the
// model can ask, the engine turns the ask into a reviewable proposal, and
// nothing reaches the filesystem.
func TestWriteProposalIsPreparedNotExecuted(t *testing.T) {
	reply := provider.Response{
		Content: "Here is the letter.",
		ToolCalls: []provider.ToolCall{{
			ID:   "call_1",
			Name: workspace.ToolName,
			Arguments: map[string]any{
				"path":    "hosting-letter.md",
				"content": "Dear hosting provider,\n\nPlease …\n",
			},
		}},
	}
	eng, root := toolEngine(t, &toolProvider{}, reply)
	got, proposal, _ := collect(t, eng, "write a short letter and save it as hosting-letter.md")

	if proposal == nil {
		t.Fatalf("no proposal emitted; reply = %q", got)
	}
	if proposal.Path != "hosting-letter.md" {
		t.Errorf("path = %q", proposal.Path)
	}
	if string(proposal.Content) != "Dear hosting provider,\n\nPlease …\n" {
		t.Errorf("content = %q", proposal.Content)
	}
	if proposal.Overwrite {
		t.Error("a new file must not be marked as an overwrite")
	}
	if proposal.Session != "chat" || proposal.Turn != 1 {
		t.Errorf("binding = %q/%d", proposal.Session, proposal.Turn)
	}
	if !strings.Contains(got, "hosting-letter.md") || !strings.Contains(got, "awaiting your approval") {
		t.Errorf("reply does not describe the proposal: %q", got)
	}
	if strings.Contains(strings.ToLower(got), "saved") {
		t.Errorf("the reply claims a save that has not happened: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "hosting-letter.md")); !os.IsNotExist(err) {
		t.Error("the turn wrote a file")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Error("the turn created the workspace")
	}

	// The stored conversation keeps the text, and the proposal is not
	// part of it.
	history, err := eng.sessions.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || !strings.Contains(history[1].Content, "hosting-letter.md") {
		t.Fatalf("history = %#v", history)
	}
}

// TestWriteProposalOnlyOfferedWithAWorkspace checks that the tool is
// absent unless the application configured a workspace.
func TestWriteProposalOnlyOfferedWithAWorkspace(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &toolProvider{reply: provider.Response{
		Content: "I can't save files without a workspace.",
		ToolCalls: []provider.ToolCall{{
			Name:      workspace.ToolName,
			Arguments: map[string]any{"path": "x.md", "content": "x"},
		}},
	}}
	eng := New(st, p, provider.Model{ID: "m"})
	reply, err := eng.RespondStream(context.Background(), "save this", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.offered) != 1 || len(p.offered[0]) != 0 {
		t.Errorf("tools offered without a workspace: %#v", p.offered)
	}
	if !strings.Contains(reply, "no workspace") {
		t.Errorf("reply = %q", reply)
	}
}

// TestWriteProposalIsRefusedForBadDestinations checks that the workspace's
// rules reach the conversation as one short line, with no proposal.
func TestWriteProposalIsRefusedForBadDestinations(t *testing.T) {
	cases := []struct {
		path   string
		expect string
	}{
		{"../secrets.md", "plain file name"},
		{"/etc/passwd", "plain file name"},
		{"", "plain file name"},
	}
	for _, c := range cases {
		eng, root := toolEngine(t, &toolProvider{}, provider.Response{
			ToolCalls: []provider.ToolCall{{
				Name:      workspace.ToolName,
				Arguments: map[string]any{"path": c.path, "content": "x"},
			}},
		})
		reply, proposal, _ := collect(t, eng, "save it")
		if proposal != nil {
			t.Errorf("%q: a proposal was emitted", c.path)
		}
		if !strings.Contains(reply, c.expect) {
			t.Errorf("%q: reply = %q, want %q", c.path, reply, c.expect)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("%q: the workspace was created", c.path)
		}
	}
}

// TestWriteProposalRejectsMalformedArguments checks the argument types.
func TestWriteProposalRejectsMalformedArguments(t *testing.T) {
	cases := []map[string]any{
		{"path": "x.md"},
		{"content": "x"},
		{"path": 42, "content": "x"},
		{"path": "x.md", "content": map[string]any{"a": 1}},
		{"_invalid": "not json"},
	}
	for _, args := range cases {
		eng, _ := toolEngine(t, &toolProvider{}, provider.Response{
			ToolCalls: []provider.ToolCall{{Name: workspace.ToolName, Arguments: args}},
		})
		reply, proposal, _ := collect(t, eng, "save it")
		if proposal != nil {
			t.Errorf("%#v: a proposal was emitted", args)
		}
		if !strings.Contains(reply, "can't save that") {
			t.Errorf("%#v: reply = %q", args, reply)
		}
	}
}

// TestOnlyOneProposalPerTurn checks duplicates and other capabilities.
func TestOnlyOneProposalPerTurn(t *testing.T) {
	eng, _ := toolEngine(t, &toolProvider{}, provider.Response{
		ToolCalls: []provider.ToolCall{
			{Name: workspace.ToolName, Arguments: map[string]any{"path": "a.md", "content": "a"}},
			{Name: workspace.ToolName, Arguments: map[string]any{"path": "b.md", "content": "b"}},
			{Name: "exec", Arguments: map[string]any{"cmd": "id"}},
			{Name: "web_search", Arguments: map[string]any{"q": "x"}},
		},
	})
	reply, proposal, _ := collect(t, eng, "do several things")
	if proposal == nil || proposal.Path != "a.md" {
		t.Fatalf("proposal = %#v", proposal)
	}
	if !strings.Contains(reply, "Only one write") {
		t.Errorf("the duplicate was not refused: %q", reply)
	}
	if !strings.Contains(reply, "444") {
		t.Errorf("the unknown operation was not refused: %q", reply)
	}
	if !strings.Contains(reply, "not yet implemented") {
		t.Errorf("the allowed capability was not reported honestly: %q", reply)
	}
}

// TestProviderWithoutToolsKeepsWorking checks the fallback: a provider
// that refuses function definitions continues the conversation without
// them, and the engine stops offering them.
func TestProviderWithoutToolsKeepsWorking(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(filepath.Join(t.TempDir(), "HAL"))
	if err != nil {
		t.Fatal(err)
	}
	p := &rejectingProvider{}
	eng := New(st, p, provider.Model{ID: "m"}).WithWorkspace(ws)
	reply, err := eng.RespondStream(context.Background(), "hello", nil)
	if err != nil {
		t.Fatalf("the turn failed instead of degrading: %v", err)
	}
	if reply != "plain answer" {
		t.Errorf("reply = %q", reply)
	}
	if p.withTools != 1 || p.without != 1 {
		t.Errorf("attempts: with tools %d, without %d", p.withTools, p.without)
	}
	// The next turn does not try tools again.
	if _, err := eng.RespondStream(context.Background(), "again", nil); err != nil {
		t.Fatal(err)
	}
	if p.withTools != 1 {
		t.Errorf("tools were retried: %d", p.withTools)
	}
}

// TestProviderWithoutToolSupportIsNotAsked checks the plain provider: no
// interface, no tools, no proposal.
func TestProviderWithoutToolSupportIsNotAsked(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(filepath.Join(t.TempDir(), "HAL"))
	if err != nil {
		t.Fatal(err)
	}
	p := &plainProvider{reply: provider.Response{Content: "hello"}}
	eng := New(st, p, provider.Model{ID: "m"}).WithWorkspace(ws)
	reply, err := eng.RespondStream(context.Background(), "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "hello" || p.calls != 1 {
		t.Errorf("reply = %q, calls = %d", reply, p.calls)
	}
}

// TestApprovedWriteAfterProposal checks the engine's half of the
// transaction: the proposal it emitted is the one the store writes.
func TestApprovedWriteAfterProposal(t *testing.T) {
	eng, root := toolEngine(t, &toolProvider{}, provider.Response{
		ToolCalls: []provider.ToolCall{{
			Name:      workspace.ToolName,
			Arguments: map[string]any{"path": "notes/acc-fuel.md", "content": "# ACC fuel\n"},
		}},
	})
	_, proposal, _ := collect(t, eng, "make a research note")
	if proposal == nil {
		t.Fatal("no proposal")
	}
	if _, err := eng.workspace.Approve(*proposal); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "notes", "acc-fuel.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# ACC fuel\n" {
		t.Errorf("content = %q", got)
	}
}

// TestTurnNumberCountsUserMessages checks the identifier recorded with a
// proposal.
func TestTurnNumberCountsUserMessages(t *testing.T) {
	eng, _ := toolEngine(t, &toolProvider{}, provider.Response{Content: "ok"})
	if _, err := eng.RespondStream(context.Background(), "one", nil); err != nil {
		t.Fatal(err)
	}
	eng.toolProviderReply(t, provider.Response{
		ToolCalls: []provider.ToolCall{{
			Name:      workspace.ToolName,
			Arguments: map[string]any{"path": "x.md", "content": "x"},
		}},
	})
	_, proposal, _ := collect(t, eng, "two")
	if proposal == nil {
		t.Fatal("no proposal")
	}
	if proposal.Turn != 2 {
		t.Errorf("turn = %d, want 2", proposal.Turn)
	}
}

// toolProviderReply swaps the scripted reply on the engine's provider.
func (e *Engine) toolProviderReply(t *testing.T, r provider.Response) {
	t.Helper()
	p, ok := e.provider.(*toolProvider)
	if !ok {
		t.Fatal("not a tool provider")
	}
	p.reply = r
}
