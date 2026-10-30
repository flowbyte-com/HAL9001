package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

// deepClawScopedSessionPrefix marks a session key that has already been bound
// to a DeepClaw project. A key with this prefix is passed through unchanged so
// ListSessions consumers (seahorse bootstrap, pruning) do not double-scope.
const deepClawScopedSessionPrefix = "sk_v1_dc1_"

// projectScopeDigest binds a scoped key to one project. The digest is embedded
// in the key so "already scoped" can be verified instead of trusted: a key
// carrying another project's digest is re-scoped into this project's namespace
// rather than passed through. That keeps a caller-supplied key from escaping
// its project by imitating the prefix.
func projectScopeDigest(projectID string) string {
	sum := sha256.Sum256([]byte("deepclaw-project-scope-v1\x00" + projectID))
	return hex.EncodeToString(sum[:8])
}

// projectScopedSessionKey returns the effective storage key for a DeepClaw
// session. The scope includes the project on purpose: workspace + agent store +
// project + base session key. A project switch in the same workspace therefore
// cannot read the previous project's JSONL history, summary or seahorse rows.
//
// An empty project leaves the key untouched (non-DeepClaw callers). The hash
// is case-preserving, unlike session.BuildOpaqueSessionKey, so project IDs
// that differ only in case remain distinct.
func projectScopedSessionKey(projectID, sessionKey string) string {
	projectID = strings.TrimSpace(projectID)
	sessionKey = strings.TrimSpace(sessionKey)
	if projectID == "" || sessionKey == "" {
		return sessionKey
	}
	prefix := deepClawScopedSessionPrefix + projectScopeDigest(projectID) + "_"
	if strings.HasPrefix(sessionKey, prefix) {
		return sessionKey
	}
	sum := sha256.Sum256([]byte("deepclaw-session-scope-v1\x00" + projectID + "\x00" + sessionKey))
	return prefix + hex.EncodeToString(sum[:])
}

// projectScopedSessionStore applies the DeepClaw project scope to every
// session-store operation. Routing, steering and outbound messages keep using
// the original opaque session key; only persisted conversation state is
// project-scoped.
type projectScopedSessionStore struct {
	inner     session.SessionStore
	projectID string
}

func newProjectScopedSessionStore(inner session.SessionStore, projectID string) session.SessionStore {
	if inner == nil || strings.TrimSpace(projectID) == "" {
		return inner
	}
	return &projectScopedSessionStore{inner: inner, projectID: projectID}
}

func (s *projectScopedSessionStore) scopeKey(sessionKey string) string {
	return projectScopedSessionKey(s.projectID, sessionKey)
}

func (s *projectScopedSessionStore) scopedPrefix() string {
	return deepClawScopedSessionPrefix + projectScopeDigest(s.projectID) + "_"
}

func (s *projectScopedSessionStore) AddMessage(
	ctx context.Context,
	sessionKey, role, content string,
) error {
	return s.inner.AddMessage(ctx, s.scopeKey(sessionKey), role, content)
}

func (s *projectScopedSessionStore) AddFullMessage(
	ctx context.Context,
	sessionKey string,
	msg providers.Message,
) error {
	return s.inner.AddFullMessage(ctx, s.scopeKey(sessionKey), msg)
}

func (s *projectScopedSessionStore) GetHistory(
	ctx context.Context,
	sessionKey string,
) ([]providers.Message, error) {
	return s.inner.GetHistory(ctx, s.scopeKey(sessionKey))
}

func (s *projectScopedSessionStore) GetSummary(ctx context.Context, sessionKey string) (string, error) {
	return s.inner.GetSummary(ctx, s.scopeKey(sessionKey))
}

func (s *projectScopedSessionStore) SetSummary(ctx context.Context, sessionKey, summary string) error {
	return s.inner.SetSummary(ctx, s.scopeKey(sessionKey), summary)
}

func (s *projectScopedSessionStore) SetHistory(
	ctx context.Context,
	sessionKey string,
	history []providers.Message,
) error {
	return s.inner.SetHistory(ctx, s.scopeKey(sessionKey), history)
}

func (s *projectScopedSessionStore) TruncateHistory(
	ctx context.Context,
	sessionKey string,
	keepLast int,
) error {
	return s.inner.TruncateHistory(ctx, s.scopeKey(sessionKey), keepLast)
}

func (s *projectScopedSessionStore) Save(ctx context.Context, sessionKey string) error {
	return s.inner.Save(ctx, s.scopeKey(sessionKey))
}

// ListSessions exposes only keys that belong to this project's scope. Legacy
// unscoped sessions and other projects' sessions are deliberately not listed or
// adopted; they remain on disk as quarantined data. This also keeps seahorse
// bootstrap and pruning from touching another project's rows.
func (s *projectScopedSessionStore) ListSessions() []string {
	keys := s.inner.ListSessions()
	prefix := s.scopedPrefix()
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	return out
}

func (s *projectScopedSessionStore) Close() error {
	return s.inner.Close()
}

