package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/skills"
)

// tuiSkillStore is a read-only skill store for the interface tests.
type tuiSkillStore struct {
	entries []skills.Entry
	bodies  map[string]string
}

func (f *tuiSkillStore) List(context.Context) ([]skills.Entry, error) {
	return f.entries, nil
}

func (f *tuiSkillStore) Read(_ context.Context, name string) (skills.Skill, error) {
	body, ok := f.bodies[name]
	if !ok {
		return skills.Skill{}, skills.ErrUnavailable
	}
	return skills.Skill{Name: name, Version: "1.0.0", Body: body}, nil
}

// skillModel builds an interface with a skill store attached.
func skillModel(t *testing.T) (Model, *skills.Selector) {
	t.Helper()
	store := &tuiSkillStore{
		entries: []skills.Entry{
			{Name: "system-prompt-writer", Version: "1.0.0",
				WhenToUse: `Writing instructions a machine will follow. Triggers: "system prompt".`},
			{Name: "humanize-text", Version: "1.0.0",
				WhenToUse: `Rewriting text a person will read. Triggers: "email", "warmer".`},
		},
		bodies: map[string]string{
			"system-prompt-writer": "# System prompt writer",
			"humanize-text":        "# Humanize text",
		},
	}
	sel := skills.NewSelector(store)
	if err := sel.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := testModel()
	m.SetSkills(sel)
	return m, sel
}

// TestSkillsCommandListsTheCatalogue checks that the operator can see
// what is available.
func TestSkillsCommandListsTheCatalogue(t *testing.T) {
	m, _ := skillModel(t)
	m = typeText(m, "/skills")
	m = enterCurrent(m)
	if m.view != viewSkills {
		t.Fatalf("view = %d, want the skills panel", m.view)
	}
	rows := stripANSI(strings.Join(m.skillsRows(), "\n"))
	for _, want := range []string{"system-prompt-writer", "humanize-text", "1.0.0", "routing"} {
		if !strings.Contains(rows, want) {
			t.Errorf("the panel is missing %q:\n%s", want, rows)
		}
	}
	// Esc leaves the panel.
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.view == viewSkills {
		t.Error("Esc did not leave the panel")
	}
}

// TestSkillCommandPinsAndRestores checks the operator's override.
func TestSkillCommandPinsAndRestores(t *testing.T) {
	m, sel := skillModel(t)
	m = typeText(m, "/skill humanize-text")
	m = enterCurrent(m)
	forced := sel.Forced()
	if len(forced) != 1 || forced[0] != "humanize-text" {
		t.Fatalf("forced = %v", forced)
	}
	if !strings.Contains(m.Notice(), "humanize-text") {
		t.Errorf("notice = %q", m.Notice())
	}
	m = typeText(m, "/skill auto")
	m = enterCurrent(m)
	if len(sel.Forced()) != 0 {
		t.Errorf("the override was not cleared: %v", sel.Forced())
	}
	if !strings.Contains(m.Notice(), "automatically") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// TestSkillCommandRefusesAnUnknownSkill checks that a wrong name is
// reported rather than silently ignored.
func TestSkillCommandRefusesAnUnknownSkill(t *testing.T) {
	m, sel := skillModel(t)
	m = typeText(m, "/skill no-such-skill")
	m = enterCurrent(m)
	if len(sel.Forced()) != 0 {
		t.Errorf("an unknown skill was pinned: %v", sel.Forced())
	}
	if !strings.Contains(m.Notice(), "No such skill") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// TestSkillCommandWithoutAStore checks the no-store path.
func TestSkillCommandWithoutAStore(t *testing.T) {
	m := testModel()
	m = typeText(m, "/skills")
	m = enterCurrent(m)
	if m.view != viewSkills {
		t.Fatalf("view = %d", m.view)
	}
	rows := stripANSI(strings.Join(m.skillsRows(), "\n"))
	if !strings.Contains(rows, "No skill store") {
		t.Errorf("rows:\n%s", rows)
	}
	// Leave the panel before typing the next command.
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	m = typeText(m, "/skill humanize-text")
	m = enterCurrent(m)
	if !strings.Contains(m.Notice(), "No skill store") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// TestDiagShowsTheSkillDecision checks that the selection is observable
// through the diagnostics.
func TestDiagShowsTheSkillDecision(t *testing.T) {
	m, sel := skillModel(t)
	// A decision made by the router, as the engine would report it.
	decision := skills.Decision{
		Skills:  []string{"humanize-text"},
		Applied: []string{"humanize-text"},
		Source:  skills.SourceAuto,
		Reason:  `matched "email"`,
	}
	sel.SetForced(nil)
	m.lastSkill = &decision
	m = typeText(m, "/diag")
	m = enterCurrent(m)
	if m.view != viewDiag {
		t.Fatalf("view = %d, want the diagnostics panel", m.view)
	}
	rows := stripANSI(strings.Join(m.diagRows(), "\n"))
	if !strings.Contains(rows, "humanize-text") {
		t.Errorf("the diagnostics do not name the skill:\n%s", rows)
	}
	if !strings.Contains(rows, "auto") {
		t.Errorf("the diagnostics do not name the source:\n%s", rows)
	}
}

// TestTheSkillsPanelIsNotPartOfTheConversation checks that HAL shows no
// permanent skill badge: the panel is only a place to look.
func TestTheSkillsPanelIsNotPartOfTheConversation(t *testing.T) {
	m, _ := skillModel(t)
	m = typeText(m, "hello")
	m = sendCurrent(m)
	conversation := stripANSI(strings.Join(m.Lines(), "\n"))
	for _, leak := range []string{"humanize-text", "system-prompt-writer", "MPM skill"} {
		if strings.Contains(conversation, leak) {
			t.Errorf("the conversation shows %q:\n%s", leak, conversation)
		}
	}
}
