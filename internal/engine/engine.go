// Package engine is HAL9001's conversation core: session continuity plus a
// provider turn, with the 444 capability boundary enforced on every
// provider-requested operation. Presentation lives in internal/tui; this
// package has no terminal code. No implementation-capable tool is
// registered here: the engine knows text turns, and anything else the
// provider asks for is refused with the 444 refusal.
package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/capability"
	"github.com/flowbyte-com/HAL9001/internal/knowledge"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/skills"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// ErrNoProvider reports that no model provider is configured. The
// conversation is still stored; nothing is fabricated in its place.
var ErrNoProvider = errors.New("no provider configured")

// ErrNoVisibleAnswer reports that the provider finished without any
// visible answer text — for example it exhausted the output budget while
// reasoning. Nothing is stored for the assistant side: a note is not an
// answer, and storing one would pollute the next request's context.
var ErrNoVisibleAnswer = errors.New("the model produced no visible answer")

// Engine runs one conversation: it stores both sides of every turn and
// asks the provider for the next assistant message.
type Engine struct {
	sessions     *session.Store
	provider     provider.Provider
	model        provider.Model
	systemPrompt string
	// workspace prepares write proposals. Nil means the turn offers no
	// write capability at all.
	workspace *workspace.Store
	// toolUnsupported records a provider that refused function
	// definitions, so the conversation continues without them.
	toolUnsupported bool
	// skills routes each request to a skill and loads its guidance.
	// Nil means no skill is ever selected.
	skills *skills.Selector
	// permission is the read-only view of the operator's permission
	// gate. Nil means the conversation is permanently 444.
	permission permission.Gate
	// execution performs the four 666 tools. Nil means no privileged tool
	// is ever offered, even if the gate says 666.
	execution *agenttools.Executor
	// knowledge exposes MPM's own knowledge tools. It is available in both
	// 444 and 666: it mutates MPM records, never arbitrary OS files.
	knowledge *knowledge.Registry
}

// maxToolSteps bounds a single provider conversation. It is a safety limit,
// not a target: ordinary turns use zero or one step.
const maxToolSteps = 8

// New binds a session store to a provider and model.
func New(sessions *session.Store, p provider.Provider, model provider.Model) *Engine {
	return &Engine{sessions: sessions, provider: p, model: model}
}

// WithWorkspace enables the single write proposal: a model may ask to save
// one file, and only the interface can approve it. Without a workspace the
// engine offers no tools and every model-requested write is refused.
func (e *Engine) WithWorkspace(w *workspace.Store) *Engine {
	e.workspace = w
	return e
}

// WithSkills attaches the skill selector.
//
// The selector decides which MPM-managed skill applies to a request and
// loads its guidance. Without one, no skill is ever selected and every
// turn is exactly the turn it was before.
func (e *Engine) WithSkills(s *skills.Selector) *Engine {
	e.skills = s
	return e
}

// WithPermission attaches the read-only permission gate. The engine can
// observe and recheck the mode, but it cannot change it.
func (e *Engine) WithPermission(g permission.Gate) *Engine {
	e.permission = g
	return e
}

// WithExecution attaches the 666 tool executor. The executor still asks the
// gate at every operation boundary; attaching it grants nothing by itself.
func (e *Engine) WithExecution(x *agenttools.Executor) *Engine {
	e.execution = x
	return e
}

// WithKnowledge attaches MPM's knowledge tools. They remain available in
// 444: they are typed knowledge operations, not unrestricted filesystem
// writes, and MPM still owns validation and confirmation.
func (e *Engine) WithKnowledge(k *knowledge.Registry) *Engine {
	e.knowledge = k
	return e
}

// permissionMode is the safe snapshot used for the system instruction and
// the offered-tool list. It never replaces the gate's execution check.
func (e *Engine) permissionMode() permission.Mode {
	if e.permission == nil {
		return permission.Guarded
	}
	return e.permission.Mode()
}

// WithSystemPrompt sets the system instruction prepended to every request.
// It is never stored in the session: history stays provider-independent
// and the instruction foundation stays byte-stable across turns.
func (e *Engine) WithSystemPrompt(prompt string) *Engine {
	e.systemPrompt = prompt
	return e
}

