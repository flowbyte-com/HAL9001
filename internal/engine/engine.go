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
	"time"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/capability"
	"github.com/flowbyte-com/HAL9001/internal/knowledge"
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

// WithWebResearch attaches Firecrawl's read-only research tools. They are
// offered in 444; the registry is never a permission authority and cannot
// reach the 666 executor or MPM's write tools.
func (e *Engine) WithWebResearch(w *websearch.Registry) *Engine {
	e.web = w
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
	if e.web != nil {
		if block := e.web.Guidance(); block != "" {
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
	resp, reply, proposal, evidence, err := e.toolTurn(ctx, messages, tools, turn, turnID, emit)
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
func (e *Engine) toolTurn(ctx context.Context, messages []provider.Message, tools []provider.Tool, turn int, turnID string, emit func(Event)) (provider.Response, string, *workspace.Proposal, []session.ToolExecution, error) {
	current := append([]provider.Message(nil), messages...)
	var (
		parts    []string
		proposal *workspace.Proposal
		evidence []session.ToolExecution
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
			return resp, strings.Join(parts, "\n\n"), proposal, evidence, err
		}
		last = resp
		if strings.TrimSpace(resp.Content) != "" {
			parts = append(parts, resp.Content)
		}
		if len(resp.ToolCalls) == 0 {
			break
		}
		if ctx.Err() != nil {
			return resp, strings.Join(parts, "\n\n"), proposal, evidence, ctx.Err()
		}
		current = append(current, provider.Message{
			Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
		})
		stop := false
		for i, call := range resp.ToolCalls {
			if call.ID == "" {
				call.ID = fmt.Sprintf("call-%d-%d", step, i)
			}
			content, activity, prepared, halt := e.executeTool(ctx, call, turn, turnID)
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
	return last, strings.Join(parts, "\n\n"), proposal, evidence, nil
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
