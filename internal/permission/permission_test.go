package permission

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDefaultIsGuarded(t *testing.T) {
	c := New()
	if got := c.Mode(); got != Guarded {
		t.Fatalf("mode = %v, want 444", got)
	}
	if _, _, err := c.Begin(context.Background(), "bash"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("guarded Begin error = %v", err)
	}
}

func TestEnableRequiresABoundSession(t *testing.T) {
	c := New()
	if err := c.Enable(""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Enable(\"\") = %v", err)
	}
	if c.Mode() != Guarded {
		t.Fatal("an unbound enable changed the mode")
	}
	c.SetSession("one")
	if err := c.Enable(""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Enable(\"\") on a bound controller = %v", err)
	}
	if c.Mode() != Guarded {
		t.Fatal("an empty session enabled 666")
	}
}

func TestEnableIsSessionBound(t *testing.T) {
	c := New()
	c.SetSession("one")
	if err := c.Enable("two"); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("enable for another session = %v", err)
	}
	if c.Mode() != Guarded {
		t.Fatal("a refused enable changed the mode")
	}
	if err := c.Enable("one"); err != nil {
		t.Fatal(err)
	}
	if c.Mode() != Unrestricted {
		t.Fatal("enable did not switch to 666")
	}
}

func TestSessionChangeAlwaysReturnsTo444(t *testing.T) {
	c := New()
	c.SetSession("one")
	if err := c.Enable("one"); err != nil {
		t.Fatal(err)
	}
	c.SetSession("two")
	if c.Mode() != Guarded {
		t.Fatal("a new conversation inherited 666")
	}
	if err := c.Enable("one"); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("the old session could re-enable itself: %v", err)
	}
}

func TestDisableCancelsActiveOperations(t *testing.T) {
	c := New()
	c.SetSession("one")
	if err := c.Enable("one"); err != nil {
		t.Fatal(err)
	}
	ctx, done, err := c.Begin(context.Background(), "bash")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if c.Active() != 1 {
		t.Fatalf("active = %d", c.Active())
	}
	c.Disable()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("revoking 666 did not cancel the running operation")
	}
	if c.Active() != 0 {
		t.Fatalf("active after disable = %d", c.Active())
	}
	if _, _, err := c.Begin(context.Background(), "bash"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("a call after disable = %v", err)
	}
}

func TestBeginAndDisableDoNotRace(t *testing.T) {
	c := New()
	c.SetSession("one")
	if err := c.Enable("one"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			ctx, done, err := c.Begin(ctx, "read")
			if err == nil {
				defer done()
				<-ctx.Done()
			}
		}()
		go func() {
			defer wg.Done()
			c.Disable()
			c.Enable("one")
		}()
	}
	// Disable before waiting: a goroutine may have entered Begin after the
	// last iteration's own Enable. The final revocation unblocks it.
	c.Disable()
	wg.Wait()
	if c.Active() != 0 {
		t.Fatalf("active = %d after all operations ended", c.Active())
	}
}

func TestEnableAgainCancelsThePreviousWindow(t *testing.T) {
	c := New()
	c.SetSession("one")
	if err := c.Enable("one"); err != nil {
		t.Fatal(err)
	}
	ctx, done, err := c.Begin(context.Background(), "bash")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if err := c.Enable("one"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("re-enabling left the previous operation running")
	}
}

func TestValidDoesNotReplaceBegin(t *testing.T) {
	c := New()
	c.SetSession("one")
	if c.Valid(context.Background()) {
		t.Fatal("Valid authorized without Begin")
	}
	if err := c.Enable("one"); err != nil {
		t.Fatal(err)
	}
	ctx, done, err := c.Begin(context.Background(), "write")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if !c.Valid(ctx) {
		t.Fatal("a live 666 operation was reported invalid")
	}
	c.Disable()
	if c.Valid(ctx) {
		t.Fatal("a cancelled operation was reported valid")
	}
}

func TestNilControllerIsSafe(t *testing.T) {
	var c *Controller
	if c.Mode() != Guarded {
		t.Fatal("nil controller must be 444")
	}
	if _, _, err := c.Begin(context.Background(), "x"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("nil Begin = %v", err)
	}
	c.Disable()
	c.SetSession("x")
}