// UseStore swaps the conversation store (for example when starting a new
// conversation). History already written stays where it was.
func (e *Engine) UseStore(sessions *session.Store) {
	e.sessions = sessions
}

// requestMessages builds the provider request.
//
// The guidance belongs to the request, not to the persona and not to the
// history: it is composed into the system instruction for this turn and
// nowhere else, so the next turn starts from the same instruction again.
func (e *Engine) requestMessages(history []session.Message, guidance string) []provider.Message {
	messages := make([]provider.Message, 0, len(history)+1)
	system := e.systemPrompt
	if block := permissionBlock(e.permissionMode()); block != "" {
		if system != "" {
			system += "\n\n" + block
		} else {
			system = block
		}
	}
	if guidance != "" {
		if system != "" {
			system += "\n\n" + guidance
		} else {
			system = guidance
		}
	}
	if system != "" {
		messages = append(messages, provider.Message{Role: "system", Content: system})
	}
	for _, msg := range history {
		messages = append(messages, provider.Message{
			Role:    string(msg.Role),
			Content: msg.Content,
		})
	}
	return messages
}

// permissionBlock tells the model the authority for this request. It is
// composed into the system instruction for this turn only and never stored.
// It cannot grant anything: the executor checks the same gate at each
// operation.
func permissionBlock(mode permission.Mode) string {
	if mode == permission.Unrestricted {
		return "## Current permission: 666 UNRESTRICTED\n\n" +
			"The operator enabled 666 for this conversation. The read, write, edit and bash tools run with the " +
			"permissions of the OS user running HAL and are not confined to the HAL workspace. Use them only when " +
			"the operator's request warrants an action; do not edit files or run commands merely because they are " +
			"available. You cannot change this mode. Only the operator can return to 444, and a tool result that " +
			"says the operation was denied or cancelled must be reported honestly."
	}
	return "## Current permission: 444 READ ONLY\n\n" +
		"You may converse, retrieve and update MPM knowledge through its typed tools, and prepare one guarded " +
		"write proposal for operator approval. You cannot run shell commands or edit OS files. A request for " +
		"those capabilities is outside your authority; do not claim an operation happened unless a tool result " +
		"confirms it. You cannot change the permission mode."
}

// Respond answers without streaming (kept for tests and non-interactive
// use). See RespondStream for the interactive path.
func (e *Engine) Respond(ctx context.Context, input string) (string, error) {
	return e.RespondStream(ctx, input, nil)
}

// Event is one streaming update from a turn.
//
// Text is user-facing content. Reasoning reports that the provider sent a
// reasoning event without exposing any of it: the private reasoning text
// never leaves the provider package. Stats is set on the final event.
type Event struct {
	Text      string
	Reasoning bool
	Stats     *provider.TurnStats
	// Skill is the routing decision for this turn: which skill was
	// selected, why, and which one was actually loaded. It is set on
	// every turn, including the ones that selected nothing.
	Skill *skills.Decision
	// Attribution is what was actually supplied to the model for this
	// turn: the skills whose instructions went into the request, with
	// the version and the content hash that were used. It is nil when
	// no skill shaped the turn.
	Attribution *session.Attribution
	// TurnID is the identity of the exchange this event belongs to. It
	// is the same identity the session stores, so an attribution can
	// never be attached to the wrong answer.
	TurnID string
	// Proposal is one prepared workspace write, awaiting the operator's
	// decision. It carries no authority: the interface approves or
	// rejects it, and only then may the filesystem be touched.
	Proposal *workspace.Proposal
	// Tool is one execution-boundary record. It never contains file
	// contents or command text; the interface uses it for the compact
	// activity line and the diagnostics panel.
	Tool *ToolActivity
}

// ToolActivity is one tool call as the interface and diagnostics see it.
type ToolActivity struct {
	CallID    string
	Name      string
	Action    string
	Target    string
	Status    string
	Summary   string
	Duration  time.Duration
	Bytes     int
	Truncated bool
	ExitCode  *int
	Mode      permission.Mode
}

