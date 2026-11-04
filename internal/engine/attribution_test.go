package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/skills"
)

// attributionStore is a skill store that hands out stable identities and
// can be told to fail.
type attributionStore struct {
	entries []skills.Entry
	bodies  map[string]string
	// failOn makes the named skills unreadable.
	failOn map[string]bool
	reads  []string
}

func (f *attributionStore) List(context.Context) ([]skills.Entry, error) {
	return f.entries, nil
}

func (f *attributionStore) Read(_ context.Context, name string) (skills.Skill, error) {
	f.reads = append(f.reads, name)
	if f.failOn[name] {
		return skills.Skill{}, errors.New("the store is unreachable")
	}
	body, ok := f.bodies[name]
	if !ok {
		return skills.Skill{}, skills.ErrUnavailable
	}
	return skills.Skill{
		ID:          "skill:" + name + "-v1.0.0",
		Name:        name,
		Version:     "1.0.0",
		Body:        body,
		Constraints: []string{"Keep the facts."},
	}, nil
}

// twoSkillStore is a store with both shipped skills.
func twoSkillStore() *attributionStore {
	return &attributionStore{
		entries: []skills.Entry{
			{Name: "system-prompt-writer", Version: "1.0.0",
				WhenToUse: `Writing instructions a machine will follow. Triggers: "system prompt", "task brief".`},
			{Name: "humanize-text", Version: "1.0.0",
				WhenToUse: `Rewriting text a person will read. Triggers: "email", "warmer".`},
		},
		bodies: map[string]string{
			"system-prompt-writer": "# System prompt writer",
			"humanize-text":        "# Humanize text",
		},
		failOn: map[string]bool{},
	}
}

// attributionEngine builds an engine over a store, with a scripted
// provider.
func attributionEngine(t *testing.T, store *attributionStore, replies ...string) (*Engine, *session.Store, *recordingProvider) {
	t.Helper()
	p := &recordingProvider{MockProvider: provider.MockProvider{}}
	for _, r := range replies {
		p.Replies = append(p.Replies, provider.Response{Content: r})
	}
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	sel := skills.NewSelector(store)
	if err := sel.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	eng := New(st, p, provider.Model{ID: "m"}).WithSkills(sel)
	return eng, st, p
}

// lastAssistant returns the last stored assistant message.
func lastAssistant(t *testing.T, st *session.Store) session.Message {
	t.Helper()
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == session.RoleAssistant {
			return history[i]
		}
	}
	t.Fatal("no assistant message was stored")
	return session.Message{}
}

// TestTheAttributionIsPersistedWithTheAnswer checks that a skill that
// reached the model is credited with the answer, with the version and
// the content hash that were supplied.
func TestTheAttributionIsPersistedWithTheAnswer(t *testing.T) {
	store := twoSkillStore()
	eng, st, _ := attributionEngine(t, store, "Dear landlord, the boiler is broken.")
	if _, err := eng.RespondStream(context.Background(), "Write an email to my landlord.", nil); err != nil {
		t.Fatal(err)
	}
	msg := lastAssistant(t, st)
	if msg.Attribution == nil {
		t.Fatal("the answer was not credited with the skill")
	}
	if len(msg.Attribution.Skills) != 1 {
		t.Fatalf("skills = %#v", msg.Attribution.Skills)
	}
	s := msg.Attribution.Skills[0]
	if s.Name != "humanize-text" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Version != "1.0.0" {
		t.Errorf("version = %q", s.Version)
	}
	if s.ID == "" {
		t.Error("the skill has no stable identity")
	}
	if !strings.HasPrefix(s.ContentHash, "sha256:") {
		t.Errorf("content hash = %q", s.ContentHash)
	}
	if msg.Attribution.TurnID == "" || msg.Attribution.TurnID != msg.TurnID {
		t.Errorf("the attribution names another turn: %q vs %q",
			msg.Attribution.TurnID, msg.TurnID)
	}
}

// TestTheHistoricalAttributionDoesNotChange checks that a later update
// of the skill in the store cannot change what an earlier answer is
// credited with.
func TestTheHistoricalAttributionDoesNotChange(t *testing.T) {
	store := twoSkillStore()
	eng, st, _ := attributionEngine(t, store, "Dear landlord,")
	if _, err := eng.RespondStream(context.Background(), "Write an email to my landlord.", nil); err != nil {
		t.Fatal(err)
	}
	before := lastAssistant(t, st).Attribution

	// The store is updated: a new version, new instructions.
	store.entries[1].Version = "1.1.0"
	store.bodies["humanize-text"] = "# Humanize text, rewritten"

	if _, err := eng.RespondStream(context.Background(), "Write an email to my landlord.", nil); err != nil {
		t.Fatal(err)
	}
	after := lastAssistant(t, st).Attribution

	if before.Skills[0].ContentHash == after.Skills[0].ContentHash {
		t.Error("the updated skill produced the same content hash")
	}
	if before.Skills[0].Version != "1.0.0" {
		t.Errorf("the earlier attribution changed version: %q", before.Skills[0].Version)
	}
	if before.Skills[0].ContentHash == "" {
		t.Error("the earlier attribution lost its hash")
	}
}

