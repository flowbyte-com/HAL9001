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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/clipboard"
	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/persona"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/skills"
	"github.com/flowbyte-com/HAL9001/internal/tui"
	"github.com/flowbyte-com/HAL9001/internal/usage"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
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
	// The terminal's mouse modes are restored on every exit path: a
	// clean quit, a cancellation, and an error on the way out.
	if cfg.Mouse {
		defer restoreMouseModes()
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
	// Token accounting is best effort: a failure must never stop the
	// conversation from starting.
	if store, err := usage.Open(filepath.Join(config.ConfigDir(), "usage.json")); err == nil {
		app.usage = store
	}
	// The one directory a proposed write may reach. Opening it writes
	// nothing: the store only resolves the path, and the directory is
	// created when an approved write needs it.
	ws, err := workspace.Open(cfg.WorkspaceDir)
	if err != nil {
		return err
	}
	// The audit trail is trusted bookkeeping under HAL's own data
	// directory; a failure to open it leaves the workspace working
	// without a trail rather than blocking the conversation.
	if audit, err := workspace.OpenAudit(filepath.Join(config.ConfigDir(), "audit.jsonl")); err == nil {
		ws.WithAudit(audit)
	}
	// Defence in depth: even a misconfigured workspace can never reach
	// HAL's own configuration and session data.
	ws.Protect(config.ConfigDir())
	app.workspace = ws
	// The MPM skill store. HAL reads skills from it and never writes to
	// it: the catalogue is fetched once at startup and refreshed on
	// demand. A missing store is not a reason to refuse to start.
	app.skills = skills.NewSelector(&skills.Client{})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := app.skills.Refresh(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "hal9001: skills unavailable:", err)
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

// restoreMouseModes turns the terminal's mouse reporting off.
//
// Bubble Tea already resets the modes when the program closes. Writing
// the reset again is cheap insurance for the paths where the program
// dies before the renderer gets there, and it leaves the terminal
// exactly as HAL found it.
func restoreMouseModes() {
	f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString("\x1b[?1002l\x1b[?1003l\x1b[?1006l")
}

type app struct {
	cfg          config.Config
	stored       config.FileConfig
	store        *session.Store
	eng          *engine.Engine
	model        string
	session      string
	sessionTitle string
	sessionSeq   int
	usage        *usage.Store
	// workspace is the single write capability shared by the engine
	// (which prepares proposals) and the interface (which approves them).
	workspace *workspace.Store
	// skills is the MPM skill store, shared by the engine (which routes
	// each turn) and the interface (which lists and pins skills).
	skills *skills.Selector
}

// defaultSessionName is the conversation used when nothing has been
// recorded yet. It matches the name older installations already use.
const defaultSessionName = "cli"

// resolveSession picks the conversation to open. Precedence: an explicit
// --session, then the session recorded as active, then the most recently
// modified stored conversation (for installations that predate the
// metadata), then an empty default. It never falls back to the earliest
// session when a newer one is known.
func (a *app) resolveSession() string {
	if explicit := strings.TrimSpace(a.cfg.Session); explicit != "" {
		return explicit
	}
	if stored := strings.TrimSpace(a.stored.ActiveSession); stored != "" && session.ValidName(stored) {
		return stored
	}
	if newest := a.newestSession(); newest != "" {
		return newest
	}
	return defaultSessionName
}

// newestSession returns the most recently modified stored conversation.
func (a *app) newestSession() string {
	names, err := config.ListSessions(a.cfg.StoreDir)
	if err != nil || len(names) == 0 {
		return ""
	}
	best := ""
	var bestTime time.Time
	for _, name := range names {
		if !session.ValidName(name) {
			continue
		}
		info, err := os.Stat(filepath.Join(a.cfg.StoreDir, name+".jsonl"))
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best, bestTime = name, info.ModTime()
		}
	}
	return best
}

// persistActiveSession records the conversation for the next launch. It
// is written before the in-memory switch, so a failure leaves the app and
// the interface consistent.
func (a *app) persistActiveSession(name string) error {
	if a.stored.ActiveSession == name {
		return nil
	}
	previous := a.stored.ActiveSession
	a.stored.ActiveSession = name
	if err := config.SaveFile(config.ConfigPath(), a.stored); err != nil {
		a.stored.ActiveSession = previous
		return fmt.Errorf("cannot record the active session: %w", err)
	}
	return nil
}

