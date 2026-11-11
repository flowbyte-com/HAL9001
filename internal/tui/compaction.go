package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
)

// The manual compaction of the conversation.
//
// The operator asks for it and nothing else does. There is no automatic
// compaction: a conversation that grows is the operator's business, and
// HAL never decides on its own to summarise what was said.
//
// A compaction runs like a turn: it can be stopped, and a stopped
// compaction stores nothing. The queued messages are left exactly where
// they were — a compaction sends no operator message, so it cannot
// reorder them — and a steering message keeps waiting for the turn it
// was meant for.

// CompactEngine is implemented by an engine that can compact a
// conversation.
//
// The interface asks; it never assumes. An engine without it cannot
// compact, and the command says so.
type CompactEngine interface {
	// Compact replaces the older part of the conversation with a summary
	// and reports what it did.
	Compact(ctx context.Context, emit func(engine.Event)) (engine.CompactionReport, error)
}

// init registers the compaction command.
//
// The command table is the single source of truth for the dispatch, the
// suggestion panel and /help. Appending to it here keeps that property
// while leaving the table itself alone.
func init() {
	commandTable = append(commandTable, compactionCommands()...)
}

// compactionCommands are the commands that act on the conversation.
func compactionCommands() []*command {
	return []*command{
		{
			name:        "/compact",
			description: "Summarise the older part of the conversation",
			run: func(m *Model, args []string) tea.Cmd {
				if len(args) > 0 {
					m.notice = "Usage: /compact"
					return nil
				}
				return m.compactTurn()
			},
		},
	}
}

// compactionMsg carries the result of a finished compaction.
type compactionMsg struct {
	id     int
	report engine.CompactionReport
}

// compactionErrMsg carries the failure of a compaction.
type compactionErrMsg struct {
	id  int
	err error
}

// compactTurn starts a compaction.
//
// It is refused while HAL is working: two operations at once would
// compete for the same provider and the same conversation, and the
// operator can stop the turn and ask again.
func (m *Model) compactTurn() tea.Cmd {
	eng, ok := m.engine.(CompactEngine)
	if !ok {
		m.notice = "this session cannot compact the conversation"
		return nil
	}
	if m.sending {
		m.notice = "HAL is working — stop the turn first"
		return nil
	}
	m.streamID++
	id := m.streamID
	m.sending = true
	m.streamStarted = m.clock()
	m.reasoningSeen = false
	m.turnStats = nil
	m.beginActivity()
	m.notice = "compacting the conversation…"
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	cmd := m.compactCmd(id, eng, ctx)
	if m.sender == nil {
		return cmd
	}
	if tick := m.animationTickCmd(); tick != nil {
		return tea.Batch(cmd, tick)
	}
	return cmd
}

// compactCmd starts the compaction.
func (m Model) compactCmd(id int, eng CompactEngine, ctx context.Context) tea.Cmd {
	if m.sender == nil {
		return func() tea.Msg { return m.runCompactInline(id, eng, ctx) }
	}
	return func() tea.Msg {
		go m.runCompact(id, eng, ctx)
		return streamStartedMsg{id: id}
	}
}

// runCompact drives the compaction in the background.
func (m Model) runCompact(id int, eng CompactEngine, ctx context.Context) {
	report, err := eng.Compact(ctx, func(engine.Event) {})
	if err != nil {
		m.sender(compactionErrMsg{id: id, err: err})
		return
	}
	m.sender(compactionMsg{id: id, report: report})
}

// runCompactInline runs the compaction headless.
func (m Model) runCompactInline(id int, eng CompactEngine, ctx context.Context) tea.Msg {
	report, err := eng.Compact(ctx, func(engine.Event) {})
	if err != nil {
		return compactionErrMsg{id: id, err: err}
	}
	return compactionMsg{id: id, report: report}
}

// compactionNotice says what the compaction did, in one line.
//
// The numbers are estimates: HAL does not know the provider's
// tokenizer, and it never claims to.
func compactionNotice(r engine.CompactionReport) string {
	return fmt.Sprintf("compacted — %s summarised, %d kept · est. %d → %d tokens",
		compactExchangeCount(r.CoveredExchanges), r.KeptExchanges,
		r.BeforeTokens, r.AfterTokens)
}

// compactionNote is what the conversation shows after a compaction.
//
// It says what the model now receives, and it says that the
// conversation above is unchanged: a compaction changes what is sent,
// never what was said.
func compactionNote(r engine.CompactionReport) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(
		"Context compacted: the model now receives a summary of %s instead of the exchanges themselves. "+
			"%d recent exchange(s) were left as they are. The estimated context went from about %d to "+
			"about %d tokens. The request itself is fitted to the %d-token budget, so the older exchanges "+
			"are now summarised rather than dropped. The conversation above is unchanged, and every "+
			"message is still on disk.",
		compactExchangeCount(r.CoveredExchanges), r.KeptExchanges,
		r.BeforeTokens, r.AfterTokens, r.Limit))
	if r.Steps > 1 {
		b.WriteString(fmt.Sprintf(" The summary was written in %d bounded steps.", r.Steps))
	}
	if r.InputTokens > 0 || r.OutputTokens > 0 {
		b.WriteString(fmt.Sprintf(
			" The compaction itself cost about %d tokens (%d in, %d out); those requests are not "+
				"conversation turns.", r.InputTokens+r.OutputTokens, r.InputTokens, r.OutputTokens))
	}
	if strings.TrimSpace(r.Summary) != "" {
		b.WriteString("\n\n")
		b.WriteString(r.Summary)
	}
	return b.String()
}

// compactExchangeCount names the number of exchanges a summary covers.
func compactExchangeCount(n int) string {
	if n == 1 {
		return "1 earlier exchange"
	}
	return fmt.Sprintf("%d earlier exchanges", n)
}

// compactionFailureNotice says that the conversation was not compacted.
//
// A failed compaction changes nothing: the usable context is exactly
// what it was, and the operator is told so.
func compactionFailureNotice(err error) string {
	if errors.Is(err, context.Canceled) {
		return "compaction cancelled — the conversation is unchanged"
	}
	if errors.Is(err, engine.ErrNoProvider) {
		return "no provider configured — the conversation is unchanged"
	}
	return "compaction failed — the conversation is unchanged: " + err.Error()
}
