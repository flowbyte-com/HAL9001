// Command hal9001 launches HAL9001's native terminal interface: a
// conversational thinking partner with permanent 444 read-only authority.
// On first run it asks for three values (provider endpoint, model,
// API key) and stores them in HAL9001's own user configuration directory
// with restrictive permissions. It never reads PicoClaw, OpenCode or MPM
// credentials.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/clipboard"
	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/persona"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hal9001:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.LoadErr(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // usage already printed
		}
		return err
	}
	if cfg.NoColor {
		os.Setenv("NO_COLOR", "1")
	}
	if cfg.List {
		return listSessions(cfg.StoreDir)
	}
	stored, err := config.LoadFile(config.ConfigPath())
	if err != nil {
		return err
	}
	app := &app{
		cfg:    cfg,
		stored: stored,
		model:  cfg.Model,
	}
	return app.run()
}

func listSessions(storeDir string) error {
	names, err := config.ListSessions(storeDir)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Println("No stored conversations yet.")
		return nil
	}
	for _, name := range names {
		fmt.Println(name)
	}
	return nil
}

type app struct {
	cfg     config.Config
	stored  config.FileConfig
	store   *session.Store
	eng     *engine.Engine
	model   string
	session string
}

func (a *app) run() error {
	store, err := session.Open(a.cfg.StoreDir, a.cfg.Session)
	if err != nil {
		return err
	}
	a.store = store
	a.session = a.cfg.Session
	a.rebuildEngine()

	hooks := tui.Hooks{
		SaveOnboard:    a.saveOnboard,
		TestOnboard:    a.testOnboard,
		HasConfig:      a.hasConfig,
		ListModels:     a.listModels,
		SwitchModel:    a.switchModel,
		CurrentOnboard: a.currentOnboard,
		NewSession:     a.newSession,
		Theme:          a.currentTheme,
		SaveTheme:      a.saveTheme,
		ListSessions:   a.listSessionNames,
		SwitchSession:  a.switchSession,
		Copy:           a.copyText,
	}
	m := tui.NewWithHooks(a.eng, hooks)
	m.SetModelName(a.activeModelID())
	m.SetTheme(tui.ThemeByName(a.stored.Theme))
	if history, err := a.store.History(); err == nil {
		lines := make([]tui.SessionLine, 0, len(history))
		for _, msg := range history {
			role := "assistant"
			if msg.Role == session.RoleUser {
				role = "user"
			}
			lines = append(lines, tui.SessionLine{Role: role, Text: msg.Content})
		}
		m.SetHistory(lines)
	}
	if !a.hasConfig() {
		m.EnterOnboarding()
	}
	return tui.RunWithModel(m)
}

func (a *app) hasConfig() bool {
	if a.cfg.Endpoint != "" && a.cfg.Model != "" && a.cfg.APIKey != "" {
		return true
	}
	_, ok := a.stored.Active()
	return ok && a.stored.Endpoint != ""
}

// providerFor builds the configured provider, or nil when nothing usable
// is configured yet. No adapters beyond OpenAI-compatible ship in this
// milestone.
func (a *app) providerFor() *provider.OpenAICompatible {
	endpoint, key := a.cfg.Endpoint, a.cfg.APIKey
	if endpoint == "" {
		endpoint = a.stored.Endpoint
	}
	if key == "" {
		key = a.stored.APIKey
	}
	if endpoint == "" || key == "" {
		return nil
	}
	return &provider.OpenAICompatible{Endpoint: endpoint, APIKey: key}
}

func (a *app) modelFor() provider.Model {
	id := a.model
	if id == "" {
		id = a.cfg.Model
	}
	if id == "" {
		if active, ok := a.stored.Active(); ok {
			id = active.ID
		}
	}
	return provider.Model{ID: id}
}

func (a *app) rebuildEngine() {
	// A typed nil pointer must never reach the provider interface: the
	// engine's "no provider" check would pass and the first request would
	// dereference a nil client.
	var p provider.Provider
	if impl := a.providerFor(); impl != nil {
		p = impl
	}
	a.eng = engine.New(a.store, p, a.modelFor()).
		WithSystemPrompt(persona.Identity("native-foundation", a.cfg.StoreDir))
}

