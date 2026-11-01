package agent

import (
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// codingProfileEnabled reports whether the minimal coding profile is requested.
func codingProfileEnabled() bool {
	return os.Getenv("DEEPCLAW_CODING_PROFILE") == "1"
}

// codingProfileRuntimeName maps a provider-visible profile name to the
// underlying tool that must be registered for it.
var codingProfileRuntimeName = map[string]string{
	"read":  "read_file",
	"write": "write_file",
	"edit":  "edit_file",
	"bash":  "exec",
}

// expandCodingProfileAllowlist accepts profile names in an agent allowlist while
// preserving the stricter original-name semantics. A nil allowlist stays nil;
// an empty-but-non-nil allowlist stays empty.
func expandCodingProfileAllowlist(names []string) []string {
	if names == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(names)*2)
	out := make([]string, 0, len(names)*2)
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, name := range names {
		add(name)
		if runtimeName, ok := codingProfileRuntimeName[strings.ToLower(strings.TrimSpace(name))]; ok {
			add(runtimeName)
		}
	}
	return out
}

// applyCodingProfile installs the four-tool coding contract and an explicit
// provider-visible profile on the agent's existing registry. It preserves the
// registry's allowlist and every tool already installed: originals become
// hidden, late MCP/plugin registrations stay hidden, and only tools derived
// from an allowed original become visible.
func applyCodingProfile(agent *AgentInstance) []string {
	if agent == nil || agent.Tools == nil {
		return nil
	}
	registry := agent.Tools
	if registry.HasVisibilityProfile() {
		return registry.VisibleToolNames()
	}

	_, editAvailable := registry.Get("edit_file")
	visible := make([]string, 0, len(tools.CodingProfileToolNames))

	if inner, ok := registry.Get("read_file"); ok {
		registry.RegisterProfileTool(tools.NewCodingReadTool(inner))
		visible = append(visible, "read")
	}
	if inner, ok := registry.Get("write_file"); ok {
		registry.RegisterProfileTool(tools.NewCodingWriteTool(inner, editAvailable))
		visible = append(visible, "write")
	}
	if inner, ok := registry.Get("edit_file"); ok {
		registry.RegisterProfileTool(tools.NewCodingEditTool(inner))
		visible = append(visible, "edit")
	}
	if inner, ok := registry.Get("exec"); ok {
		registry.RegisterProfileTool(tools.NewCodingBashTool(inner))
		visible = append(visible, "bash")
	}

	// Setting the profile last means every adapter was derived from a tool that
	// already passed the agent allowlist. If a tool was denied or removed (for
	// example `exec` after a failed isolation preflight), its alias is absent.
	registry.SetVisibleTools(visible)
	return visible
}

// enableHal9001Profile installs the chat-first visible tool set on every
// HAL9001 agent in the registry. Tools are already registered (including
// the workshop tools from NewAgentInstance); this only narrows what the
// provider sees. Agents without the persona are untouched, and coding
// profile takes precedence (callers check that first).
func enableHal9001Profile(registry *AgentRegistry) {
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil || !agent.Hal9001 || agent.Tools == nil {
			continue
		}
		if agent.Tools.HasVisibilityProfile() {
			continue
		}
		visible := Hal9001DefaultVisibleTools()
		agent.Tools.SetVisibleTools(visible)
		logger.InfoCF("agent", "HAL9001 chat-first tool profile enabled", map[string]any{
			"agent_id": agentID,
			"tools":    agent.Tools.VisibleToolNames(),
		})
	}
}

func enableCodingProfile(registry *AgentRegistry) {
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil || agent.Tools == nil {
			continue
		}
		visible := applyCodingProfile(agent)
		logger.InfoCF("agent", "minimal coding profile enabled", map[string]any{
			"agent_id": agentID,
			"tools":    visible,
		})
	}
}
