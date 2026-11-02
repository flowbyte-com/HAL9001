package commands

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/config"
)

// startGreeting returns the greeting for the active persona. The HAL9001
// persona greets without the PicoClaw branding; every other configuration
// keeps the original greeting.
func startGreeting(rt *Runtime) string {
	if rt != nil && rt.Config != nil &&
		config.IsHal9001Persona(rt.Config.Agents.Defaults.Persona) {
		return "Hello! I am HAL9001."
	}
	return "Hello! I am PicoClaw 🦞"
}

func startCommand() Definition {
	return Definition{
		Name:        "start",
		Description: "Start the bot",
		Usage:       "/start",
		ReadOnly:    true,
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			return req.Reply(startGreeting(rt))
		},
	}
}