// RespondStream answers with live text deltas forwarded to emit (nil
// disables streaming and uses the non-streaming path).
func (e *Engine) RespondStream(ctx context.Context, input string, emit func(string)) (string, error) {
	if emit == nil {
		return e.RespondStreamEvents(ctx, input, nil)
	}
	return e.RespondStreamEvents(ctx, input, func(ev Event) {
		if ev.Text != "" {
			emit(ev.Text)
		}
	})
}

// RespondStreamEvents answers with live events: text for the answer,
// reasoning presence for progress, and content-free stats at the end.
// Cancellation aborts the provider call and stores nothing from the
// assistant side: an interrupted stream never becomes a complete answer
// in the session.
//
// When a workspace is configured, the turn also offers the single write
// proposal. A prepared proposal is emitted as an event and the interface
// decides what happens to it; the engine never writes anything itself.
func (e *Engine) RespondStreamEvents(ctx context.Context, input string, emit func(Event)) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("engine: empty input")
	}
	// The exchange gets its identity before anything else happens, so
	// the operator's message, the answer and the attribution of that
	// answer all carry the same name in the session.
	turnID := session.NewTurnID()
	if _, err := e.sessions.AppendTurn(session.RoleUser, input, session.Turn{ID: turnID}); err != nil {
		return "", err
	}
	if e.provider == nil {
		return "", ErrNoProvider
	}
	history, err := e.sessions.History()
	if err != nil {
		return "", err
	}
	// The skill is chosen and loaded before the provider is called, so
	// the model writes with the guidance rather than being told about it
	// afterwards. The guidance belongs to this request: it is composed
	// into the system instruction here and never enters the session.
	decision, loaded := e.prepareSkill(ctx, input)
	attribution := attributionOf(turnID, decision)
	if emit != nil && decision.Source != "" {
		d := decision
		emit(Event{Skill: &d, Attribution: attribution, TurnID: turnID})
	}
	messages := e.requestMessages(history, skillBlock(loaded))
	turn := userTurns(history)
	tools := e.offeredTools()
	resp, reply, proposal, err := e.toolTurn(ctx, messages, tools, turn, emit)
	if err != nil {
		// A failed turn still reports its content-free diagnostics: the
		// elapsed time and the event counts are how a stall is explained.
		if emit != nil && !resp.Stats.StartedAt.IsZero() {
			stats := resp.Stats
			emit(Event{Stats: &stats})
		}
		return "", err
	}
	if emit != nil {
		stats := resp.Stats
		if stats.Status == "" {
			stats.Status = provider.StatusFinished
		}
		emit(Event{Stats: &stats})
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		// The provider completed without visible answer text. Report it
		// honestly and store nothing: an explanation is not an assistant
		// message, and silently retrying or raising the cap would hide
		// the cost instead of explaining it.
		finish := resp.FinishReason
		if finish == "" {
			finish = "unknown"
		}
		if resp.Reasoning != "" {
			return "", fmt.Errorf("%w: the output budget was spent on reasoning (finish_reason %s)", ErrNoVisibleAnswer, finish)
		}
		return "", fmt.Errorf("%w (finish_reason %s)", ErrNoVisibleAnswer, finish)
	}
	if _, err := e.sessions.AppendTurn(session.RoleAssistant, reply, session.Turn{
		ID:          turnID,
		Attribution: attribution,
	}); err != nil {
		return "", err
	}
	if proposal != nil && emit != nil {
		// The last event of the turn: one prepared write for the operator
		// to review. It carries no authority, only a request.
		emit(Event{Proposal: proposal})
	}
	return reply, nil
}

