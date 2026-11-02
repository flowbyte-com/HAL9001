package tui

import (
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// dispatchSlash handles a leading-slash command locally. It reports the
// follow-up command to run (nil is fine). Slash input is always consumed
// here and never reaches the engine, so commands are never submitted as
// user messages. A leading double slash (//help) is unescaped by the
// caller and sent as an ordinary message.
func (m *Model) dispatchSlash(input string) tea.Cmd {
	fields := strings.Fields(input)
	cmd, args := fields[0], fields[1:]
	switch cmd {
	case "/help":
		m.view = viewHelp
		return nil
	case "/theme":
		m.enterTheme()
		return nil
	case "/model":
		m.enterPicker()
		return nil
	case "/new":
		m.newSession()
		return nil
	case "/sessions":
		m.enterSessions()
		return nil
	case "/copy":
		if len(args) > 0 && args[0] == "code" {
			return m.copyCmd(true)
		}
		if len(args) > 0 {
			m.notice = "Usage: /copy [code]"
			return nil
		}
		return m.copyCmd(false)
	default:
		m.notice = "Unknown command " + cmd + " — try /help."
		return nil
	}
}

type copyResultMsg struct {
	chars int
	label string
	err   error
}

// copyCmd copies the last assistant response (or its latest code block)
// via the configured clipboard. Copying runs off the event loop; it never
// modifies the conversation.
func (m *Model) copyCmd(codeOnly bool) tea.Cmd {
	text := m.lastResponse
	label := "response"
	if codeOnly {
		block, ok := latestCodeBlock(text)
		if !ok {
			m.notice = "No code block in the last response."
			return nil
		}
		text, label = block, "code block"
	}
	if strings.TrimSpace(text) == "" {
		m.notice = "Nothing to copy yet — no completed response."
		return nil
	}
	if m.hooks.Copy == nil {
		m.notice = "Clipboard unavailable: no copy handler (use terminal-native copy as a fallback)."
		return nil
	}
	copy := m.hooks.Copy
	if m.sender == nil {
		if err := copy(text); err != nil {
			return func() tea.Msg { return copyResultMsg{err: err} }
		}
		return func() tea.Msg { return copyResultMsg{chars: len(text), label: label} }
	}
	return func() tea.Msg {
		go func() {
			if err := copy(text); err != nil {
				m.sender(copyResultMsg{err: err})
				return
			}
			m.sender(copyResultMsg{chars: len(text), label: label})
		}()
		return nil
	}
}

var fencePattern = regexp.MustCompile("(?s)```(?:\\w+)?\n(.*?)```")

// latestCodeBlock extracts the most recent fenced code block.
func latestCodeBlock(text string) (string, bool) {
	matches := fencePattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return "", false
	}
	block := strings.Trim(matches[len(matches)-1][1], "\n")
	if block == "" {
		return "", false
	}
	return block, true
}

func (m Model) updateCopyResult(msg copyResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.notice = "Copy failed: " + msg.err.Error()
		return m, nil
	}
	m.notice = "Copied."
	if msg.label != "" {
		m.notice = "Copied " + msg.label + "."
	}
	return m, nil
}
