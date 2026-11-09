// Package engine is HAL9001's conversation core: session continuity plus a
// provider turn. The 444 capability boundary is the default, and the
// operator-enabled session 666 tools are checked again at each operation.
// Presentation lives in internal/tui; this package has no terminal code.
package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/attachments"
	"github.com/flowbyte-com/HAL9001/internal/capability"
	"github.com/flowbyte-com/HAL9001/internal/knowledge"
	"github.com/flowbyte-com/HAL9001/internal/lifecycle"
	"github.com/flowbyte-com/HAL9001/internal/mcp"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/skills"
	"github.com/flowbyte-com/HAL9001/internal/websearch"
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
	// budgetResolver answers where the context budget of a request comes
	// from. Nil means the budget on the model is the budget in force.
	budgetResolver func() (provider.ContextBudget, error)
	// permission is the read-only view of the operator's permission
	// gate. Nil means the conversation is permanently 444.
	permission permission.Gate
	// execution performs the four 666 tools. Nil means no privileged tool
	// is ever offered, even if the gate says 666.
	execution *agenttools.Executor
	// knowledge exposes MPM's own knowledge tools. It is available in both
	// 444 and 666: it mutates MPM records, never arbitrary OS files.
	knowledge *knowledge.Registry
	// web is Firecrawl's read-only research surface. It is available in
	// 444 and 666, and it cannot execute code, write files or change the
	// permission mode.
	web *websearch.Registry
	// attachments reads the files the operator attached to the turn
	// being answered. It is read-only here: the engine can compose the
	// contents of a copy the operator already made, and it is offered
	// no tool that could start an ingestion of its own.
	attachments *attachments.Store
	// mcp exposes the tools of the connected Model Context Protocol
	// servers. It is available in both modes, but the permission of
	// every call is decided by the gate and by the operator's local
	// policy, never by the server.
	mcp MCPRegistry
	// lifecycle is the state of the exchange the engine is running. The
	// engine runs the work, so the engine owns its state.
	lifecycle *lifecycle.Manager
	// checkpoint is the safe point of the exchange that was interrupted.
	// It is nil when there is nothing to continue from.
	//
	// It is guarded: the interface asks about the exchange from its own
	// thread while the engine's thread is running it.
	checkpointMu sync.Mutex
	checkpoint   *checkpoint
	// exchangeSeq counts the exchanges the engine has started. It tells
	// a late report from an interrupted exchange that a newer exchange
	// has already taken the operator's attention.
	exchangeSeq int
}

// MCPRegistry is the engine's narrow view of the native MCP client.
//
// The engine can list the tools, ask whether one exists and execute it.
// It cannot connect a server, change a policy or grant a permission.
type MCPRegistry interface {
	Tools() []provider.Tool
	Has(name string) bool
	Execute(ctx context.Context, call provider.ToolCall) mcp.Result
	Active() bool
}

// maxToolSteps bounds a single provider conversation. It is a safety limit,
// not a target: ordinary turns use zero or one step.
const maxToolSteps = 8

