// Package config loads HAL9001's runtime configuration from flags, the
// environment, and the user configuration file. Onboarding is three
// values: an API key, a provider endpoint and a model identifier. Secrets
// live in a 0600 user config file or the environment (which wins and is
// never written back); they never appear in logs or the repository, and
// HAL9001 never reads PicoClaw, OpenCode or MPM credentials.
package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Environment variables for onboarding.
const (
	EnvProvider = "HAL9001_PROVIDER"
	EnvEndpoint = "HAL9001_ENDPOINT"
	EnvModel    = "HAL9001_MODEL"
	EnvAPIKey   = "HAL9001_API_KEY"
	EnvHome     = "HAL9001_HOME"
	// EnvMaxTokens caps one response. Zero means the default.
	EnvMaxTokens = "HAL9001_MAX_TOKENS"
	// EnvStreamIdleTimeout aborts a stream that sends nothing for this
	// many seconds.
	EnvStreamIdleTimeout = "HAL9001_STREAM_IDLE_TIMEOUT"
	// EnvTurnTimeout bounds one whole turn, in seconds.
	EnvTurnTimeout = "HAL9001_TURN_TIMEOUT"
)

// ModelRecord is one configured model. The record is intentionally small
// and extensible: future milestones may add per-model thinking levels or
// endpoint overrides without changing the shape.
type ModelRecord struct {
	// ID is the provider-side model identifier.
	ID string `json:"id"`
	// Label is the display name. Empty means ID.
	Label string `json:"label,omitempty"`
}

// Display returns the label or the id.
func (m ModelRecord) Display() string {
	if m.Label != "" {
		return m.Label
	}
	return m.ID
}

// FileConfig is the persisted user configuration.
type FileConfig struct {
	Provider    string        `json:"provider"`
	Endpoint    string        `json:"endpoint"`
	APIKey      string        `json:"api_key,omitempty"`
	Models      []ModelRecord `json:"models,omitempty"`
	ActiveModel string        `json:"active_model,omitempty"`
	// Theme names the active TUI preset (empty means HAL Classic).
	Theme string `json:"theme,omitempty"`
	// MaxTokens caps one response (0 uses the built-in default).
	MaxTokens int `json:"max_tokens,omitempty"`
	// StreamIdleSeconds closes a stream that sends nothing for this long.
	StreamIdleSeconds int `json:"stream_idle_timeout_seconds,omitempty"`
	// TurnTimeoutSeconds bounds one whole turn.
	TurnTimeoutSeconds int `json:"turn_timeout_seconds,omitempty"`
}

// Active returns the active model record (first model when unset).
func (f FileConfig) Active() (ModelRecord, bool) {
	if len(f.Models) == 0 {
		return ModelRecord{}, false
	}
	if f.ActiveModel == "" {
		return f.Models[0], true
	}
	for _, m := range f.Models {
		if m.ID == f.ActiveModel {
			return m, true
		}
	}
	return ModelRecord{}, false
}

// AddModel appends a model id when absent.
func (f *FileConfig) AddModel(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	for _, m := range f.Models {
		if m.ID == id {
			return
		}
	}
	f.Models = append(f.Models, ModelRecord{ID: id})
}

// ConfigDir returns HAL9001's own user configuration directory. Nothing
// outside it is ever read.
func ConfigDir() string {
	if home := strings.TrimSpace(os.Getenv(EnvHome)); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".hal9001")
	}
	return filepath.Join(home, ".hal9001")
}

// ConfigPath is the user configuration file.
func ConfigPath() string {
	if path := strings.TrimSpace(os.Getenv("HAL9001_CONFIG")); path != "" {
		return path
	}
	return filepath.Join(ConfigDir(), "config.json")
}

// LoadFile reads the user configuration. A missing file is not an error.
func LoadFile(path string) (FileConfig, error) {
	var fc FileConfig
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fc, nil
	}
	if err != nil {
		return fc, fmt.Errorf("config: %w", err)
	}
	if err := json.Unmarshal(data, &fc); err != nil {
		return fc, fmt.Errorf("config: %w", err)
	}
	return fc, nil
}

// SaveFile writes the user configuration with restrictive permissions
// (0600 file, 0700 directory). Secrets live here or in the environment.
func SaveFile(path string, fc FileConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// ListSessions returns stored session names for the resume flow.
func ListSessions(storeDir string) ([]string, error) {
	entries, err := os.ReadDir(storeDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".jsonl") {
			out = append(out, strings.TrimSuffix(name, ".jsonl"))
		}
	}
	return out, nil
}