// openSession resolves and opens the conversation for this launch, then
// records it as the active session for the next one.
func (a *app) openSession() error {
	name := a.resolveSession()
	store, err := session.Open(a.cfg.StoreDir, name)
	if err != nil {
		return err
	}
	a.store = store
	a.session = name
	a.sessionTitle = a.displayTitle(name)
	// Best effort on startup: a read-only configuration must not stop the
	// conversation from starting.
	_ = a.persistActiveSession(name)
	a.rebuildEngine()
	return nil
}

func (a *app) run() error {
	if err := a.openSession(); err != nil {
		return err
	}

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
		Effort:         a.currentEffort,
		SetEffort:      a.setEffort,
		Usage:          a.currentUsage,
		RecordTurn:     a.recordTurn,
		RecordFailed:   a.recordFailed,
		RenameSession:  a.renameSession,
		SessionTitle:   func() string { return a.sessionTitle },
	}
	m := tui.NewWithHooks(a.eng, hooks)
	m.SetModelName(a.activeModelID())
	m.SetEffort(a.currentEffort())
	m.SetUsage(a.currentUsage())
	m.SetMouse(a.cfg.Mouse)
	m.SetWorkspace(a.workspace)
	m.SetSkills(a.skills)
	m.SetTheme(tui.ThemeByName(a.stored.Theme))
	// The header shows the human-readable title; the immutable session
	// identity stays in the diagnostics.
	m.SetTitle(a.displayTitle(a.session))
	m.SetSessionID(a.session)
	if history, err := a.store.History(); err == nil {
		m.SetHistory(sessionLines(history))
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
	return &provider.OpenAICompatible{
		Endpoint:         endpoint,
		APIKey:           key,
		DefaultMaxTokens: a.cfg.MaxTokens,
		IdleTimeout:      seconds(a.cfg.StreamIdleSeconds),
		TurnTimeout:      seconds(a.cfg.TurnTimeoutSeconds),
	}
}

// seconds converts a configured second count to a duration, leaving zero
// as "use the built-in default".
func seconds(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

func (a *app) modelFor() provider.Model {
	id := a.activeModelID()
	return provider.Model{
		ID:     id,
		Effort: a.effortFor(id),
	}
}

// effortFor resolves the effort level for a model: the operator's stored
// choice when present, otherwise the built-in default for capable models
// and nothing for everything else.
func (a *app) effortFor(id string) string {
	if !provider.EffortCapable(id) {
		return ""
	}
	if stored := provider.NormalizeEffort(a.stored.EffortFor(id)); stored != "" {
		return stored
	}
	return provider.DefaultEffortFor(id)
}

// currentEffort reports the active level and whether the active model
// supports effort control at all.
func (a *app) currentEffort() (string, bool) {
	id := a.activeModelID()
	if !provider.EffortCapable(id) {
		return "", false
	}
	return a.effortFor(id), true
}

// setEffort persists a reasoning level for the active model and rebuilds
// the engine, so the next request carries it.
func (a *app) setEffort(level string) (tui.Engine, error) {
	id := a.activeModelID()
	if !provider.EffortCapable(id) {
		return nil, fmt.Errorf("model %q does not support reasoning effort", id)
	}
	normalized := provider.NormalizeEffort(level)
	if normalized == "" {
		return nil, fmt.Errorf("unknown effort level %q", level)
	}
	a.stored.SetEffort(id, normalized)
	if err := config.SaveFile(config.ConfigPath(), a.stored); err != nil {
		return nil, err
	}
	a.rebuildEngine()
	return a.eng, nil
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
		WithSystemPrompt(persona.Identity("native-foundation", a.cfg.StoreDir)).
		WithWorkspace(a.workspace).
		WithSkills(a.skills)
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
	// A new session must never reuse an existing one: two clears in the
	// same second are still two separate conversations. The counter
	// covers names not yet written to disk; the stat covers a file left
	// by an earlier run.
	base := "cli-" + time.Now().Format("20060102-150405")
	a.sessionSeq++
	name := base
	if a.sessionSeq > 1 {
		name = fmt.Sprintf("%s-%d", base, a.sessionSeq)
	}
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(a.cfg.StoreDir, name+".jsonl")); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	store, err := session.Open(a.cfg.StoreDir, name)
	if err != nil {
		return nil, err
	}
	if err := a.persistActiveSession(name); err != nil {
		return nil, err
	}
	// A new session exists from creation, even before its first message:
	// the browser can list it and a restart keeps it selected.
	path := filepath.Join(a.cfg.StoreDir, name+".jsonl")
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		return nil, err
	} else {
		_ = f.Close()
	}
	a.store = store
	a.session = name
	// A fresh conversation gets a date/time display title and a creation
	// timestamp; the ID stays immutable.
	now := time.Now().UTC()
	_ = session.SaveMeta(a.cfg.StoreDir, name, session.Meta{CreatedAt: now})
	a.sessionTitle = session.DefaultTitle(now)
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

