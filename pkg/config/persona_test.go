package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsHal9001Persona(t *testing.T) {
	for _, persona := range []string{"hal9001", "HAL9001", " hal9001 ", "HaL9001"} {
		if !IsHal9001Persona(persona) {
			t.Errorf("IsHal9001Persona(%q) = false, want true", persona)
		}
	}
	for _, persona := range []string{"", "picoclaw", "hal", "hal9002", "hal 9001"} {
		if IsHal9001Persona(persona) {
			t.Errorf("IsHal9001Persona(%q) = true, want false", persona)
		}
	}
}

func TestResolvePersona(t *testing.T) {
	cfg := &Config{}
	cfg.Agents.Defaults.Persona = "picoclaw"
	if got := ResolvePersona(cfg, ""); got != "picoclaw" {
		t.Errorf("ResolvePersona with empty env = %q, want the config value", got)
	}
	if got := ResolvePersona(cfg, "hal9001"); got != "hal9001" {
		t.Errorf("ResolvePersona must prefer the env value, got %q", got)
	}
	if got := ResolvePersona(nil, ""); got != "" {
		t.Errorf("ResolvePersona(nil, \"\") = %q, want empty", got)
	}
}

// The persona environment variable must select the persona both when a
// config file exists and when it does not, so the CLI branding and the
// running agent always agree.
func TestLoadConfigPersonaEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvPersona, "hal9001")

	withFile := filepath.Join(dir, "config.json")
	if err := os.WriteFile(withFile, []byte(`{"version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(withFile)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Agents.Defaults.Persona != "hal9001" {
		t.Errorf("persona = %q, want the env value", cfg.Agents.Defaults.Persona)
	}

	missing := filepath.Join(dir, "does-not-exist.json")
	cfg, err = LoadConfig(missing)
	if err != nil {
		t.Fatalf("LoadConfig (missing file): %v", err)
	}
	if cfg.Agents.Defaults.Persona != "hal9001" {
		t.Errorf("persona without a config file = %q, want the env value", cfg.Agents.Defaults.Persona)
	}
}

func TestLoadConfigPersonaFromFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvPersona, "")
	path := filepath.Join(dir, "config.json")
	content := `{"version":3,"agents":{"defaults":{"persona":"hal9001"}}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Agents.Defaults.Persona != "hal9001" {
		t.Errorf("persona = %q, want hal9001 from the config file", cfg.Agents.Defaults.Persona)
	}
}
