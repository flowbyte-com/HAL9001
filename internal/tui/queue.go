package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/flowbyte-com/HAL9001/internal/queue"
)

// The operator's outbox.
//
// HAL works one turn at a time, and a turn can take a while. The
// operator is not idle in the meantime: they can write the next
// message, or change the direction of the work that is running.
//
// An ordinary queued MESSAGE is an addition to the conversation. It is
// sent as a turn of its own, and ordinary messages keep the order they
// were submitted in.
//
// A STEER is a change of direction. It is not an addition to the
// conversation: it is an instruction about the work in progress, and it
// takes effect at the next request boundary of the running turn - the
// first moment the turn is about to send a request it has not sent
// yet. A request that was already sent is never rewritten, and the
// operator is told when the steer will be seen. If the running turn has
// no request left to send, the steer waits and leads the next turn.
//
// Every queued message is visible: it is in the conversation with a
// marker that says whether it is waiting, has been sent, or is done, and
// /queue lists them all.
//
// Nothing is lost, nothing is sent twice and nothing is sent again
// after it was sent. The operator can clear what is waiting.

// queueMessage puts the operator's message in the queue.
//
// It is used when the operator writes while HAL is working. The message
// waits instead of being dropped on the floor, and the operator is told
// that it is waiting.
func (m Model) queueMessage(input string) (tea.Model, tea.Cmd) {
	m.composer.Reset()
	m.clearUndo()
	m.fitComposer()
	// The files the operator attached belong to this message. They are
	// taken out of the pending list here, so the next message starts
	// clean, and they travel with the message while it waits.
	atts := m.pendingFiles
	m.pendingFiles = nil
	item, err := m.queue.Push(queue.KindMessage, input, atts)
	if err != nil {
		m.pendingFiles = atts
		m.notice = "the queue is full — /queue clear makes room"
		return m, nil
	}
	i := m.appendEntry("user", input)
	if len(atts) > 0 {
		m.entries[i].files = atts
	}
	m.setQueueEntry(i, item)
	m.renderEntry(i)
	m.renderConversation()
	m.saveQueue()
	m.notice = m.queueNotice("queued")
	return m, nil
}

// steerCommand is the /steer command.
//
// It records a change of direction. The instruction is taken up by the
// running turn at its next request boundary; when no turn is running it
// waits and leads the next one.
func (m *Model) steerCommand(args []string) tea.Cmd {
	text := strings.TrimSpace(strings.Join(args, " "))
	if text == "" {
		m.notice = "/steer needs an instruction — /steer <what to do instead>"
		return nil
	}
	item, err := m.queue.Push(queue.KindSteer, text, nil)
	if err != nil {
		m.notice = "the queue is full — /queue clear makes room"
		return nil
	}
	i := m.appendEntry("user", text)
	m.setQueueEntry(i, item)
	m.renderEntry(i)
	m.renderConversation()
	m.saveQueue()
	if m.sending {
		m.notice = "steering — your instruction joins the next request this turn sends"
	} else {
		m.notice = "steering — your instruction leads the next turn"
	}
	return nil
}

// queueCommand is the /queue command.
//
// Without an argument it lists the queue. With "clear" it removes the
// messages that are still waiting: the operator is in control of the
// work they have not sent yet. With "send" it sends the messages that
// came from a previous run, which wait for the operator by themselves.
func (m *Model) queueCommand(args []string) tea.Cmd {
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "clear":
			removed := m.queue.ClearPending()
			m.refreshQueueMarkers()
			m.saveQueue()
			m.renderConversation()
			if len(removed) == 0 {
				m.notice = "the queue was already empty"
				return nil
			}
			m.notice = fmt.Sprintf("cleared %d waiting %s", len(removed), plural(len(removed), "message", "messages"))
			return nil
		case "send":
			return m.sendRestored()
		}
	}
	m.view = viewQueue
	return nil
}

// sendRestored sends the messages that came from a previous run.
//
// They are the operator's own words, written for a turn that never
// happened. The drain does not send them on its own - that would be a
// replay nobody asked for - so the operator sends them here, in the
// order they were written.
func (m *Model) sendRestored() tea.Cmd {
	if m.queue.RestoredPending() == 0 {
		m.notice = "nothing is waiting from before"
		return nil
	}
	m.sendingRestored = true
	item, ok := m.nextQueued()
	if !ok {
		m.sendingRestored = false
		m.notice = "nothing is waiting from before"
		return nil
	}
	next, cmd := m.startTurn(item.Text, item.Files, m.entryForQueueItem(item.ID))
	// startTurn works on a copy of the model. The changes it made have
	// to be kept, so they are written back.
	*m = next.(Model)
	return cmd
}

