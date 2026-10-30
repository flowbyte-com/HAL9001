// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// SetupTurn extracts the one-time initialization phase, returning a
// turnExecution populated with history, messages, and candidate selection.
// It replaces lines 56-145 of the original runTurn.
func (p *Pipeline) SetupTurn(ctx context.Context, ts *turnState) (*turnExecution, error) {
	cfg := p.Cfg
	mediaOpts := mediaResolveOptionsFor(cfg)

	var history []providers.Message
	var summary string
	if !ts.opts.NoHistory {
		if resp, err := p.ContextManager.Assemble(ctx, &AssembleRequest{
			SessionKey: ts.sessionKey,
			Budget:     ts.agent.ContextWindow,
			MaxTokens:  ts.agent.MaxTokens,
		}); err == nil && resp != nil {
			history = resp.History
			summary = resp.Summary
		} else if err != nil {
			warnSessionStoreError(ts.sessionKey, "assemble", err)
		}
	}
	ts.captureRestorePoint(history, summary)

	if ts.agent != nil && ts.agent.MPMRetriever != nil && strings.TrimSpace(ts.userMessage) != "" {
		retrievalStart := time.Now()
		retrieval, retrieveErr := ts.agent.MPMRetriever.Retrieve(ctx, ts.userMessage)
		if retrieveErr != nil {
			// Retrieved memory is advisory, read-only context. A retrieval
			// failure degrades this turn to no fresh context instead of
			// aborting the user's work; the error is explicit in the log.
			logger.WarnCF("agent", "MPM retrieval failed; continuing without retrieved context",
				map[string]any{
					"session_key": ts.sessionKey,
					"error":       retrieveErr.Error(),
				})
			retrieval = nil
		} else {
			logger.DebugCF("agent", "MPM retrieval", map[string]any{
				"duration_ms": time.Since(retrievalStart).Milliseconds(),
				"records":     len(retrieval.Records),
				"tokens":      retrieval.Tokens,
			})
		}
		if ts.agent.ActiveMemory != nil {
			prev, loadErr := ts.agent.ActiveMemory.Load(ts.sessionKey)
			if loadErr != nil {
				logger.WarnCF("agent", "Active memory load failed; starting from fresh scoped state",
					map[string]any{
						"session_key": ts.sessionKey,
						"error":       loadErr.Error(),
					})
				prev = nil
			}
			projectID := ""
			if ts.agent.MPMRetriever != nil {
				projectID = ts.agent.MPMRetriever.Config().ProjectID
			}
			ws := MergeActiveWorkingSet(prev, retrieval, ts.userMessage, projectID, 500)
			ws.SessionKey = ts.sessionKey
			if err := ts.agent.ActiveMemory.Save(ws); err != nil {
				return nil, fmt.Errorf("mpm active memory save failed: %w", err)
			}
			if text := strings.TrimSpace(ws.Render(500)); text != "" {
				ts.mpmRetrievedContext = text
			}
		} else if retrieval != nil && strings.TrimSpace(retrieval.Text) != "" {
			ts.mpmRetrievedContext = strings.TrimSpace(retrieval.Text)
		}
	}

	contextualSkills := ts.activeSkills
	if ts.agent.ContextBuilder != nil {
		contextualSkills = ts.agent.ContextBuilder.ResolveActiveSkillsForContext(ts.activeSkills)
	}
	ts.recordSkillContextSnapshot(skillContextTriggerInitialBuild, contextualSkills)
	initialPromptReq := promptBuildRequestForTurn(ts, history, summary, ts.userMessage, ts.media, cfg)
	initialPromptReq.ActiveSkills = append([]string(nil), contextualSkills...)
	messages := ts.agent.ContextBuilder.BuildMessagesFromPrompt(initialPromptReq)
	currentTurnStart := len(messages)
	if strings.TrimSpace(ts.userMessage) != "" || len(ts.media) > 0 {
		currentTurnStart = len(messages) - 1
	}

	messages = resolveMediaRefsWithOptions(messages, p.MediaStore, mediaOpts, currentTurnStart)

	if !ts.opts.NoHistory {
		toolDefs := filterToolsByTurnProfile(ts.agent.Tools.ToProviderDefs(), ts.profile)
		if isOverContextBudget(ts.agent.ContextWindow, messages, toolDefs, ts.agent.MaxTokens) {
			logger.WarnCF("agent", "Proactive compression: context budget exceeded before LLM call",
				map[string]any{"session_key": ts.sessionKey})
			if err := p.ContextManager.Compact(ctx, &CompactRequest{
				SessionKey: ts.sessionKey,
				Reason:     ContextCompressReasonProactive,
				Budget:     ts.agent.ContextWindow,
			}); err != nil {
				logger.WarnCF("agent", "Proactive compact failed", map[string]any{
					"session_key": ts.sessionKey,
					"error":       err.Error(),
				})
			}
			ts.refreshRestorePointFromSession(ts.agent)
			if resp, err := p.ContextManager.Assemble(ctx, &AssembleRequest{
				SessionKey: ts.sessionKey,
				Budget:     ts.agent.ContextWindow,
				MaxTokens:  ts.agent.MaxTokens,
			}); err == nil && resp != nil {
				history = resp.History
				summary = resp.Summary
			} else if err != nil {
				warnSessionStoreError(ts.sessionKey, "assemble", err)
			}
			originalHistoryCount := len(history)
			var fit bool
			history, messages, fit = trimHistoryToFitContextWindow(
				history,
				func(trimmedHistory []providers.Message) []providers.Message {
					rebuildPromptReq := promptBuildRequestForTurn(
						ts,
						trimmedHistory,
						summary,
						ts.userMessage,
						ts.media,
						cfg,
					)
					rebuildPromptReq.ActiveSkills = append([]string(nil), contextualSkills...)
					rebuilt := ts.agent.ContextBuilder.BuildMessagesFromPrompt(rebuildPromptReq)
					rebuiltCurrentTurnStart := len(rebuilt)
					if strings.TrimSpace(ts.userMessage) != "" || len(ts.media) > 0 {
						rebuiltCurrentTurnStart = len(rebuilt) - 1
					}
					return resolveMediaRefsWithOptions(rebuilt, p.MediaStore, mediaOpts, rebuiltCurrentTurnStart)
				},
				ts.agent.ContextWindow,
				toolDefs,
				ts.agent.MaxTokens,
			)
			if dropped := originalHistoryCount - len(history); dropped > 0 {
				logger.WarnCF("agent", "Trimmed rebuilt history after proactive compaction", map[string]any{
					"session_key":     ts.sessionKey,
					"dropped_msgs":    dropped,
					"remaining_msgs":  len(history),
					"context_window":  ts.agent.ContextWindow,
					"max_tokens":      ts.agent.MaxTokens,
					"still_overlimit": !fit,
				})
			} else if !fit {
				logger.WarnCF("agent", "Context still exceeds budget "+
					"after proactive compaction rebuild", map[string]any{
					"session_key":    ts.sessionKey,
					"history_msgs":   len(history),
					"context_window": ts.agent.ContextWindow,
					"max_tokens":     ts.agent.MaxTokens,
				})
			}
		}
	}

	if !ts.opts.NoHistory && (strings.TrimSpace(ts.userMessage) != "" || len(ts.media) > 0) {
		rootMsg := userPromptMessage(ts.userMessage, ts.media)
		if len(rootMsg.Media) > 0 {
			warnSessionStoreError(ts.sessionKey, "add full message",
				ts.agent.Sessions.AddFullMessage(ctx, ts.sessionKey, rootMsg))
		} else {
			warnSessionStoreError(ts.sessionKey, "add message",
				ts.agent.Sessions.AddMessage(ctx, ts.sessionKey, rootMsg.Role, rootMsg.Content))
		}
		ts.recordPersistedMessage(rootMsg)
		ts.ingestMessage(ctx, p.al, rootMsg)
	}

	// Model routing scores the turn against the assembled session history
	// (not the rendered prompt): ConversationDepth and RecentToolCalls must
	// reflect real prior conversation, not the system prompt or the current
	// user message that BuildMessagesFromPrompt injects. hasMedia carries the
	// structured attachments, which the classifier cannot see in the text.
	activeCandidates, activeModel, usedLight := p.al.selectCandidates(
		ts.agent,
		ts.userMessage,
		history,
		len(ts.media) > 0,
	)
	activeProvider := ts.agent.Provider
	if usedLight && ts.agent.LightProvider != nil {
		activeProvider = ts.agent.LightProvider
	}
	activeModelName := strings.TrimSpace(ts.agent.Model)
	if usedLight {
		activeModelName = strings.TrimSpace(lightModelName(ts.agent, true))
	}
	activeModelName = resolvedCandidateModelName(activeCandidates, activeModelName)

	exec := newTurnExecution(
		ts.agent,
		ts.opts,
		history,
		summary,
		messages,
	)
	exec.currentTurnStart = currentTurnStart
	exec.activeCandidates = activeCandidates
	exec.activeModel = activeModel
	exec.activeModelConfig = resolveActiveModelConfig(
		p.Cfg,
		ts.agent.Workspace,
		activeCandidates,
		activeModel,
		p.Cfg.Agents.Defaults.Provider,
	)
	exec.llmModelName = activeModelName
	exec.activeProvider = activeProvider
	exec.usedLight = usedLight

	return exec, nil
}
