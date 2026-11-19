package tui

import "strconv"

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

// thinkingStyle returns how a reasoning row is styled, and the name of
// that style.
//
// The name lets the buffer know when the rows have to be styled again:
// a theme change restyles the block, and nothing else does.
func (m Model) thinkingStyle() (func(string) string, string) {
	st := m.theme.styles()
	prefix := m.transcriptPrefix()
	key := m.theme.Name + "|" + strconv.FormatBool(m.color)
	return func(line string) string {
		if !m.color {
			return prefix + line
		}
		return prefix + st.footer.Render(line)
	}, key
}

// thinkingRows renders the reasoning a provider exposed, as a block of
// its own.
//
// The block is dim, labelled and set apart from the answer, so it can
// never be mistaken for HAL's answer or for the tool activity under
// it. Nothing is invented: a turn with no reasoning renders no block.
//
// The rows come from the buffer, which wrapped and styled them once: a
// reasoning that is still arriving costs what the piece that arrived
// costs, and the rows that are already on screen do not move.
func (m Model) thinkingRows(buf *thinkingBuffer) []string {
	if buf == nil || buf.Empty() {
		return nil
	}
	style, key := m.thinkingStyle()
	buf.SetStyle(style, key)
	rows := []string{style("thinking")}
	rows = append(rows, buf.Rows()...)
	return rows
}

// pendingThinkingRows renders the reasoning of the running turn, with
// the decoration flags that keep it out of a selection.
//
// It is empty when the operator asked for no reasoning, or when the
// provider has exposed none so far.
func (m Model) pendingThinkingRows() ([]string, []bool) {
	if !m.showThinking || m.pendingThinking == nil || m.pendingThinking.Empty() {
		return nil, nil
	}
	rows := m.thinkingRows(m.pendingThinking)
	// One blank row keeps the reasoning apart from the answer.
	rows = append(rows, m.transcriptPrefix())
	deco := make([]bool, len(rows))
	for i := range deco {
		deco[i] = true
	}
	return rows, deco
}

// thinkingBufferFor returns the buffer of the running turn, creating it
// when the provider exposed its first reasoning.
//
// The buffer is created with the width of the moment: a window that
// changes later re-wraps it.
func (m *Model) thinkingBufferFor() *thinkingBuffer {
	if m.pendingThinking == nil {
		style, key := m.thinkingStyle()
		m.pendingThinking = newThinkingBuffer(m.contentWidth(), style, key)
	}
	return m.pendingThinking
}