// plural names a count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// setQueueEntry remembers which row of the conversation shows a queued
// message, so the row can be updated when the message is sent or done.
func (m *Model) setQueueEntry(index int, item queue.Item) {
	if m.queueEntries == nil {
		m.queueEntries = map[string]int{}
	}
	m.queueEntries[item.ID] = index
	m.entries[index].queueID = item.ID
	m.entries[index].queueKind = string(item.Kind)
	m.entries[index].queueState = string(item.State)
	m.entries[index].queueRestored = item.Restored
}

// entryForQueueItem returns the row that shows a queued message.
func (m Model) entryForQueueItem(id string) int {
	if index, ok := m.queueEntries[id]; ok {
		return index
	}
	return -1
}

// refreshQueueMarkers updates every row that shows a queued message, so
// the conversation always says what state the messages are in.
func (m *Model) refreshQueueMarkers() {
	for _, item := range m.queue.Snapshot() {
		index, ok := m.queueEntries[item.ID]
		if !ok || index < 0 || index >= len(m.entries) {
			continue
		}
		m.entries[index].queueState = string(item.State)
		m.entries[index].queueKind = string(item.Kind)
		m.entries[index].queueRestored = item.Restored
		m.renderEntry(index)
	}
}

// endQueueItems ends the messages the finished turn carried.
//
// A turn that was cancelled or that failed still ends, and the
// operator's words are not sent again. The messages that were waiting
// keep waiting: a cancellation is the operator's decision and it does
// not discard what they had already written.
func (m *Model) endQueueItems() {
	m.queue.ConsumeAccepted()
	m.refreshQueueMarkers()
	// The conversation is rebuilt, so the rows that show a message say
	// that it is done and not that it was sent.
	m.renderConversation()
	m.saveQueue()
}

// nextQueued takes the next message to send as a turn.
//
// It is used only after a turn that produced an answer. A turn that
// failed or was cancelled leaves the queue alone, so the operator is
// the one who decides whether the work continues.
//
// While the operator is sending the messages that came from a previous
// run, those come first: they are the ones they asked for.
func (m *Model) nextQueued() (queue.Item, bool) {
	if m.sendingRestored {
		item, ok := m.queue.NextRestored()
		if !ok {
			m.sendingRestored = false
			return queue.Item{}, false
		}
		return item, true
	}
	return m.queue.Next()
}

// noteSteer records that the running turn took a steering message.
func (m *Model) noteSteer(text string, stored bool) {
	items := m.queue.Snapshot()
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if item.Kind != queue.KindSteer || item.State != queue.StateAccepted {
			continue
		}
		if item.Text != text {
			continue
		}
		m.refreshQueueMarkers()
		m.renderConversation()
		if stored {
			m.notice = "steering — your instruction is with the running turn"
		} else {
			m.notice = "steering — your instruction is with the running turn, but it could not be written to the session"
		}
		return
	}
}

// queueNotice describes the queue for the status line.
func (m Model) queueNotice(prefix string) string {
	pending := m.queue.Pending()
	if pending <= 1 {
		return prefix + " — it is sent when this turn ends"
	}
	return fmt.Sprintf("%s — %d waiting, sent in the order they were written", prefix, pending)
}

// queueCountNote names the waiting messages for the activity line.
func (m Model) queueCountNote() string {
	pending := m.queue.Pending()
	if pending == 0 {
		return ""
	}
	return fmt.Sprintf(" · %d queued", pending)
}

// saveQueue writes the waiting messages to the session's own file.
//
// A queue that cannot be written is reported: the operator is told that
// their message is not safe against a restart, rather than finding out
// later that it is gone.
func (m *Model) saveQueue() {
	if m.hooks.QueueStore == nil {
		return
	}
	store := m.hooks.QueueStore()
	if store == nil {
		return
	}
	if err := m.queue.Save(store); err != nil {
		m.notice = "the queued message could not be saved: " + err.Error()
	}
}

