package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/mcp"
)

// The MCP panel is a place to look, not a place to live. HAL shows no
// permanent MCP badge: the operator asks for this view when they want
// to know which servers exist, what they offer and whether they are
// connected.
//
// Every action here is operator-driven. HAL never creates a server,
// never edits one and never lets a model change one: the configuration
// file is the only source of servers.

// MCPController is the operator-facing MCP surface.
//
// The interface is deliberately narrow: the panel can look, connect,
// disconnect and read. It cannot create a server, change a policy or
// grant a permission.
type MCPController interface {
	// Servers returns the state of every configured server.
	Servers() []mcp.ServerStatus
	// Connect opens a session with one configured server.
	Connect(ctx context.Context, name string) error
	// Disconnect closes the session with one server.
	Disconnect(ctx context.Context, name string) error
	// ListTools returns the tools of one connected server.
	ListTools(ctx context.Context, name string) ([]mcp.ToolInfo, error)
	// ListResources returns the resources of one connected server.
	ListResources(ctx context.Context, name string) ([]mcp.ResourceInfo, error)
	// ReadResource reads one resource document.
	ReadResource(ctx context.Context, name, uri string) (string, bool, error)
	// ListPrompts returns the prompts of one connected server.
	ListPrompts(ctx context.Context, name string) ([]mcp.PromptInfo, error)
	// GetPrompt fetches one prompt document.
	GetPrompt(ctx context.Context, name, prompt string, args map[string]string) (string, error)
}

// mcpActionMsg is the result of one MCP action started by the panel.
type mcpActionMsg struct {
	action    string
	name      string
	err       error
	tools     []mcp.ToolInfo
	resources []mcp.ResourceInfo
	prompts   []mcp.PromptInfo
	document  string
	title     string
	truncated bool
}

// updateMCP handles the MCP panel. Esc leaves it, as it leaves every
// other panel.
func (m Model) updateMCP(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case mcpActionMsg:
		m.applyMCPAction(msg)
		m.view = viewMCP
		m.panelOffset = 0
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, m.quit()
		case "esc":
			m.view = viewChat
			return m, nil
		case "up", "shift+tab":
			m.scrollPanel(-1)
			return m, nil
		case "down", "tab":
			m.scrollPanel(1)
			return m, nil
		case "pgup":
			m.scrollPanel(-contentHeight(m.height))
			return m, nil
		case "pgdown":
			m.scrollPanel(contentHeight(m.height))
			return m, nil
		}
	}
	return m, nil
}

// applyMCPAction stores the result of one MCP action. It is shared by
// the panel and the conversation view: the operator may close the panel
// while a slow server is still answering, and the error must not vanish.
func (m *Model) applyMCPAction(msg mcpActionMsg) {
	m.mcpFocus = oneLine(msg.name)
	if msg.tools != nil {
		m.mcpTools = msg.tools
	}
	if msg.resources != nil {
		m.mcpResources = msg.resources
	}
	if msg.prompts != nil {
		m.mcpPrompts = msg.prompts
	}
	if msg.document != "" {
		m.mcpDocument = msg.document
		m.mcpDocumentTitle = oneLine(msg.title)
		m.mcpDocumentCut = msg.truncated
	} else if msg.action != "read" && msg.action != "prompt" {
		m.mcpDocument = ""
		m.mcpDocumentTitle = ""
		m.mcpDocumentCut = false
	}
	if msg.err != nil {
		m.notice = "MCP " + msg.action + " failed: " + oneLine(msg.err.Error())
	} else {
		m.notice = "MCP " + msg.action + " " + oneLine(msg.name) + " · done"
	}
}

