package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flowbyte-com/HAL9001/internal/attachments"
	"github.com/flowbyte-com/HAL9001/internal/lifecycle"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The lifecycle of an exchange.
//
// The engine runs the work, so the engine owns its state: what is
// running, what finished, what was stopped and what can be continued.
// The interface asks the engine about it and never guesses.
//
// The checkpoint is the safe point of an interrupted exchange. It holds
// the identity of the exchange, the operator's message and the
// operations that already ran inside it. A continuation starts there
// and never replays an operation that already ran: the recorded result
// is handed to the model instead.

// ErrNothingToResume reports that there is no interrupted exchange to
// continue.
//
// It is not a failure of the model or of the provider: there is simply
// no safe point, and the operator's message stays unanswered.
var ErrNothingToResume = errors.New("there is no interrupted exchange to continue")

// checkpoint is the safe point of an interrupted exchange.
type checkpoint struct {
	// turnID is the identity of the exchange.
	turnID string
	// input is the operator's message that is still unanswered.
	input string
	// atts are the files the operator attached to the exchange.
	atts []attachments.Meta
	// evidence is what the exchange performed, in the session's own
	// ledger form. It is carried into the continued exchange, so the
	// record of what really ran is never lost.
	evidence []session.ToolExecution
	// ran are the operations that reached the executor, with the
	// fingerprint that identifies the exact call.
	ran []executedOperation
	// reason says why the exchange was interrupted.
	reason string
}

// executedOperation is one operation that ran inside an exchange.
//
// The fingerprint identifies the exact call: the same tool with the
// same arguments. It is what lets a continuation recognise an
// operation that already happened.
type executedOperation struct {
	// CallID is the provider's identity of the call.
	CallID string
	// Tool is the name of the tool.
	Tool string
	// Action is what the tool did.
	Action string
	// Target is what it acted on.
	Target string
	// Fingerprint identifies the exact call.
	Fingerprint string
	// Summary is the short, redacted description of the outcome.
	Summary string
	// Status is the outcome of the execution.
	Status string
}

// ranOperation records one executed operation.
//
// An operation that never reached the executor is not recorded: a
// refused or denied call changed nothing, and the operator may ask for
// it again.
func ranOperation(call provider.ToolCall, activity ToolActivity) (executedOperation, bool) {
	switch activity.Status {
	case "ok", "failed", "cancelled", "timeout":
	default:
		return executedOperation{}, false
	}
	return executedOperation{
		CallID:      call.ID,
		Tool:        call.Name,
		Action:      activity.Action,
		Target:      activity.Target,
		Fingerprint: fingerprint(call),
		Summary:     activity.Summary,
		Status:      activity.Status,
	}, true
}

// fingerprint identifies one exact call.
//
// The tool's name and its arguments decide it: the same tool with
// different arguments is a different operation. The arguments are
// hashed, never kept: a fingerprint says "the same call", not what
// the call was.
func fingerprint(call provider.ToolCall) string {
	sum := sha256.Sum256([]byte(call.Name + "\x00" + call.ArgumentsJSON()))
	return hex.EncodeToString(sum[:])
}

// result renders the recorded outcome of an operation that already ran.
//
// It is what the model receives instead of a second execution. The
// record says the work is done, and it never carries the content of
// the original result: the model is told the outcome, not the output.
func (op executedOperation) result(call provider.ToolCall, mode permission.Mode) (string, ToolActivity) {
	summary := op.Summary
	if summary == "" {
		summary = op.Status
	}
	payload, _ := json.Marshal(map[string]any{
		"ok":           true,
		"tool":         call.Name,
		"already_done": true,
		"status":       op.Status,
		"summary":      summary,
		"note": "this operation already ran in the interrupted attempt of this exchange; " +
			"it was not run again",
	})
	activity := ToolActivity{
		CallID: call.ID, Name: call.Name, Action: op.Action, Target: op.Target,
		Mode: mode, Status: "already done", Summary: summary,
	}
	return string(payload), activity
}

// Lifecycle reports the state of the exchange the engine is running, or
// the last one it finished.
//
// The zero operation means nothing has run yet.
func (e *Engine) Lifecycle() lifecycle.Operation {
	if op := e.lifecycle.Current(); op.ID != "" {
		return e.withCheckpoint(op)
	}
	return e.withCheckpoint(e.lifecycle.Last())
}