// loadQueue reads the waiting messages back when a session is opened.
//
// The messages that are waiting are restored. The ones that were
// already sent are not: they are in the session, and restoring them
// would send them twice.
func (m *Model) loadQueue() {
	if m.hooks.QueueStore == nil {
		return
	}
	store := m.hooks.QueueStore()
	if store == nil {
		return
	}
	items, err := store.Load()
	if err != nil {
		m.notice = "the queued messages could not be read: " + err.Error()
		return
	}
	if len(items) == 0 {
		return
	}
	m.queue.Restore(items)
	m.notice = fmt.Sprintf("%d %s waiting from before — /queue send sends them",
		len(items), plural(len(items), "message", "messages"))
}

// moveQueue moves the queue to another session.
//
// The messages that were waiting are written to the session they
// belong to, and the messages waiting for the session that is opened
// are read back. A message never follows the operator into another
// conversation.
func (m *Model) moveQueue(after func()) {
	m.saveQueue()
	m.queue.Reset()
	m.queueEntries = map[string]int{}
	after()
	m.loadQueue()
}

// queueMarker names the state of a queued message under its row.
func queueMarker(kind, state string, restored bool) string {
	if restored && state == string(queue.StatePending) {
		// A message from a previous run waits for the operator: it is
		// not sent on its own.
		return "waiting from before"
	}
	switch state {
	case string(queue.StatePending):
		if kind == string(queue.KindSteer) {
			return "steering"
		}
		return "queued"
	case string(queue.StateAccepted):
		if kind == string(queue.KindSteer) {
			return "steering · taken up"
		}
		return "sent"
	case string(queue.StateConsumed):
		return "done"
	}
	return ""
}

// queueMarkerRow renders the marker under a message.
//
// The row is interface, not the operator's text: it is dim, and a
// selection never copies it.
func (m Model) queueMarkerRow(marker string) string {
	line := "· " + marker
	if m.color {
		line = m.theme.styles().footer.Render(line)
	}
	return line
}

// queueRows renders the queue for the panel.
//
// It lists every message with its state, so the operator can see what
// is waiting, what has been sent and what is done.
func (m Model) queueRows() []string {
	st := m.theme.styles()
	rows := []string{"Queued messages (Esc back)", ""}
	items := m.queue.Snapshot()
	if len(items) == 0 {
		rows = append(rows, "  Nothing is waiting.")
		rows = append(rows, "")
		rows = append(rows, "  A message written while HAL is working waits here, and")
		rows = append(rows, "  is sent when the turn ends.")
		return rows
	}
	var waiting, sent, done, fromBefore int
	for _, item := range items {
		switch item.State {
		case queue.StatePending:
			waiting++
		case queue.StateAccepted:
			sent++
		case queue.StateConsumed:
			done++
		}
		if item.Restored && item.State == queue.StatePending {
			fromBefore++
		}
	}
	rows = append(rows, "  "+fmt.Sprintf("%d waiting · %d sent · %d done", waiting, sent, done))
	if fromBefore > 0 {
		rows = append(rows, "  "+fmt.Sprintf("%d of them from before — they wait for you", fromBefore))
	}
	rows = append(rows, "")
	for _, item := range items {
		kind := "message"
		if item.Kind == queue.KindSteer {
			kind = "steer"
		}
		label := padTo(st.active.Render(queueMarker(string(item.Kind), string(item.State), item.Restored)), 22)
		rows = append(rows, "  "+label+kind+"  "+truncateCells(item.Text, m.contentWidth()-30))
	}
	rows = append(rows, "")
	rows = append(rows, "  A waiting message is sent when the running turn ends, in the")
	rows = append(rows, "  order it was written. A steer is taken up by the running turn")
	rows = append(rows, "  at its next request; a request already sent is never rewritten.")
	rows = append(rows, "  /queue clear removes what is waiting.")
	if fromBefore > 0 {
		rows = append(rows, "  /queue send sends what came from before.")
	}
	return rows
}

// updateQueue handles the keys of the queue panel.
//
// The panel is a view of the queue: it is read, and it can be cleared.
// It never sends anything by itself.
//
// The panel is meant to be opened while HAL is working, so the turn
// that is running behind it keeps running: the answer, the tool
// activity and the queue moving on belong to the conversation, and the
// panel only looks at them.
func (m Model) updateQueue(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, m.quit()
		case "esc", "enter", "q":
			m.view = viewChat
			return m, nil
		case "up", "shift+tab":
			m.scrollPanel(-1)
			return m, nil
		case "down", "tab":
			m.scrollPanel(1)
			return m, nil
		}
		// A key the panel does not use is not typed into the composer:
		// the operator is reading the queue, not writing.
		return m, nil
	}
	return m.updateChat(msg)
}
