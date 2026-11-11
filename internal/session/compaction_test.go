package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The record of a manual compaction.
//
// The record lives beside the transcript, in a file of its own. The
// transcript is never rewritten: a compaction adds no message to it and
// changes no message in it, so every word the operator and the model
// wrote is still there.

func TestTheCompactionRecordIsStoredBesideTheTranscript(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(RoleUser, "hello", Turn{ID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	record := Compaction{
		Summary:          "## Objective\nship the parser",
		CoveredMessages:  2,
		CoveredExchanges: 1,
		FirstTurnID:      "turn-1",
		LastTurnID:       "turn-1",
		BeforeTokens:     900,
		AfterTokens:      120,
		Limit:            7168,
	}
	if err := st.WriteCompaction(record); err != nil {
		t.Fatal(err)
	}
	got := st.ReadCompaction()
	if got == nil {
		t.Fatal("the record was not read back")
	}
	if got.Summary != record.Summary {
		t.Errorf("the summary = %q", got.Summary)
	}
	if got.CoveredMessages != 2 || got.CoveredExchanges != 1 {
		t.Errorf("the record = %+v", got)
	}
	if got.BeforeTokens != 900 || got.AfterTokens != 120 || got.Limit != 7168 {
		t.Errorf("the record = %+v", got)
	}
	if got.FirstTurnID != "turn-1" || got.LastTurnID != "turn-1" {
		t.Errorf("the provenance = %+v", got)
	}
	if got.Version != CompactionVersion {
		t.Errorf("the version = %d", got.Version)
	}
	// The record is its own file, beside the transcript.
	want := filepath.Join(dir, "chat.compaction.json")
	if st.CompactionPath() != want {
		t.Errorf("the path = %q, want %q", st.CompactionPath(), want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the record file is missing: %v", err)
	}
}

func TestTheCompactionLeavesTheTranscriptUntouched(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", "second", "third"} {
		if _, err := st.AppendTurn(RoleUser, text, Turn{}); err != nil {
			t.Fatal(err)
		}
	}
	path := st.Path()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteCompaction(Compaction{Summary: "a summary", CoveredMessages: 2}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("the transcript changed:\n%q\n%q", before, after)
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("the transcript holds %d messages", len(history))
	}
}

func TestTheCompactionSurvivesAReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteCompaction(Compaction{Summary: "a summary", CoveredMessages: 2}); err != nil {
		t.Fatal(err)
	}
	// A new process opens the same conversation.
	again, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	record := again.ReadCompaction()
	if record == nil {
		t.Fatal("the record did not survive the reopen")
	}
	if record.Summary != "a summary" {
		t.Errorf("the summary = %q", record.Summary)
	}
}

func TestAnUnreadableRecordIsTreatedAsAbsent(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.CompactionPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if record := st.ReadCompaction(); record != nil {
		t.Errorf("the record = %+v, want none", record)
	}
	// A record of an unknown version is ignored rather than guessed at.
	if err := os.WriteFile(st.CompactionPath(), []byte(`{"version":99,"summary":"x","covered_messages":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if record := st.ReadCompaction(); record != nil {
		t.Errorf("the record = %+v, want none", record)
	}
	// A record without a summary is ignored too.
	if err := os.WriteFile(st.CompactionPath(), []byte(`{"version":1,"summary":"  ","covered_messages":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if record := st.ReadCompaction(); record != nil {
		t.Errorf("the record = %+v, want none", record)
	}
}

func TestAnEmptyOrUselessRecordIsRefused(t *testing.T) {
	st, err := Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteCompaction(Compaction{Summary: "  ", CoveredMessages: 2}); err == nil {
		t.Error("an empty summary must be refused")
	}
	if err := st.WriteCompaction(Compaction{Summary: "a summary", CoveredMessages: 0}); err == nil {
		t.Error("a compaction that covers nothing must be refused")
	}
	if err := st.WriteCompaction(Compaction{Summary: "a summary", CoveredMessages: 2, Version: 99}); err == nil {
		t.Error("an unknown version must be refused")
	}
	if record := st.ReadCompaction(); record != nil {
		t.Errorf("the record = %+v, want none", record)
	}
}

func TestTheRecordIsCleared(t *testing.T) {
	st, err := Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteCompaction(Compaction{Summary: "a summary", CoveredMessages: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearCompaction(); err != nil {
		t.Fatal(err)
	}
	if record := st.ReadCompaction(); record != nil {
		t.Errorf("the record = %+v, want none", record)
	}
	// Clearing a record that is not there is not an error.
	if err := st.ClearCompaction(); err != nil {
		t.Errorf("clearing a missing record failed: %v", err)
	}
}

func TestTheRecordIsWrittenAtomically(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(RoleUser, "hello", Turn{}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteCompaction(Compaction{Summary: "a summary", CoveredMessages: 2}); err != nil {
		t.Fatal(err)
	}
	// The write leaves no temporary file behind: a debris file beside
	// the transcript would be picked up as a conversation later.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".compact-") {
			t.Errorf("the write left %q behind", e.Name())
		}
	}
	if len(entries) != 2 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the directory holds %v", names)
	}
}