func (a *app) saveOnboard(d tui.OnboardData) (tui.Engine, error) {
	a.stored.Provider = "openai-compatible"
	if a.cfg.Endpoint == "" {
		a.stored.Endpoint = d.Endpoint
	}
	if a.cfg.APIKey == "" {
		a.stored.APIKey = d.Key
	}
	a.stored.AddModel(d.Model)
	a.stored.ActiveModel = d.Model
	if err := config.SaveFile(config.ConfigPath(), a.stored); err != nil {
		return nil, err
	}
	a.model = d.Model
	a.rebuildEngine()
	return a.eng, nil
}

func (a *app) testOnboard(d tui.OnboardData) (string, error) {
	client := &provider.OpenAICompatible{Endpoint: d.Endpoint, APIKey: d.Key}
	ids, err := client.Models(context.Background())
	if err != nil {
		return "", err
	}
	switch len(ids) {
	case 0:
		return "reachable, no models listed", nil
	case 1:
		return "reachable, 1 model", nil
	default:
		return fmt.Sprintf("reachable, %d models", len(ids)), nil
	}
}

func (a *app) currentOnboard() tui.OnboardData {
	endpoint := a.cfg.Endpoint
	if endpoint == "" {
		endpoint = a.stored.Endpoint
	}
	key := a.cfg.APIKey
	if key == "" {
		key = a.stored.APIKey
	}
	model := a.model
	if model == "" {
		model = a.cfg.Model
	}
	return tui.OnboardData{Endpoint: endpoint, Model: model, Key: key}
}

func (a *app) listModels() []tui.ModelInfo {
	active := a.model
	if active == "" {
		active = a.cfg.Model
	}
	if active == "" {
		if m, ok := a.stored.Active(); ok {
			active = m.ID
		}
	}
	seen := map[string]bool{}
	var out []tui.ModelInfo
	add := func(id, label string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, tui.ModelInfo{ID: id, Label: label, Active: id == active})
	}
	for _, m := range a.stored.Models {
		add(m.ID, m.Display())
	}
	if a.cfg.Model != "" {
		add(a.cfg.Model, a.cfg.Model+" (flags)")
	}
	if a.model != "" {
		add(a.model, a.model)
	}
	return out
}

func (a *app) switchModel(id string) (tui.Engine, error) {
	found := false
	for _, m := range a.listModels() {
		if m.ID == id {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("unknown model %q", id)
	}
	a.model = id
	a.rebuildEngine()
	return a.eng, nil
}

func (a *app) newSession() (tui.Engine, error) {
	name := "cli-" + time.Now().Format("20060102-150405")
	store, err := session.Open(a.cfg.StoreDir, name)
	if err != nil {
		return nil, err
	}
	a.store = store
	a.session = name
	a.rebuildEngine()
	return a.eng, nil
}

func (a *app) activeModelID() string {
	if a.model != "" {
		return a.model
	}
	if a.cfg.Model != "" {
		return a.cfg.Model
	}
	if active, ok := a.stored.Active(); ok {
		return active.ID
	}
	return ""
}

func (a *app) currentTheme() string {
	return a.stored.Theme
}

func (a *app) saveTheme(name string) error {
	a.stored.Theme = name
	return config.SaveFile(config.ConfigPath(), a.stored)
}

func (a *app) listSessionNames() ([]string, error) {
	return config.ListSessions(a.cfg.StoreDir)
}

func (a *app) switchSession(name string) (tui.Engine, []tui.SessionLine, error) {
	store, err := session.Open(a.cfg.StoreDir, name)
	if err != nil {
		return nil, nil, err
	}
	history, err := store.History()
	if err != nil {
		return nil, nil, err
	}
	a.store = store
	a.session = name
	a.rebuildEngine()
	lines := make([]tui.SessionLine, 0, len(history))
	for _, msg := range history {
		role := "assistant"
		if msg.Role == session.RoleUser {
			role = "user"
		}
		lines = append(lines, tui.SessionLine{Role: role, Text: msg.Content})
	}
	return a.eng, lines, nil
}

func (a *app) copyText(text string) error {
	copier, err := clipboard.System()
	if err != nil {
		return err
	}
	return copier.Copy(text)
}
