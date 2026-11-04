package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/skills"
)

// skillEngine answers with a routing decision and a credit, the way the
// engine does when a skill shaped the answer.
type skillEngine struct {
	decision    skills.Decision
	attribution *session.Attribution
	reply       string
	stats       *provider.TurnStats
}

func (e *skillEngine) RespondStream(_ context.Context, _ string, _ func(string)) (string, error) {
	return e.reply, nil
}

func (e *skillEngine) RespondStreamEvents(_ context.Context, _ string, emit func(engine.Event)) (string, error) {
	d := e.decision
	emit(engine.Event{Skill: &d, Attribution: e.attribution, TurnID: "turn-1"})
	if e.stats != nil {
		s := *e.stats
		emit(engine.Event{Stats: &s})
	}
	emit(engine.Event{Text: e.reply})
	return e.reply, nil
}

// humanizeCredit is the credit a turn with the humanize skill carries.
func humanizeCredit() *session.Attribution {
	return &session.Attribution{
		TurnID: "turn-1",
		Source: skills.SourceAuto,
		Skills: []session.AttributedSkill{{
			ID:          "skill:humanize-text-v1.0.1",
			Name:        "humanize-text",
			Source:      skills.SourceAuto,
			Version:     "1.0.1",
			ContentHash: "sha256:0123456789abcdef",
		}},
	}
}

// creditedModel builds an interface whose engine credits the answer with
// a skill.
func creditedModel(t *testing.T, a *session.Attribution) Model {
	t.Helper()
	m := New(&skillEngine{
		decision: skills.Decision{
			Skills:  []string{"humanize-text"},
			Applied: []string{"humanize-text"},
			Source:  skills.SourceAuto,
			Reason:  `matched "email"`,
		},
		attribution: a,
		reply:       "Dear landlord, the boiler is broken.",
	})
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(Model)
}

// TestTheMetadataLineNamesTheSkill checks that the credit appears on
// the reply's metadata line.
func TestTheMetadataLineNamesTheSkill(t *testing.T) {
	m := creditedModel(t, humanizeCredit())
	m = typeText(m, "write an email")
	m = sendCurrent(m)
	view := stripANSI(string(m.View().Content))
	if !strings.Contains(view, "skill: humanize-text") {
		t.Errorf("the metadata line does not name the skill:\n%s", view)
	}
}

// TestTheMetadataLineHasNoSkillLabelWithoutOne checks that an ordinary
// answer carries no skill label.
func TestTheMetadataLineHasNoSkillLabelWithoutOne(t *testing.T) {
	m := creditedModel(t, nil)
	m = typeText(m, "hello")
	m = sendCurrent(m)
	view := stripANSI(string(m.View().Content))
	if strings.Contains(view, "skill:") {
		t.Errorf("an ordinary answer carries a skill label:\n%s", view)
	}
}

// TestTheMetadataLineNamesSeveralSkills checks that a combination is
// named, without pushing the operator towards one.
func TestTheMetadataLineNamesSeveralSkills(t *testing.T) {
	a := &session.Attribution{
		TurnID: "turn-1",
		Skills: []session.AttributedSkill{
			{Name: "humanize-text", Version: "1.0.1"},
			{Name: "system-prompt-writer", Version: "1.0.1"},
		},
	}
	m := creditedModel(t, a)
	m = typeText(m, "write an email")
	m = sendCurrent(m)
	view := stripANSI(string(m.View().Content))
	if !strings.Contains(view, "skills: humanize-text, system-prompt-writer") {
		t.Errorf("the metadata line does not name both skills:\n%s", view)
	}
}

// TestTheCreditIsNotPartOfTheAnswer checks that the credit is metadata
// and never text: it is not in the answer, not in the copied answer and
// not in the model context.
func TestTheCreditIsNotPartOfTheAnswer(t *testing.T) {
	m := creditedModel(t, humanizeCredit())
	m = typeText(m, "write an email")
	m = sendCurrent(m)
	// The entry text is the answer alone.
	entries := m.Entries()
	if len(entries) < 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	answer := entries[len(entries)-1]
	if strings.Contains(answer.text, "humanize-text") {
		t.Errorf("the answer carries the credit: %q", answer.text)
	}
	// The copy command copies the answer, not the metadata.
	copied := ""
	m.hooks.Copy = func(text string) error {
		copied = text
		return nil
	}
	m = typeText(m, "/copy")
	m = enterCurrent(m)
	if strings.Contains(copied, "humanize-text") {
		t.Errorf("the clipboard carries the credit: %q", copied)
	}
	if strings.Contains(copied, "sha256:") {
		t.Errorf("the clipboard carries the hash: %q", copied)
	}
}

// TestTheCreditIsNotSelectable checks that a mouse selection over the
// answer never picks the credit up.
func TestTheCreditIsNotSelectable(t *testing.T) {
	m := creditedModel(t, humanizeCredit())
	// The cell map only exists when the pointer is enabled.
	m.SetMouse(true)
	m = typeText(m, "write an email")
	m = sendCurrent(m)
	rows := m.conv.rows
	last := len(rows) - 1
	if last < 0 {
		t.Fatal("no conversation rows")
	}
	if m.conv.rows[last].logical != decorationRow {
		t.Errorf("the last row is not marked as decoration: %#v", m.conv.rows[last])
	}
	text := m.conv.extract(cellPos{row: 0, cell: 0}, cellPos{row: last, cell: 1000})
	if strings.Contains(text, "humanize-text") {
		t.Errorf("the selection carries the credit: %q", text)
	}
}

