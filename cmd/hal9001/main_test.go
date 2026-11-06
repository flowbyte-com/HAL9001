package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/usage"
)

// A missing API key must leave the engine without a provider, never with a
// typed nil pointer that would crash on the first request.
// A resumed line keeps the turn identity and the verified ledger so the
// interface can associate historical tools with the answer they produced.
func TestSessionLinesKeepTurnAndToolEvidence(t *testing.T) {
	msg := session.Message{
		Role: session.RoleAssistant, Content: "the answer", TurnID: "turn-1",
		ToolLedger: &session.ToolLedger{
			Version:   session.ToolLedgerVersion,
			SessionID: "chat",
			TurnID:    "turn-1",
			Executions: []session.ToolExecution{{
				TurnID: "turn-1", CallID: "call-1", Tool: "bash",
				Mode: "666", Status: session.ToolSuccess, Summary: "exit 0",
			}},
		},
	}
	lines := sessionLines([]session.Message{msg})
	if len(lines) != 1 {
		t.Fatalf("lines = %d", len(lines))
	}
	if lines[0].TurnID != "turn-1" || len(lines[0].Tools) != 1 ||
		lines[0].Tools[0].Tool != "bash" || lines[0].Tools[0].Mode != "666" {
		t.Fatalf("line = %#v", lines[0])
	}
	old := sessionLines([]session.Message{{
		Role: session.RoleAssistant, Content: "old", TurnID: "turn-old",
	}})
	if len(old[0].Tools) != 0 || old[0].TurnID != "turn-old" {
		t.Fatalf("old line = %#v", old[0])
	}
}

func TestRebuildEngineWithoutProviderIsSafe(t *testing.T) {
	store, err := session.Open(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{
		cfg:     config.Config{StoreDir: t.TempDir()},
		store:   store,
		session: "test",
	}
	a.rebuildEngine()
	if a.eng == nil {
		t.Fatal("the engine must exist")
	}
	_, err = a.eng.RespondStream(context.Background(), "hello", nil)
	if !errors.Is(err, engine.ErrNoProvider) {
		t.Errorf("error = %v, want ErrNoProvider", err)
	}
}

func TestRunHelp(t *testing.T) {
	if err := run([]string{"-h"}); err != nil {
		t.Errorf("run(-h) = %v, want nil", err)
	}
}

func TestWantsVersion(t *testing.T) {
	for _, arg := range []string{"--version", "-version"} {
		if !wantsVersion([]string{arg}) {
			t.Errorf("wantsVersion(%q) = false", arg)
		}
	}
	for _, args := range [][]string{{}, {"--help"}, {"--version=false"}} {
		if wantsVersion(args) {
			t.Errorf("wantsVersion(%v) = true", args)
		}
	}
}

func TestRunVersionDoesNotReadConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAL9001_CONFIG", configPath)

	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	runErr := run([]string{"--version"})
	os.Stdout = old
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("run --version = %v", runErr)
	}
	if !strings.Contains(string(out), "HAL9001") || !strings.Contains(string(out), "commit:") {
		t.Fatalf("output = %q", out)
	}
}

func TestRunBadFlag(t *testing.T) {
	if err := run([]string{"--no-such-flag"}); err == nil {
		t.Error("run with a bad flag must fail")
	}
}

func TestRunListSessions(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"--store", dir, "--list-sessions"}); err != nil {
		t.Errorf("run --list-sessions = %v", err)
	}
}

// captureServer records the last chat request body and answers with a
// minimal completion.
type captureServer struct {
	body map[string]any
}

func (c *captureServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.body = body
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	})
}

