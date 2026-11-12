package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/provider"
)

func animationModel(t *testing.T) (Model, *time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	m := testModel()
	m.now = func() time.Time { return now }
	m.sender = func(tea.Msg) {}
	return m, &now
}

func startActivity(m Model, now time.Time) Model {
	m.streamID = 1
	m.sending = true
	m.streamStarted = now
	m.beginActivity()
	m.setActivity()
	return m
}

func advance(now *time.Time, d time.Duration) { *now = now.Add(d) }

func TestAnimationFramesAndTransitions(t *testing.T) {
	m, now := animationModel(t)
	m = startActivity(m, *now)
	if m.activityLabel() != "waiting" || !strings.Contains(m.Notice(), "Waiting for model") {
		t.Fatalf("initial activity = %q / %q", m.activityLabel(), m.Notice())
	}
	if !strings.HasPrefix(m.Notice(), "🕛") {
		t.Fatalf("initial frame = %q", m.Notice())
	}
	gen := m.activityGen
	updated, cmd := m.Update(streamThinkingMsg{id: 1})
	m = updated.(Model)
	if m.activityLabel() != "thinking" || !strings.Contains(m.Notice(), "Thinking") {
		t.Fatalf("thinking = %q / %q", m.activityLabel(), m.Notice())
	}
	if cmd != nil {
		t.Fatal("a stream event must not schedule an unrelated command")
	}
	updated, cmd = m.Update(animationTickMsg{gen: gen, turn: 1})
	m = updated.(Model)
	if !strings.HasPrefix(m.Notice(), "🕧") {
		t.Fatalf("tick did not advance the clock: %q", m.Notice())
	}
	if cmd == nil {
		t.Fatal("an animation tick must schedule the next frame")
	}

	updated, _ = m.Update(streamDeltaMsg{id: 1, text: "hello"})
	m = updated.(Model)
	if m.activityLabel() != "streaming" || !strings.Contains(m.Notice(), "Streaming") {
		t.Fatalf("streaming = %q / %q", m.activityLabel(), m.Notice())
	}
	updated, _ = m.Update(streamToolStartMsg{id: 1, activity: engine.ToolActivity{
		CallID: "c1", Name: "bash", Action: "bash", Status: "running", Mode: m.PermissionMode(),
	}})
	m = updated.(Model)
	if m.activityLabel() != "tool" || !strings.Contains(m.Notice(), "💻 bash") ||
		!strings.Contains(m.Notice(), "running") {
		t.Fatalf("tool start = %q / %q", m.activityLabel(), m.Notice())
	}
	updated, _ = m.Update(streamToolMsg{id: 1, activity: engine.ToolActivity{
		CallID: "c1", Name: "bash", Action: "bash", Status: "ok", Mode: m.PermissionMode(),
	}})
	m = updated.(Model)
	if m.activityLabel() != "tool" || !strings.Contains(m.Notice(), "completed") {
		t.Fatalf("tool result = %q / %q", m.activityLabel(), m.Notice())
	}
	updated, _ = m.Update(streamDeltaMsg{id: 1, text: "more"})
	m = updated.(Model)
	if m.activityLabel() != "streaming" || !strings.Contains(m.Notice(), "Streaming") {
		t.Fatalf("tool to streaming = %q / %q", m.activityLabel(), m.Notice())
	}
}

func TestAnimationAdvancesWithoutTokens(t *testing.T) {
	m, now := animationModel(t)
	m = startActivity(m, *now)
	gen := m.activityGen
	first := m.activityFrame
	for i := 0; i < 3; i++ {
		advance(now, animationInterval)
		updated, _ := m.Update(animationTickMsg{gen: gen, turn: 1})
		m = updated.(Model)
	}
	if m.activityFrame == first {
		t.Fatal("the clock did not advance without provider tokens")
	}
	if !strings.Contains(m.Notice(), "0.4s") {
		t.Fatalf("elapsed time did not advance: %q", m.Notice())
	}
}

func TestAnimationNoDataTimer(t *testing.T) {
	m, now := animationModel(t)
	m = startActivity(m, *now)
	m.activityPhase = activityThinking
	m.lastProviderEvent = now.Add(-5 * time.Second)
	if notice := m.activityNotice(); !strings.Contains(notice, "no new data for 5s") {
		t.Fatalf("no-data suffix missing: %q", notice)
	}
	m.lastProviderEvent = now.Add(-4 * time.Second)
	if notice := m.activityNotice(); strings.Contains(notice, "no new data") {
		t.Fatalf("no-data suffix appeared too early: %q", notice)
	}
	m.noteProviderEvent()
	if notice := m.activityNotice(); strings.Contains(notice, "no new data") {
		t.Fatalf("a provider event did not reset the silence timer: %q", notice)
	}

	// A long tool call is activity, not silence.
	m.activityPhase = activityTool
	m.activityTool = engine.ToolActivity{Name: "bash", Action: "bash", Status: "running"}
	m.lastProviderEvent = now.Add(-20 * time.Second)
	if notice := m.activityNotice(); strings.Contains(notice, "no new data") {
		t.Fatalf("tool activity was reported as provider silence: %q", notice)
	}
	if !strings.Contains(m.activityNotice(), "Tool") {
		t.Fatalf("tool phase missing: %q", m.activityNotice())
	}
}