// New binds a session store to a provider and model.
func New(sessions *session.Store, p provider.Provider, model provider.Model) *Engine {
	return &Engine{
		sessions:  sessions,
		provider:  p,
		model:     model,
		lifecycle: lifecycle.New(),
	}
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

// WithBudgetResolver sets where the context budget of a request comes
// from.
//
// The budget is resolved before the request is sent, and an endpoint
// may report a model's context window after the engine was built. A
// resolver lets the application answer that question at request time
// instead of once at build time, without the interface waiting for a
// lookup.
//
// The resolver may refuse: a budget that cannot be resolved — two
// configured numbers that contradict each other — is reported to the
// operator instead of being guessed at.
//
// Without a resolver the engine uses the budget on the model it was
// built with, which is the same number the application resolved.
func (e *Engine) WithBudgetResolver(f func() (provider.ContextBudget, error)) *Engine {
	e.budgetResolver = f
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

// WithWebResearch attaches Firecrawl's read-only research tools. They are
// offered in 444; the registry is never a permission authority and cannot
// reach the 666 executor or MPM's write tools.
func (e *Engine) WithWebResearch(w *websearch.Registry) *Engine {
	e.web = w
	return e
}

// WithAttachments attaches the upload store.
//
// The store lets the engine read a copy the operator already made, so
// the file can be discussed. It grants the engine no way to choose a
// file: ingestion happens in the interface, on the operator's command,
// and no tool is ever offered that could start one.
func (e *Engine) WithAttachments(a *attachments.Store) *Engine {
	e.attachments = a
	return e
}

// WithMCP attaches the Model Context Protocol registry.
//
// The registry contributes the tools of the servers the operator
// configured and connected. It is not a permission authority: every call
// is checked again at its execution boundary, and a server's own
// annotations are never treated as authority.
func (e *Engine) WithMCP(r MCPRegistry) *Engine {
	e.mcp = r
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
//
// expandTurn names the exchange whose attachments are included with
// their contents. Older exchanges keep their attachment references, so
// the model still knows which file was discussed without every file's
// text being carried through the conversation.
func (e *Engine) requestMessages(history []session.Message, guidance string, expandTurn string) []provider.Message {
	messages := make([]provider.Message, 0, len(history)+1)
	system := e.systemPrompt
	if block := permissionBlock(e.permissionMode()); block != "" {
		if system != "" {
			system += "\n\n" + block
		} else {
			system = block
		}
	}
	if e.web != nil {
		if block := e.web.Guidance(); block != "" {
			if system != "" {
				system += "\n\n" + block
			} else {
				system = block
			}
		}
	}
	if e.mcp != nil && e.mcp.Active() {
		if block := mcp.Guidance(); block != "" {
			if system != "" {
				system += "\n\n" + block
			} else {
				system = block
			}
		}
	}
	if block := e.historicalEvidenceBlock(history); block != "" {
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
	if block := e.attachmentSystemBlock(history, expandTurn); block != "" {
		if system != "" {
			system += "\n\n" + block
		} else {
			system = block
		}
	}
	if system != "" {
		messages = append(messages, provider.Message{Role: "system", Content: system})
	}
	// The boundary that delimits the untrusted attachment blocks is
	// fresh for this request, so nothing inside a file can forge it.
	boundary := attachmentBoundary()
	for _, msg := range history {
		content := msg.Content
		if len(msg.Attachments) > 0 {
			content = e.attachmentRequest(msg, msg.TurnID == expandTurn, boundary)
		}
		messages = append(messages, provider.Message{
			Role:    string(msg.Role),
			Content: content,
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
			"says the operation was denied or cancelled must be reported honestly. " +
			"When the operator explicitly asks you to verify an expected failure, put the expectation in the " +
			"tool call itself: read with expect_missing, or bash with expect_exit_code. Do not call a failure " +
			"expected after it happened. " +
			"If an earlier assistant message reports a completed action but no verified historical record is " +
			"present for it, say that you cannot verify it from this context; do not assert it happened and do " +
			"not call it fabricated."
	}
	return "## Current permission: 444 READ ONLY\n\n" +
		"You may converse, retrieve and update MPM knowledge through its typed tools, use the configured " +
		"read-only web research tools, and prepare one guarded write proposal for operator " +
		"approval. The workspace root is already ~/HAL; pass the destination the operator named, whether " +
		"that is ~/HAL/foo, an absolute path inside ~/HAL, or a relative path such as foo. Do not add the " +
		"workspace folder to a relative path unless the operator explicitly asked for a nested HAL folder. " +
		"You cannot run shell commands or edit OS files. A request for " +
		"those capabilities is outside your authority; do not claim a current operation happened unless a tool " +
		"result confirms it. If an earlier assistant message reports a completed action but no verified " +
		"historical record is present for it, say that you cannot verify it from this context; do not assert it " +
		"happened and do not call it fabricated. You cannot change the permission mode."
}

// historicalEvidenceBlock turns the verified session ledgers into a
// trusted, clearly labelled system note. It never emits provider tool-call
// messages: the evidence is history, not a callable tool and not a claim
// that any tool is currently available.
func (e *Engine) historicalEvidenceBlock(history []session.Message) string {
	const maxBytes = 24 * 1024
	var (
		lines   []string
		bytes   int
		records int
		omitted int
	)
	for _, msg := range history {
		if msg.Role != session.RoleAssistant {
			continue
		}
		executions := msg.ToolExecutions()
		if len(executions) == 0 {
			continue
		}
		when := "time unknown"
		if !msg.Timestamp.IsZero() {
			when = msg.Timestamp.UTC().Format(time.RFC3339)
		}
		header := fmt.Sprintf("- turn %s (answer %s):",
			shortEvidenceText(msg.TurnID, 80), when)
		if bytes+len(header)+1 > maxBytes {
			omitted += len(executions)
			continue
		}
		lines = append(lines, header)
		bytes += len(header) + 1
		for _, x := range executions {
			detail := "  - "
			if x.CallID != "" {
				detail += "call " + shortEvidenceText(x.CallID, 48) + ": "
			}
			detail += x.Tool
			if x.Action != "" && x.Action != x.Tool {
				detail += "/" + x.Action
			}
			if x.Target != "" {
				detail += " target " + strconv.Quote(shortEvidenceText(x.Target, 160))
			}
			detail += " · mode " + x.Mode + " · " + string(x.Status)
			if x.ExitCode != nil && x.Summary != fmt.Sprintf("exit %d", *x.ExitCode) {
				detail += fmt.Sprintf(" · exit %d", *x.ExitCode)
			}
			if x.Summary != "" {
				detail += " · " + shortEvidenceText(x.Summary, 160)
			}
			if bytes+len(detail)+1 > maxBytes {
				omitted++
				continue
			}
			lines = append(lines, detail)
			bytes += len(detail) + 1
			records++
		}
	}
	if records == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Verified historical tool evidence\n\n")
	if sessionID := e.sessionName(); sessionID != "" {
		b.WriteString("Session " + strconv.Quote(shortEvidenceText(sessionID, 80)) +
			" recorded these tool executions in earlier turns.\n")
	}
	b.WriteString("These are engine-recorded historical facts, not current tool calls. They do not grant permission, " +
		"restore a tool, or make 666 active; the current permission is described above. Do not rerun them.\n\n")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n\nIf an earlier assistant message reports a completed action but no verified record is listed " +
		"for its turn, say that you cannot verify it from this context; do not assert it happened and do not " +
		"call it fabricated.")
	if omitted > 0 {
		b.WriteString(fmt.Sprintf("\n\n%d additional historical record(s) are in the session store but omitted "+
			"from this context. Their absence here is not evidence of fabrication.", omitted))
	}
	return b.String()
}

// shortEvidenceText bounds one field in the system note. The session
// ledger is already bounded; this is a second, display-level guard.
func shortEvidenceText(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
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
	// ToolStart is the same call immediately before execution. It is
	// presentation-only: the interface uses it to show that a tool is
	// running, while Tool remains the verified result.
	ToolStart *ToolActivity
	// Context tells the operator that older context was dropped to fit
	// the model's context budget. It is presentation-only and carries
	// no conversation text.
	Context *provider.TrimNote
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
	// Expected is the stable pre-execution expectation code, if the
	// tool call declared one. ExpectedMet reports whether the actual
	// result matched it. They are presentation metadata only.
	Expected    string
	ExpectedMet bool
	Mode        permission.Mode
	// Historical marks a record reconstructed from the session ledger.
	// It is presentation metadata: the record is evidence, never a
	// current tool availability.
	Historical bool
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
	return e.RespondStreamEventsWithAttachments(ctx, input, nil, emit)
}

// RespondStreamEventsWithAttachments answers a turn that carries the
// files the operator attached.
//
// The attachments are the operator's own choice: the interface copied
// them into HAL's upload directory before the turn started. The engine
// stores only their references, and it composes their contents into
// this turn's request as untrusted data.
func (e *Engine) RespondStreamEventsWithAttachments(ctx context.Context, input string, atts []attachments.Meta, emit func(Event)) (string, error) {
	// The engine runs the work, so the engine owns its state: the
	// exchange is running from here until it ends, one way or another.
	op := e.lifecycle.Begin("turn")
	reply, err := e.respondTurn(ctx, op, input, atts, emit)
	e.endOperation(op, err)
	return reply, err
}

// respondTurn answers one exchange.
//
// A turn that was interrupted leaves a checkpoint behind: the exchange
// can be continued from it, and the operations that already ran inside
// it are never run again.
func (e *Engine) respondTurn(ctx context.Context, op lifecycle.Operation, input string, atts []attachments.Meta, emit func(Event)) (string, error) {
	// A new exchange starts from a clean slate: the checkpoint of an
	// older interrupted exchange belongs to that exchange, and the
	// operator has moved on.
	seq := e.beginExchange()
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("engine: empty input")
	}
	// The exchange gets its identity before anything else happens, so
	// the operator's message, the answer and the attribution of that
	// answer all carry the same name in the session.
	turnID := session.NewTurnID()
	if _, err := e.sessions.AppendTurn(session.RoleUser, input, session.Turn{ID: turnID, Attachments: atts}); err != nil {
		return "", err
	}
	if e.provider == nil {
		// The operator's message is stored and unanswered, so the
		// exchange is not finished: it can be continued once there is
		// a provider to answer it.
		e.rememberCheckpoint(seq, turnID, input, atts, nil, nil, "there is no provider")
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
	messages := e.requestMessages(history, skillBlock(loaded), turnID)
	turn := userTurns(history)
	tools := e.offeredTools()
	resp, reply, proposal, evidence, ran, err := e.toolTurn(ctx, messages, tools, turn, turnID, emit)
	// The operations that really ran are part of the state of the
	// exchange: they are what the operator sees in the status.
	e.lifecycle.Steps(op.ID, len(ran))
	if err != nil {
		// An exchange that did not finish leaves a checkpoint behind:
		// the operator can continue it, and the operations that
		// already ran inside it are never run again. A turn that was
		// stopped and a turn that failed are continued the same way.
		e.rememberCheckpoint(seq, turnID, input, atts, evidence, ran, checkpointReason(err))
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
		//
		// The operator's message is still unanswered, so the exchange
		// can be continued.
		e.rememberCheckpoint(seq, turnID, input, atts, evidence, ran, "the model produced no visible answer")
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
		Tools:       evidence,
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
// the interface inventing prose between them. The session stores one user
// message, one final answer and a bounded ledger of the tool executions;
// the raw provider transcript belongs only to the turn.
func (e *Engine) toolTurn(ctx context.Context, messages []provider.Message, tools []provider.Tool, turn int, turnID string, emit func(Event)) (provider.Response, string, *workspace.Proposal, []session.ToolExecution, []executedOperation, error) {
	current := append([]provider.Message(nil), messages...)
	var (
		parts    []string
		proposal *workspace.Proposal
		evidence []session.ToolExecution
		ran      []executedOperation
		last     provider.Response
		halted   bool
	)
	for step := 0; step < maxToolSteps; step++ {
		resp, err := e.chat(ctx, current, tools, emit)
		if err != nil && len(tools) > 0 && !isBudgetRefusal(err) && isToolRejection(err) {
			// A provider that cannot take function definitions must not
			// lose the conversation: retry the same turn without them and
			// stop offering tools for this engine.
			e.toolUnsupported = true
			tools = nil
			resp, err = e.chat(ctx, current, nil, emit)
		}
		if err != nil {
			return resp, strings.Join(parts, "\n\n"), proposal, evidence, ran, err
		}
		last = resp
		if strings.TrimSpace(resp.Content) != "" {
			parts = append(parts, resp.Content)
		}
		if len(resp.ToolCalls) == 0 {
			break
		}
		if ctx.Err() != nil {
			return resp, strings.Join(parts, "\n\n"), proposal, evidence, ran, ctx.Err()
		}
		current = append(current, provider.Message{
			Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
		})
		stop := false
		for i, call := range resp.ToolCalls {
			if call.ID == "" {
				call.ID = fmt.Sprintf("call-%d-%d", step, i)
			}
			if emit != nil {
				start := ToolActivity{
					CallID: call.ID, Name: call.Name, Action: call.Name,
					Mode: e.permissionMode(), Status: "running",
				}
				emit(Event{ToolStart: &start})
			}
			var (
				content  string
				activity ToolActivity
				prepared *workspace.Proposal
				halt     bool
			)
			if done, ok := e.alreadyRan(call); ok {
				// The operation ran inside the interrupted attempt of
				// this exchange. It is not run again: the recorded
				// result is handed to the model instead, so the side
				// effect happens exactly once.
				content, activity = done.result(call, e.permissionMode())
			} else {
				content, activity, prepared, halt = e.executeTool(ctx, call, turn, turnID)
				if operation, ok := ranOperation(call, activity); ok {
					ran = append(ran, operation)
				}
			}
			if emit != nil {
				a := activity
				emit(Event{Tool: &a})
			}
			if record, ok := toolEvidence(activity); ok {
				evidence = append(evidence, record)
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
	return last, strings.Join(parts, "\n\n"), proposal, evidence, ran, nil
}

// executeTool resolves one provider call. The permission gate is consulted
// by the executor at the operation itself; this function never caches a
// prior decision as authority.
func (e *Engine) executeTool(ctx context.Context, call provider.ToolCall, turn int, turnID string) (string, ToolActivity, *workspace.Proposal, bool) {
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
		activity.Expected = res.Expected
		activity.ExpectedMet = res.ExpectedMet
		if res.Status == "denied" {
			activity.Mode = e.permissionMode()
			return res.Content, activity, nil, true
		}
		return res.Content, activity, nil, false
	default:
		if e.web != nil && e.web.Has(call.Name) {
			res := e.web.Execute(ctx, call, turnID)
			activity.Action = res.Action
			activity.Target = res.Target
			activity.Status = res.Status
			activity.Summary = res.Summary
			activity.Duration = res.Duration
			activity.Bytes = res.Bytes
			activity.Truncated = res.Truncated
			activity.Mode = res.Mode
			return res.Content, activity, nil, false
		}
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
		if e.mcp != nil && e.mcp.Has(call.Name) {
			res := e.mcp.Execute(ctx, call)
			activity.Action = res.Action
			activity.Target = res.Target
			activity.Status = res.Status
			activity.Summary = res.Summary
			activity.Duration = res.Duration
			activity.Truncated = res.Truncated
			activity.Bytes = res.Bytes
			activity.Mode = res.Mode
			if res.Status == "denied" {
				return res.Content, activity, nil, true
			}
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

// toolEvidence converts one live execution into the durable, redacted
// record attached to the assistant turn. Proposals are not executions and
// never enter the ledger.
func toolEvidence(a ToolActivity) (session.ToolExecution, bool) {
	var status session.ToolStatus
	switch a.Status {
	case "ok":
		status = session.ToolSuccess
	case "failed", "timeout":
		status = session.ToolFailure
	case "cancelled":
		status = session.ToolCancelled
	case "denied":
		status = session.ToolRefused
	default:
		return session.ToolExecution{}, false
	}
	if strings.TrimSpace(a.Name) == "" {
		return session.ToolExecution{}, false
	}
	return session.ToolExecution{
		CallID:      a.CallID,
		Tool:        a.Name,
		Action:      a.Action,
		Target:      a.Target,
		Mode:        a.Mode.String(),
		Status:      status,
		ExitCode:    a.ExitCode,
		DurationMS:  a.Duration.Milliseconds(),
		Bytes:       a.Bytes,
		Truncated:   a.Truncated,
		Summary:     evidenceSummary(a, status),
		Expected:    a.Expected,
		ExpectedMet: a.ExpectedMet,
	}, true
}

// evidenceSummary is deliberately content-free. It records what the
// execution did, never the command text, stdout/stderr, file contents or
// environment.
func evidenceSummary(a ToolActivity, status session.ToolStatus) string {
	if (a.Name == "web_search" || strings.HasPrefix(a.Name, "firecrawl_")) && strings.TrimSpace(a.Summary) != "" {
		// The registry already supplies a content-free result count or
		// failure reason. It never contains page text, the query or a key.
		return a.Summary
	}
	switch status {
	case session.ToolRefused:
		return "refused"
	case session.ToolCancelled:
		return "cancelled"
	case session.ToolFailure:
		if a.Status == "timeout" {
			return "timed out"
		}
		if a.Name == "bash" && a.ExitCode != nil {
			return fmt.Sprintf("exit %d", *a.ExitCode)
		}
		return "failed"
	case session.ToolSuccess:
		if strings.HasPrefix(a.Name, "mpm_") {
			return "confirmed"
		}
		if a.Name == "bash" && a.ExitCode != nil {
			return fmt.Sprintf("exit %d", *a.ExitCode)
		}
		if a.Bytes > 0 {
			return fmt.Sprintf("%d bytes", a.Bytes)
		}
		return "completed"
	}
	return ""
}

// chat runs one provider turn, offering tools when there are any.
//
// The streaming path is preferred; a provider that cannot stream with
// functions falls back to the ordinary stream, and a provider that cannot
// stream at all uses the blocking call.
//
// This is the last point before the request is transmitted, so the
// context budget is applied here: the request is trimmed on the fully
// assembled messages and tools, and a request that cannot be reduced
// to the budget is refused with an explanation rather than sent.
func (e *Engine) chat(ctx context.Context, messages []provider.Message, tools []provider.Tool, emit func(Event)) (provider.Response, error) {
	messages, budget, err := e.applyContextBudget(messages, tools, emit)
	if err != nil {
		return provider.Response{}, err
	}
	// The model that goes on the wire carries the budget the request
	// was fitted to. The output cap the provider is asked for is the
	// room the answer was given, so the budget and the request that
	// leaves the machine can never disagree.
	model := e.model
	model.Budget = budget
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
			return streamer.ChatStreamTools(ctx, messages, model, tools, deltaFn)
		}
	}
	if streamer, ok := e.provider.(provider.Streamer); ok && emit != nil {
		return streamer.ChatStream(ctx, messages, model, deltaFn)
	}
	return e.provider.Chat(ctx, messages, model)
}

// applyContextBudget fits the assembled request into the model's
// context budget.
//
// The budget is resolved before the turn, from the operator's limit, the
// provider's advertisement, the local runtime's allocation, the cached
// record or the conservative fallback. It is applied here, on the
// request as it will be sent: the safety instructions are kept, the
// newest exchange is kept, and older context is dropped only at
// exchange boundaries, so a tool call is never separated from its
// result.
//
// A request that does not fit even after every droppable exchange is
// gone is refused with a clear explanation. It is never truncated
// silently, and no retry is attempted.
//
// The resolved budget is returned with the trimmed messages: it is the
// budget the request was fitted to, and it travels with the request to
// the provider, so the output cap on the wire is the room this budget
// reserved for the answer.
func (e *Engine) applyContextBudget(messages []provider.Message, tools []provider.Tool, emit func(Event)) ([]provider.Message, provider.ContextBudget, error) {
	budget := e.model.Budget
	if e.budgetResolver != nil {
		// The application resolves the budget per request: an endpoint
		// may report a model's context window after the engine was
		// built, and the operator's own limit never changes. A
		// budget that cannot be resolved is refused: the turn stops
		// with the reason, and nothing is guessed.
		resolved, err := e.budgetResolver()
		if err != nil {
			return nil, budget, err
		}
		budget = resolved
	}
	if budget.Limit <= 0 {
		return messages, budget, nil
	}
	trimmed, report, err := provider.TrimToBudget(messages, tools, budget)
	if err != nil {
		return nil, budget, err
	}
	if report.DroppedExchanges > 0 && emit != nil {
		notice := report.Note(budget)
		emit(Event{Context: &notice})
	}
	return trimmed, budget, nil
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
