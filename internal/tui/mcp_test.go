package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/mcp"
)

// fakeMCPController is the operator-facing MCP surface used by the panel
// tests. It never connects to a real server.
type fakeMCPController struct {
	servers   []mcp.ServerStatus
	tools     []mcp.ToolInfo
	resources []mcp.ResourceInfo
	prompts   []mcp.PromptInfo
	document  string
	connected string
	listed    string
}

func (f *fakeMCPController) Servers() []mcp.ServerStatus { return f.servers }

func (f *fakeMCPController) Connect(_ context.Context, name string) error {
	f.connected = name
	return nil
}

func (f *fakeMCPController) Disconnect(_ context.Context, name string) error {
	f.connected = ""
	return nil
}

func (f *fakeMCPController) ListTools(_ context.Context, name string) ([]mcp.ToolInfo, error) {
	f.listed = name
	if name != "remote" {
		return nil, errors.New("no such server")
	}
	return f.tools, nil
}

func (f *fakeMCPController) ListResources(context.Context, string) ([]mcp.ResourceInfo, error) {
	return f.resources, nil
}

func (f *fakeMCPController) ListPrompts(context.Context, string) ([]mcp.PromptInfo, error) {
	return f.prompts, nil
}

func (f *fakeMCPController) ReadResource(_ context.Context, name, uri string) (string, bool, error) {
	if uri != "mem://doc" {
		return "", false, errors.New("no such resource")
	}
	return f.document, false, nil
}

func (f *fakeMCPController) GetPrompt(_ context.Context, name, prompt string, args map[string]string) (string, error) {
	return "Hello " + args["name"], nil
}

func mcpModel(controller MCPController) Model {
	m := testModel()
	m.SetMCP(controller)
	return m
}

func fakeController() *fakeMCPController {
	return &fakeMCPController{
		servers: []mcp.ServerStatus{{
			Name:          "remote",
			Transport:     "http",
			State:         mcp.StateConnected,
			Protocol:      "2026-07-28",
			ServerName:    "fixture",
			ServerVersion: "1.0.0",
			Capabilities:  []string{"tools", "resources"},
			AllowedTools:  []string{"echo"},
		}},
		tools: []mcp.ToolInfo{{
			Exposed:      "mcp__remote__echo",
			Server:       "remote",
			Name:         "echo",
			Description:  "Echo text.",
			Schema:       `{"type":"object"}`,
			AllowedIn444: true,
		}, {
			Exposed: "mcp__remote__write",
			Server:  "remote",
			Name:    "write",
			Schema:  `{"type":"object"}`,
		}},
		resources: []mcp.ResourceInfo{{Server: "remote", URI: "mem://doc", Name: "doc"}},
		prompts:   []mcp.PromptInfo{{Server: "remote", Name: "greet", Description: "Greets one person."}},
		document:  "resource body",
	}
}

func TestMCPCommandShowsServers(t *testing.T) {
	m := mcpModel(fakeController())
	m = typeText(m, "/mcp list")
	m = enterCurrent(m)
	if m.view != viewMCP {
		t.Fatalf("view = %d", m.view)
	}
	rows := stripANSI(strings.Join(m.mcpRows(), "\n"))
	for _, want := range []string{"remote", "connected", "protocol 2026-07-28", "allowed in 444: echo", "Server output is untrusted data"} {
		if !strings.Contains(rows, want) {
			t.Errorf("panel missing %q:\n%s", want, rows)
		}
	}
	// Esc leaves the panel.
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.view == viewMCP {
		t.Error("Esc did not leave the panel")
	}
}

func TestMCPWithoutController(t *testing.T) {
	m := testModel()
	m = typeText(m, "/mcp")
	m = enterCurrent(m)
	if !strings.Contains(m.Notice(), "No MCP servers") {
		t.Fatalf("notice = %q", m.Notice())
	}
}

