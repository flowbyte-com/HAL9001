package tui

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Show Thinking is a presentation control.
//
// It says whether the reasoning a provider exposed is shown. It never
// asks for more or less reasoning: the effort level is a separate
// choice, and turning the visibility on or off leaves it exactly as it
// was. It also never changes what the provider is told, so a turn that
// was already running is not rewritten.
//
// The reasoning shown is the provider's own. A provider that exposes
// none shows nothing at all: there is no placeholder, because a
// placeholder would be text HAL made up.

// SetShowThinking sets whether provider reasoning is shown.
//
// It is the operator's preference and nothing else: the effort level,
// the request and the conversation are untouched by it.
func (m *Model) SetShowThinking(on bool) {
	m.showThinking = on
}

// ShowThinking reports whether provider reasoning is shown.
func (m Model) ShowThinking() bool { return m.showThinking }

// toggleThinking turns the visibility of the provider's reasoning on or
// off and says which state it is in.
func (m *Model) toggleThinking() {
	m.setThinking(!m.showThinking)
}

// setThinking turns the visibility of the provider's reasoning on or
// off and says which state it is in.
//
// The change is presentation only. The effort level is not touched, the
// running turn is not rewritten, and a provider that exposed no
// reasoning simply shows nothing either way.
func (m *Model) setThinking(on bool) {
	if on == m.showThinking {
		m.notice = "Show thinking is already " + thinkingState(on) + "."
		return
	}
	before := m.showThinking
	m.showThinking = on
	if m.hooks.SaveShowThinking != nil {
		if err := m.hooks.SaveShowThinking(on); err != nil {
			m.showThinking = before
			m.notice = "Show thinking unchanged: " + err.Error()
			return
		}
	}
	// The reasoning of the turns already on screen follows the
	// preference, so the operator sees the change at once.
	m.rewrap()
	m.renderConversation()
	if on {
		m.notice = "Show thinking: on — the reasoning a provider exposes is shown with the answer."
		return
	}
	m.notice = "Show thinking: off — reasoning is hidden."
}

// thinkingState names the state of the visibility preference.
func thinkingState(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// thinkingRows renders the reasoning a provider exposed, as a block of
// its own.
//
// The block is dim, labelled and set apart from the answer, so it can
// never be mistaken for HAL's answer or for the tool activity under
// it. Nothing is invented: a turn with no reasoning renders no block.
func (m Model) thinkingRows(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	st := m.theme.styles()
	prefix := m.transcriptPrefix()
	rows := []string{prefix + st.footer.Render("thinking")}
	for _, line := range wrapPlainText(text, m.contentWidth()) {
		rows = append(rows, prefix+st.footer.Render(line))
	}
	return rows
}

// pendingThinkingRows renders the reasoning of the running turn, with
// the decoration flags that keep it out of a selection.
//
// It is empty when the operator asked for no reasoning, or when the
// provider has exposed none so far.
func (m Model) pendingThinkingRows() ([]string, []bool) {
	if !m.showThinking || strings.TrimSpace(m.pendingReasoning) == "" {
		return nil, nil
	}
	rows := m.thinkingRows(m.pendingReasoning)
	// One blank row keeps the reasoning apart from the answer.
	rows = append(rows, m.transcriptPrefix())
	deco := make([]bool, len(rows))
	for i := range deco {
		deco[i] = true
	}
	return rows, deco
}

// wrapPlainText wraps text to a width in display cells, keeping the
// line breaks the provider wrote.
//
// The reasoning is plain text, not Markdown: it is shown as it was
// received, and it is wrapped so a long line cannot push the frame out
// of shape.
func wrapPlainText(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapPlainLine(line, width)...)
	}
	return out
}

// wrapPlainLine wraps one line of plain text within width display
// cells. Words are kept whole where they fit and broken by grapheme
// cluster where they do not.
func wrapPlainLine(line string, width int) []string {
	var out []string
	current := ""
	currentWidth := 0
	for _, word := range strings.Fields(line) {
		wordWidth := uniseg.StringWidth(word)
		if current == "" {
			if wordWidth <= width {
				current, currentWidth = word, wordWidth
				continue
			}
			parts := splitHelpWord(word, width)
			out = append(out, parts[:len(parts)-1]...)
			current, currentWidth = parts[len(parts)-1], uniseg.StringWidth(parts[len(parts)-1])
			continue
		}
		if currentWidth+1+wordWidth <= width {
			current += " " + word
			currentWidth += 1 + wordWidth
			continue
		}
		out = append(out, current)
		if wordWidth <= width {
			current, currentWidth = word, wordWidth
			continue
		}
		parts := splitHelpWord(word, width)
		out = append(out, parts[:len(parts)-1]...)
		current, currentWidth = parts[len(parts)-1], uniseg.StringWidth(parts[len(parts)-1])
	}
	if current != "" {
		out = append(out, current)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}
