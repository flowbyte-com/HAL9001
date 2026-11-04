package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheTurnIdentityIsStored checks that both sides of an exchange
// carry the same identity.
func TestTheTurnIdentityIsStored(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	id := NewTurnID()
	if _, err := store.AppendTurn(RoleUser, "hello", Turn{ID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendTurn(RoleAssistant, "hi there", Turn{ID: id}); err != nil {
		t.Fatal(err)
	}
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("messages = %d", len(history))
	}
	for _, m := range history {
		if m.TurnID != id {
			t.Errorf("turn id = %q, want %q", m.TurnID, id)
		}
	}
}

// TestNewTurnIDIsUnique checks that two exchanges never share an
// identity.
func TestNewTurnIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewTurnID()
		if id == "" {
			t.Fatal("the turn identity is empty")
		}
		if seen[id] {
			t.Fatalf("the turn identity repeated: %q", id)
		}
		seen[id] = true
	}
}

// TestTheAttributionIsStoredBesideTheContent checks that the credit for
// a skill is kept apart from the answer it belongs to.
func TestTheAttributionIsStoredBesideTheContent(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	id := NewTurnID()
	turn := Turn{
		ID: id,
		Attribution: &Attribution{
			TurnID: id,
			Source: "auto",
			Skills: []AttributedSkill{{
				ID:          "skill:humanize-text-v1.0.1",
				Name:        "humanize-text",
				Source:      "auto",
				Version:     "1.0.1",
				ContentHash: "sha256:abc123",
			}},
		},
	}
	if _, err := store.AppendTurn(RoleAssistant, "Dear landlord,", turn); err != nil {
		t.Fatal(err)
	}
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	msg := history[0]
	if msg.Content != "Dear landlord," {
		t.Errorf("the content changed: %q", msg.Content)
	}
	if strings.Contains(msg.Content, "humanize-text") {
		t.Error("the attribution leaked into the content")
	}
	if msg.Attribution == nil {
		t.Fatal("the attribution was not stored")
	}
	if len(msg.Attribution.Skills) != 1 {
		t.Fatalf("skills = %#v", msg.Attribution.Skills)
	}
	s := msg.Attribution.Skills[0]
	if s.Name != "humanize-text" || s.Version != "1.0.1" || s.ContentHash != "sha256:abc123" {
		t.Errorf("skill = %#v", s)
	}
	if msg.Attribution.TurnID != id {
		t.Errorf("the attribution names another turn: %q", msg.Attribution.TurnID)
	}
}

// TestTheAttributionSurvivesReopening checks that a stored credit is
// still there when the conversation is opened again.
func TestTheAttributionSurvivesReopening(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	id := NewTurnID()
	_, err = store.AppendTurn(RoleAssistant, "Here you go.", Turn{
		ID: id,
		Attribution: &Attribution{
			TurnID: id,
			Skills: []AttributedSkill{{Name: "humanize-text", Version: "1.0.1"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A second store over the same directory is a reopened conversation.
	reopened, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopened.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("messages = %d", len(history))
	}
	if history[0].Attribution == nil {
		t.Fatal("the attribution was lost")
	}
	if history[0].Attribution.Skills[0].Name != "humanize-text" {
		t.Errorf("skill = %#v", history[0].Attribution.Skills[0])
	}
}

// TestAnOlderSessionStillReads checks that a conversation written before
// attributions existed still opens: the new fields are optional.
func TestAnOlderSessionStillReads(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "old.jsonl")
	line := `{"role":"assistant","content":"an old answer","ts":"2026-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir, "old")
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Content != "an old answer" {
		t.Fatalf("history = %#v", history)
	}
	if history[0].Attribution != nil {
		t.Error("an old message has an attribution")
	}
	if history[0].TurnID != "" {
		t.Error("an old message has a turn identity")
	}
}

// TestTheStoredRecordIsPlainJSON checks that the attribution is stored
// as data, not as something the reader has to interpret.
func TestTheStoredRecordIsPlainJSON(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	id := NewTurnID()
	_, err = store.AppendTurn(RoleAssistant, "answer", Turn{
		ID:          id,
		Attribution: &Attribution{TurnID: id, Skills: []AttributedSkill{{Name: "x"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["attribution"]; !ok {
		t.Errorf("the record has no attribution field: %s", raw)
	}
	if probe["content"] != "answer" {
		t.Errorf("content = %v", probe["content"])
	}
}

// TestEmptyContentIsStillRefused checks that the turn identity does not
// make padding acceptable.
func TestEmptyContentIsStillRefused(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendTurn(RoleAssistant, "   ", Turn{ID: "turn-1"}); err == nil {
		t.Error("empty content was stored")
	}
}
