package cliui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
)

// Brand is the terminal identity used for startup output and conversation
// labels. The default brand is PicoClaw; the HAL9001 brand is selected when
// the hal9001 persona is active.
type Brand struct {
	// Name is the product/persona name shown in headings.
	Name string
	// Logo is the brand symbol used in terminal output. The HAL9001 brand
	// has none: the lobster emoji never appears in its output.
	Logo string
	// UserLabel prefixes the user input prompt.
	UserLabel string
	// AgentLabel prefixes assistant output.
	AgentLabel string
	// Compact marks a brand that renders a compact heading instead of the
	// large ASCII banner.
	Compact bool
}

// PicoClawBrand is the default branding: the lobster logo and the ASCII
// banner.
func PicoClawBrand() Brand {
	return Brand{
		Name:       "PicoClaw",
		Logo:       "🦞",
		UserLabel:  "🦞 You: ",
		AgentLabel: "🦞 ",
		Compact:    false,
	}
}

// Hal9001Brand is the HAL9001 branding: a compact heading with a small red
// eye and plain conversation labels.
func Hal9001Brand() Brand {
	return Brand{
		Name:       "HAL9001",
		Logo:       "",
		UserLabel:  "You > ",
		AgentLabel: "HAL > ",
		Compact:    true,
	}
}

// BrandForPersona returns the brand for a persona value. Only the hal9001
// persona changes the branding; everything else keeps PicoClaw.
func BrandForPersona(persona string) Brand {
	if config.IsHal9001Persona(persona) {
		return Hal9001Brand()
	}
	return PicoClawBrand()
}

var (
	brandMu sync.RWMutex
	brand   = PicoClawBrand()
)

// SetBrand installs the active brand for this process.
func SetBrand(b Brand) {
	brandMu.Lock()
	defer brandMu.Unlock()
	brand = b
}

// CurrentBrand returns the active brand.
func CurrentBrand() Brand {
	brandMu.RLock()
	defer brandMu.RUnlock()
	return brand
}

// DetectBrand resolves the brand from the environment and the config file.
// It is called before any startup output is rendered so the very first line
// already matches the persona that will run.
//
// Resolution order:
//  1. the persona environment variable (authoritative, overrides the file);
//  2. agents.defaults.persona from the config file.
//
// The config file is read with a minimal probe instead of the full config
// loader so detection stays silent and cheap: a missing or malformed file
// simply yields the default brand.
func DetectBrand() Brand {
	probe := &config.Config{}
	probe.Agents.Defaults.Persona = personaFromConfigFile()
	return BrandForPersona(config.ResolvePersona(probe, os.Getenv(config.EnvPersona)))
}

// personaFromConfigFile reads only agents.defaults.persona. Errors are
// ignored: branding falls back to the default persona.
func personaFromConfigFile() string {
	data, err := os.ReadFile(configFilePath())
	if err != nil {
		return ""
	}
	var probe struct {
		Agents struct {
			Defaults struct {
				Persona string `json:"persona"`
			} `json:"defaults"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return ""
	}
	return probe.Agents.Defaults.Persona
}

// configFilePath mirrors the runtime config path resolution.
func configFilePath() string {
	if path := strings.TrimSpace(os.Getenv(config.EnvConfig)); path != "" {
		return path
	}
	return filepath.Join(config.GetHome(), "config.json")
}
