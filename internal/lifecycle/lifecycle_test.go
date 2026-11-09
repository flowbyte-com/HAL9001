package lifecycle

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// The states an operator can act on.
//
// The three rules the package exists for: a stop is not a failure, a
// stop is not a completion, and still running is still running.
func TestTheStatesAreDistinct(t *testing.T) {
	if StateStopped == StateFailed || StateStopped == StateCompleted {
		t.Fatal("a stopped operation must not look like a failed or a completed one")
	}
	if StateCompleted.Finished() != true || StateStopped.Finished() != true ||
		StateFailed.Finished() != true {
		t.Error("every state but running and idle is finished")
	}
	if StateRunning.Finished() {
		t.Error("a running operation is not finished")
	}
	if !StateRunning.Running() {
		t.Error("a running operation is running")
	}
	if StateStopped.Running() {
		t.Error("a stopped operation is not running")
	}
}

// An operation that begins is current, and it is the only one.
func TestBeginStartsTheCurrentOperation(t *testing.T) {
	m := New()
	op := m.Begin("turn")
	if op.State != StateRunning {
		t.Fatalf("state = %q, want running", op.State)
	}
	if op.ID == "" {
		t.Fatal("the operation has no identity")
	}
	current := m.Current()
	if current.ID != op.ID {
		t.Fatalf("current = %q, want %q", current.ID, op.ID)
	}
	if m.Last().ID != "" {
		t.Fatal("nothing has finished yet")
	}
	// A second operation replaces the first: HAL9001 runs one exchange
	// at a time.
	second := m.Begin("turn")
	if second.ID == op.ID {
		t.Fatal("two operations share an identity")
	}
	if m.Current().ID != second.ID {
		t.Fatalf("current = %q, want %q", m.Current().ID, second.ID)
	}
}

// The three endings are kept apart.
func TestTheEndingsAreKeptApart(t *testing.T) {
	cases := []struct {
		name  string
		end   func(m *Manager, id string)
		state State
	}{
		{"completed", func(m *Manager, id string) { m.Complete(id) }, StateCompleted},
		{"stopped", func(m *Manager, id string) { m.Stop(id, "the operator stopped it") }, StateStopped},
		{"failed", func(m *Manager, id string) { m.Fail(id, "the provider refused") }, StateFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			op := m.Begin("turn")
			c.end(m, op.ID)
			last := m.Last()
			if last.State != c.state {
				t.Fatalf("state = %q, want %q", last.State, c.state)
			}
			if last.Ended.IsZero() {
				t.Error("a finished operation has no end")
			}
			if m.Current().ID != "" {
				t.Error("nothing is running any more")
			}
			if c.state != StateCompleted && !strings.Contains(last.Status(), string(c.state)) {
				t.Errorf("status = %q, want the state named", last.Status())
			}
		})
	}
}

// A report about an operation that is not current changes nothing.
//
// A late report about a turn that was already replaced must not
// overwrite the state of the turn that is running now.
func TestALateReportChangesNothing(t *testing.T) {
	m := New()
	first := m.Begin("turn")
	second := m.Begin("turn")
	m.Complete(first.ID)
	if m.Current().ID != second.ID {
		t.Fatalf("current = %q, want %q", m.Current().ID, second.ID)
	}
	if m.Current().State != StateRunning {
		t.Fatalf("the running operation was overwritten: %#v", m.Current())
	}
	if m.Last().ID != "" {
		t.Fatal("a late report was recorded as the last operation")
	}
}

// The status names the state, and the current operation wins.
func TestTheStatusNamesTheState(t *testing.T) {
	m := New()
	if m.Status() != string(StateIdle) {
		t.Fatalf("status = %q, want idle", m.Status())
	}
	op := m.Begin("turn")
	m.Steps(op.ID, 3)
	if !strings.Contains(m.Status(), "running") {
		t.Errorf("status = %q, want it to say running", m.Status())
	}
	if !strings.Contains(m.Status(), "3 operation") {
		t.Errorf("status = %q, want the step count", m.Status())
	}
	m.Stop(op.ID, "the operator stopped it")
	if !strings.Contains(m.Status(), "stopped") {
		t.Errorf("status = %q, want it to say stopped", m.Status())
	}
	if !strings.Contains(m.Status(), "the operator stopped it") {
		t.Errorf("status = %q, want the reason", m.Status())
	}
	// A running operation is reported over the last finished one.
	next := m.Begin("turn")
	if !strings.Contains(m.Status(), "running") {
		t.Errorf("status = %q, want the running operation", m.Status())
	}
	m.Complete(next.ID)
}

// The manager is safe for concurrent use: the request path and the
// interface's own thread both touch it.
func TestTheManagerIsSafeForConcurrentUse(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				op := m.Begin("turn")
				m.Steps(op.ID, 1)
				m.Status()
				m.Current()
				m.Last()
				m.Complete(op.ID)
			}
		}()
	}
	wg.Wait()
	if m.Current().ID != "" {
		t.Fatal("something is still running")
	}
	if m.Last().ID == "" {
		t.Fatal("nothing was recorded")
	}
}

// The clock of the manager is the one it was given, so a test can
// place the two ends of an operation.
func TestTheOperationCarriesItsTimes(t *testing.T) {
	m := New()
	now := time.Now()
	m.now = func() time.Time { return now }
	op := m.Begin("turn")
	if !op.Started.Equal(now) {
		t.Fatalf("started = %v, want %v", op.Started, now)
	}
	m.Complete(op.ID)
	if !m.Last().Ended.Equal(now) {
		t.Fatalf("ended = %v, want %v", m.Last().Ended, now)
	}
}