// TestTheModelCannotWriteTheCredit checks that text in the answer
// cannot be mistaken for the metadata.
func TestTheModelCannotWriteTheCredit(t *testing.T) {
	m := New(&skillEngine{
		decision: skills.Decision{Source: skills.SourceAuto, Reason: "no skill matched the request"},
		reply:    "Here you go. · skill: made-up-skill",
	})
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "hello")
	m = sendCurrent(m)
	entries := m.Entries()
	answer := entries[len(entries)-1]
	if answer.attribution != nil {
		t.Errorf("the model's text produced a credit: %#v", answer.attribution)
	}
	// The metadata line itself carries no credit.
	meta := m.metaLine(answer.meta, answer.attribution)
	if strings.Contains(meta, "made-up-skill") {
		t.Errorf("the metadata line took the model's word: %q", meta)
	}
}

// TestTheDiagnosticsSeparateTheRoutingStates checks that the four states
// are told apart, and that a failure is never shown as a use.
func TestTheDiagnosticsSeparateTheRoutingStates(t *testing.T) {
	m := creditedModel(t, humanizeCredit())
	m = typeText(m, "write an email")
	m = sendCurrent(m)
	m = typeText(m, "/diag")
	m = enterCurrent(m)
	rows := stripANSI(strings.Join(m.diagRows(), "\n"))
	for _, want := range []string{"candidates", "selected", "activated", "used"} {
		if !strings.Contains(rows, want) {
			t.Errorf("the diagnostics do not report %q:\n%s", want, rows)
		}
	}
	if !strings.Contains(rows, "humanize-text") {
		t.Errorf("the diagnostics do not name the skill:\n%s", rows)
	}
}

// TestTheDiagnosticsShowALoadFailure checks that a skill that could not
// be read is reported as a failure and never as a use.
func TestTheDiagnosticsShowALoadFailure(t *testing.T) {
	m := New(&skillEngine{
		decision: skills.Decision{
			Skills:  []string{"humanize-text"},
			Source:  skills.SourceAuto,
			Reason:  `matched "email" (the skill could not be loaded)`,
			Failure: "the store is unreachable",
		},
		reply: "Answered without the skill.",
	})
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "write an email")
	m = sendCurrent(m)
	m = typeText(m, "/diag")
	m = enterCurrent(m)
	rows := stripANSI(strings.Join(m.diagRows(), "\n"))
	if !strings.Contains(rows, "failure") {
		t.Errorf("the diagnostics do not report the failure:\n%s", rows)
	}
	if !strings.Contains(rows, "the store is unreachable") {
		t.Errorf("the diagnostics do not explain the failure:\n%s", rows)
	}
	// The answer itself is not credited.
	entries := m.Entries()
	if entries[len(entries)-1].attribution != nil {
		t.Errorf("a failed skill was credited: %#v", entries[len(entries)-1].attribution)
	}
}

// TestAReopenedSessionKeepsItsCredit checks that the credit of a stored
// answer is shown again when the conversation is opened.
func TestAReopenedSessionKeepsItsCredit(t *testing.T) {
	m := testModel()
	m.SetHistory([]SessionLine{
		{Role: "user", Text: "write an email"},
		{Role: "assistant", Text: "Dear landlord,", Attribution: humanizeCredit()},
	})
	view := stripANSI(string(m.View().Content))
	if !strings.Contains(view, "skill: humanize-text") {
		t.Errorf("the reopened conversation lost the credit:\n%s", view)
	}
}

// TestALaterUpdateDoesNotChangeTheCredit checks that the credit of a
// stored answer is the one that was recorded, not one looked up again.
func TestALaterUpdateDoesNotChangeTheCredit(t *testing.T) {
	a := humanizeCredit()
	m := testModel()
	m.SetHistory([]SessionLine{
		{Role: "assistant", Text: "Dear landlord,", Attribution: a},
	})
	entries := m.Entries()
	got := entries[0].attribution
	if got == nil {
		t.Fatal("the credit was lost")
	}
	if got.Skills[0].Version != "1.0.1" {
		t.Errorf("version = %q", got.Skills[0].Version)
	}
	if got.Skills[0].ContentHash != "sha256:0123456789abcdef" {
		t.Errorf("hash = %q", got.Skills[0].ContentHash)
	}
}

// failingSkillEngine answers with a routing decision and then fails, the
// way a provider that gives up does.
type failingSkillEngine struct {
	decision skills.Decision
}

func (e *failingSkillEngine) RespondStream(context.Context, string, func(string)) (string, error) {
	return "", errors.New("the provider is down")
}

func (e *failingSkillEngine) RespondStreamEvents(_ context.Context, _ string, emit func(engine.Event)) (string, error) {
	d := e.decision
	emit(engine.Event{Skill: &d, Attribution: humanizeCredit(), TurnID: "turn-1"})
	return "", errors.New("the provider is down")
}

// TestAFailedTurnIsNotCredited checks that a turn which produced no
// answer carries no credit: the skill was loaded, but it never shaped
// an answer.
func TestAFailedTurnIsNotCredited(t *testing.T) {
	m := New(&failingSkillEngine{decision: skills.Decision{
		Skills:  []string{"humanize-text"},
		Applied: []string{"humanize-text"},
		Source:  skills.SourceAuto,
	}})
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "write an email")
	m = sendCurrent(m)
	for _, e := range m.Entries() {
		if e.attribution != nil {
			t.Errorf("a failed turn was credited: %#v", e.attribution)
		}
	}
	// The failure is explained on screen, and no answer carries a
	// credit.
	view := stripANSI(string(m.View().Content))
	if strings.Contains(view, "skill: humanize-text") {
		t.Errorf("the failed turn shows a credit:\n%s", view)
	}
}
