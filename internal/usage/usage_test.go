package usage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddTurnAccumulatesWithoutDoubleCountingReasoning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddTurn("chat", Turn{Input: 100, Output: 50, Reasoning: 30, Cached: 10}, true); err != nil {
		t.Fatal(err)
	}
	if err := store.AddTurn("chat", Turn{Input: 200, Output: 80, Reasoning: 40, Cached: 20}, true); err != nil {
		t.Fatal(err)
	}
	got := store.Get("chat")
	if got.Input != 300 || got.Output != 130 {
		t.Errorf("totals = %+v", got)
	}
	// Reasoning is a subset of the completion tokens; it must never be
	// added to the output total.
	if got.Reasoning != 70 {
		t.Errorf("reasoning = %d, want 70", got.Reasoning)
	}
	if got.Cached != 30 {
		t.Errorf("cached = %d, want 30", got.Cached)
	}
	if got.Turns != 2 || got.Incomplete != 0 {
		t.Errorf("turns = %d/%d", got.Turns, got.Incomplete)
	}
	if got.Last.Input != 200 || got.Last.Output != 80 || got.Last.Reasoning != 40 {
		t.Errorf("last turn = %+v", got.Last)
	}
}

func TestUnreportedTurnIsIncompleteNotInvented(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddTurn("chat", Turn{}, false); err != nil {
		t.Fatal(err)
	}
	got := store.Get("chat")
	if got.Input != 0 || got.Output != 0 || got.Reasoning != 0 {
		t.Errorf("tokens invented for an unreported turn: %+v", got)
	}
	if got.Incomplete != 1 || got.Turns != 0 {
		t.Errorf("counters = %+v", got)
	}
	if !got.Total() {
		t.Error("an incomplete turn is still a record")
	}
}

func TestPersistenceAndIsolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddTurn("a", Turn{Input: 10, Output: 5}, true); err != nil {
		t.Fatal(err)
	}
	if err := store.AddTurn("b", Turn{Input: 7, Output: 3}, true); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get("a"); got.Input != 10 || got.Output != 5 {
		t.Errorf("session a = %+v", got)
	}
	if got := reopened.Get("b"); got.Input != 7 || got.Output != 3 {
		t.Errorf("session b = %+v", got)
	}
	if got := reopened.Get("missing"); got.Total() {
		t.Errorf("unknown session has data: %+v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("usage file mode = %o, want 600", perm)
	}
}

func TestCorruptFileIsPreservedAndIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("a corrupt usage file must not be fatal: %v", err)
	}
	if got := store.Get("chat"); got.Total() {
		t.Errorf("corrupt data survived: %+v", got)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Error("the corrupt file was not preserved for inspection")
	}
	// The store still works after the reset.
	if err := store.AddTurn("chat", Turn{Input: 1, Output: 2}, true); err != nil {
		t.Fatal(err)
	}
	if got := Open2(t, path).Get("chat"); got.Input != 1 || got.Output != 2 {
		t.Errorf("after recovery = %+v", got)
	}
}

func Open2(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMissingFileStartsEmpty(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get("chat"); got.Total() {
		t.Errorf("missing file produced data: %+v", got)
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	var store *Store
	if got := store.Get("chat"); got.Total() {
		t.Error("nil store returned data")
	}
	if err := store.AddTurn("chat", Turn{Input: 1}, true); err != nil {
		t.Errorf("nil store add: %v", err)
	}
}
