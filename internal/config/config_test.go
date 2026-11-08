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

// TestSaveFileIsAtomic checks that a write never leaves a fragment
// behind: the configuration is replaced by a rename, so a reader sees the
// old file or the new one, never half of either.
func TestSaveFileIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	first := FileConfig{Provider: "openai-compatible", Endpoint: "https://one.example/v1"}
	if err := SaveFile(path, first); err != nil {
		t.Fatal(err)
	}
	second := FileConfig{Provider: "openai-compatible", Endpoint: "https://two.example/v1"}
	if err := SaveFile(path, second); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Endpoint != second.Endpoint {
		t.Errorf("endpoint = %q, want %q", loaded.Endpoint, second.Endpoint)
	}
	// The temporary file is gone: only the configuration is left.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only config.json", names)
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

func TestSafeguardDefaultsAndEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_HOME", dir)
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("HAL9001_MAX_TOKENS", "")
	t.Setenv("HAL9001_STREAM_IDLE_TIMEOUT", "")
	t.Setenv("HAL9001_TURN_TIMEOUT", "")

	cfg, err := LoadErr(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxTokens != 0 || cfg.StreamIdleSeconds != 0 || cfg.TurnTimeoutSeconds != 0 {
		t.Errorf("zero must mean 'use the built-in default': %+v", cfg)
	}

	t.Setenv("HAL9001_MAX_TOKENS", "4096")
	t.Setenv("HAL9001_STREAM_IDLE_TIMEOUT", "90")
	t.Setenv("HAL9001_TURN_TIMEOUT", "600")
	cfg, err = LoadErr(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxTokens != 4096 || cfg.StreamIdleSeconds != 90 || cfg.TurnTimeoutSeconds != 600 {
		t.Errorf("environment not applied: %+v", cfg)
	}

	// A malformed value must not silently disable a safeguard.
	t.Setenv("HAL9001_MAX_TOKENS", "lots")
	cfg, err = LoadErr(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxTokens != 0 {
		t.Errorf("malformed value accepted: %d", cfg.MaxTokens)
	}
}

func TestSafeguardFlagsWin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_HOME", dir)
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("HAL9001_MAX_TOKENS", "4096")
	// Every value differs from the built-in default, so the assertion
	// proves precedence rather than accidental agreement.
	cfg, err := LoadErr([]string{"--max-tokens", "2048", "--stream-idle-timeout", "30", "--turn-timeout", "300"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxTokens != 2048 || cfg.StreamIdleSeconds != 30 || cfg.TurnTimeoutSeconds != 300 {
		t.Errorf("flags not applied: %+v", cfg)
	}
}

func TestSafeguardsRoundTripInFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	fc := FileConfig{MaxTokens: 8192, StreamIdleSeconds: 45, TurnTimeoutSeconds: 120}
	if err := SaveFile(path, fc); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MaxTokens != 8192 || loaded.StreamIdleSeconds != 45 || loaded.TurnTimeoutSeconds != 120 {
		t.Errorf("file round-trip lost the safeguards: %+v", loaded)
	}
}

func TestMouseDefaultsOnWithOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_HOME", dir)
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "none.json"))
	t.Setenv("HAL9001_MOUSE", "")
	cfg, err := LoadErr(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Mouse {
		t.Error("mouse must default to on")
	}
	for _, v := range []string{"1", "true", "yes", "on", "TRUE"} {
		t.Setenv("HAL9001_MOUSE", v)
		cfg, err = LoadErr(nil)
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Mouse {
			t.Errorf("HAL9001_MOUSE=%q must enable mouse mode", v)
		}
	}
	for _, v := range []string{"0", "false", "no", "off", "OFF"} {
		t.Setenv("HAL9001_MOUSE", v)
		cfg, err = LoadErr(nil)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Mouse {
			t.Errorf("HAL9001_MOUSE=%q must disable mouse mode", v)
		}
	}
	// A malformed value is refused: a typo must not quietly change
	// the terminal's behaviour.
	t.Setenv("HAL9001_MOUSE", "maybe")
	_, err = LoadErr(nil)
	if err == nil {
		t.Fatal("a malformed HAL9001_MOUSE must be an error")
	}
	if !strings.Contains(err.Error(), "HAL9001_MOUSE") {
		t.Errorf("the error must name the variable: %v", err)
	}
	if !strings.Contains(err.Error(), "maybe") {
		t.Errorf("the error must show the value: %v", err)
	}
	t.Setenv("HAL9001_MOUSE", "")
	cfg, err = LoadErr([]string{"--mouse"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Mouse {
		t.Error("--mouse must enable mouse mode")
	}
	cfg, err = LoadErr([]string{"--no-mouse"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mouse {
		t.Error("--no-mouse must disable mouse mode")
	}
	// The flag's value is honoured, so the flag package's own
	// "-flag=false" form works too.
	cfg, err = LoadErr([]string{"--mouse=false"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mouse {
		t.Error("--mouse=false must disable mouse mode")
	}
	cfg, err = LoadErr([]string{"--no-mouse=false"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Mouse {
		t.Error("--no-mouse=false must enable mouse mode")
	}
}

// TestMouseFlagBeatsEnvironment checks the precedence: an explicit flag
// wins over the environment, which wins over the default.
func TestMouseFlagBeatsEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_HOME", dir)
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "none.json"))

	t.Setenv("HAL9001_MOUSE", "1")
	cfg, err := LoadErr([]string{"--no-mouse"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mouse {
		t.Error("--no-mouse must win over HAL9001_MOUSE=1")
	}
	t.Setenv("HAL9001_MOUSE", "0")
	cfg, err = LoadErr([]string{"--mouse"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Mouse {
		t.Error("--mouse must win over HAL9001_MOUSE=0")
	}
	t.Setenv("HAL9001_MOUSE", "0")
	cfg, err = LoadErr(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mouse {
		t.Error("HAL9001_MOUSE=0 must win over the default")
	}
}

// TestMouseFlagBeatsMalformedEnvironment checks that a valid flag is not
// blocked by a malformed environment value: the flag decides, so the
// malformed value is irrelevant.
func TestMouseFlagBeatsMalformedEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_HOME", dir)
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "none.json"))
	t.Setenv("HAL9001_MOUSE", "maybe")

	cfg, err := LoadErr([]string{"--mouse"})
	if err != nil {
		t.Fatalf("a valid flag must not be blocked: %v", err)
	}
	if !cfg.Mouse {
		t.Error("--mouse must enable mouse mode")
	}
	cfg, err = LoadErr([]string{"--no-mouse"})
	if err != nil {
		t.Fatalf("a valid flag must not be blocked: %v", err)
	}
	if cfg.Mouse {
		t.Error("--no-mouse must disable mouse mode")
	}
}

// TestMouseErrorHoldsNoSecrets checks that the diagnostic for a
// malformed value names the one variable that is wrong and nothing else
// from the environment.
func TestMouseErrorHoldsNoSecrets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_HOME", dir)
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "none.json"))
	t.Setenv("HAL9001_API_KEY", "super-secret-key")
	t.Setenv("HAL9001_MOUSE", "maybe")

	_, err := LoadErr(nil)
	if err == nil {
		t.Fatal("a malformed HAL9001_MOUSE must be an error")
	}
	msg := err.Error()
	if strings.Contains(msg, "super-secret-key") {
		t.Errorf("the error leaks the API key: %v", err)
	}
	if strings.Contains(msg, "HAL9001_API_KEY") {
		t.Errorf("the error names an unrelated variable: %v", err)
	}
	if !strings.Contains(msg, "HAL9001_MOUSE") {
		t.Errorf("the error must name the variable: %v", err)
	}
}

// TestMouseFlagsConflict checks that asking for both is refused instead
// of silently choosing one.
func TestMouseFlagsConflict(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_HOME", dir)
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "none.json"))
	t.Setenv("HAL9001_MOUSE", "")
	if _, err := LoadErr([]string{"--mouse", "--no-mouse"}); err == nil {
		t.Fatal("--mouse with --no-mouse must be an error")
	}
	if _, err := LoadErr([]string{"--no-mouse", "--mouse"}); err == nil {
		t.Fatal("--no-mouse with --mouse must be an error")
	}
}

// TestMCPServersLoadFromFile checks the operator-controlled MCP server
// record: the list is read from the same configuration file as everything
// else, and an absent list means no server at all.
func TestMCPServersLoadFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	isolateConfig(t)
	t.Setenv("HAL9001_CONFIG", path)
	t.Setenv(EnvAPIKey, "k")
	if err := SaveFile(path, FileConfig{
		Provider: "openai-compatible",
		Endpoint: "https://api.example.com/v1",
		Models:   []ModelRecord{{ID: "m"}},
		MCPServers: []MCPServerRecord{
			{
				Name:         "local",
				Command:      "mock-mcp",
				Args:         []string{"--stdio"},
				Env:          map[string]string{"MOCK_TOKEN": "${MOCK_TOKEN}"},
				AllowedTools: []string{"read"},
			},
			{
				Name:           "remote",
				Transport:      "http",
				URL:            "https://mcp.example.com/mcp",
				Headers:        map[string]string{"X-Api-Key": "${MOCK_KEY}"},
				TokenEnv:       "MOCK_TOKEN",
				TimeoutSeconds: 12,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := Load(nil)
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("servers = %#v", cfg.MCPServers)
	}
	if cfg.MCPServers[0].Name != "local" || cfg.MCPServers[0].Command != "mock-mcp" || len(cfg.MCPServers[0].AllowedTools) != 1 {
		t.Errorf("local = %#v", cfg.MCPServers[0])
	}
	if cfg.MCPServers[1].Transport != "http" || cfg.MCPServers[1].TimeoutSeconds != 12 || cfg.MCPServers[1].TokenEnv != "MOCK_TOKEN" {
		t.Errorf("remote = %#v", cfg.MCPServers[1])
	}
}

func TestMCPServersDefaultToEmpty(t *testing.T) {
	isolateConfig(t)
	t.Setenv(EnvAPIKey, "k")
	cfg := Load(nil)
	if len(cfg.MCPServers) != 0 {
		t.Fatalf("unconfigured servers = %#v", cfg.MCPServers)
	}
}
