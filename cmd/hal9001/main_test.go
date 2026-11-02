package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// A missing API key must leave the engine without a provider, never with a
// typed nil pointer that would crash on the first request.
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
