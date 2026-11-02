package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendAndHistory(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(RoleUser, "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(RoleAssistant, "hi there"); err != nil {
		t.Fatal(err)
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("got %d messages, want 2", len(history))
	}
	if history[0].Role != RoleUser || history[0].Content != "hello" {
		t.Errorf("first message wrong: %+v", history[0])
	}
	if history[1].Role != RoleAssistant || history[1].Content != "hi there" {
		t.Errorf("second message wrong: %+v", history[1])
	}
	if history[0].Timestamp.IsZero() {
		t.Error("messages must carry timestamps")
	}
}

func TestMissingSessionIsEmpty(t *testing.T) {
	st, err := Open(t.TempDir(), "new")
	if err != nil {
		t.Fatal(err)
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Errorf("missing session must read as empty, got %v", history)
	}
}

func TestRejectsBadInput(t *testing.T) {
	st, err := Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(RoleUser, "   "); err == nil {
		t.Error("empty content must be rejected")
	}
	if _, err := st.Append("wizard", "hi"); err == nil {
		t.Error("unknown role must be rejected")
	}
	for _, bad := range []string{"", "../escape", "a/b"} {
		if _, err := Open(t.TempDir(), bad); err == nil {
			t.Errorf("session name %q must be rejected", bad)
		}
	}
}

func TestCorruptionFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat.jsonl"), []byte("{broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.History(); err == nil {
		t.Error("corrupt lines must fail, never silently absorb")
	} else if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error should locate the bad line, got: %v", err)
	}
}