// TestNoAttributionWithoutASkill checks that an ordinary answer carries
// no skill credit.
func TestNoAttributionWithoutASkill(t *testing.T) {
	store := twoSkillStore()
	eng, st, _ := attributionEngine(t, store, "Iceland's capital is Reykjavik.")
	if _, err := eng.RespondStream(context.Background(), "What is the capital of Iceland?", nil); err != nil {
		t.Fatal(err)
	}
	msg := lastAssistant(t, st)
	if msg.Attribution != nil {
		t.Errorf("an ordinary answer was credited: %#v", msg.Attribution)
	}
	if len(store.reads) != 0 {
		t.Errorf("a skill was read: %v", store.reads)
	}
}

// TestNoAttributionWhenTheSkillFailsToLoad checks that a skill which
// never reached the model is not credited with the answer.
func TestNoAttributionWhenTheSkillFailsToLoad(t *testing.T) {
	store := twoSkillStore()
	store.failOn["humanize-text"] = true
	eng, st, p := attributionEngine(t, store, "Answered without the skill.")
	if _, err := eng.RespondStream(context.Background(), "Write an email to my landlord.", nil); err != nil {
		t.Fatal(err)
	}
	msg := lastAssistant(t, st)
	if msg.Attribution != nil {
		t.Errorf("a failed skill was credited: %#v", msg.Attribution)
	}
	// The turn still worked.
	if msg.Content != "Answered without the skill." {
		t.Errorf("content = %q", msg.Content)
	}
	// The failure is reported to the interface as a failure.
	var decision *skills.Decision
	_, err := eng.RespondStreamEvents(context.Background(), "Write an email to my landlord.", func(ev Event) {
		if ev.Skill != nil {
			d := *ev.Skill
			decision = &d
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision == nil {
		t.Fatal("no decision was reported")
	}
	if decision.Failure == "" {
		t.Error("the load failure was not reported")
	}
	if len(decision.Applied) != 0 {
		t.Errorf("a failed skill was reported as applied: %v", decision.Applied)
	}
	if len(p.requests) == 0 {
		t.Fatal("the provider was never called")
	}
}

// TestTheAttributionForAnExplicitOverride checks that a pinned skill is
// credited like an automatic one.
func TestTheAttributionForAnExplicitOverride(t *testing.T) {
	store := twoSkillStore()
	eng, st, _ := attributionEngine(t, store, "Here is the prompt.")
	sel := eng.skills
	if err := sel.SetForced([]string{"system-prompt-writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RespondStream(context.Background(), "Write a letter to the team.", nil); err != nil {
		t.Fatal(err)
	}
	msg := lastAssistant(t, st)
	if msg.Attribution == nil || len(msg.Attribution.Skills) != 1 {
		t.Fatalf("attribution = %#v", msg.Attribution)
	}
	if msg.Attribution.Skills[0].Name != "system-prompt-writer" {
		t.Errorf("name = %q", msg.Attribution.Skills[0].Name)
	}
	if msg.Attribution.Source != skills.SourceExplicit {
		t.Errorf("source = %q", msg.Attribution.Source)
	}
}

// TestTheAttributionForSeveralSkills checks that a combination is
// recorded as it was supplied.
func TestTheAttributionForSeveralSkills(t *testing.T) {
	store := twoSkillStore()
	eng, st, _ := attributionEngine(t, store, "Both, please.")
	sel := eng.skills
	if err := sel.SetForced([]string{"humanize-text", "system-prompt-writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RespondStream(context.Background(), "Write an email and a system prompt.", nil); err != nil {
		t.Fatal(err)
	}
	msg := lastAssistant(t, st)
	if msg.Attribution == nil {
		t.Fatal("the answer was not credited")
	}
	if len(msg.Attribution.Skills) != 2 {
		t.Fatalf("skills = %#v", msg.Attribution.Skills)
	}
	names := []string{
		msg.Attribution.Skills[0].Name,
		msg.Attribution.Skills[1].Name,
	}
	if names[0] != "humanize-text" || names[1] != "system-prompt-writer" {
		t.Errorf("skills = %v", names)
	}
	for _, s := range msg.Attribution.Skills {
		if s.ContentHash == "" {
			t.Errorf("%s has no content hash", s.Name)
		}
	}
}

// TestNoAttributionForAFailedGeneration checks that a turn that produced
// no answer is not credited with a skill.
func TestNoAttributionForAFailedGeneration(t *testing.T) {
	store := twoSkillStore()
	p := &recordingProvider{MockProvider: provider.MockProvider{}}
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	sel := skills.NewSelector(store)
	if err := sel.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The provider fails outright.
	failing := &failingProvider{}
	eng := New(st, failing, provider.Model{ID: "m"}).WithSkills(sel)
	if _, err := eng.RespondStream(context.Background(), "Write an email to my landlord.", nil); err == nil {
		t.Fatal("the turn should have failed")
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range history {
		if m.Role == session.RoleAssistant {
			t.Errorf("an answer was stored for a failed turn: %q", m.Content)
		}
	}
	_ = p
}

// failingProvider always fails.
type failingProvider struct{ provider.MockProvider }

func (f *failingProvider) Chat(context.Context, []provider.Message, provider.Model) (provider.Response, error) {
	return provider.Response{}, errors.New("the provider is down")
}

// TestTheAttributionIsNotPartOfTheContent checks that the credit never
// becomes part of the answer itself.
func TestTheAttributionIsNotPartOfTheContent(t *testing.T) {
	store := twoSkillStore()
	eng, st, _ := attributionEngine(t, store, "Dear landlord, the boiler is broken.")
	if _, err := eng.RespondStream(context.Background(), "Write an email to my landlord.", nil); err != nil {
		t.Fatal(err)
	}
	msg := lastAssistant(t, st)
	if strings.Contains(msg.Content, "humanize-text") {
		t.Errorf("the content carries the credit: %q", msg.Content)
	}
	if strings.Contains(msg.Content, "sha256:") {
		t.Errorf("the content carries the hash: %q", msg.Content)
	}
}

// TestTheModelCannotSpoofTheAttribution checks that the credit comes
// from the routing decision and never from what the model said.
func TestTheModelCannotSpoofTheAttribution(t *testing.T) {
	store := twoSkillStore()
	// The model claims a skill of its own.
	eng, st, _ := attributionEngine(t, store,
		"Here is your letter. [skill: totally-made-up] · skill: evil-skill")
	if _, err := eng.RespondStream(context.Background(), "What is the capital of Iceland?", nil); err != nil {
		t.Fatal(err)
	}
	msg := lastAssistant(t, st)
	if msg.Attribution != nil {
		t.Errorf("the model's text produced an attribution: %#v", msg.Attribution)
	}
}

// TestTheAttributionReachesTheInterface checks that the credit is
// reported with the turn it belongs to.
func TestTheAttributionReachesTheInterface(t *testing.T) {
	store := twoSkillStore()
	eng, _, _ := attributionEngine(t, store, "Dear landlord,")
	var got *session.Attribution
	var turn string
	_, err := eng.RespondStreamEvents(context.Background(), "Write an email to my landlord.", func(ev Event) {
		if ev.Skill != nil {
			a := ev.Attribution
			got = a
			turn = ev.TurnID
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("no attribution was reported")
	}
	if len(got.Skills) != 1 || got.Skills[0].Name != "humanize-text" {
		t.Errorf("attribution = %#v", got)
	}
	if turn == "" || turn != got.TurnID {
		t.Errorf("the turn identity does not match: %q vs %q", turn, got.TurnID)
	}
}

// TestTheTurnIdentityIsSharedByBothSides checks that the operator's
// message and the answer belong to the same exchange.
func TestTheTurnIdentityIsSharedByBothSides(t *testing.T) {
	store := twoSkillStore()
	eng, st, _ := attributionEngine(t, store, "Dear landlord,")
	if _, err := eng.RespondStream(context.Background(), "Write an email to my landlord.", nil); err != nil {
		t.Fatal(err)
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("messages = %d", len(history))
	}
	if history[0].TurnID == "" {
		t.Error("the operator's message has no turn identity")
	}
	if history[0].TurnID != history[1].TurnID {
		t.Errorf("the two sides name different turns: %q vs %q",
			history[0].TurnID, history[1].TurnID)
	}
}

// TestASkillGuidedRequestKeepsTheWriteBoundary checks that loading a
// skill does not give the model a write capability.
func TestASkillGuidedRequestKeepsTheWriteBoundary(t *testing.T) {
	store := twoSkillStore()
	eng, _, _ := attributionEngine(t, store, "Saved.")
	if tools := eng.offeredTools(); len(tools) != 0 {
		t.Errorf("a skill offered tools: %#v", tools)
	}
}
