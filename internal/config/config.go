// Package config loads HAL9001's runtime configuration from flags and the
// environment. Onboarding is three values: an API key, a provider endpoint
// and a model identifier. Secrets are never written to files; the key is
// read from the environment at startup.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Environment variables for onboarding.
const (
	EnvProvider = "HAL9001_PROVIDER"
	EnvEndpoint = "HAL9001_ENDPOINT"
	EnvModel    = "HAL9001_MODEL"
	EnvAPIKey   = "HAL9001_API_KEY"
	EnvHome     = "HAL9001_HOME"
)

// Config is the resolved runtime configuration.
type Config struct {
	Provider   string
	Endpoint   string
	Model      string
	APIKey     string
	Session    string
	StoreDir   string
	NoColor    bool
	ShowHelp   bool
	configured bool
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
// reports flag.ErrHelp after printing usage).
func LoadErr(args []string) (Config, error) {
	fs := flag.NewFlagSet("hal9001", flag.ContinueOnError)
	cfg := Config{
		Provider: os.Getenv(EnvProvider),
		Endpoint: os.Getenv(EnvEndpoint),
		Model:    os.Getenv(EnvModel),
		APIKey:   os.Getenv(EnvAPIKey),
		Session:  "cli",
		StoreDir: DefaultStoreDir(),
	}
	fs.StringVar(&cfg.Provider, "provider", cfg.Provider, "model provider adapter")
	fs.StringVar(&cfg.Endpoint, "endpoint", cfg.Endpoint, "provider API endpoint")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model identifier")
	fs.StringVar(&cfg.Session, "session", cfg.Session, "conversation session name")
	fs.StringVar(&cfg.StoreDir, "store", cfg.StoreDir, "conversation storage directory")
	fs.BoolVar(&cfg.NoColor, "no-color", os.Getenv("NO_COLOR") != "", "disable colors")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	cfg.configured = strings.TrimSpace(cfg.Endpoint) != "" &&
		strings.TrimSpace(cfg.Model) != "" &&
		strings.TrimSpace(cfg.APIKey) != ""
	return cfg, nil
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
