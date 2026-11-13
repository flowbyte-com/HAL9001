package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/tui"
)

// Editing the active model through /model edit must change the very
// next request: the persisted record, the rebuilt engine and the wire
// payload all agree, without a restart.
func TestEditModelEffortReachesProviderWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("HAL9001_CONFIG", cfgPath)
	cap := &captureServer{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	store, err := session.Open(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{
		{
			ID: "minimax", Name: "MiniMax", Protocol: config.ProtocolOpenAI,
			BaseURL: srv.URL, CredentialEnv: "MINIMAX_API_KEY",
			Models: []config.ModelRecord{
				{ID: "MiniMax-M3.1-Flash", Effort: "low", ContextMode: config.ContextAuto},
				{ID: "MiniMax-M2", Effort: "max", ContextMode: config.ContextAuto},
			},
		},
	}
	stored.SetActive("minimax", "MiniMax-M3.1-Flash")
	a := &app{
		cfg:     config.Config{StoreDir: t.TempDir()},
		stored:  stored,
		store:   store,
		session: "test",
	}
	a.rebuildEngine()

	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if got := cap.body["reasoning_effort"]; got != "low" {
		t.Fatalf("before edit: reasoning_effort = %v, want low", got)
	}

	if err := a.editModel("minimax", "MiniMax-M3.1-Flash", tui.ModelSpec{Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	// The persisted record changed.
	if got := a.stored.EffortForModel("minimax", "MiniMax-M3.1-Flash"); got != "high" {
		t.Fatalf("persisted effort = %q, want high", got)
	}
	// The live runtime reports the new level.
	if level, supported := a.currentEffort(); !supported || level != "high" {
		t.Fatalf("currentEffort = (%q, %v), want (high, true)", level, supported)
	}
	// The very next request carries it: no restart.
	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if got := cap.body["reasoning_effort"]; got != "high" {
		t.Errorf("after edit: reasoning_effort = %v, want high", got)
	}

	// Switching away and back keeps the saved levels.
	if _, err := a.switchModel("minimax", "MiniMax-M2"); err != nil {
		t.Fatal(err)
	}
	if level, _ := a.currentEffort(); level != "max" {
		t.Fatalf("M2 effort = %q, want max", level)
	}
	if _, err := a.switchModel("minimax", "MiniMax-M3.1-Flash"); err != nil {
		t.Fatal(err)
	}
	if level, _ := a.currentEffort(); level != "high" {
		t.Fatalf("after switch back: effort = %q, want high", level)
	}
	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if got := cap.body["reasoning_effort"]; got != "high" {
		t.Errorf("after switch back: reasoning_effort = %v, want high", got)
	}
}

// An invalid edit never touches the live runtime.
func TestInvalidModelEditKeepsLiveRuntime(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("HAL9001_CONFIG", cfgPath)
	cap := &captureServer{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	store, err := session.Open(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{
		{
			ID: "minimax", Name: "MiniMax", Protocol: config.ProtocolOpenAI,
			BaseURL: srv.URL, CredentialEnv: "MINIMAX_API_KEY",
			Models: []config.ModelRecord{
				{ID: "MiniMax-M3.1-Flash", Effort: "low", ContextMode: config.ContextAuto},
			},
		},
	}
	stored.SetActive("minimax", "MiniMax-M3.1-Flash")
	a := &app{
		cfg:     config.Config{StoreDir: t.TempDir()},
		stored:  stored,
		store:   store,
		session: "test",
	}
	a.rebuildEngine()

	if err := a.editModel("minimax", "MiniMax-M3.1-Flash", tui.ModelSpec{Effort: "turbo"}); err == nil {
		t.Fatal("an unknown effort level was accepted")
	}
	if got := a.stored.EffortForModel("minimax", "MiniMax-M3.1-Flash"); got != "low" {
		t.Errorf("the refused edit changed the record: %q", got)
	}
	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if got := cap.body["reasoning_effort"]; got != "low" {
		t.Errorf("the refused edit changed the wire: %v", got)
	}
}