// withCheckpoint marks the operation as continuable when a checkpoint
// is waiting for it.
func (e *Engine) withCheckpoint(op lifecycle.Operation) lifecycle.Operation {
	op.Resumable = e.hasCheckpoint()
	return op
}

// hasCheckpoint reports whether a safe point is waiting to be continued.
func (e *Engine) hasCheckpoint() bool {
	e.checkpointMu.Lock()
	defer e.checkpointMu.Unlock()
	return e.checkpoint != nil
}

// endOperation records how an exchange ended.
//
// A cancellation is a stop, not a failure: the operator asked for it.
func (e *Engine) endOperation(op lifecycle.Operation, err error) {
	switch {
	case err == nil:
		e.lifecycle.Complete(op.ID)
	case errors.Is(err, context.Canceled):
		e.lifecycle.Stop(op.ID, "the operator stopped it")
	default:
		e.lifecycle.Fail(op.ID, err.Error())
	}
}

// beginExchange marks the start of a new exchange.
//
// The number it returns identifies the exchange: a report about an
// interrupted exchange that arrives after a newer exchange has started
// is history, and its safe point would answer a question that is no
// longer the current one.
func (e *Engine) beginExchange() int {
	e.checkpointMu.Lock()
	defer e.checkpointMu.Unlock()
	e.exchangeSeq++
	// A new exchange starts from a clean slate: the checkpoint of an
	// older interrupted exchange belongs to that exchange, and the
	// operator has moved on.
	e.checkpoint = nil
	return e.exchangeSeq
}

// rememberCheckpoint keeps the safe point of an interrupted exchange.
//
// The exchange is identified by the number it was given when it
// started. A checkpoint for an exchange that is no longer the current
// one is dropped: it could only continue a question the operator has
// already moved past.
func (e *Engine) rememberCheckpoint(seq int, turnID, input string, atts []attachments.Meta,
	evidence []session.ToolExecution, ran []executedOperation) {
	cp := &checkpoint{
		turnID:   turnID,
		input:    input,
		atts:     atts,
		evidence: evidence,
		ran:      ran,
		reason:   "the operator stopped it",
	}
	e.checkpointMu.Lock()
	defer e.checkpointMu.Unlock()
	if seq != e.exchangeSeq {
		return
	}
	e.checkpoint = cp
}

// forgetCheckpoint drops the safe point of an interrupted exchange.
//
// A new exchange starts from a clean slate: the checkpoint belongs to
// the exchange that was interrupted, and the operator has moved on.
func (e *Engine) forgetCheckpoint() {
	e.checkpointMu.Lock()
	defer e.checkpointMu.Unlock()
	e.checkpoint = nil
}

// extendCheckpoint adds what a continued attempt did to the safe point.
//
// A continuation that is interrupted as well leaves its own operations
// behind. They join the checkpoint, so a second continuation does not
// run them again and the ledger of the exchange keeps one record of
// each.
func (e *Engine) extendCheckpoint(evidence []session.ToolExecution, ran []executedOperation) {
	e.checkpointMu.Lock()
	defer e.checkpointMu.Unlock()
	if e.checkpoint == nil {
		return
	}
	e.checkpoint.evidence = append(e.checkpoint.evidence, evidence...)
	for _, operation := range ran {
		known := false
		for _, existing := range e.checkpoint.ran {
			if existing.Fingerprint == operation.Fingerprint {
				known = true
				break
			}
		}
		if !known {
			e.checkpoint.ran = append(e.checkpoint.ran, operation)
		}
	}
}

// alreadyRan reports the recorded outcome of an operation that ran
// inside the interrupted exchange of this exchange.
//
// It is the guard that keeps a continuation from replaying an external
// side effect: the operation is not run again, and the model is told
// what happened instead.
func (e *Engine) alreadyRan(call provider.ToolCall) (executedOperation, bool) {
	want := fingerprint(call)
	e.checkpointMu.Lock()
	defer e.checkpointMu.Unlock()
	if e.checkpoint == nil {
		return executedOperation{}, false
	}
	for _, op := range e.checkpoint.ran {
		if op.Fingerprint == want {
			return op, true
		}
	}
	return executedOperation{}, false
}

