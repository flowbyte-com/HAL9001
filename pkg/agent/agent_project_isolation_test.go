package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

// projectIsolationSessionKey is a deliberately explicit session key so the
// benchmark-style "the -s value is ignored" trap cannot hide a leak here.
const projectIsolationSessionKey = "sk_v1_003b_project_isolation"

type projectIsolationHarness struct {
	loop     *AgentLoop
	provider *recordingProvider
}

func newProjectIsolationHarness(
	t *testing.T,
	agentWorkspace, mpmWorkspace, mpmHome, project string,
	activeMemory bool,
) *projectIsolationHarness {
	t.Helper()
	return newProjectIsolationHarnessWithContextManager(
		t, agentWorkspace, mpmWorkspace, mpmHome, project, activeMemory, "",
	)
}

// newProjectIsolationHarnessWithContextManager selects a context manager
// implementation so seahorse-backed history/compaction can be exercised against
// the same project switch.
func newProjectIsolationHarnessWithContextManager(
	t *testing.T,
	agentWorkspace, mpmWorkspace, mpmHome, project string,
	activeMemory bool,
	contextManager string,
) *projectIsolationHarness {
	t.Helper()
	isolation.ResetProtectedPaths()
	isolation.Configure(config.DefaultConfig())
	t.Cleanup(func() {
		isolation.ResetProtectedPaths()
		isolation.Configure(config.DefaultConfig())
	})
	if err := os.MkdirAll(mpmWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	if activeMemory {
		t.Setenv("DEEPCLAW_ACTIVE_MEMORY", "1")
	} else {
		t.Setenv("DEEPCLAW_ACTIVE_MEMORY", "0")
	}
	t.Setenv("MPM_WORKSPACE", mpmWorkspace)
	t.Setenv("DEEPCLAW_MPM_EXECUTABLE", mpmTestBinary(t))
	t.Setenv("DEEPCLAW_MPM_HOME", mpmHome)
	t.Setenv("DEEPCLAW_MPM_PROJECT", project)
	t.Setenv("DEEPCLAW_MPM_BUDGET", "350")

	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace:         agentWorkspace,
		ModelName:         "test-model",
		MaxTokens:         4096,
		MaxToolIterations: 10,
		ContextManager:    contextManager,
	}}}
	provider := &recordingProvider{}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(func() { loop.Close() })
	return &projectIsolationHarness{loop: loop, provider: provider}
}

func (h *projectIsolationHarness) send(t *testing.T, content string) string {
	t.Helper()
	if _, err := h.loop.processMessage(context.Background(), testInboundMessage(bus.InboundMessage{
		Channel:    "pico",
		ChatID:     "chat-1",
		SessionKey: projectIsolationSessionKey,
		SenderID:   "audit-user",
		Content:    content,
	})); err != nil {
		t.Fatalf("processMessage(%q): %v", content, err)
	}
	if len(h.provider.lastMessages) == 0 {
		t.Fatal("provider captured no messages")
	}
	return joinPrompt(h.provider.lastMessages)
}