// toolTurn runs the provider/tool loop for one exchange.
//
// Text from every provider round is part of the visible answer. A tool call
// becomes a tool-result message and the model is asked again, so a request
// such as "read this, fix that, run the test" can take several steps without
// the interface inventing prose between them. The session still stores one
// user message and one final answer: tool transcripts belong to the turn, not
// to the permanent conversation format.
func (e *Engine) toolTurn(ctx context.Context, messages []provider.Message, tools []provider.Tool, turn int, emit func(Event)) (provider.Response, string, *workspace.Proposal, error) {
	current := append([]provider.Message(nil), messages...)
	var (
		parts    []string
		proposal *workspace.Proposal
		last     provider.Response
		halted   bool
	)
	for step := 0; step < maxToolSteps; step++ {
		resp, err := e.chat(ctx, current, tools, emit)
		if err != nil && len(tools) > 0 && isToolRejection(err) {
			// A provider that cannot take function definitions must not
			// lose the conversation: retry the same turn without them and
			// stop offering tools for this engine.
			e.toolUnsupported = true
			tools = nil
			resp, err = e.chat(ctx, current, nil, emit)
		}
		if err != nil {
			return resp, strings.Join(parts, "\n\n"), proposal, err
		}
		last = resp
		if strings.TrimSpace(resp.Content) != "" {
			parts = append(parts, resp.Content)
		}
		if len(resp.ToolCalls) == 0 {
			break
		}
		if ctx.Err() != nil {
			return resp, strings.Join(parts, "\n\n"), proposal, ctx.Err()
		}
		current = append(current, provider.Message{
			Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
		})
		stop := false
		for i, call := range resp.ToolCalls {
			if call.ID == "" {
				call.ID = fmt.Sprintf("call-%d-%d", step, i)
			}
			content, activity, prepared, halt := e.executeTool(ctx, call, turn)
			if emit != nil {
				a := activity
				emit(Event{Tool: &a})
			}
			// The assistant message above carries the original ID. If
			// the provider omitted one, the tool result still needs the
			// same generated identity; patch the last call in place.
			if resp.ToolCalls[i].ID == "" {
				resp.ToolCalls[i].ID = call.ID
				current[len(current)-1].ToolCalls = resp.ToolCalls
			}
			current = append(current, provider.Message{
				Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: content,
			})
			switch {
			case prepared != nil && proposal == nil:
				proposal = prepared
				stop = true
				if activity.Summary != "" {
					parts = append(parts, activity.Summary)
				}
			case prepared != nil:
				stop = true
				parts = append(parts, "Only one write can be approved per message; the second proposal was ignored.")
			case halt:
				stop = true
				if activity.Summary != "" {
					parts = append(parts, activity.Summary)
				}
			}
		}
		if stop {
			halted = true
			break
		}
	}
	if !halted && len(last.ToolCalls) > 0 {
		parts = append(parts, fmt.Sprintf("Stopped after %d tool steps; ask me to continue if you want the remaining work done.", maxToolSteps))
	}
	return last, strings.Join(parts, "\n\n"), proposal, nil
}