// mcpCmd handles /mcp.
//
// The sub-commands are the operator's own controls: list, status, tools,
// connect and disconnect. Nothing here can create or edit a server.
func (m *Model) mcpCmd(args []string) tea.Cmd {
	if m.mcp == nil {
		m.notice = "No MCP servers are configured."
		return nil
	}
	if len(args) == 0 {
		m.openMCP()
		return nil
	}
	switch strings.ToLower(args[0]) {
	case "list", "status":
		m.openMCP()
		return nil
	case "connect":
		if len(args) != 2 {
			m.notice = "Usage: /mcp connect <server>"
			return nil
		}
		return m.mcpAction("connect", args[1], nil)
	case "disconnect":
		if len(args) != 2 {
			m.notice = "Usage: /mcp disconnect <server>"
			return nil
		}
		return m.mcpAction("disconnect", args[1], nil)
	case "tools":
		if len(args) != 2 {
			m.notice = "Usage: /mcp tools <server>"
			return nil
		}
		return m.mcpAction("tools", args[1], nil)
	case "read":
		if len(args) != 3 {
			m.notice = "Usage: /mcp read <server> <uri>"
			return nil
		}
		return m.mcpAction("read", args[1], args[2:])
	case "prompt":
		if len(args) < 3 {
			m.notice = "Usage: /mcp prompt <server> <name> [key=value ...]"
			return nil
		}
		return m.mcpAction("prompt", args[1], args[2:])
	default:
		m.notice = "Usage: /mcp [list|status|tools <server>|connect <server>|disconnect <server>|read <server> <uri>|prompt <server> <name> [key=value ...]]"
		return nil
	}
}

// openMCP shows the panel.
func (m *Model) openMCP() {
	m.view = viewMCP
	m.panelOffset = 0
}

// mcpAction runs one MCP action off the event loop.
//
// The work happens in a command, so a slow server never freezes the
// interface. The result comes back as a message.
func (m *Model) mcpAction(action, name string, extra []string) tea.Cmd {
	controller := m.mcp
	// Show the panel immediately so the result (including an error) lands
	// in a view that is listening for it.
	m.view = viewMCP
	m.panelOffset = 0
	run := func() mcpActionMsg {
		out := mcpActionMsg{action: action, name: name}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		switch action {
		case "connect":
			out.err = controller.Connect(ctx, name)
			if out.err == nil {
				out.tools, out.resources, out.prompts = mcpDiscover(ctx, controller, name)
			}
		case "disconnect":
			out.err = controller.Disconnect(ctx, name)
			out.tools = []mcp.ToolInfo{}
			out.resources = []mcp.ResourceInfo{}
			out.prompts = []mcp.PromptInfo{}
		case "tools":
			out.tools, out.resources, out.prompts = mcpDiscover(ctx, controller, name)
		case "read":
			var cut bool
			out.document, cut, out.err = controller.ReadResource(ctx, name, extra[0])
			out.truncated = cut
			out.title = "resource " + extra[0]
		case "prompt":
			out.document, out.err = controller.GetPrompt(ctx, name, extra[0], promptArgs(extra[1:]))
			out.title = "prompt " + extra[0]
		}
		return out
	}
	if m.sender == nil {
		// Headless: run the action now and hand the result back as a
		// message, exactly as the copy command does.
		msg := run()
		return func() tea.Msg { return msg }
	}
	return func() tea.Msg {
		go func() { m.sender(run()) }()
		return nil
	}
}

// mcpDiscover lists what one server offers. A server without a capability
// simply contributes an empty list; the error of a failed listing is kept
// so the operator sees why the panel is empty.
func mcpDiscover(ctx context.Context, controller MCPController, name string) ([]mcp.ToolInfo, []mcp.ResourceInfo, []mcp.PromptInfo) {
	tools, err := controller.ListTools(ctx, name)
	if err != nil {
		return nil, nil, nil
	}
	resources, rerr := controller.ListResources(ctx, name)
	if rerr != nil {
		resources = nil
	}
	prompts, perr := controller.ListPrompts(ctx, name)
	if perr != nil {
		prompts = nil
	}
	return tools, resources, prompts
}

