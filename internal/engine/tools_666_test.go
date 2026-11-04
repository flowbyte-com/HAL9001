package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/knowledge"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// stepProvider replays one response per provider call and records the full
// transcript each call received.
type stepProvider struct {
	responses []provider.Response
	requests  [][]provider.Message
	offered   [][]provider.Tool
}

func (p *stepProvider) Name() string { return "step-test" }

func (p *stepProvider) Chat(_ context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	p.requests = append(p.requests, append([]provider.Message(nil), messages...))
	if len(p.responses) == 0 {
		return provider.Response{Content: "script exhausted"}, nil
	}
	resp := p.responses[0]
	p.responses = p.responses[1:]
	return resp, nil
}

func (p *stepProvider) ChatStream(ctx context.Context, messages []provider.Message, model provider.Model, emit func(provider.Delta) error) (provider.Response, error) {
	return p.Chat(ctx, messages, model)
}

func (p *stepProvider) ChatStreamTools(_ context.Context, messages []provider.Message, _ provider.Model, tools []provider.Tool, emit func(provider.Delta) error) (provider.Response, error) {
	p.requests = append(p.requests, append([]provider.Message(nil), messages...))
	p.offered = append(p.offered, append([]provider.Tool(nil), tools...))
	if len(p.responses) == 0 {
		return provider.Response{Content: "script exhausted"}, nil
	}
	resp := p.responses[0]
	p.responses = p.responses[1:]
	if resp.Content != "" && emit != nil {
		if err := emit(provider.Delta{Text: resp.Content}); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

func toolNames(tools []provider.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func hasTool(tools []provider.Tool, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func Test444OffersGuardedWriteAndKnowledgeButNotPrivilegedTools(t *testing.T) {
	st, _ := session.Open(t.TempDir(), "chat")
	perm := permission.New()
	perm.SetSession("chat")
	eng := New(st, &stepProvider{}, provider.Model{ID: "m"}).
		WithWorkspace(mustWorkspace(t)).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil)).
		WithKnowledge(knowledge.NewRegistry(&knowledge.Client{Bin: "/nonexistent"}))
	tools := eng.offeredTools()
	for _, want := range []string{workspaceToolName, "mpm_memory", "mpm_skills"} {
		if !hasTool(tools, want) {
			t.Errorf("444 offer is missing %q: %v", want, toolNames(tools))
		}
	}
	for _, forbidden := range []string{"read", "write", "edit", "bash"} {
		if hasTool(tools, forbidden) {
			t.Errorf("444 offered %q", forbidden)
		}
	}
}

func Test666OffersTheRealToolsAndReplacesTheProposal(t *testing.T) {
	st, _ := session.Open(t.TempDir(), "chat")
	perm := permission.New()
	perm.SetSession("chat")
	_ = perm.Enable("chat")
	eng := New(st, &stepProvider{}, provider.Model{ID: "m"}).
		WithWorkspace(mustWorkspace(t)).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	tools := eng.offeredTools()
	for _, want := range []string{"read", "write", "edit", "bash"} {
		if !hasTool(tools, want) {
			t.Errorf("666 offer is missing %q: %v", want, toolNames(tools))
		}
	}
	if hasTool(tools, workspaceToolName) {
		t.Error("666 still offered the guarded write proposal")
	}
}

func TestMultiStepReadCarriesToolResultBackToTheModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(path, []byte("the answer is 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := session.Open(t.TempDir(), "chat")
	perm := permission.New()
	perm.SetSession("chat")
	_ = perm.Enable("chat")
	p := &stepProvider{responses: []provider.Response{
		{
			Content: "I'll look.",
			ToolCalls: []provider.ToolCall{{
				ID: "call_read", Name: "read",
				Arguments: map[string]any{"path": path},
			}},
		},
		{Content: "The file says 42."},
	}}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	var activity []ToolActivity
	reply, err := eng.RespondStreamEvents(context.Background(), "read the file", func(ev Event) {
		if ev.Tool != nil {
			activity = append(activity, *ev.Tool)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "I'll look.") || !strings.Contains(reply, "The file says 42.") {
		t.Errorf("reply = %q", reply)
	}
	if len(p.requests) != 2 {
		t.Fatalf("provider calls = %d", len(p.requests))
	}
	second := p.requests[1]
	var assistant, result *provider.Message
	for i := range second {
		switch second[i].Role {
		case "assistant":
			if len(second[i].ToolCalls) > 0 {
				assistant = &second[i]
			}
		case "tool":
			result = &second[i]
		}
	}
	if assistant == nil || assistant.ToolCalls[0].Name != "read" {
		t.Fatalf("assistant tool call missing: %#v", second)
	}
	if result == nil || result.ToolCallID != "call_read" ||
		!strings.Contains(result.Content, "the answer is 42") {
		t.Fatalf("tool result missing: %#v", second)
	}
	if len(activity) != 1 || activity[0].Status != "ok" || activity[0].Name != "read" {
		t.Fatalf("activity = %#v", activity)
	}
	history, _ := st.History()
	if len(history) != 2 || history[1].Role != session.RoleAssistant {
		t.Fatalf("history = %#v", history)
	}
	if strings.Contains(history[1].Content, `"stdout"`) {
		t.Errorf("tool transcript leaked into the stored answer: %q", history[1].Content)
	}
}

func Test444RefusesAPrivilegedCallEvenWhenTheModelInventsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(path, []byte("TOP-SECRET"), 0o644)
	st, _ := session.Open(t.TempDir(), "chat")
	perm := permission.New()
	perm.SetSession("chat")
	p := &stepProvider{responses: []provider.Response{{
		ToolCalls: []provider.ToolCall{{
			ID: "call_read", Name: "read",
			Arguments: map[string]any{"path": path},
		}},
	}}}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	reply, err := eng.RespondStream(context.Background(), "read it", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "444") {
		t.Errorf("refusal = %q", reply)
	}
	if strings.Contains(reply, "TOP-SECRET") {
		t.Error("444 read the file")
	}
	if len(p.requests) != 1 {
		t.Errorf("a denied tool started another provider round: %d", len(p.requests))
	}
}

func TestRevoking666DuringAProviderTurnStopsTheToolCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(path, []byte("TOP-SECRET"), 0o644)
	st, _ := session.Open(t.TempDir(), "chat")
	perm := permission.New()
	perm.SetSession("chat")
	_ = perm.Enable("chat")
	p := &revokeProvider{
		perm: perm,
		response: provider.Response{ToolCalls: []provider.ToolCall{{
			ID: "call_read", Name: "read",
			Arguments: map[string]any{"path": path},
		}}},
	}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	reply, err := eng.RespondStreamEvents(context.Background(), "read it", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "444") || strings.Contains(reply, "TOP-SECRET") {
		t.Errorf("call after revocation = %q", reply)
	}
	if perm.Mode() != permission.Guarded {
		t.Fatal("test did not revoke 666")
	}
}

type revokeProvider struct {
	perm     *permission.Controller
	response provider.Response
}

func (p *revokeProvider) Name() string { return "revoke" }
func (p *revokeProvider) Chat(context.Context, []provider.Message, provider.Model) (provider.Response, error) {
	return p.response, nil
}
func (p *revokeProvider) ChatStream(ctx context.Context, m []provider.Message, model provider.Model, _ func(provider.Delta) error) (provider.Response, error) {
	return p.Chat(ctx, m, model)
}
func (p *revokeProvider) ChatStreamTools(ctx context.Context, m []provider.Message, model provider.Model, _ []provider.Tool, _ func(provider.Delta) error) (provider.Response, error) {
	p.perm.Disable()
	return p.Chat(ctx, m, model)
}

func Test666WriteFileProposalIsNotSilentlyExecuted(t *testing.T) {
	st, _ := session.Open(t.TempDir(), "chat")
	perm := permission.New()
	perm.SetSession("chat")
	_ = perm.Enable("chat")
	p := &stepProvider{responses: []provider.Response{{
		ToolCalls: []provider.ToolCall{{
			ID: "call_w", Name: workspaceToolName,
			Arguments: map[string]any{"path": "x.md", "content": "x"},
		}},
	}}}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithWorkspace(mustWorkspace(t)).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	reply, err := eng.RespondStream(context.Background(), "write", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "write tool") {
		t.Errorf("reply = %q", reply)
	}
}

func mustWorkspace(t *testing.T) *workspace.Store {
	t.Helper()
	w, err := workspace.Open(filepath.Join(t.TempDir(), "HAL"))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

const workspaceToolName = "write_file"
