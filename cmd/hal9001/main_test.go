package main

import (
	"context"
	"errors"
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