// currentUsage returns the cumulative accounting for the active session.
func (a *app) currentUsage() usage.Session {
	if a.usage == nil {
		return usage.Session{}
	}
	return a.usage.Get(a.session)
}

// usageTurn converts provider statistics into a stored record. Duration
// is wall-clock time, finish and truncation describe how the turn ended.
func usageTurn(stats provider.TurnStats) usage.Turn {
	return usage.Turn{
		Input:     stats.Usage.PromptTokens,
		Output:    stats.Usage.CompletionTokens,
		Reasoning: stats.Usage.ReasoningTokens,
		Cached:    stats.Usage.CachedTokens,
		Duration:  stats.Duration.Milliseconds(),
		Finish:    stats.FinishReason,
		Truncated: stats.OutputLimitHit,
		Reported:  stats.HasUsage,
	}
}

// recordTurn stores one finished answer. A turn without provider-reported
// usage is recorded as incomplete so the counts are never invented.
func (a *app) recordTurn(stats provider.TurnStats) {
	if a.usage == nil || stats.StartedAt.IsZero() {
		return
	}
	_ = a.usage.AddAnswer(a.session, usageTurn(stats))
}

// recordFailed stores a turn that produced no answer. Usage the provider
// did report still counts toward the session totals; a report-less
// failure only increments the failed counter.
func (a *app) recordFailed(stats *provider.TurnStats) {
	if a.usage == nil {
		return
	}
	if stats == nil || stats.StartedAt.IsZero() {
		_ = a.usage.AddFailed(a.session, usage.Turn{})
		return
	}
	_ = a.usage.AddFailed(a.session, usageTurn(*stats))
}

func (a *app) currentTheme() string {
	return a.stored.Theme
}

func (a *app) saveTheme(name string) error {
	a.stored.Theme = name
	return config.SaveFile(config.ConfigPath(), a.stored)
}

// listSessionNames returns stored conversations for the browser: display
// title, creation and activity dates, most recently active first, with
// the active session marked. History files are never modified.
func (a *app) listSessionNames() ([]tui.SessionInfo, error) {
	names, err := config.ListSessions(a.cfg.StoreDir)
	if err != nil {
		return nil, err
	}
	out := make([]tui.SessionInfo, 0, len(names))
	for _, name := range names {
		info := session.Stat(a.cfg.StoreDir, name)
		out = append(out, tui.SessionInfo{
			ID:      info.ID,
			Title:   info.Title,
			Created: info.Created,
			Updated: info.Updated,
			Current: name == a.session,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// displayTitle resolves what the header should show for a session.
func (a *app) displayTitle(id string) string {
	info := session.Stat(a.cfg.StoreDir, id)
	if info.Title != "" {
		return info.Title
	}
	return id
}

// renameSession sets the display title of the active conversation.
func (a *app) renameSession(title string) (string, error) {
	display, err := session.RenameSession(a.cfg.StoreDir, a.session, title)
	if err != nil {
		return "", err
	}
	return display, nil
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
	if err := a.persistActiveSession(name); err != nil {
		return nil, nil, err
	}
	// Selecting a conversation is activity: it becomes the most recent
	// one for the next launch's browser order.
	now := time.Now()
	_ = os.Chtimes(filepath.Join(a.cfg.StoreDir, name+".jsonl"), now, now)
	a.store = store
	a.session = name
	a.sessionTitle = a.displayTitle(name)
	a.rebuildEngine()
	return a.eng, sessionLines(history), nil
}

// sessionLines turns stored messages into the interface's own lines.
//
// The attribution of a turn travels with its answer: it is what the
// stored record says shaped that answer, and it is shown beside the
// reply's metadata. It never becomes part of the text.
func sessionLines(history []session.Message) []tui.SessionLine {
	lines := make([]tui.SessionLine, 0, len(history))
	for _, msg := range history {
		role := "assistant"
		if msg.Role == session.RoleUser {
			role = "user"
		}
		lines = append(lines, tui.SessionLine{
			Role:        role,
			Text:        msg.Content,
			Attribution: msg.Attribution,
		})
	}
	return lines
}

func (a *app) copyText(text string) error {
	copier, err := clipboard.System()
	if err != nil {
		return err
	}
	return copier.Copy(text)
}
