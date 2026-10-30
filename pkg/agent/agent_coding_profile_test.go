package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type lateTool struct{ name string }

func (f lateTool) Name() string               { return f.name }
func (f lateTool) Description() string        { return "late registered tool" }
func (f lateTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (f lateTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.NewToolResult("late")
}

func newCodingProfileLoop(t *testing.T, workspace string, allowlist []string) *AgentLoop {
	t.Helper()
	t.Setenv("DEEPCLAW_CODING_PROFILE", "1")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if allowlist != nil {
		frontmatter := "---\ntools: [" + strings.Join(allowlist, ", ") + "]\n---\n# Agent\n"
		if err := os.WriteFile(filepath.Join(workspace, "AGENT.md"), []byte(frontmatter), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "test-model"
	// Keep full test outputs inline so assertions see tool guidance rather than
	// the large-result spill preview.
	cfg.Tools.MaxInlineResultChars = 200000
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), &recordingProvider{})
	t.Cleanup(loop.Close)
	return loop
}

func codingAgent(t *testing.T, loop *AgentLoop) *AgentInstance {
	t.Helper()
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	return agent
}

func executeTool(t *testing.T, agent *AgentInstance, name string, args map[string]any) *tools.ToolResult {
	t.Helper()
	return agent.Tools.ExecuteWithContext(context.Background(), name, args, "cli", "chat-1", nil)
}

func TestAgentCodingProfileExposesOnlyCoreTools(t *testing.T) {
	loop := newCodingProfileLoop(t, t.TempDir(), nil)
	agent := codingAgent(t, loop)
	got := agent.Tools.List()
	want := []string{"bash", "edit", "read", "write"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	for _, name := range want {
		if !agent.Tools.HasRegistered(name) {
			t.Fatalf("missing tool %q in %v", name, got)
		}
		if _, ok := agent.Tools.Get(name); !ok {
			t.Fatalf("profile tool %q is not callable", name)
		}
	}
	for _, hidden := range []string{"read_file", "write_file", "edit_file", "exec"} {
		if _, ok := agent.Tools.Get(hidden); ok {
			t.Fatalf("underlying tool %q must be hidden", hidden)
		}
	}
}

func TestCodingBashAcceptsCommandOnly(t *testing.T) {
	loop := newCodingProfileLoop(t, t.TempDir(), nil)
	agent := codingAgent(t, loop)

	result := executeTool(t, agent, "bash", map[string]any{"command": "printf coding-bash-ok"})
	if result.IsError {
		t.Fatalf("bash failed: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "coding-bash-ok") {
		t.Fatalf("bash output = %q", result.ForLLM)
	}
}

func TestCodingBashSchemaHasNoSessionSurface(t *testing.T) {
	loop := newCodingProfileLoop(t, t.TempDir(), nil)
	bash, ok := codingAgent(t, loop).Tools.Get("bash")
	if !ok {
		t.Fatal("bash missing")
	}
	params := bash.Parameters()
	properties := params["properties"].(map[string]any)
	got := make([]string, 0, len(properties))
	for name := range properties {
		got = append(got, name)
	}
	for _, name := range []string{"command", "cwd", "timeout"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("bash schema missing %q: %v", name, got)
		}
	}
	for _, forbidden := range []string{"action", "sessionId", "background", "pty", "keys", "data"} {
		if _, ok := properties[forbidden]; ok {
			t.Fatalf("bash schema exposes removed field %q: %v", forbidden, got)
		}
	}
	required := params["required"].([]string)
	if len(required) != 1 || required[0] != "command" {
		t.Fatalf("required = %v, want [command]", required)
	}
}

func TestCodingProfileDescriptionsAndSchemasReferenceOnlyVisibleTools(t *testing.T) {
	loop := newCodingProfileLoop(t, t.TempDir(), nil)
	agent := codingAgent(t, loop)
	forbidden := regexp.MustCompile(`\b(read_file|write_file|edit_file|append_file|list_dir|search_files|exec|action|background|pty|sessionId|poll|stdin)\b`)
	for _, name := range []string{"read", "write", "edit", "bash"} {
		tool, ok := agent.Tools.Get(name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		description := tool.Description()
		if strings.TrimSpace(description) == "" {
			t.Fatalf("%s has no description", name)
		}
		if match := forbidden.FindString(description); match != "" {
			t.Fatalf("%s description references unavailable capability %q: %s", name, match, description)
		}
		raw, err := json.Marshal(tool.Parameters())
		if err != nil {
			t.Fatal(err)
		}
		if match := forbidden.FindString(string(raw)); match != "" {
			t.Fatalf("%s schema references unavailable capability %q: %s", name, match, raw)
		}
	}
}

func TestCodingProfilePreservesAgentAllowlist(t *testing.T) {
	loop := newCodingProfileLoop(t, t.TempDir(), []string{"read_file", "write_file"})
	agent := codingAgent(t, loop)
	got := agent.Tools.List()
	want := []string{"read", "write"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("profile ignored allowlist: tools = %v, want %v", got, want)
	}
	if _, ok := agent.Tools.Get("bash"); ok {
		t.Fatal("bash was not in the agent allowlist but is callable")
	}
	if _, ok := agent.Tools.Get("edit"); ok {
		t.Fatal("edit was not in the agent allowlist but is callable")
	}
}

func TestCodingProfileAllowlistAcceptsProfileNames(t *testing.T) {
	loop := newCodingProfileLoop(t, t.TempDir(), []string{"read", "bash"})
	agent := codingAgent(t, loop)
	got := agent.Tools.List()
	want := []string{"bash", "read"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("profile-name allowlist produced %v, want %v", got, want)
	}
	for _, denied := range []string{"write", "edit"} {
		if _, ok := agent.Tools.Get(denied); ok {
			t.Fatalf("%q was not in the allowlist but is callable", denied)
		}
	}
}

func TestCodingWriteGuidanceUsesVisibleEdit(t *testing.T) {
	workspace := t.TempDir()
	loop := newCodingProfileLoop(t, workspace, nil)
	agent := codingAgent(t, loop)

	path := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := executeTool(t, agent, "write", map[string]any{
		"path":    path,
		"content": "replacement",
	})
	if !result.IsError {
		t.Fatal("write replaced an existing file without overwrite=true")
	}
	if !strings.Contains(result.ForLLM, "edit") {
		t.Fatalf("write guidance does not name edit: %s", result.ForLLM)
	}
	for _, absent := range []string{"edit_file", "append_file"} {
		if strings.Contains(result.ForLLM, absent) {
			t.Fatalf("write guidance names absent tool %q: %s", absent, result.ForLLM)
		}
	}
}

func TestCodingProfileHidesLateRegistrations(t *testing.T) {
	loop := newCodingProfileLoop(t, t.TempDir(), nil)
	agent := codingAgent(t, loop)
	registry := agent.Tools

	registry.Register(lateTool{name: "extra_tool"})
	if _, ok := registry.Get("extra_tool"); ok {
		t.Fatal("late registered tool became callable")
	}
	if added, err := registry.RegisterUnique(lateTool{name: "unique_extra"}, false); err != nil || !added {
		t.Fatalf("late unique tool should remain installed but hidden: added=%v err=%v", added, err)
	}
	for _, name := range registry.List() {
		if name == "extra_tool" || name == "unique_extra" {
			t.Fatalf("late tool %q is provider-visible: %v", name, registry.List())
		}
	}
	for _, def := range registry.ToProviderDefs() {
		if def.Function.Name == "extra_tool" || def.Function.Name == "unique_extra" {
			t.Fatalf("late tool %q is in provider definitions", def.Function.Name)
		}
	}
	if !registry.HasRegistered("extra_tool") {
		t.Fatal("late tool should remain installed but hidden")
	}

	// A late registration must not overwrite a profile-visible tool.
	registry.Register(lateTool{name: "read"})
	read, ok := registry.Get("read")
	if !ok {
		t.Fatal("read disappeared after conflicting late registration")
	}
	if read.Description() == (lateTool{name: "read"}).Description() {
		t.Fatal("late registration overwrote the profile read tool")
	}
}

func TestCodingProfileToolOperations(t *testing.T) {
	workspace := t.TempDir()
	loop := newCodingProfileLoop(t, workspace, nil)
	agent := codingAgent(t, loop)
	path := filepath.Join(workspace, "code.txt")

	// Write a new file.
	result := executeTool(t, agent, "write", map[string]any{"path": path, "content": "alpha\nbeta\n"})
	if result.IsError {
		t.Fatalf("write: %s", result.ForLLM)
	}
	// Read it back.
	result = executeTool(t, agent, "read", map[string]any{"path": path})
	if result.IsError || !strings.Contains(result.ForLLM, "alpha") {
		t.Fatalf("read: %+v", result)
	}
	// Edit one unique occurrence.
	result = executeTool(t, agent, "edit", map[string]any{
		"path": path, "old_text": "beta", "new_text": "gamma",
	})
	if result.IsError {
		t.Fatalf("edit: %s", result.ForLLM)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "gamma") {
		t.Fatalf("edit result = %q, %v", data, err)
	}
	// Edit misses are errors with usable guidance.
	result = executeTool(t, agent, "edit", map[string]any{
		"path": path, "old_text": "missing", "new_text": "x",
	})
	if !result.IsError || !strings.Contains(result.ForLLM, "old_text not found") {
		t.Fatalf("missing edit error = %+v", result)
	}
	// Overwrite is still opt-in.
	result = executeTool(t, agent, "write", map[string]any{"path": path, "content": "replaced"})
	if !result.IsError {
		t.Fatal("write without overwrite replaced an existing file")
	}
	result = executeTool(t, agent, "write", map[string]any{
		"path": path, "content": "replaced", "overwrite": true,
	})
	if result.IsError {
		t.Fatalf("overwrite write: %s", result.ForLLM)
	}
	// Command execution, cwd and failure handling.
	sub := filepath.Join(workspace, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	result = executeTool(t, agent, "bash", map[string]any{"command": "pwd", "cwd": "sub"})
	if result.IsError || !strings.Contains(result.ForLLM, sub) {
		t.Fatalf("bash cwd: %+v", result)
	}
	result = executeTool(t, agent, "bash", map[string]any{"command": "echo boom >&2; exit 3"})
	if !result.IsError || !strings.Contains(result.ForLLM, "boom") {
		t.Fatalf("bash failure handling: %+v", result)
	}
	result = executeTool(t, agent, "bash", map[string]any{"command": "sleep 2", "timeout": 1})
	if !result.IsError || !strings.Contains(result.ForLLM, "timed out") {
		t.Fatalf("bash timeout: %+v", result)
	}
}

func TestCodingReadTruncationNamesVisibleTool(t *testing.T) {
	workspace := t.TempDir()
	loop := newCodingProfileLoop(t, workspace, nil)
	agent := codingAgent(t, loop)
	path := filepath.Join(workspace, "large.txt")
	line := "lorem ipsum dolor sit amet, consectetur adipiscing elit.\n"
	content := strings.Repeat(line, (70*1024)/len(line)+1)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result := executeTool(t, agent, "read", map[string]any{"path": path})
	if result.IsError {
		t.Fatalf("read large file: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "Call read again") {
		t.Fatalf("truncation guidance does not name read: %s", result.ForLLM)
	}
	if strings.Contains(result.ForLLM, "read_file") {
		t.Fatalf("truncation guidance names hidden read_file: %s", result.ForLLM)
	}
}