// Config is the resolved runtime configuration.
type Config struct {
	Provider string
	Endpoint string
	Model    string
	APIKey   string
	Session  string
	StoreDir string
	NoColor  bool
	ShowHelp bool
	List     bool
	Models   []ModelRecord
	// MaxTokens caps one response. Zero means the provider default is
	// replaced by the built-in sensible cap.
	MaxTokens int
	// StreamIdleSeconds aborts a stream that sends nothing for this long.
	// Zero means the default.
	StreamIdleSeconds int
	// TurnTimeoutSeconds bounds one whole turn. Zero means the default.
	TurnTimeoutSeconds int
	configured         bool
}

// DefaultStoreDir returns the conversation storage directory.
func DefaultStoreDir() string {
	if home := strings.TrimSpace(os.Getenv(EnvHome)); home != "" {
		return filepath.Join(home, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".hal9001", "sessions")
	}
	return filepath.Join(home, ".hal9001", "sessions")
}

// Load resolves flags over environment defaults.
func Load(args []string) Config {
	cfg, _ := LoadErr(args)
	return cfg
}

// LoadErr is Load but surfaces flag errors (including -h/--help, which
// reports flag.ErrHelp after printing usage). Resolution order: config
// file, then environment, then flags. The environment wins over the file
// and is never written back.
func LoadErr(args []string) (Config, error) {
	stored, err := LoadFile(ConfigPath())
	if err != nil {
		return Config{}, err
	}
	fs := flag.NewFlagSet("hal9001", flag.ContinueOnError)
	cfg := Config{
		Provider:           firstNonEmpty(os.Getenv(EnvProvider), stored.Provider, "openai-compatible"),
		Endpoint:           firstNonEmpty(os.Getenv(EnvEndpoint), stored.Endpoint),
		Model:              firstNonEmpty(os.Getenv(EnvModel), stored.ActiveModel),
		APIKey:             firstNonEmpty(os.Getenv(EnvAPIKey), stored.APIKey),
		Session:            "cli",
		StoreDir:           DefaultStoreDir(),
		Models:             append([]ModelRecord(nil), stored.Models...),
		MaxTokens:          firstNonZero(envInt(EnvMaxTokens), stored.MaxTokens),
		StreamIdleSeconds:  firstNonZero(envInt(EnvStreamIdleTimeout), stored.StreamIdleSeconds),
		TurnTimeoutSeconds: firstNonZero(envInt(EnvTurnTimeout), stored.TurnTimeoutSeconds),
	}
	fs.StringVar(&cfg.Provider, "provider", cfg.Provider, "model provider adapter")
	fs.StringVar(&cfg.Endpoint, "endpoint", cfg.Endpoint, "provider API endpoint")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model identifier")
	fs.StringVar(&cfg.Session, "session", cfg.Session, "conversation session name")
	fs.StringVar(&cfg.StoreDir, "store", cfg.StoreDir, "conversation storage directory")
	fs.BoolVar(&cfg.NoColor, "no-color", os.Getenv("NO_COLOR") != "", "disable colors")
	fs.BoolVar(&cfg.List, "list-sessions", false, "list stored sessions and exit")
	fs.IntVar(&cfg.MaxTokens, "max-tokens", cfg.MaxTokens, "cap one response (0 uses the default)")
	fs.IntVar(&cfg.StreamIdleSeconds, "stream-idle-timeout", cfg.StreamIdleSeconds, "seconds of silence before a stream is closed")
	fs.IntVar(&cfg.TurnTimeoutSeconds, "turn-timeout", cfg.TurnTimeoutSeconds, "seconds one turn may run")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	cfg.configured = strings.TrimSpace(cfg.Endpoint) != "" &&
		strings.TrimSpace(cfg.Model) != "" &&
		strings.TrimSpace(cfg.APIKey) != ""
	return cfg, nil
}

// envInt parses a non-negative integer from the environment, ignoring
// anything malformed so a typo cannot disable a safeguard silently.
func envInt(name string) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// firstNonZero returns the first positive value.
func firstNonZero(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Configured reports whether a provider can be reached. The foundation
// ships no provider adapters yet, so even a complete config only unlocks
// the provider milestone — it never invents a backend.
func (c Config) Configured() bool { return c.configured }

// Redacted summarizes the config without secrets, for startup logs.
func (c Config) Redacted() string {
	key := "unset"
	if c.APIKey != "" {
		key = "set"
	}
	return fmt.Sprintf("provider=%q endpoint=%q model=%q api_key=%s session=%q store=%q",
		c.Provider, c.Endpoint, c.Model, key, c.Session, c.StoreDir)
}