func TestAnimationStopsAtCompletion(t *testing.T) {
	m, now := animationModel(t)
	m = startActivity(m, *now)
	gen := m.activityGen
	advance(now, 9200*time.Millisecond)
	updated, _ := m.Update(responseMsg{id: 1, text: "done"})
	m = updated.(Model)
	if m.activityLabel() != "complete" {
		t.Fatalf("activity after response = %q", m.activityLabel())
	}
	if !strings.Contains(m.Notice(), "✓ Complete · 9.2s") {
		t.Fatalf("completion notice = %q", m.Notice())
	}
	frame := m.activityFrame
	updated, cmd := m.Update(animationTickMsg{gen: gen, turn: 1})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("a stale tick restarted the animation")
	}
	if m.activityFrame != frame || !strings.Contains(m.Notice(), "✓ Complete") {
		t.Fatalf("a stale tick changed the completed line: %q", m.Notice())
	}
}

func TestAnimationStopsAtCancellation(t *testing.T) {
	m, now := animationModel(t)
	m = startActivity(m, *now)
	gen := m.activityGen
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.Sending() || m.activityLabel() != "idle" {
		t.Fatalf("cancel left sending=%v activity=%q", m.Sending(), m.activityLabel())
	}
	if !strings.Contains(m.Notice(), "cancelled") {
		t.Fatalf("cancel notice = %q", m.Notice())
	}
	updated, cmd := m.Update(animationTickMsg{gen: gen, turn: 1})
	m = updated.(Model)
	if cmd != nil || m.activityLabel() != "idle" {
		t.Fatal("a stale tick revived a cancelled turn")
	}
}

func TestAnimationErrorsEndTheClock(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"provider error", errors.New("provider failed"), "error:"},
		{"stream idle", provider.ErrStreamIdle, "went silent"},
		{"turn timeout", provider.ErrTurnTimeout, "time limit"},
		{"cancelled", context.Canceled, "cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, now := animationModel(t)
			m = startActivity(m, *now)
			gen := m.activityGen
			updated, _ := m.Update(errorMsg{id: 1, err: tc.err})
			m = updated.(Model)
			if m.activityLabel() != "idle" {
				t.Fatalf("activity = %q after %v", m.activityLabel(), tc.err)
			}
			if !strings.Contains(m.Notice(), tc.want) {
				t.Fatalf("notice = %q, want %q", m.Notice(), tc.want)
			}
			updated, cmd := m.Update(animationTickMsg{gen: gen, turn: 1})
			m = updated.(Model)
			if cmd != nil || m.activityLabel() != "idle" {
				t.Fatal("a stale tick revived an ended turn")
			}
		})
	}
}

func TestAnimationToggle(t *testing.T) {
	m, now := animationModel(t)
	m.dispatchSlash("/animation")
	if !strings.Contains(m.Notice(), "Animation: on") {
		t.Fatalf("status = %q", m.Notice())
	}
	m.dispatchSlash("/animation off")
	if m.animation || !strings.Contains(m.Notice(), "Animation: off") {
		t.Fatalf("off = animation %v, notice %q", m.animation, m.Notice())
	}
	m = startActivity(m, *now)
	gen := m.activityGen
	if cmd := m.dispatchSlash("/animation off"); cmd != nil {
		t.Fatal("disabling animation scheduled a command")
	}
	if notice := m.activityNotice(); !strings.HasPrefix(notice, "🕛") {
		t.Fatalf("disabled animation did not fall back to a static clock: %q", notice)
	}
	frame := m.activityFrame
	updated, cmd := m.Update(animationTickMsg{gen: gen, turn: 1})
	m = updated.(Model)
	if cmd != nil || m.activityFrame != frame {
		t.Fatal("a tick advanced a disabled animation")
	}
	if cmd := m.dispatchSlash("/animation on"); cmd == nil {
		t.Fatal("enabling animation during a turn did not schedule a frame")
	}
	updated, cmd = m.Update(animationTickMsg{gen: m.activityGen, turn: 1})
	m = updated.(Model)
	if cmd == nil || m.activityFrame == frame {
		t.Fatal("enabled animation did not advance")
	}
}