// promptArgs parses the key=value arguments of /mcp prompt.
func promptArgs(args []string) map[string]string {
	out := map[string]string{}
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok || key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

// mcpRows renders the configured servers and what they offer.
func (m Model) mcpRows() []string {
	st := m.theme.styles()
	rows := []string{"MCP servers (Esc back)", ""}
	if m.mcp == nil {
		rows = append(rows, "  No MCP servers are configured.")
		rows = append(rows, "")
		rows = append(rows, st.footer.Render("  Add an mcp_servers entry to the configuration file, then restart HAL."))
		return rows
	}
	servers := m.mcp.Servers()
	if len(servers) == 0 {
		rows = append(rows, "  No MCP servers are configured.")
		rows = append(rows, "")
		rows = append(rows, st.footer.Render("  Add an mcp_servers entry to the configuration file, then restart HAL."))
		return rows
	}
	for _, server := range servers {
		rows = append(rows, "  "+padTo(st.active.Render(oneLine(server.Name)), 18)+st.footer.Render(oneLine(string(server.State))))
		facts := []string{"transport " + oneLine(server.Transport)}
		if server.URL != "" {
			facts = append(facts, "url "+oneLine(server.URL))
		}
		if server.Protocol != "" {
			facts = append(facts, "protocol "+oneLine(server.Protocol))
		}
		if server.ServerName != "" {
			facts = append(facts, "server "+oneLine(server.ServerName)+" "+oneLine(server.ServerVersion))
		}
		if len(server.Capabilities) > 0 {
			facts = append(facts, "capabilities "+oneLine(strings.Join(server.Capabilities, ", ")))
		}
		facts = append(facts, fmt.Sprintf("tools %d · resources %d · prompts %d",
			len(server.Tools), len(server.Resources), len(server.Prompts)))
		rows = append(rows, "      "+st.footer.Render(strings.Join(facts, " · ")))
		if len(server.AllowedTools) > 0 {
			rows = append(rows, "      "+st.footer.Render("allowed in 444: "+oneLine(strings.Join(server.AllowedTools, ", "))))
		} else {
			rows = append(rows, "      "+st.footer.Render("allowed in 444: none (every tool needs 666)"))
		}
		if server.Error != "" {
			rows = append(rows, "      "+st.notice.Render("error: "+oneLine(server.Error)))
		}
		rows = append(rows, "")
	}
	if m.mcpFocus != "" {
		rows = append(rows, "  "+st.active.Render("tools of "+oneLine(m.mcpFocus)))
		if len(m.mcpTools) == 0 {
			rows = append(rows, "    (no tools, or the server is not connected)")
		}
		for _, tool := range m.mcpTools {
			policy := "needs 666"
			if tool.AllowedIn444 {
				policy = "allowed in 444"
			}
			rows = append(rows, "    "+padTo(st.footer.Render(oneLine(tool.Exposed)), 32)+oneLine(tool.Name))
			rows = append(rows, "        "+st.footer.Render(policy))
			if tool.Description != "" {
				rows = append(rows, "        "+st.footer.Render(truncate(oneLine(tool.Description), 72)))
			}
		}
		if len(m.mcpResources) > 0 {
			rows = append(rows, "")
			rows = append(rows, "  "+st.active.Render("resources of "+oneLine(m.mcpFocus)))
			for _, resource := range m.mcpResources {
				rows = append(rows, "    "+padTo(st.footer.Render(oneLine(resource.URI)), 40)+oneLine(resource.Name))
			}
		}
		if len(m.mcpPrompts) > 0 {
			rows = append(rows, "")
			rows = append(rows, "  "+st.active.Render("prompts of "+oneLine(m.mcpFocus)))
			for _, prompt := range m.mcpPrompts {
				rows = append(rows, "    "+padTo(st.footer.Render(oneLine(prompt.Name)), 40)+oneLine(prompt.Description))
			}
		}
	}
	if m.mcpDocument != "" {
		rows = append(rows, "")
		rows = append(rows, "  "+st.active.Render(oneLine(m.mcpDocumentTitle)+" (untrusted document)"))
		lines := strings.Split(sanitizeMarkdown(m.mcpDocument), "\n")
		const maxDocumentLines = 200
		for i, line := range lines {
			if i >= maxDocumentLines {
				rows = append(rows, st.notice.Render(fmt.Sprintf("    … document cut after %d lines", maxDocumentLines)))
				break
			}
			rows = append(rows, "    "+line)
		}
		if m.mcpDocumentCut {
			rows = append(rows, st.notice.Render("    … the server document was longer than HAL returns"))
		}
	}
	rows = append(rows, "")
	rows = append(rows, st.footer.Render("  /mcp connect <server> · /mcp disconnect <server> · /mcp tools <server>"))
	rows = append(rows, st.footer.Render("  /mcp read <server> <uri> · /mcp prompt <server> <name> [key=value ...]"))
	rows = append(rows, st.footer.Render("  Server output is untrusted data and never widens your authority."))
	return rows
}

// oneLine makes untrusted text safe for a single panel row: terminal
// control characters are removed and a line break cannot break the frame.
func oneLine(text string) string {
	text = sanitizeMarkdown(text)
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\t", " ")
	return text
}