// executeTool resolves one provider call. The permission gate is consulted
// by the executor at the operation itself; this function never caches a
// prior decision as authority.
func (e *Engine) executeTool(ctx context.Context, call provider.ToolCall, turn int) (string, ToolActivity, *workspace.Proposal, bool) {
	mode := e.permissionMode()
	activity := ToolActivity{
		CallID: call.ID, Name: call.Name, Action: call.Name, Mode: mode,
		Status: "failed", Summary: call.Name + " failed",
	}
	switch call.Name {
	case workspace.ToolName:
		if e.workspace == nil {
			activity.Status = "denied"
			activity.Summary = "I can't save files here: no workspace is configured."
			return `{"ok":false,"tool":"write_file","error":"no workspace is configured"}`, activity, nil, true
		}
		if mode == permission.Unrestricted {
			// 666 uses the real write tool. A stale write_file call is
			// still a proposal request, never an execution.
			activity.Status = "denied"
			activity.Summary = "The guarded write_file proposal is not used in 666; use the write tool."
			return `{"ok":false,"tool":"write_file","error":"in 666 use the write tool instead of the guarded write proposal"}`, activity, nil, true
		}
		p, err := e.prepareWrite(call, e.sessionName(), turn)
		if err != nil {
			activity.Status = "failed"
			activity.Summary = writeRefusal(err)
			return errorJSON("write_file", writeRefusal(err)), activity, nil, true
		}
		activity.Status = "proposed"
		activity.Target = p.Path
		activity.Summary = "Proposed write: " + e.workspace.DisplayPath(p) +
			" (" + humanBytes(int64(p.Size())) + ") — awaiting your approval."
		content := `{"ok":true,"tool":"write_file","status":"awaiting_operator_approval","path":` +
			quoteJSON(e.workspace.DisplayPath(p)) + `}`
		return content, activity, &p, true
	case "read", "write", "edit", "bash":
		if e.execution == nil || mode != permission.Unrestricted {
			activity.Status = "denied"
			activity.Summary = fmt.Sprintf("Requested operation %q refused: %s", call.Name, capability.Refusal())
			return `{"ok":false,"tool":` + quoteJSON(call.Name) + `,"error":` + quoteJSON(capability.Refusal()) + `}`, activity, nil, true
		}
		res := e.execution.Execute(ctx, call)
		activity.Action = res.Action
		activity.Target = res.Target
		activity.Status = res.Status
		activity.Summary = res.Summary
		activity.Duration = res.Duration
		activity.Bytes = res.Bytes
		activity.Truncated = res.Truncated
		activity.ExitCode = res.ExitCode
		if res.Status == "denied" {
			activity.Mode = e.permissionMode()
			return res.Content, activity, nil, true
		}
		return res.Content, activity, nil, false
	default:
		if e.knowledge != nil && e.knowledge.Has(call.Name) {
			res := e.knowledge.Execute(ctx, call)
			activity.Action = res.Action
			activity.Target = res.Target
			activity.Status = res.Status
			activity.Summary = res.Summary
			activity.Duration = res.Duration
			activity.Truncated = res.Truncated
			activity.Bytes = len(res.Content)
			return res.Content, activity, nil, false
		}
		// The existing 444 boundary, unchanged: nothing the model asks
		// for can execute, and a call it repeats is refused again.
		if err := capability.Check(call.Name); err != nil {
			activity.Status = "denied"
			activity.Summary = fmt.Sprintf("Requested operation %q refused: %s", call.Name, capability.Refusal())
			return `{"ok":false,"tool":` + quoteJSON(call.Name) + `,"error":` + quoteJSON(capability.Refusal()) + `}`, activity, nil, true
		}
		activity.Status = "denied"
		activity.Summary = fmt.Sprintf("Operation %q is allowed but not yet implemented in this foundation.", call.Name)
		return `{"ok":false,"tool":` + quoteJSON(call.Name) + `,"error":"operation is allowed but not implemented"}`, activity, nil, true
	}
}

// chat runs one provider turn, offering tools when there are any.
//
// The streaming path is preferred; a provider that cannot stream with
// functions falls back to the ordinary stream, and a provider that cannot
// stream at all uses the blocking call.
func (e *Engine) chat(ctx context.Context, messages []provider.Message, tools []provider.Tool, emit func(Event)) (provider.Response, error) {
	deltaFn := func(d provider.Delta) error {
		ev := Event{Text: d.Text, Reasoning: d.Reasoning != ""}
		if ev.Text == "" && !ev.Reasoning {
			return nil
		}
		if emit != nil {
			emit(ev)
		}
		return nil
	}
	if len(tools) > 0 {
		if streamer, ok := e.provider.(provider.ToolStreamer); ok {
			return streamer.ChatStreamTools(ctx, messages, e.model, tools, deltaFn)
		}
	}
	if streamer, ok := e.provider.(provider.Streamer); ok && emit != nil {
		return streamer.ChatStream(ctx, messages, e.model, deltaFn)
	}
	return e.provider.Chat(ctx, messages, e.model)
}

// userTurns counts the user messages in the history: the turn number the
// review and the audit refer to, stable across restarts.
func userTurns(history []session.Message) int {
	n := 0
	for _, m := range history {
		if m.Role == session.RoleUser {
			n++
		}
	}
	return n
}

// sessionName is the stored conversation's identifier, derived from the
// store's own path. It never leaves the application.
func (e *Engine) sessionName() string {
	if e.sessions == nil {
		return ""
	}
	base := filepath.Base(e.sessions.Path())
	return strings.TrimSuffix(base, ".jsonl")
}