func TestAnimationNonInteractiveDoesNotSchedule(t *testing.T) {
	m := testModel()
	m.sending = true
	m.streamID = 1
	m.activityPhase = activityStreaming
	if m.animationTickCmd() != nil {
		t.Fatal("a headless model scheduled an animation")
	}
	m.sender = func(tea.Msg) {}
	if m.animationTickCmd() == nil {
		t.Fatal("an interactive model did not schedule an animation")
	}
	m.animation = false
	if m.animationTickCmd() != nil {
		t.Fatal("a disabled model scheduled an animation")
	}
}

func TestAnimationIntervalAndFrames(t *testing.T) {
	if animationInterval < 120*time.Millisecond || animationInterval > 150*time.Millisecond {
		t.Fatalf("animation interval = %s", animationInterval)
	}
	if len(animationFrames) != 24 {
		t.Fatalf("frames = %d, want 24", len(animationFrames))
	}
	seen := map[string]bool{}
	for _, frame := range animationFrames {
		if frame == "" || seen[frame] {
			t.Fatalf("duplicate or empty frame %q", frame)
		}
		seen[frame] = true
	}
	if animationFrames[0] != "🕛" || animationFrames[23] != "🕦" {
		t.Fatalf("frame endpoints = %q / %q", animationFrames[0], animationFrames[23])
	}
}

func TestAnimationSmallTerminal(t *testing.T) {
	m, now := animationModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 9})
	m = updated.(Model)
	m = startActivity(m, *now)
	m.activityPhase = activityStreaming
	m.lastProviderEvent = now.Add(-6 * time.Second)
	m.setActivity()
	if width := lipgloss.Width(m.Notice()); width > 80 {
		t.Fatalf("activity line is %d cells: %q", width, m.Notice())
	}
	rows := m.layoutRows()
	if len(rows) != 9 {
		t.Fatalf("80x9 layout has %d rows", len(rows))
	}
	for _, row := range rows {
		if width := lipgloss.Width(row); width > 80 {
			t.Fatalf("80x9 row is %d cells: %q", width, stripANSI(row))
		}
	}
}

func TestAnimationEyeWidthAndGutter(t *testing.T) {
	m := transcriptModel(t, 80, 24)
	if got := lipgloss.Width("🔴"); got != 2 {
		t.Fatalf("red eye width = %d, want 2", got)
	}
	if got := lipgloss.Width(m.eyePrefix()); got != m.contentColumn() {
		t.Fatalf("eye prefix = %d cells, content column = %d", got, m.contentColumn())
	}
	if m.contentColumn() != 3 {
		t.Fatalf("content column = %d, want 3", m.contentColumn())
	}
	m.SetHistory([]SessionLine{{Role: "assistant", Text: "answer", TurnID: "t1"}})
	row := stripANSI(m.entries[0].rows[0])
	if !strings.HasPrefix(row, "🔴 ") {
		t.Fatalf("assistant row = %q", row)
	}
}

func TestAnimationSessionRestorationResetsClock(t *testing.T) {
	m, now := animationModel(t)
	m = startActivity(m, *now)
	gen := m.activityGen
	m.SetHistory([]SessionLine{{Role: "assistant", Text: "restored", TurnID: "t1"}})
	if m.activityLabel() != "idle" {
		t.Fatalf("restored activity = %q", m.activityLabel())
	}
	if notice := m.activityNotice(); notice != "" {
		t.Fatalf("restored activity notice = %q", notice)
	}
	updated, cmd := m.Update(animationTickMsg{gen: gen, turn: 1})
	m = updated.(Model)
	if cmd != nil || m.activityLabel() != "idle" {
		t.Fatal("restoration allowed an old tick to restart the clock")
	}
}

func TestAnimationFiveThemes(t *testing.T) {
	for _, theme := range Themes() {
		t.Run(theme.Name, func(t *testing.T) {
			m, now := animationModel(t)
			m.SetColor(true)
			m.SetTheme(theme)
			m = startActivity(m, *now)
			m.activityPhase = activityThinking
			m.setActivity()
			if !strings.Contains(stripANSI(m.noticeRow()), "Thinking") {
				t.Fatalf("%s: activity missing: %q", theme.Name, stripANSI(m.noticeRow()))
			}
			rows := m.layoutRows()
			for _, row := range rows {
				if width := lipgloss.Width(row); width > m.width {
					t.Fatalf("%s: row is %d cells", theme.Name, width)
				}
			}
		})
	}
}
