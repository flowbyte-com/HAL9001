package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestAgentCodingProfileExposesOnlyCoreTools(t *testing.T) {
	t.Setenv("DEEPCLAW_CODING_PROFILE", "1")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "test-model"
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &recordingProvider{})
	ag := al.registry.GetDefaultAgent()
	got := ag.Tools.List()
	want := map[string]bool{"read": true, "write": true, "edit": true, "bash": true}
	if len(got) != len(want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("unexpected tool %q in %v", name, got)
		}
	}
	for name := range want {
		if !ag.Tools.HasRegistered(name) {
			t.Fatalf("missing tool %q in %v", name, got)
		}
	}
}