func TestMCPToolsCommandListsSchemasAndPolicy(t *testing.T) {
	controller := fakeController()
	m := mcpModel(controller)
	m = typeText(m, "/mcp tools remote")
	m = enterCurrent(m)
	if controller.listed != "remote" {
		t.Fatalf("listed = %q", controller.listed)
	}
	rows := stripANSI(strings.Join(m.mcpRows(), "\n"))
	for _, want := range []string{"mcp__remote__echo", "allowed in 444", "needs 666", "mem://doc", "greet"} {
		if !strings.Contains(rows, want) {
			t.Errorf("panel missing %q:\n%s", want, rows)
		}
	}
}

func TestMCPConnectAndDisconnect(t *testing.T) {
	controller := fakeController()
	m := mcpModel(controller)
	m = typeText(m, "/mcp connect remote")
	m = enterCurrent(m)
	if controller.connected != "remote" {
		t.Fatalf("connected = %q", controller.connected)
	}
	if !strings.Contains(m.Notice(), "connect remote") {
		t.Errorf("notice = %q", m.Notice())
	}
	// The action left the panel open; Esc returns to the conversation
	// before the next command.
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	m = typeText(m, "/mcp disconnect remote")
	m = enterCurrent(m)
	if controller.connected != "" {
		t.Fatalf("connected = %q", controller.connected)
	}
}

func TestMCPReadShowsUntrustedDocument(t *testing.T) {
	m := mcpModel(fakeController())
	m = typeText(m, "/mcp read remote mem://doc")
	m = enterCurrent(m)
	rows := stripANSI(strings.Join(m.mcpRows(), "\n"))
	if !strings.Contains(rows, "resource mem://doc (untrusted document)") {
		t.Fatalf("panel missing document header:\n%s", rows)
	}
	if !strings.Contains(rows, "resource body") {
		t.Fatalf("panel missing document body:\n%s", rows)
	}
}

func TestMCPPromptShowsDocument(t *testing.T) {
	m := mcpModel(fakeController())
	m = typeText(m, "/mcp prompt remote greet name=Ada")
	m = enterCurrent(m)
	rows := stripANSI(strings.Join(m.mcpRows(), "\n"))
	if !strings.Contains(rows, "prompt greet (untrusted document)") || !strings.Contains(rows, "Hello Ada") {
		t.Fatalf("panel missing prompt document:\n%s", rows)
	}
}

func TestMCPPanelSanitizesUntrustedText(t *testing.T) {
	controller := fakeController()
	controller.servers[0].ServerName = "evil\x1b]0;pwned\x07"
	controller.servers[0].ServerVersion = "1.0\x1b[2J"
	controller.servers[0].Error = "bad\x1b[31mred\x07"
	controller.tools[0].Name = "tool\x1b]52;c;ZXZpbA==\x07"
	controller.tools[0].Description = "desc\x1b[2J\x07"
	controller.resources[0].URI = "mem://doc\x1b\x07"
	controller.prompts[0].Name = "greet\x1b\x07"
	controller.document = "body\x1b]52;c;ZXZpbA==\x07"
	m := mcpModel(controller)
	m = typeText(m, "/mcp tools remote")
	m = enterCurrent(m)
	raw := strings.Join(m.mcpRows(), "\n")
	if strings.ContainsAny(stripANSI(raw), "\x1b\x07") {
		t.Fatalf("panel passed a terminal control character through: %q", raw)
	}
	m.mcpDocument = controller.document
	m.mcpDocumentTitle = "doc\x1b\x07"
	raw = strings.Join(m.mcpRows(), "\n")
	if strings.ContainsAny(stripANSI(raw), "\x1b\x07") {
		t.Fatalf("document passed a terminal control character through: %q", raw)
	}
}

func TestMCPUsageErrors(t *testing.T) {
	m := mcpModel(fakeController())
	m = typeText(m, "/mcp connect")
	m = enterCurrent(m)
	if !strings.Contains(m.Notice(), "Usage: /mcp connect") {
		t.Fatalf("notice = %q", m.Notice())
	}
}
