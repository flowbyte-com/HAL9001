package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HAL9001_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv(EnvProvider, "")
	t.Setenv(EnvEndpoint, "")
	t.Setenv(EnvModel, "")
	t.Setenv(EnvAPIKey, "")
}

func TestLoadDefaults(t *testing.T) {
	isolateConfig(t)
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
	isolateConfig(t)
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

func TestFileConfigRoundTripWithStrictPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	fc := FileConfig{
		Provider:    "openai-compatible",
		Endpoint:    "https://api.example.com/v1",
		APIKey:      "secret-key",
		Models:      []ModelRecord{{ID: "m1"}, {ID: "m2"}},
		ActiveModel: "m2",
	}
	if err := SaveFile(path, fc); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config perms = %o, want 600", info.Mode().Perm())
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKey != "secret-key" || loaded.Endpoint != fc.Endpoint {
		t.Errorf("round trip lost data: %+v", loaded)
	}
	active, ok := loaded.Active()
	if !ok || active.ID != "m2" {
		t.Errorf("active = %+v, %v", active, ok)
	}
}

func TestActiveDefaultsToFirst(t *testing.T) {
	fc := FileConfig{Models: []ModelRecord{{ID: "a"}, {ID: "b"}}}
	active, ok := fc.Active()
	if !ok || active.ID != "a" {
		t.Errorf("active = %+v, %v", active, ok)
	}
	if _, ok := (FileConfig{}).Active(); ok {
		t.Error("empty model list must not yield an active model")
	}
}

func TestAddModelDedupes(t *testing.T) {
	var fc FileConfig
	fc.AddModel("m1")
	fc.AddModel(" m1 ")
	fc.AddModel("")
	if len(fc.Models) != 1 {
		t.Errorf("models = %+v, want one entry", fc.Models)
	}
}

func TestEnvWinsOverFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("HAL9001_CONFIG", path)
	t.Setenv(EnvProvider, "")
	t.Setenv(EnvEndpoint, "")
	t.Setenv(EnvModel, "env-model")
	t.Setenv(EnvAPIKey, "env-key")
	if err := SaveFile(path, FileConfig{
		Endpoint:    "https://file.example.com",
		APIKey:      "file-key",
		Models:      []ModelRecord{{ID: "file-model"}},
		ActiveModel: "file-model",
	}); err != nil {
		t.Fatal(err)
	}
	cfg := Load(nil)
	if cfg.Model != "env-model" || cfg.APIKey != "env-key" {
		t.Errorf("env must win: %+v", cfg.Redacted())
	}
	if cfg.Endpoint != "https://file.example.com" {
		t.Errorf("unset env must fall back to file: %q", cfg.Endpoint)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ID != "file-model" {
		t.Errorf("models must come from the file: %+v", cfg.Models)
	}
}

func TestListSessions(t *testing.T) {
	dir := t.TempDir()
	names, err := ListSessions(filepath.Join(dir, "missing"))
	if err != nil || len(names) != 0 {
		t.Errorf("missing dir = %v, %v", names, err)
	}
	for _, name := range []string{"a.jsonl", "b.jsonl", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names, err = ListSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Errorf("sessions = %v", names)
	}
}

func TestThemeRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	fc := FileConfig{Theme: "Amber"}
	if err := SaveFile(path, fc); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Theme != "Amber" {
		t.Errorf("theme = %q, want Amber", loaded.Theme)
	}
}
