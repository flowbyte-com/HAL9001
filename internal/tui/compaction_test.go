package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
)

// The manual compaction at the interface.
//
// The command is a local one: it runs when the operator asks for it and
// at no other time. These tests watch what the operator is told and
// what the conversation looks like afterwards.

// compactEngineDouble is an engine that compacts on demand.
type compactEngineDouble struct {
	scriptEngine
	report engine.CompactionReport
	err    error
	calls  int
}

func (c *compactEngineDouble) Compact(_ context.Context, _ func(engine.Event)) (engine.CompactionReport, error) {
	c.calls++
	if c.err != nil {
		return engine.CompactionReport{}, c.err
	}
	return c.report, nil
}

// compactModel builds an interface with a compaction-capable engine.
func compactModel(t *testing.T, double *compactEngineDouble) Model {
	t.Helper()
	m := New(double)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return updated.(Model)
}

// runCompact types the command and runs it.
func runCompact(m Model, command string) Model {
	m = typeText(m, command)
	return sendCurrent(m)
}

func TestTheCompactionCommandIsRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, c := range commandTable {
		names[c.name] = true
	}
	if !names["/compact"] {
		t.Error("/compact is not in the command table")
	}
}

func TestTheCompactionRunsWhenTheOperatorAsks(t *testing.T) {
	double := &compactEngineDouble{report: engine.CompactionReport{
		CoveredExchanges: 9, KeptExchanges: 2,
		BeforeTokens: 12400, AfterTokens: 5100, Limit: 7168,
		Summary: "## Objective\nship the parser",
	}}
	m := compactModel(t, double)
	m = runCompact(m, "/compact")
	if double.calls != 1 {
		t.Fatalf("the engine was asked %d times", double.calls)
	}
	// The operator is told what happened, with both numbers.
	if !strings.Contains(m.notice, "compacted") {
		t.Errorf("the notice = %q", m.notice)
	}
	if !strings.Contains(m.notice, "12400") || !strings.Contains(m.notice, "5100") {
		t.Errorf("the notice must carry both estimates: %q", m.notice)
	}
	// The conversation shows the budget and what the compaction means.
	joined := strings.Join(m.Lines(), "\n")
	if !strings.Contains(joined, "7168") {
		t.Errorf("the conversation must carry the budget:\n%s", joined)
	}
	if !strings.Contains(joined, "summarised rather than dropped") {
		t.Errorf("the conversation must say what the compaction means:\n%s", joined)
	}
	// The conversation shows what the model now receives.
	if !strings.Contains(joined, "Context compacted") {
		t.Errorf("the conversation must say what was compacted:\n%s", joined)
	}
	if !strings.Contains(joined, "ship the parser") {
		t.Errorf("the conversation must show the summary:\n%s", joined)
	}
}

func TestTheCompactionNeverRunsOnItsOwn(t *testing.T) {
	double := &compactEngineDouble{}
	m := compactModel(t, double)
	m = runCompact(m, "hello there")
	if double.calls != 0 {
		t.Errorf("the engine was asked %d times without an explicit command", double.calls)
	}
}

func TestAFailedCompactionLeavesTheConversationUnchanged(t *testing.T) {
	double := &compactEngineDouble{err: errors.New("the provider refused")}
	m := compactModel(t, double)
	m = runCompact(m, "/compact")
	if !strings.Contains(m.notice, "unchanged") {
		t.Errorf("the notice = %q", m.notice)
	}
	joined := strings.Join(m.Lines(), "\n")
	if !strings.Contains(joined, "was not compacted") {
		t.Errorf("the conversation must say what happened:\n%s", joined)
	}
}

func TestACancelledCompactionSaysTheConversationIsUnchanged(t *testing.T) {
	double := &compactEngineDouble{err: context.Canceled}
	m := compactModel(t, double)
	m = runCompact(m, "/compact")
	if !strings.Contains(m.notice, "cancelled") {
		t.Errorf("the notice = %q", m.notice)
	}
	if !strings.Contains(m.notice, "unchanged") {
		t.Errorf("the notice = %q", m.notice)
	}
}

func TestAnEngineWithoutCompactionSaysSo(t *testing.T) {
	m := New(&scriptEngine{})
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m = runCompact(m, "/compact")
	if !strings.Contains(m.notice, "cannot compact") {
		t.Errorf("the notice = %q", m.notice)
	}
}

func TestTheCompactionIsRefusedWhileHALIsWorking(t *testing.T) {
	double := &compactEngineDouble{}
	m := compactModel(t, double)
	m.sending = true
	m = runCompact(m, "/compact")
	if double.calls != 0 {
		t.Errorf("the engine was asked %d times while HAL was working", double.calls)
	}
	if !strings.Contains(m.notice, "stop the turn first") {
		t.Errorf("the notice = %q", m.notice)
	}
}

func TestTheCompactionTakesArgumentsAway(t *testing.T) {
	double := &compactEngineDouble{}
	m := compactModel(t, double)
	m = runCompact(m, "/compact now")
	if double.calls != 0 {
		t.Errorf("the engine was asked %d times", double.calls)
	}
	if !strings.Contains(m.notice, "Usage: /compact") {
		t.Errorf("the notice = %q", m.notice)
	}
}
