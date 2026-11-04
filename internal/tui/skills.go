package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/skills"
)

// The skills panel is deliberately small: it is a place to look, not a
// place to live. HAL shows no skill badge in the conversation and no
// permanent status panel; the operator asks for this view when they
// want to know what is available.

// updateSkills handles the skills panel. Esc leaves it, as it leaves
// every other panel.
func (m Model) updateSkills(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, m.quit()
		case "esc":
			m.view = viewChat
			return m, nil
		}
	}
	return m, nil
}

// skillsRows renders the available skills and the current selection.
func (m Model) skillsRows() []string {
	st := m.theme.styles()
	rows := []string{"Skills (Esc back)", ""}
	if m.skills == nil {
		rows = append(rows, "  No skill store is configured.")
		return rows
	}
	catalogue := m.skills.Catalogue()
	if catalogue.Err != nil && !catalogue.Loaded {
		rows = append(rows, "  The skill store is not available: "+catalogue.Err.Error())
		rows = append(rows, "")
		rows = append(rows, "  HAL continues without skills.")
		return rows
	}
	if len(catalogue.Entries) == 0 {
		rows = append(rows, "  The skill store holds no skills.")
		return rows
	}
	rows = append(rows, "  "+st.active.Render("available"))
	for _, e := range catalogue.Entries {
		// The name is padded to a column, but a name longer than the
		// column keeps a space of its own so the version never runs
		// into it.
		rows = append(rows, "    "+padTo(st.footer.Render(e.Name), 28)+e.Version)
		if e.WhenToUse != "" {
			rows = append(rows, "      "+st.footer.Render(truncate(e.WhenToUse, 72)))
		}
	}
	forced := m.skills.Forced()
	rows = append(rows, "")
	if len(forced) > 0 {
		rows = append(rows, "  "+st.active.Render("pinned")+"  "+strings.Join(forced, ", "))
	} else {
		rows = append(rows, "  "+st.active.Render("routing")+"  automatic")
	}
	if m.lastSkill != nil {
		d := m.lastSkill
		rows = append(rows, "  "+st.active.Render("last turn"))
		if len(d.Applied) > 0 {
			rows = append(rows, "    skill   "+strings.Join(d.Applied, ", "))
		} else if len(d.Skills) > 0 {
			rows = append(rows, "    skill   "+strings.Join(d.Skills, ", ")+" (not loaded)")
		} else {
			rows = append(rows, "    skill   none")
		}
		rows = append(rows, "    source  "+d.Source)
		if d.Reason != "" {
			rows = append(rows, "    reason  "+d.Reason)
		}
	}
	rows = append(rows, "")
	rows = append(rows, "  "+st.footer.Render("/skill <name> pins a skill · /skill auto restores routing"))
	return rows
}

// skillCmd handles /skill.
//
// Without an argument it reports the current state. "auto" restores
// automatic routing. A name pins that skill for the writing task ahead,
// until it is cleared or another conversation starts.
func (m *Model) skillCmd(args []string) tea.Cmd {
	if m.skills == nil {
		m.notice = "No skill store is configured."
		return nil
	}
	if len(args) == 0 {
		forced := m.skills.Forced()
		if len(forced) > 0 {
			m.notice = "Pinned skill: " + strings.Join(forced, ", ")
		} else {
			m.notice = "Skills are routed automatically — /skill <name> pins one."
		}
		return nil
	}
	name := strings.TrimSpace(args[0])
	if strings.EqualFold(name, "auto") {
		m.skills.ClearForced()
		m.notice = "Skills are routed automatically again."
		return nil
	}
	// A comma-separated list is an explicit request for a combination.
	names := strings.Split(name, ",")
	if err := m.skills.SetForced(names); err != nil {
		m.notice = "No such skill — /skills lists what is available."
		return nil
	}
	m.notice = "Pinned skill: " + strings.Join(m.skills.Forced(), ", ")
	return nil
}

// truncate shortens a string to at most n characters.
func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// lastRouting returns the routing decision of the latest turn.
//
// The decision the engine reported wins: it is the one that was actually
// applied. The selector's own decision is the fallback, for the turns
// that never reached the provider.
func (m Model) lastRouting() (skills.Decision, bool) {
	if m.lastSkill != nil {
		return *m.lastSkill, true
	}
	if m.skills != nil {
		d := m.skills.LastDecision()
		if d.Source != "" {
			return d, true
		}
	}
	return skills.Decision{}, false
}

// skillSummary renders the routing decision as one line, for the
// diagnostics view.
func skillSummary(d skills.Decision) string {
	if d.Source == "" {
		return "not routed"
	}
	if len(d.Applied) > 0 {
		return fmt.Sprintf("%s (%s)", strings.Join(d.Applied, ", "), d.Source)
	}
	if len(d.Skills) > 0 {
		return fmt.Sprintf("%s (%s, not loaded)", strings.Join(d.Skills, ", "), d.Source)
	}
	return fmt.Sprintf("none (%s)", d.Source)
}