// Resume continues the exchange that was interrupted.
//
// The exchange is continued from its checkpoint: the operator's message
// is not stored a second time, and the operations that already ran
// inside it are not run again.
//
// It is refused when there is no checkpoint, and when the conversation
// has moved on: the operator's message must still be the last one, or
// the exchange that would be continued is not the one the operator is
// looking at.
func (e *Engine) Resume(ctx context.Context, emit func(Event)) (string, error) {
	e.checkpointMu.Lock()
	cp := e.checkpoint
	e.checkpointMu.Unlock()
	if cp == nil {
		return "", ErrNothingToResume
	}
	history, err := e.sessions.History()
	if err != nil {
		return "", err
	}
	if !awaitingAnswer(history, cp.turnID) {
		// The conversation moved on: the operator's message is no
		// longer the last one, so continuing would answer a question
		// that is not the current one.
		e.forgetCheckpoint()
		return "", ErrNothingToResume
	}
	if e.provider == nil {
		return "", ErrNoProvider
	}
	// The continued exchange is an operation like any other.
	op := e.lifecycle.Begin("turn")
	reply, evidence, ran, err := e.continueExchange(ctx, op, cp, history, emit)
	e.endOperation(op, err)
	switch {
	case err == nil:
		// The exchange is whole now: there is nothing left to
		// continue.
		e.forgetCheckpoint()
	case errors.Is(err, context.Canceled):
		// The continuation was interrupted as well. What it did joins
		// the checkpoint: a second continuation must not run it again.
		e.extendCheckpoint(evidence, ran)
	}
	return reply, err
}

// continueExchange answers the interrupted exchange from its checkpoint.
func (e *Engine) continueExchange(ctx context.Context, op lifecycle.Operation, cp *checkpoint,
	history []session.Message, emit func(Event)) (string, []session.ToolExecution, []executedOperation, error) {
	// The skill is chosen again, from the operator's own message: the
	// continuation is the same exchange, so it is written with the
	// same guidance.
	decision, loaded := e.prepareSkill(ctx, cp.input)
	attribution := attributionOf(cp.turnID, decision)
	if emit != nil && decision.Source != "" {
		d := decision
		emit(Event{Skill: &d, Attribution: attribution, TurnID: cp.turnID})
	}
	messages := e.requestMessages(history, skillBlock(loaded), cp.turnID)
	turn := userTurns(history)
	tools := e.offeredTools()
	resp, reply, proposal, evidence, ran, err := e.toolTurn(ctx, messages, tools, turn, cp.turnID, emit)
	e.lifecycle.Steps(op.ID, len(cp.ran)+len(ran))
	if err != nil {
		if emit != nil && !resp.Stats.StartedAt.IsZero() {
			stats := resp.Stats
			emit(Event{Stats: &stats})
		}
		return "", evidence, ran, err
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
		finish := resp.FinishReason
		if finish == "" {
			finish = "unknown"
		}
		if resp.Reasoning != "" {
			return "", evidence, ran, fmt.Errorf("%w: the output budget was spent on reasoning (finish_reason %s)",
				ErrNoVisibleAnswer, finish)
		}
		return "", evidence, ran, fmt.Errorf("%w (finish_reason %s)", ErrNoVisibleAnswer, finish)
	}
	// The ledger of the continued exchange carries what the interrupted
	// attempt did as well: the record of what really ran is never lost.
	ledger := append(append([]session.ToolExecution(nil), cp.evidence...), evidence...)
	if _, err := e.sessions.AppendTurn(session.RoleAssistant, reply, session.Turn{
		ID:          cp.turnID,
		Attribution: attribution,
		Tools:       ledger,
	}); err != nil {
		return "", evidence, ran, err
	}
	if proposal != nil && emit != nil {
		emit(Event{Proposal: proposal})
	}
	return reply, evidence, ran, nil
}

// awaitingAnswer reports whether the exchange is still waiting for its
// answer.
//
// It is true when the last stored message is the operator's message of
// that exchange: nothing has been said since, so the exchange can be
// continued without answering a different question.
func awaitingAnswer(history []session.Message, turnID string) bool {
	if len(history) == 0 {
		return false
	}
	last := history[len(history)-1]
	return last.Role == session.RoleUser && last.TurnID == turnID
}
