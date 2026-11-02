package main

import "testing"

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