func newTestApp(t *testing.T, model string, records ...config.ModelRecord) *app {
	t.Helper()
	if len(records) == 0 {
		records = []config.ModelRecord{{ID: model}}
	}
	store, err := session.Open(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	return &app{
		cfg: config.Config{
			Endpoint: "http://example.invalid", APIKey: "k", Model: model, StoreDir: t.TempDir(),
		},
		stored: config.FileConfig{
			Endpoint: "http://example.invalid", APIKey: "k",
			Models: records, ActiveModel: model,
		},
		store:   store,
		session: "test",
	}
}

// MiniMax-M3.1-Flash starts at medium effort, and that is what the HTTP
// request carries.
func TestDefaultEffortReachesProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	cap := &captureServer{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	a := newTestApp(t, "MiniMax-M3.1-Flash")
	a.cfg.Endpoint, a.stored.Endpoint = srv.URL, srv.URL
	a.rebuildEngine()
	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if got := cap.body["reasoning_effort"]; got != "medium" {
		t.Errorf("reasoning_effort = %v, want medium", got)
	}
	level, supported := a.currentEffort()
	if !supported || level != "medium" {
		t.Errorf("currentEffort = (%q, %v), want (medium, true)", level, supported)
	}
}

// An operator override is persisted and genuinely changes the next
// request, not just a label.
func TestEffortOverrideReachesProviderAndPersists(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("HAL9001_CONFIG", cfgPath)
	cap := &captureServer{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	a := newTestApp(t, "MiniMax-M3.1-Flash")
	a.cfg.Endpoint, a.stored.Endpoint = srv.URL, srv.URL
	a.rebuildEngine()

	before, err := a.store.History()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.setEffort("xhigh"); err != nil {
		t.Fatal(err)
	}
	after, err := a.store.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("changing effort altered the conversation: %d -> %d messages", len(before), len(after))
	}
	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if got := cap.body["reasoning_effort"]; got != "xhigh" {
		t.Errorf("reasoning_effort = %v, want xhigh", got)
	}
	saved, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.EffortFor("MiniMax-M3.1-Flash"); got != "xhigh" {
		t.Errorf("persisted effort = %q, want xhigh", got)
	}

	// A fresh app reading the file starts at the saved level.
	restored := newTestApp(t, "MiniMax-M3.1-Flash")
	restored.stored = saved
	if level, supported := restored.currentEffort(); !supported || level != "xhigh" {
		t.Errorf("restored effort = (%q, %v)", level, supported)
	}
}

// A model without effort support never receives the parameter.
func TestUnsupportedModelSendsNoEffort(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	cap := &captureServer{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	a := newTestApp(t, "gpt-4o")
	a.cfg.Endpoint, a.stored.Endpoint = srv.URL, srv.URL
	a.rebuildEngine()
	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := cap.body["reasoning_effort"]; ok {
		t.Errorf("unsupported model received reasoning_effort: %v", cap.body)
	}
	if _, supported := a.currentEffort(); supported {
		t.Error("currentEffort must report unsupported")
	}
	if _, err := a.setEffort("high"); err == nil {
		t.Error("setting effort on an unsupported model must fail")
	}
}

// Each model keeps its own effort across switches.
func TestEffortFollowsModelSwitch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	cap := &captureServer{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	a := newTestApp(t, "MiniMax-M3.1-Flash",
		config.ModelRecord{ID: "MiniMax-M3.1-Flash", Effort: "low"},
		config.ModelRecord{ID: "MiniMax-M2", Effort: "max"},
		config.ModelRecord{ID: "gpt-4o"},
	)
	a.cfg.Endpoint, a.stored.Endpoint = srv.URL, srv.URL
	a.rebuildEngine()

	if _, err := a.switchModel("MiniMax-M2"); err != nil {
		t.Fatal(err)
	}
	if level, supported := a.currentEffort(); !supported || level != "max" {
		t.Errorf("M2 effort = (%q, %v), want max", level, supported)
	}
	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if got := cap.body["reasoning_effort"]; got != "max" {
		t.Errorf("M2 request effort = %v", got)
	}

	if _, err := a.switchModel("MiniMax-M3.1-Flash"); err != nil {
		t.Fatal(err)
	}
	if level, _ := a.currentEffort(); level != "low" {
		t.Errorf("M3.1 effort = %q, want low", level)
	}

	if _, err := a.switchModel("gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if _, supported := a.currentEffort(); supported {
		t.Error("gpt-4o must report no effort support")
	}
}

// A fresh model with no recorded effort still gets the medium default.
func TestNewMiniMaxRecordDefaultsToMedium(t *testing.T) {
	a := newTestApp(t, "MiniMax-M3.1-Flash", config.ModelRecord{ID: "MiniMax-M3.1-Flash"})
	if level, supported := a.currentEffort(); !supported || level != "medium" {
		t.Errorf("currentEffort = (%q, %v), want (medium, true)", level, supported)
	}
}

// /clear's session hook must not reuse an existing file: two clears in
// the same second are separate conversations.
func TestNewSessionNamesAreUnique(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	a := newTestApp(t, "MiniMax-M3.1-Flash")
	a.cfg.StoreDir = dir
	a.store, _ = session.Open(dir, "test")

	first, err := a.newSession()
	if err != nil || first == nil {
		t.Fatalf("first new session: %v", err)
	}
	firstName := a.session
	second, err := a.newSession()
	if err != nil || second == nil {
		t.Fatalf("second new session: %v", err)
	}
	if a.session == firstName {
		t.Errorf("both clears used the same session %q", firstName)
	}

	// Both sessions appear once they hold a message, and the old one is
	// still recoverable.
	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err == nil {
		t.Fatal("no provider is configured; this app should fail the turn")
	}
	store, err := session.Open(dir, firstName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(session.RoleUser, "kept"); err != nil {
		t.Fatal(err)
	}
	names, err := config.ListSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range names {
		if name == firstName {
			found = true
		}
	}
	if !found {
		t.Errorf("old session %q not recoverable: %v", firstName, names)
	}
}

// launchApp builds an app the way run() does: load the config file, open
// the resolved session, and persist the choice. It re-reads both files on
// every call, so calling it twice simulates a restart.
func launchApp(t *testing.T, dir, explicit string) *app {
	t.Helper()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("HAL9001_CONFIG", cfgPath)
	t.Setenv("HAL9001_HOME", dir)
	stored, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := usage.Open(filepath.Join(dir, "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{
		cfg: config.Config{
			StoreDir: filepath.Join(dir, "sessions"),
			Session:  explicit,
			Endpoint: "http://example.invalid",
			APIKey:   "k",
			Model:    "MiniMax-M3.1-Flash",
		},
		stored: stored,
		usage:  store,
	}
	if err := a.openSession(); err != nil {
		t.Fatal(err)
	}
	return a
}

func historyLen(t *testing.T, a *app) int {
	t.Helper()
	h, err := a.store.History()
	if err != nil {
		t.Fatal(err)
	}
	return len(h)
}

// A fresh installation starts empty and records its session.
func TestFreshInstallStartsEmpty(t *testing.T) {
	a := launchApp(t, t.TempDir(), "")
	if a.session != "cli" {
		t.Errorf("session = %q, want cli", a.session)
	}
	if n := historyLen(t, a); n != 0 {
		t.Errorf("fresh install has %d messages", n)
	}
	saved, err := config.LoadFile(config.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if saved.ActiveSession != "cli" {
		t.Errorf("active session = %q, want cli", saved.ActiveSession)
	}
}

// /clear survives a restart: the new empty session is still active.
func TestClearSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	a := launchApp(t, dir, "")
	if _, err := a.store.Append(session.RoleUser, "old question"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.Append(session.RoleAssistant, "old answer"); err != nil {
		t.Fatal(err)
	}
	old := a.session

	if _, err := a.newSession(); err != nil {
		t.Fatal(err)
	}
	fresh := a.session
	if fresh == old {
		t.Fatal("new session reused the old name")
	}
	if n := historyLen(t, a); n != 0 {
		t.Errorf("new session has %d messages", n)
	}

	// Restart: the empty session must still be the active one.
	b := launchApp(t, dir, "")
	if b.session != fresh {
		t.Fatalf("restart selected %q, want %q", b.session, fresh)
	}
	if n := historyLen(t, b); n != 0 {
		t.Errorf("cleared conversation came back with %d messages", n)
	}
	// The old conversation is untouched and recoverable.
	oldStore, err := session.Open(b.cfg.StoreDir, old)
	if err != nil {
		t.Fatal(err)
	}
	history, err := oldStore.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Errorf("old session lost data: %d messages", len(history))
	}
}

// Selecting an older session makes it active for the next launch.
func TestResumeOlderSessionPersists(t *testing.T) {
	dir := t.TempDir()
	a := launchApp(t, dir, "")
	if _, err := a.store.Append(session.RoleUser, "keep me"); err != nil {
		t.Fatal(err)
	}
	old := a.session
	if _, err := a.newSession(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.switchSession(old); err != nil {
		t.Fatal(err)
	}
	b := launchApp(t, dir, "")
	if b.session != old {
		t.Fatalf("restart selected %q, want %q", b.session, old)
	}
	if n := historyLen(t, b); n != 1 {
		t.Errorf("resumed session has %d messages, want 1", n)
	}
}

// An explicit --session wins over the stored selection.
func TestExplicitSessionWins(t *testing.T) {
	dir := t.TempDir()
	a := launchApp(t, dir, "")
	if _, err := a.newSession(); err != nil {
		t.Fatal(err)
	}
	b := launchApp(t, dir, "chosen")
	if b.session != "chosen" {
		t.Errorf("session = %q, want chosen", b.session)
	}
	// And it becomes the active session for a plain restart.
	c := launchApp(t, dir, "")
	if c.session != "chosen" {
		t.Errorf("restart selected %q, want chosen", c.session)
	}
}

// Without metadata, an existing installation resumes the newest session,
// never the earliest.
func TestLegacyInstallPicksNewest(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if _, err := session.Open(sessions, "older"); err != nil {
		t.Fatal(err)
	}
	oldStore, _ := session.Open(sessions, "older")
	if _, err := oldStore.Append(session.RoleUser, "old"); err != nil {
		t.Fatal(err)
	}
	newStore, _ := session.Open(sessions, "newer")
	if _, err := newStore.Append(session.RoleUser, "new"); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(sessions, "older.jsonl"), oldTime, oldTime)

	a := launchApp(t, dir, "")
	if a.session != "newer" {
		t.Errorf("session = %q, want the newest (newer)", a.session)
	}
}

// Corrupt metadata is ignored, and a valid name is honoured even when no
// file exists yet.
func TestStaleSessionMetadata(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := config.SaveFile(cfgPath, config.FileConfig{ActiveSession: "../escape"}); err != nil {
		t.Fatal(err)
	}
	a := launchApp(t, dir, "")
	if a.session != "cli" {
		t.Errorf("unsafe stored name accepted: %q", a.session)
	}

	// A valid name without a file is an empty session, not a reason to
	// fall back to an older conversation.
	other := t.TempDir()
	if err := config.SaveFile(filepath.Join(other, "config.json"), config.FileConfig{ActiveSession: "empty-one"}); err != nil {
		t.Fatal(err)
	}
	b := launchApp(t, other, "")
	if b.session != "empty-one" {
		t.Errorf("empty stored session not honoured: %q", b.session)
	}
}

// Usage is recorded per session and a new session starts from zero while
// the old record is kept.
func TestUsagePerSession(t *testing.T) {
	dir := t.TempDir()
	a := launchApp(t, dir, "")
	a.recordTurn(provider.TurnStats{StartedAt: time.Now(), HasUsage: true, Usage: provider.Usage{
		PromptTokens: 120, CompletionTokens: 80, ReasoningTokens: 30, CachedTokens: 20,
	}})
	got := a.currentUsage()
	if got.Input != 120 || got.Output != 80 || got.Reasoning != 30 || got.Cached != 20 {
		t.Errorf("usage = %+v", got)
	}
	old := a.session
	if _, err := a.newSession(); err != nil {
		t.Fatal(err)
	}
	if fresh := a.currentUsage(); fresh.Total() {
		t.Errorf("new session inherited usage: %+v", fresh)
	}
	// The previous record survives for /sessions.
	if got := a.usage.Get(old); got.Input != 120 {
		t.Errorf("historical usage lost: %+v", got)
	}
	// A turn without reported usage is incomplete, never invented.
	a.recordTurn(provider.TurnStats{StartedAt: time.Now()})
	if got := a.currentUsage(); got.Incomplete != 1 || got.Input != 0 {
		t.Errorf("incomplete turn = %+v", got)
	}
}

// After /clear, the next provider request contains only the new message:
// no old user or assistant turns leak into the fresh context.
func TestClearKeepsOldMessagesOutOfContext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("HAL9001_HOME", dir)

	var captured []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		captured = append(captured, body.Messages...)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	a := launchApp(t, dir, "")
	a.cfg.Endpoint, a.stored.Endpoint = srv.URL, srv.URL
	a.rebuildEngine()
	if _, err := a.eng.RespondStream(context.Background(), "old question", nil); err != nil {
		t.Fatal(err)
	}
	captured = nil
	if _, err := a.newSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.eng.RespondStream(context.Background(), "new question", nil); err != nil {
		t.Fatal(err)
	}
	for _, msg := range captured {
		content, _ := msg["content"].(string)
		if content == "old question" || content == "ok" {
			t.Errorf("old turn leaked into the cleared context: %+v", msg)
		}
	}
	var users int
	for _, msg := range captured {
		if msg["role"] == "user" {
			users++
		}
	}
	if users != 1 {
		t.Errorf("user messages in the fresh request = %d, want 1: %+v", users, captured)
	}
}

// The browser lists the newest conversation first and marks the active one.
func TestSessionBrowserOrdering(t *testing.T) {
	dir := t.TempDir()
	a := launchApp(t, dir, "")
	if _, err := a.store.Append(session.RoleUser, "first words"); err != nil {
		t.Fatal(err)
	}
	first := a.session
	if _, err := a.newSession(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	// Selecting the first session makes it the most recently active.
	if _, _, err := a.switchSession(first); err != nil {
		t.Fatal(err)
	}
	items, err := a.listSessionNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].ID != first || !items[0].Current {
		t.Errorf("most recent session not first/current: %+v", items[0])
	}
	if items[1].Current {
		t.Errorf("older session marked current: %+v", items[1])
	}
	if items[0].Title == "" || items[1].Title == "" {
		t.Errorf("titles missing: %+v", items)
	}
}

// Renaming changes the display title, persists it and keeps the ID.
func TestRenameSession(t *testing.T) {
	dir := t.TempDir()
	a := launchApp(t, dir, "")
	if _, err := a.store.Append(session.RoleUser, "hi"); err != nil {
		t.Fatal(err)
	}
	old := a.session
	display, err := a.renameSession("Museum of Unfinished Ideas")
	if err != nil {
		t.Fatal(err)
	}
	if display != "Museum of Unfinished Ideas" {
		t.Errorf("display = %q", display)
	}
	if a.session != old {
		t.Errorf("the ID changed: %q -> %q", old, a.session)
	}
	b := launchApp(t, dir, "")
	if b.session != old {
		t.Errorf("restart selected %q, want %q", b.session, old)
	}
	items, err := b.listSessionNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "Museum of Unfinished Ideas" {
		t.Errorf("renamed title not listed: %+v", items)
	}
	history, err := b.store.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Content != "hi" {
		t.Errorf("history changed by rename: %+v", history)
	}
}