func joinPrompt(messages []providers.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		b.WriteString(msg.Role)
		b.WriteString(": ")
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func seedProjectFixture(t *testing.T, bin, root, project, fact string) (mpmWorkspace, mpmHome string) {
	t.Helper()
	mpmWorkspace = filepath.Join(root, "mpm-"+project)
	mpmHome = filepath.Join(root, "mpm-home-"+project)
	if err := os.MkdirAll(mpmWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	seedMPMFixture(t, bin, mpmWorkspace, mpmHome, project, fact)
	return mpmWorkspace, mpmHome
}

func expectProjectBPrompt(t *testing.T, prompt string) {
	t.Helper()
	if !strings.Contains(prompt, "beta-2") {
		t.Fatalf("project B prompt is missing beta-2:\n%s", prompt)
	}
	if strings.Contains(prompt, "alpha-1") {
		t.Fatalf("project B prompt leaked project A's alpha-1:\n%s", prompt)
	}
}

// TestProjectSwitchClearedHistoryDoesNotLeakActiveMemory covers the case where
// the conversation was cleared but a project A active-memory sidecar remains.
func TestProjectSwitchClearedHistoryDoesNotLeakActiveMemory(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	agentWorkspace := filepath.Join(root, "agent")
	mpmA, homeA := seedProjectFixture(t, bin, root, "project-a", "Project A secret code is alpha-1.")
	mpmB, homeB := seedProjectFixture(t, bin, root, "project-b", "Project B secret code is beta-2.")

	a := newProjectIsolationHarness(t, agentWorkspace, mpmA, homeA, "project-a", true)
	promptA := a.send(t, "secret code")
	if !strings.Contains(promptA, "alpha-1") {
		t.Fatalf("project A prompt is missing alpha-1:\n%s", promptA)
	}

	// Remove A's conversation but deliberately leave A's active-memory sidecar.
	if err := a.loop.contextManager.Clear(context.Background(), projectIsolationSessionKey); err != nil {
		t.Fatalf("clear project A conversation: %v", err)
	}

	b := newProjectIsolationHarness(t, agentWorkspace, mpmB, homeB, "project-b", true)
	expectProjectBPrompt(t, b.send(t, "secret code"))
}

// TestProjectSwitchRetainedHistoryDoesNotLeakConversation covers the case where
// the previous project's JSONL history is still on disk.
func TestProjectSwitchRetainedHistoryDoesNotLeakConversation(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	agentWorkspace := filepath.Join(root, "agent")
	mpmA, homeA := seedProjectFixture(t, bin, root, "project-a", "Project A secret code is alpha-1.")
	mpmB, homeB := seedProjectFixture(t, bin, root, "project-b", "Project B secret code is beta-2.")

	a := newProjectIsolationHarness(t, agentWorkspace, mpmA, homeA, "project-a", false)
	promptA := a.send(t, "Remember this project A fact: alpha-1 belongs to project A.")
	if !strings.Contains(promptA, "alpha-1") {
		t.Fatalf("project A prompt is missing alpha-1:\n%s", promptA)
	}

	b := newProjectIsolationHarness(t, agentWorkspace, mpmB, homeB, "project-b", false)
	expectProjectBPrompt(t, b.send(t, "secret code"))
}

// TestExistingActiveMemorySidecarWithWrongProjectIsRejected does not trust a
// sidecar merely because its session_key matches. It writes a forged A sidecar
// at both the legacy unscoped path and the project-scoped path B will read.
func TestExistingActiveMemorySidecarWithWrongProjectIsRejected(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	agentWorkspace := filepath.Join(root, "agent")
	mpmB, homeB := seedProjectFixture(t, bin, root, "project-b", "Project B secret code is beta-2.")

	forged := ActiveWorkingSet{
		SessionKey: projectIsolationSessionKey,
		ProjectID:  "project-a",
		Objective:  "Project A secret code is alpha-1.",
		Records: []ActiveMemoryRecord{{
			ID:      "forged-a",
			Content: "Project A secret code is alpha-1.",
			Tags:    []string{"project:project-a"},
			Score:   1,
		}},
	}
	writeActiveSidecar(t, NewActiveMemoryStore(agentWorkspace).path(projectIsolationSessionKey), forged)
	writeActiveSidecar(t, filepath.Join(agentWorkspace, ".deepclaw", "active-memory", testScopedSessionKey("project-b", projectIsolationSessionKey)+".json"), forged)

	b := newProjectIsolationHarness(t, agentWorkspace, mpmB, homeB, "project-b", true)
	prompt := b.send(t, "secret code")
	if !strings.Contains(prompt, "beta-2") {
		t.Fatalf("project B prompt is missing beta-2:\n%s", prompt)
	}
	if strings.Contains(prompt, "alpha-1") {
		t.Fatalf("forged project A sidecar leaked alpha-1:\n%s", prompt)
	}
}

// TestSameProjectSessionSurvivesRestart is the positive control: project A's
// own history must still be resumed after a new AgentLoop is constructed.
func TestSameProjectSessionSurvivesRestart(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	agentWorkspace := filepath.Join(root, "agent")
	mpmA, homeA := seedProjectFixture(t, bin, root, "project-a", "Project A secret code is alpha-1.")

	const marker = "PROJECT-A-RESTART-MARKER-7f31"
	first := newProjectIsolationHarness(t, agentWorkspace, mpmA, homeA, "project-a", true)
	first.send(t, marker+" remember this conversation")

	restarted := newProjectIsolationHarness(t, agentWorkspace, mpmA, homeA, "project-a", true)
	prompt := restarted.send(t, "What did I ask you to remember?")
	if !strings.Contains(prompt, marker) {
		t.Fatalf("same-project history was not resumed after restart:\n%s", prompt)
	}
}

// TestCompactionSummaryDoesNotCrossProjects seeds a real session summary (the
// representation compaction uses) and verifies it is project-scoped.
func TestCompactionSummaryDoesNotCrossProjects(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	agentWorkspace := filepath.Join(root, "agent")
	mpmA, homeA := seedProjectFixture(t, bin, root, "project-a", "Project A secret code is alpha-1.")
	mpmB, homeB := seedProjectFixture(t, bin, root, "project-b", "Project B secret code is beta-2.")

	const summaryMarker = "PROJECT-A-COMPACTION-SUMMARY-MARKER-4c9e"
	a := newProjectIsolationHarness(t, agentWorkspace, mpmA, homeA, "project-a", false)
	a.send(t, "secret code")
	a.agent(t).Sessions.SetSummary(context.Background(), projectIsolationSessionKey, summaryMarker+" alpha-1")

	b := newProjectIsolationHarness(t, agentWorkspace, mpmB, homeB, "project-b", false)
	promptB := b.send(t, "secret code")
	if !strings.Contains(promptB, "beta-2") {
		t.Fatalf("project B prompt is missing beta-2:\n%s", promptB)
	}
	if strings.Contains(promptB, summaryMarker) || strings.Contains(promptB, "alpha-1") {
		t.Fatalf("project A summary leaked into project B:\n%s", promptB)
	}

	restarted := newProjectIsolationHarness(t, agentWorkspace, mpmA, homeA, "project-a", false)
	promptA := restarted.send(t, "What did we decide earlier?")
	if !strings.Contains(promptA, summaryMarker) {
		t.Fatalf("same-project summary was not resumed:\n%s", promptA)
	}
}

// TestSeahorseHistoryDoesNotCrossProjects exercises the seahorse context
// manager, which keeps its own SQLite history/summary keyed by session key.
// Compaction summaries live in that store, so the project scope must reach the
// context manager, not only the JSONL session store.
func TestSeahorseHistoryDoesNotCrossProjects(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	agentWorkspace := filepath.Join(root, "agent")
	mpmA, homeA := seedProjectFixture(t, bin, root, "project-a", "Project A secret code is alpha-1.")
	mpmB, homeB := seedProjectFixture(t, bin, root, "project-b", "Project B secret code is beta-2.")

	const marker = "PROJECT-A-SEAHORSE-MARKER-8e1d"
	a := newProjectIsolationHarnessWithContextManager(
		t, agentWorkspace, mpmA, homeA, "project-a", false, "seahorse",
	)
	promptA := a.send(t, "secret code")
	if !strings.Contains(promptA, "alpha-1") {
		t.Fatalf("project A prompt is missing alpha-1:\n%s", promptA)
	}
	// The marker exists only in project A's conversation history. FTS queries
	// are AND-matched, so a separate turn keeps retrieval out of this check.
	a.send(t, "remember this project A marker: "+marker)

	b := newProjectIsolationHarnessWithContextManager(
		t, agentWorkspace, mpmB, homeB, "project-b", false, "seahorse",
	)
	promptB := b.send(t, "secret code")
	if !strings.Contains(promptB, "beta-2") {
		t.Fatalf("project B prompt is missing beta-2:\n%s", promptB)
	}
	if strings.Contains(promptB, "alpha-1") || strings.Contains(promptB, marker) {
		t.Fatalf("seahorse history leaked project A into project B:\n%s", promptB)
	}

	restarted := newProjectIsolationHarnessWithContextManager(
		t, agentWorkspace, mpmA, homeA, "project-a", false, "seahorse",
	)
	promptA2 := restarted.send(t, "what should I remember?")
	if !strings.Contains(promptA2, marker) {
		t.Fatalf("same-project seahorse history was not resumed:\n%s", promptA2)
	}
}

// TestLegacyUnscopedSessionIsNotAdopted ensures an old session that has no
// project ownership is quarantined, not silently relabelled as project A.
func TestLegacyUnscopedSessionIsNotAdopted(t *testing.T) {
	bin := mpmTestBinary(t)
	root := t.TempDir()
	agentWorkspace := filepath.Join(root, "agent")
	mpmA, homeA := seedProjectFixture(t, bin, root, "project-a", "Project A secret code is alpha-1.")

	legacy := initSessionStore(filepath.Join(agentWorkspace, "sessions"), false)
	if err := legacy.AddMessage(context.Background(), projectIsolationSessionKey, "user", "LEGACY-ALPHA-HISTORY-MARKER-1a2b"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.SetSummary(context.Background(), projectIsolationSessionKey, "LEGACY-ALPHA-SUMMARY-MARKER-3c4d"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Save(context.Background(), projectIsolationSessionKey); err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()

	a := newProjectIsolationHarness(t, agentWorkspace, mpmA, homeA, "project-a", true)
	prompt := a.send(t, "secret code")
	if strings.Contains(prompt, "LEGACY-ALPHA-HISTORY-MARKER-1a2b") {
		t.Fatalf("legacy unscoped history was adopted:\n%s", prompt)
	}
	if strings.Contains(prompt, "LEGACY-ALPHA-SUMMARY-MARKER-3c4d") {
		t.Fatalf("legacy unscoped summary was adopted:\n%s", prompt)
	}
	if !strings.Contains(prompt, "alpha-1") {
		t.Fatalf("project A retrieval did not run after legacy quarantine:\n%s", prompt)
	}
}

func (h *projectIsolationHarness) agent(t *testing.T) *AgentInstance {
	t.Helper()
	agent := h.loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	return agent
}

func writeActiveSidecar(t *testing.T, path string, ws ActiveWorkingSet) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// testScopedSessionKey mirrors the project-scope key derivation used by the
// implementation. It is defined here so the forged-sidecar test also exercises
// the exact path the fixed store will read.
func testScopedSessionKey(project, sessionKey string) string {
	projectSum := sha256.Sum256([]byte("deepclaw-project-scope-v1\x00" + project))
	prefix := "sk_v1_dc1_" + hex.EncodeToString(projectSum[:8]) + "_"
	sum := sha256.Sum256([]byte("deepclaw-session-scope-v1\x00" + project + "\x00" + sessionKey))
	return prefix + hex.EncodeToString(sum[:])
}

type recordingContextManager struct {
	assembleKeys []string
	compactKeys  []string
	ingestKeys   []string
	clearKeys    []string
}

func (m *recordingContextManager) Assemble(_ context.Context, req *AssembleRequest) (*AssembleResponse, error) {
	m.assembleKeys = append(m.assembleKeys, req.SessionKey)
	return &AssembleResponse{}, nil
}

func (m *recordingContextManager) Compact(_ context.Context, req *CompactRequest) error {
	m.compactKeys = append(m.compactKeys, req.SessionKey)
	return nil
}

func (m *recordingContextManager) Ingest(_ context.Context, req *IngestRequest) error {
	m.ingestKeys = append(m.ingestKeys, req.SessionKey)
	return nil
}

func (m *recordingContextManager) Clear(_ context.Context, sessionKey string) error {
	m.clearKeys = append(m.clearKeys, sessionKey)
	return nil
}

func TestProjectScopedContextManagerTranslatesEveryKey(t *testing.T) {
	inner := &recordingContextManager{}
	cm := newProjectScopedContextManager(inner, "project-a")
	want := testScopedSessionKey("project-a", projectIsolationSessionKey)

	if _, err := cm.Assemble(context.Background(), &AssembleRequest{SessionKey: projectIsolationSessionKey}); err != nil {
		t.Fatal(err)
	}
	if err := cm.Compact(context.Background(), &CompactRequest{SessionKey: projectIsolationSessionKey}); err != nil {
		t.Fatal(err)
	}
	if err := cm.Ingest(context.Background(), &IngestRequest{SessionKey: projectIsolationSessionKey}); err != nil {
		t.Fatal(err)
	}
	if err := cm.Clear(context.Background(), projectIsolationSessionKey); err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{
		"assemble": inner.assembleKeys[0],
		"compact":  inner.compactKeys[0],
		"ingest":   inner.ingestKeys[0],
		"clear":    inner.clearKeys[0],
	} {
		if got != want {
			t.Fatalf("%s session key = %q, want %q", name, got, want)
		}
	}
}

func TestProjectScopedSessionStoreIsolatesAndIsIdempotent(t *testing.T) {
	inner := session.NewSessionManager(t.TempDir())
	store := newProjectScopedSessionStore(inner, "project-a")
	ctx := context.Background()
	if err := store.AddMessage(ctx, projectIsolationSessionKey, "user", "alpha-only"); err != nil {
		t.Fatal(err)
	}
	scoped := testScopedSessionKey("project-a", projectIsolationSessionKey)
	for _, key := range []string{projectIsolationSessionKey, scoped} {
		history, err := store.GetHistory(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 1 || history[0].Content != "alpha-only" {
			t.Fatalf("GetHistory(%q) = %#v, want one alpha-only message", key, history)
		}
	}
	keys := store.ListSessions()
	if len(keys) != 1 || keys[0] != scoped {
		t.Fatalf("ListSessions() = %v, want [%s]", keys, scoped)
	}

	// A key that carries another project's scope digest must not be passed
	// through: doing so would let a caller-supplied key escape its project.
	foreign := testScopedSessionKey("project-b", projectIsolationSessionKey)
	if projectScopedSessionKey("project-b", foreign) != foreign {
		t.Fatal("a key scoped for the same project must be idempotent")
	}
	if projectScopedSessionKey("project-a", foreign) == foreign {
		t.Fatal("a key scoped for another project must be re-scoped, not trusted")
	}
	foreignHistory, err := store.GetHistory(ctx, foreign)
	if err != nil {
		t.Fatal(err)
	}
	if len(foreignHistory) != 0 {
		t.Fatalf("foreign-scoped key read project A history: %#v", foreignHistory)
	}
}

func TestMergeActiveWorkingSetRejectsForeignProjectRecords(t *testing.T) {
	prev := &ActiveWorkingSet{
		ProjectID: "project-a",
		Records: []ActiveMemoryRecord{
			{ID: "a", Content: "alpha-1", Tags: []string{"project:project-a"}},
			{ID: "b", Content: "beta-2", Tags: []string{"project:project-b"}},
			{ID: "legacy", Content: "unscoped"},
		},
	}
	got := MergeActiveWorkingSet(prev, nil, "objective", "project-a", 500)
	if len(got.Records) != 1 || got.Records[0].ID != "a" {
		t.Fatalf("project A merge kept foreign records: %#v", got.Records)
	}
	got = MergeActiveWorkingSet(prev, nil, "objective", "project-b", 500)
	if len(got.Records) != 0 {
		t.Fatalf("project B merge adopted project A state: %#v", got.Records)
	}
}
