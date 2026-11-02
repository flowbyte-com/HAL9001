// Package tui is HAL9001's dedicated terminal interface: a scrollable
// conversation area, a multiline composer with explicit send, and a
// permanent 444 indicator. Presentation only — the conversation core lives
// in internal/engine. There is deliberately no coding IDE, command console
// or dashboard here.
package tui

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sipeed/picoclaw/hal9001/internal/engine"
)

// Engine runs the conversation behind the interface.
type Engine interface {
	Respond(ctx context.Context, input string) (string, error)
}

// Send keys: the composer sends explicitly, Enter always means newline.
const (
	keySend  = "ctrl+s"
	keyQuit  = "ctrl+c"
	keyQuit2 = "ctrl+q"
)

// ColorEnabled reports whether styled output is appropriate. NO_COLOR and
// dumb terminals get plain text: the eye motif and labels never depend on
// ANSI escapes to be legible.
func ColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return strings.ToLower(os.Getenv("TERM")) != "dumb"
}

// Model is the Bubble Tea application state.
type Model struct {
	engine   Engine
	viewport viewport.Model
	composer textarea.Model
	lines    []string
	width    int
	height   int
	ready    bool
	sending  bool
	notice   string
	quitting bool
	color    bool
}

// New builds the interface. Color follows ColorEnabled at construction so
// tests can pin either mode deterministically.
func New(engine Engine) Model {
	ta := textarea.New()
	ta.Placeholder = "Type a message — Enter newline, Ctrl+S send, Ctrl+C quit"
	ta.Focus()
	return Model{engine: engine, composer: ta, color: ColorEnabled()}
}

// SetColor overrides color mode; used by tests.
func (m *Model) SetColor(enabled bool) { m.color = enabled }

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

type responseMsg struct{ text string }
type errorMsg struct{ err error }

// sendCmd runs the engine off the render path.
func (m Model) sendCmd(input string) tea.Cmd {
	return func() tea.Msg {
		text, err := m.engine.Respond(context.Background(), input)
		if err != nil {
			return errorMsg{err: err}
		}
		return responseMsg{text: text}
	}
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		compHeight := 5
		viewHeight := m.height - compHeight - 4
		if viewHeight < 1 {
			viewHeight = 1
		}
		if m.viewport.Width() == 0 {
			m.viewport = viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(viewHeight))
		} else {
			m.viewport.SetWidth(m.width)
			m.viewport.SetHeight(viewHeight)
		}
		m.composer.SetWidth(m.width - 2)
		m.composer.SetHeight(3)
		m.renderConversation()
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		switch key {
		case keyQuit, keyQuit2:
			m.quitting = true
			return m, tea.Quit
		case keySend:
			if m.sending {
				return m, nil
			}
			input := strings.TrimSpace(m.composer.Value())
			if input == "" {
				return m, nil
			}
			m.composer.Reset()
			m.appendLine("You > " + input)
			m.sending = true
			m.notice = "thinking…"
			return m, m.sendCmd(input)
		case "pgup":
			m.viewport.PageUp()
			return m, nil
		case "pgdown":
			m.viewport.PageDown()
			return m, nil
		}
	case responseMsg:
		m.sending = false
		m.notice = ""
		m.appendLine("HAL > " + msg.text)
		return m, nil
	case errorMsg:
		m.sending = false
		if errors.Is(msg.err, engine.ErrNoProvider) {
			m.notice = "No provider configured — message stored in this session."
			return m, nil
		}
		m.notice = "error: " + msg.err.Error()
		m.appendLine("HAL > (I couldn't answer that: " + msg.err.Error() + ")")
		return m, nil
	}
	if m.ready {
		var cmds []tea.Cmd
		var cmd tea.Cmd
		m.composer, cmd = m.composer.Update(msg)
		cmds = append(cmds, cmd)
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}
	// Before the first resize, only the composer exists.
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	return m, cmd
}

func (m *Model) appendLine(line string) {
	m.lines = append(m.lines, line)
	m.renderConversation()
	m.viewport.GotoBottom()
}

func (m *Model) renderConversation() {
	m.viewport.SetContent(strings.Join(m.lines, "\n"))
}

var ansiSequence = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// stripANSI removes ANSI escape sequences for monochrome terminals.
// Bubbles widgets style themselves internally, so the fallback applies at
// the final view: with color disabled, the interface is plain text.
func stripANSI(s string) string {
	return ansiSequence.ReplaceAllString(s, "")
}
func (m Model) heading() string {
	if !m.color {
		return "● HAL9001"
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1")).Render("● HAL9001")
}

func (m Model) statusBar() string {
	left := "Enter newline · Ctrl+S send · Ctrl+C quit · PgUp/PgDn scroll"
	right := "[444 read-only]"
	gap := m.width - len(left) - len(right) - 1
	if gap < 1 {
		gap = 1
	}
	bar := left + strings.Repeat(" ", gap) + right
	if !m.color {
		return bar
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(bar)
}

// View implements tea.Model.
func (m Model) View() tea.View {
	if m.quitting {
		return tea.NewView("Goodbye.\n")
	}
	if !m.ready {
		return tea.NewView("Starting HAL9001…\n")
	}
	var sb strings.Builder
	sb.WriteString(m.heading())
	sb.WriteString("\n\n")
	sb.WriteString(m.viewport.View())
	sb.WriteString("\n")
	sb.WriteString(m.composer.View())
	sb.WriteString("\n")
	sb.WriteString(m.statusBar())
	if m.notice != "" {
		sb.WriteString("\n" + m.notice)
	}
	sb.WriteString("\n")
	out := sb.String()
	if !m.color {
		out = stripANSI(out)
	}
	return tea.NewView(out)
}

// Run launches the interface on the real terminal.
func Run(engine Engine) error {
	_, err := tea.NewProgram(New(engine)).Run()
	return err
}

// Lines returns the conversation lines, for tests.
func (m Model) Lines() []string { return append([]string(nil), m.lines...) }

// Notice returns the current status notice, for tests.
func (m Model) Notice() string { return m.notice }

// Sending reports whether a response is in flight, for tests.
func (m Model) Sending() bool { return m.sending }

// Viewport exposes the scroll state, for tests.
func (m Model) Viewport() viewport.Model { return m.viewport }

// Composer exposes the composer, for tests.
func (m Model) Composer() textarea.Model { return m.composer }

// InjectResponse delivers a canned engine reply, for tests.
func (m *Model) InjectResponse(text string) (tea.Model, tea.Cmd) {
	return m.Update(responseMsg{text: text})
}
