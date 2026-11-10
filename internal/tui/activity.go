package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
)

// The clock is a 24-frame Unicode dial. It is a heartbeat, not a
// time-of-day display: one full turn of the dial takes about three
// seconds, whether the provider is thinking, streaming or executing a
// tool.
var animationFrames = [...]string{
	"🕛", "🕧", "🕐", "🕜", "🕑", "🕝", "🕒", "🕞",
	"🕓", "🕟", "🕔", "🕠", "🕕", "🕡", "🕖", "🕢",
	"🕗", "🕣", "🕘", "🕤", "🕙", "🕥", "🕚", "🕦",
}

const (
	// animationInterval is deliberately inside the requested
	// 120-150 ms band. Seven frames a second is smooth without making
	// the event loop do meaningful work.
	animationInterval = 140 * time.Millisecond
	// noDataThreshold is the point at which the quiet suffix appears.
	// A lack of tokens is reported as silence, never as a failed
	// connection: only the provider's own idle and turn timeouts may
	// end a turn.
	noDataThreshold = 5 * time.Second
)

type activityPhase int

const (
	activityIdle activityPhase = iota
	activityWaiting
	activityThinking
	activityStreaming
	activityTool
	activityComplete
)

// animationTickMsg is one frame of the clock. gen identifies the turn's
// scheduling chain, so a tick delivered after completion, cancellation
// or a new turn can never restart the animation.
type animationTickMsg struct {
	gen  int
	turn int
}

// animationTickCmd schedules exactly one frame through Bubble Tea's
// event loop. There is no independent animation goroutine.
func (m Model) animationTickCmd() tea.Cmd {
	if !m.animation || m.sender == nil || !m.sending {
		return nil
	}
	gen, turn := m.activityGen, m.streamID
	return tea.Tick(animationInterval, func(time.Time) tea.Msg {
		return animationTickMsg{gen: gen, turn: turn}
	})
}

// beginActivity starts a fresh turn clock and invalidates any tick from
// the previous turn.
func (m *Model) beginActivity() {
	m.activityGen++
	m.activityPhase = activityWaiting
	m.activityFrame = 0
	m.activityTool = engine.ToolActivity{}
	m.lastProviderEvent = m.clock()
}

// noteProviderEvent records meaningful provider or tool progress. It is
// the only timestamp the silence suffix reads.
func (m *Model) noteProviderEvent() {
	m.lastProviderEvent = m.clock()
}

// endActivity stops the clock and invalidates every tick already in
// flight.
func (m *Model) endActivity() {
	m.activityGen++
	m.activityPhase = activityIdle
	m.activityTool = engine.ToolActivity{}
	m.lastProviderEvent = time.Time{}
}

// finishActivity stops the animation and returns the static completion
// line. The elapsed time is measured from the start of the turn.
func (m *Model) finishActivity() string {
	elapsed := m.clock().Sub(m.streamStarted)
	if elapsed < 0 {
		elapsed = 0
	}
	m.activityGen++
	m.activityPhase = activityComplete
	return "✓ Complete · " + formatActivityElapsed(elapsed)
}

// activityNotice renders the live heartbeat. It is deliberately a
// single line in the already-reserved notice row, so starting or
// stopping it never moves the conversation or the composer.
func (m Model) activityNotice() string {
	switch m.activityPhase {
	case activityIdle:
		return ""
	case activityComplete:
		return "✓ Complete · " + formatActivityElapsed(m.activityElapsed())
	}
	clock := animationFrames[0]
	if m.animation {
		clock = animationFrames[m.activityFrame%len(animationFrames)]
	}
	label := "Waiting for model"
	switch m.activityPhase {
	case activityThinking:
		label = "Thinking"
	case activityStreaming:
		label = "Streaming"
	case activityTool:
		label = "Tool"
	}
	line := clock + " " + label + " · " + formatActivityElapsed(m.activityElapsed())
	silent := m.clock().Sub(m.lastProviderEvent)
	showSilence := !m.lastProviderEvent.IsZero() && silent >= noDataThreshold
	if m.activityPhase == activityTool {
		if tool := m.activityToolLabel(); tool != "" {
			line += " · " + tool
		}
		// A running tool is activity, not provider silence. Once its
		// verified result is in, the following model wait is ordinary
		// silence again.
		if !strings.EqualFold(strings.TrimSpace(m.activityTool.Status), "running") && showSilence {
			line += " · no new data for " + formatActivitySilence(silent)
		}
	} else if showSilence {
		line += " · no new data for " + formatActivitySilence(silent)
	}
	if m.sending {
		line += " · Esc cancels"
	}
	// The messages that are waiting are part of what the operator needs
	// to see while HAL works: they are still theirs, and they are still
	// going to be sent.
	line += m.queueCountNote()
	if m.width > 0 {
		line = truncateCells(line, m.width)
	}
	return line
}

func (m Model) activityElapsed() time.Duration {
	elapsed := m.clock().Sub(m.streamStarted)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func (m Model) activityToolLabel() string {
	name := strings.TrimSpace(m.activityTool.Name)
	if name == "" {
		return ""
	}
	prefix, _ := toolIconPrefix(m.activityTool, m.toolIcons)
	action := strings.TrimSpace(m.activityTool.Action)
	label := prefix + name
	if action != "" && action != name {
		label += " " + action
	}
	if status := strings.TrimSpace(m.activityTool.Status); status != "" {
		label += " · " + toolStatusLabel(status)
	}
	return label
}

func formatActivityElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func formatActivitySilence(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

func animationLabel(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

// activityLabel is used by tests and diagnostics without exposing the
// private phase values.
func (m Model) activityLabel() string {
	switch m.activityPhase {
	case activityWaiting:
		return "waiting"
	case activityThinking:
		return "thinking"
	case activityStreaming:
		return "streaming"
	case activityTool:
		return "tool"
	case activityComplete:
		return "complete"
	default:
		return "idle"
	}
}
