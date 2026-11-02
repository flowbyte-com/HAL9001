package config

import "strings"

// PersonaHal9001 selects the HAL9001 conversational persona: a chat-first
// thinking partner that investigates, architects, coordinates, reviews and
// briefs — and never implements.
const PersonaHal9001 = "hal9001"

// PersonaPicoClaw is the default coding-assistant persona.
const PersonaPicoClaw = "picoclaw"

// IsHal9001Persona reports whether a persona value selects HAL9001.
// Matching is case- and whitespace-insensitive; any other value (including
// the empty string) keeps the default persona. This is the single source of
// truth shared by the runtime, the CLI branding layer, and command
// greetings.
func IsHal9001Persona(persona string) bool {
	return strings.EqualFold(strings.TrimSpace(persona), PersonaHal9001)
}

// ResolvePersona returns the effective persona for a configuration, with the
// environment variable taking precedence over the config file. It mirrors
// the runtime resolution so presentation layers cannot disagree with the
// agent that actually runs.
func ResolvePersona(cfg *Config, envValue string) string {
	if trimmed := strings.TrimSpace(envValue); trimmed != "" {
		return trimmed
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Agents.Defaults.Persona)
}
