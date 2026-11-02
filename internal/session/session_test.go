package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestRenameNeverTouchesHistoryOrFilesystem(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(RoleUser, "hello"); err != nil {
		t.Fatal(err)
	}
	display, err := RenameSession(dir, "chat", "../../evil")
	if err != nil {
		t.Fatal(err)
	}
	if display != "../../evil" {
		t.Errorf("display = %q", display)
	}
	// Only the sidecar was added; the history file is byte-identical and
	// no new session file appeared.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 {
		t.Fatalf("files = %v, want the session plus its sidecar", names)
	}
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Content != "hello" {
		t.Errorf("history changed: %+v", history)
	}
	// Unicode, spaces and re-renames work; empty falls back to date/time.
	if _, err := RenameSession(dir, "chat", "Museum of Unfinished Ideas 🎭"); err != nil {
		t.Fatal(err)
	}
	if meta, ok := LoadMeta(dir, "chat"); !ok || meta.Title != "Museum of Unfinished Ideas 🎭" {
		t.Errorf("meta = %+v, %v", meta, ok)
	}
	display, err = RenameSession(dir, "chat", "   ")
	if err != nil {
		t.Fatal(err)
	}
	if display == "" || display == "   " {
		t.Errorf("empty title must fall back to a date/time title, got %q", display)
	}
	long := strings.Repeat("x", 200)
	display, err = RenameSession(dir, "chat", long)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(display)); n > MaxTitleRunes {
		t.Errorf("title not bounded: %d runes", n)
	}
	// Renaming an unknown conversation fails instead of creating one.
	if _, err := RenameSession(dir, "nope", "x"); err == nil {
		t.Error("renaming an unknown session must fail")
	}
}

func TestDefaultTitleFormat(t *testing.T) {
	got := DefaultTitle(time.Date(2026, 11, 2, 16, 48, 0, 0, time.UTC))
	if !strings.HasPrefix(got, "2026-11-02 16:") {
		t.Errorf("title = %q", got)
	}
}

func TestStatDerivesTitlesForOldSessions(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(RoleUser, "old question"); err != nil {
		t.Fatal(err)
	}
	info := Stat(dir, "legacy")
	if info.ID != "legacy" {
		t.Errorf("id = %q", info.ID)
	}
	if info.Title == "" || info.Title == "legacy" {
		t.Errorf("old session needs a derived display title, got %q", info.Title)
	}
	if info.Created.IsZero() || info.Updated.IsZero() {
		t.Errorf("timestamps missing: %+v", info)
	}
}