// EnsureSessionMetadata implements session.MetadataAwareSessionStore so
// agentForSession can still resolve the owning agent through the wrapper.
func (s *projectScopedSessionStore) EnsureSessionMetadata(
	sessionKey string,
	scope *session.SessionScope,
	aliases []string,
) {
	meta, ok := s.inner.(session.MetadataAwareSessionStore)
	if !ok {
		return
	}
	scopedAliases := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		scopedAliases = append(scopedAliases, s.scopeKey(alias))
	}
	meta.EnsureSessionMetadata(s.scopeKey(sessionKey), scope, scopedAliases)
}

func (s *projectScopedSessionStore) ResolveSessionKey(sessionKey string) string {
	if meta, ok := s.inner.(session.MetadataAwareSessionStore); ok {
		return meta.ResolveSessionKey(s.scopeKey(sessionKey))
	}
	return s.scopeKey(sessionKey)
}

func (s *projectScopedSessionStore) GetSessionScope(sessionKey string) *session.SessionScope {
	if meta, ok := s.inner.(session.MetadataAwareSessionStore); ok {
		return meta.GetSessionScope(s.scopeKey(sessionKey))
	}
	return nil
}

// DeleteSession and LastActivity are used by the session pruner (pruner.Store
// and pruner.ActivityStore). They are not part of session.SessionStore, so the
// wrapper keeps forwarding them through type assertions; otherwise wrapping the
// store would silently disable pruning.
func (s *projectScopedSessionStore) DeleteSession(sessionKey string) error {
	deleter, ok := s.inner.(interface {
		DeleteSession(string) error
	})
	if !ok {
		return nil
	}
	return deleter.DeleteSession(s.scopeKey(sessionKey))
}

func (s *projectScopedSessionStore) LastActivity(sessionKey string) time.Time {
	if activity, ok := s.inner.(interface{ LastActivity(string) time.Time }); ok {
		return activity.LastActivity(s.scopeKey(sessionKey))
	}
	return time.Time{}
}

// projectScopedContextManager applies the same project scope to every context
// manager call. This covers managers (notably seahorse) that key their own
// summary store from the session key independently of agent.Sessions.
type projectScopedContextManager struct {
	inner     ContextManager
	projectID string
}

func newProjectScopedContextManager(inner ContextManager, projectID string) ContextManager {
	if inner == nil || strings.TrimSpace(projectID) == "" {
		return inner
	}
	return &projectScopedContextManager{inner: inner, projectID: projectID}
}

// deepClawProjectIDForLoop resolves the project identity used by this loop's
// DeepClaw agents. MPM configuration is process-global in the current design,
// so every DeepClaw agent should report the same project. The first agent with
// a retriever wins; a mixed-project loop is not supported.
func deepClawProjectIDForLoop(al *AgentLoop) string {
	if al == nil {
		return ""
	}
	registry := al.GetRegistry()
	if registry == nil {
		return ""
	}
	for _, id := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(id)
		if !ok || agent == nil || agent.MPMRetriever == nil {
			continue
		}
		return strings.TrimSpace(agent.MPMRetriever.Config().ProjectID)
	}
	return ""
}

func wrapContextManagerForDeepClaw(al *AgentLoop, cm ContextManager) ContextManager {
	return newProjectScopedContextManager(cm, deepClawProjectIDForLoop(al))
}

func (m *projectScopedContextManager) scopeKey(sessionKey string) string {
	return projectScopedSessionKey(m.projectID, sessionKey)
}

func (m *projectScopedContextManager) Assemble(
	ctx context.Context,
	req *AssembleRequest,
) (*AssembleResponse, error) {
	if req == nil {
		return m.inner.Assemble(ctx, nil)
	}
	scoped := *req
	scoped.SessionKey = m.scopeKey(req.SessionKey)
	return m.inner.Assemble(ctx, &scoped)
}

func (m *projectScopedContextManager) Compact(ctx context.Context, req *CompactRequest) error {
	if req == nil {
		return m.inner.Compact(ctx, nil)
	}
	scoped := *req
	scoped.SessionKey = m.scopeKey(req.SessionKey)
	return m.inner.Compact(ctx, &scoped)
}

func (m *projectScopedContextManager) Ingest(ctx context.Context, req *IngestRequest) error {
	if req == nil {
		return m.inner.Ingest(ctx, nil)
	}
	scoped := *req
	scoped.SessionKey = m.scopeKey(req.SessionKey)
	return m.inner.Ingest(ctx, &scoped)
}

func (m *projectScopedContextManager) Clear(ctx context.Context, sessionKey string) error {
	return m.inner.Clear(ctx, m.scopeKey(sessionKey))
}

// Close forwards to managers that own resources (seahorse holds a SQLite
// engine). Without this forwarding the wrapper would hide Close from
// AgentLoop.Close and the engine would never be released.
func (m *projectScopedContextManager) Close() error {
	if closer, ok := m.inner.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
