package config

import (
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv(EnvProvider, "")
	t.Setenv(EnvEndpoint, "")
	t.Setenv(EnvModel, "")
	t.Setenv(EnvAPIKey, "k")
	cfg := Load(nil)
	if cfg.Configured() {
		t.Error("partial config must not count as configured")
	}
	if !strings.Contains(cfg.StoreDir, "sessions") {
		t.Errorf("store dir = %q, want a sessions directory", cfg.StoreDir)
	}
}

func TestLoadFull(t *testing.T) {
	t.Setenv(EnvAPIKey, "secret")
	cfg := Load([]string{
		"-provider", "openai-compatible",
		"-endpoint", "https://api.example.com/v1",
		"-model", "hal-flash",
		"-session", "work",
	})
	if !cfg.Configured() {
		t.Errorf("complete config must count as configured: %s", cfg.Redacted())
	}
	if cfg.Session != "work" {
		t.Errorf("session = %q, want work", cfg.Session)
	}
}

func TestRedactedHidesKey(t *testing.T) {
	cfg := Config{Provider: "p", Endpoint: "e", Model: "m", APIKey: "shh"}
	out := cfg.Redacted()
	if strings.Contains(out, "shh") {
		t.Errorf("redacted config must not contain the key: %q", out)
	}
	if !strings.Contains(out, "api_key=set") {
		t.Errorf("redacted config must confirm the key presence: %q", out)
	}
}
