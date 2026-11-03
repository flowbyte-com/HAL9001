// Package config loads HAL9001's runtime configuration from flags, the
// environment, and the user configuration file. Onboarding is three
// values: an API key, a provider endpoint and a model identifier. Secrets
// live in a 0600 user config file or the environment (which wins and is
// never written back); they never appear in logs or the repository, and
// HAL9001 never reads PicoClaw, OpenCode or MPM credentials.
package config

import (
	"encoding/json"
	"errors"
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
	// EnvMouse turns terminal mouse reporting on or off: 1/true/yes/on
	// enables it, 0/false/no/off disables it. Unset leaves the default,
	// which is enabled. A value that is neither is refused.
	EnvMouse = "HAL9001_MOUSE"
	// EnvWorkspace overrides the one directory HAL may propose files in.
	// The default is ~/HAL.
	EnvWorkspace = "HAL9001_WORKSPACE"
)

// ModelRecord is one configured model. The record is intentionally small
// and extensible: future milestones may add endpoint overrides without
// changing the shape.
type ModelRecord struct {
	// ID is the provider-side model identifier.
	ID string `json:"id"`
	// Label is the display name. Empty means ID.
	Label string `json:"label,omitempty"`
	// Effort is the persisted reasoning level for models that support
	// effort control. Empty means the built-in default for the model.
	Effort string `json:"effort,omitempty"`
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
	// ActiveSession is the conversation HAL opens on the next launch. It
	// is written whenever a session is started or selected, so /clear and
	// /sessions survive a restart.
	ActiveSession string `json:"active_session,omitempty"`
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

// SetEffort stores the reasoning level for a configured model, creating
// the record when it does not exist yet. It reports whether anything
// changed.
func (f *FileConfig) SetEffort(id, effort string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for i := range f.Models {
		if f.Models[i].ID == id {
			if f.Models[i].Effort == effort {
				return false
			}
			f.Models[i].Effort = effort
			return true
		}
	}
	f.Models = append(f.Models, ModelRecord{ID: id, Effort: effort})
	return true
}

// EffortFor returns the recorded level for a model, or "" when none is
// recorded.
func (f FileConfig) EffortFor(id string) string {
	for _, m := range f.Models {
		if m.ID == id {
			return m.Effort
		}
	}
	return ""
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
	// Mouse requests terminal mouse reporting, so the wheel scrolls the
	// conversation and a drag selects text. On by default; --no-mouse
	// turns it off and leaves the terminal's own selection in charge.
	Mouse bool
	// WorkspaceDir is the only directory a model-proposed write may
	// reach. It is not a permission: every write is approved one at a
	// time in the interface.
	WorkspaceDir string
	ShowHelp     bool
	List         bool
	Models       []ModelRecord
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

// DefaultWorkspaceDir returns the directory proposed writes may reach.
func DefaultWorkspaceDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "HAL")
	}
	return filepath.Join(home, "HAL")
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
		Provider: firstNonEmpty(os.Getenv(EnvProvider), stored.Provider, "openai-compatible"),
		Endpoint: firstNonEmpty(os.Getenv(EnvEndpoint), stored.Endpoint),
		Model:    firstNonEmpty(os.Getenv(EnvModel), stored.ActiveModel),
		APIKey:   firstNonEmpty(os.Getenv(EnvAPIKey), stored.APIKey),
		// Empty unless --session is given: the active session is
		// resolved from the stored metadata at startup.
		Session:            "",
		StoreDir:           DefaultStoreDir(),
		WorkspaceDir:       firstNonEmpty(os.Getenv(EnvWorkspace), DefaultWorkspaceDir()),
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
	fs.StringVar(&cfg.WorkspaceDir, "workspace", cfg.WorkspaceDir, "directory HAL may propose files in")
	fs.BoolVar(&cfg.NoColor, "no-color", os.Getenv("NO_COLOR") != "", "disable colors")
	// The mouse flags are read after parsing, because the flag package
	// cannot tell "not given" from "given as false".
	var mouseFlag, noMouseFlag bool
	fs.BoolVar(&mouseFlag, "mouse", false, "report mouse events (the default): the wheel scrolls and a drag selects text")
	fs.BoolVar(&noMouseFlag, "no-mouse", false, "do not report mouse events: the terminal keeps its own text selection")
	fs.BoolVar(&cfg.List, "list-sessions", false, "list stored sessions and exit")
	fs.IntVar(&cfg.MaxTokens, "max-tokens", cfg.MaxTokens, "cap one response (0 uses the default)")
	fs.IntVar(&cfg.StreamIdleSeconds, "stream-idle-timeout", cfg.StreamIdleSeconds, "seconds of silence before a stream is closed")
	fs.IntVar(&cfg.TurnTimeoutSeconds, "turn-timeout", cfg.TurnTimeoutSeconds, "seconds one turn may run")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	mouse, err := resolveMouse(fs, mouseFlag, noMouseFlag)
	if err != nil {
		return cfg, err
	}
	cfg.Mouse = mouse
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

// resolveMouse decides whether the terminal reports mouse events.
//
// Precedence: an explicit flag wins, then the environment, then the
// default, which is enabled. Asking for both --mouse and --no-mouse is
// a contradiction and is refused rather than resolved silently.
//
// A malformed environment value is only an error when the environment
// is what decides. A flag that says what to do wins, and a malformed
// value that no longer decides anything is irrelevant.
func resolveMouse(fs *flag.FlagSet, mouseFlag, noMouseFlag bool) (bool, error) {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	switch {
	case given["mouse"] && given["no-mouse"]:
		return false, errors.New("--mouse and --no-mouse cannot be used together")
	case given["mouse"]:
		// The flag's value is honoured, so --mouse=false means off.
		return mouseFlag, nil
	case given["no-mouse"]:
		return !noMouseFlag, nil
	}
	value, set, err := envMouse(EnvMouse)
	if err != nil {
		return false, err
	}
	if set {
		return value, nil
	}
	return true, nil
}

// envMouse reads the mouse setting from the environment.
//
// It reports whether the variable was set: 1/true/yes/on enables mouse
// reporting, 0/false/no/off disables it. A value that is neither is an
// error, so a typo cannot quietly change how the terminal behaves. Only
// the one variable is read and reported: nothing else about the
// environment is touched.
func envMouse(name string) (bool, bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false, false, nil
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true, true, nil
	case "0", "false", "no", "off":
		return false, true, nil
	}
	return false, false, fmt.Errorf(
		"%s=%q is not a valid mouse setting: use 1 or 0 (also true/false, yes/no, on/off)",
		name, raw)
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
