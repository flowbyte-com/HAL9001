package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func startReply(t *testing.T, persona string) string {
	t.Helper()
	cfg := &config.Config{}
	cfg.Agents.Defaults.Persona = persona
	rt := &Runtime{Config: cfg}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	var reply string
	res := ex.Execute(context.Background(), Request{
		Text: "/start",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})
	if res.Outcome != OutcomeHandled {
		t.Fatalf("outcome=%v, want=%v", res.Outcome, OutcomeHandled)
	}
	return reply
}

// The HAL persona greets without the PicoClaw branding; every other
// configuration keeps the original greeting.
func TestStartGreeting_BrandAware(t *testing.T) {
	hal := startReply(t, "hal9001")
	if !strings.Contains(hal, "HAL9001") {
		t.Errorf("HAL greeting = %q, want it to name HAL9001", hal)
	}
	if strings.Contains(hal, "🦞") || strings.Contains(hal, "PicoClaw") {
		t.Errorf("HAL greeting must not carry PicoClaw branding: %q", hal)
	}

	defaultReply := startReply(t, "")
	if defaultReply != "Hello! I am PicoClaw 🦞" {
		t.Errorf("default greeting = %q, want the original text", defaultReply)
	}

	other := startReply(t, "picoclaw")
	if other != "Hello! I am PicoClaw 🦞" {
		t.Errorf("picoclaw greeting = %q, want the original text", other)
	}
}
