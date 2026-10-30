package agent

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func newDefaultProfileLoop(t *testing.T, profile bool) *AgentLoop {
	t.Helper()
	if profile {
		t.Setenv("DEEPCLAW_CODING_PROFILE", "1")
	} else {
		t.Setenv("DEEPCLAW_CODING_PROFILE", "0")
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "test-model"
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), &recordingProvider{})
	t.Cleanup(loop.Close)
	return loop
}

func measureToolSchemas(defs []providers.ToolDefinition) (int, map[string]int) {
	total := 0
	individual := make(map[string]int, len(defs))
	for _, def := range defs {
		raw, _ := json.Marshal(def)
		total += len(raw)
		individual[def.Function.Name] = len(raw)
	}
	return total, individual
}

func TestCodingProfileSchemaMeasurement(t *testing.T) {
	defaultLoop := newDefaultProfileLoop(t, false)
	profileLoop := newDefaultProfileLoop(t, true)

	defaultDefs := defaultLoop.registry.GetDefaultAgent().Tools.ToProviderDefs()
	profileDefs := profileLoop.registry.GetDefaultAgent().Tools.ToProviderDefs()

	defaultBytes, defaultIndividual := measureToolSchemas(defaultDefs)
	profileBytes, profileIndividual := measureToolSchemas(profileDefs)

	t.Logf("default profile: tools=%d serialized_bytes=%d token_estimate=%d",
		len(defaultDefs), defaultBytes, EstimateToolDefsTokens(defaultDefs))
	defaultNames := make([]string, 0, len(defaultIndividual))
	for name := range defaultIndividual {
		defaultNames = append(defaultNames, name)
	}
	sort.Strings(defaultNames)
	for _, name := range defaultNames {
		t.Logf("  %-20s %d bytes", name, defaultIndividual[name])
	}

	t.Logf("coding profile: tools=%d serialized_bytes=%d token_estimate=%d",
		len(profileDefs), profileBytes, EstimateToolDefsTokens(profileDefs))
	profileNames := make([]string, 0, len(profileIndividual))
	for name := range profileIndividual {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)
	for _, name := range profileNames {
		t.Logf("  %-20s %d bytes", name, profileIndividual[name])
	}

	if len(profileDefs) != len(tools.CodingProfileToolNames) {
		t.Fatalf("coding profile exposes %d tools, want %d", len(profileDefs), len(tools.CodingProfileToolNames))
	}
	if profileBytes >= defaultBytes {
		t.Fatalf("coding profile schema (%d bytes) is not smaller than default (%d bytes)",
			profileBytes, defaultBytes)
	}
	if EstimateToolDefsTokens(profileDefs) >= EstimateToolDefsTokens(defaultDefs) {
		t.Fatal("coding profile token estimate is not smaller than default")
	}
	for _, name := range []string{"read", "write", "edit", "bash"} {
		if _, ok := profileIndividual[name]; !ok {
			t.Fatalf("coding profile missing tool %q: %v", name, profileNames)
		}
	}
}
