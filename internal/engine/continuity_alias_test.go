package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The continuity of an alias on the DeepSeek endpoint.
//
// The model name is a Claude name, but the provider behind it is
// DeepSeek's, and its thinking is on by default. The record says how
// the request that produced the blocks asked for thinking, because a
// later request cannot work that out on its own.

// aliasProvider answers as the DeepSeek endpoint does: thinking blocks
// whether the request asked for them or not.
type aliasProvider struct {
	requests [][]provider.Message
	step     int
}

func (p *aliasProvider) Name() string { return "alias" }

func (p *aliasProvider) Chat(_ context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	p.requests = append(p.requests, append([]provider.Message(nil), messages...))
	p.step++
	key := provider.ReasoningKey("claude-sonnet-4-5", provider.WireAnthropic)
	return provider.Response{
		Content: "the answer",
		ReasoningBlocks: []provider.ReasoningBlock{
			{Type: provider.ReasoningThinking, Text: "the model weighed it"},
		},
		ReasoningKey: key,
		// The request said nothing about thinking, and the provider
		// thought anyway: that is the fact the record keeps.
		Thinking: provider.ThinkingUnset,
	}, nil
}

// firstAssistant returns the first assistant message of the last
// request: the one the session restored.
func (p *aliasProvider) firstAssistant() provider.Message {
	if len(p.requests) == 0 {
		return provider.Message{}
	}
	messages := p.requests[len(p.requests)-1]
	for i := range messages {
		if messages[i].Role == "assistant" {
			return messages[i]
		}
	}
	return provider.Message{}
}

// The alias keeps its thinking across a session.
//
// The record holds the blocks and the thinking of the request that
// produced them, and a restored conversation replays both.
func TestTheAliasKeepsItsThinkingAcrossTheSession(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Open(filepath.Join(dir, "session"), "test")
	if err != nil {
		t.Fatal(err)
	}
	perm := permission.New()
	perm.SetSession("test")
	_ = perm.Enable("test")
	p := &aliasProvider{}
	eng := New(store, p, provider.Model{ID: "claude-sonnet-4-5"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	if _, err := eng.Respond(context.Background(), "read the file"); err != nil {
		t.Fatal(err)
	}

	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	var stored *session.Protocol
	for i := range history {
		if history[i].Role == session.RoleAssistant {
			stored = history[i].Protocol
		}
	}
	if stored == nil {
		t.Fatal("the answer carries no continuity data")
	}
	continuity, ok := provider.DecodeReasoning(stored.Payload)
	if !ok {
		t.Fatal("the record was not read back")
	}
	if continuity.Key != provider.ReasoningKey("claude-sonnet-4-5", provider.WireAnthropic) {
		t.Errorf("the record names %q", continuity.Key)
	}
	if continuity.Thinking != provider.ThinkingUnset {
		t.Errorf("the record says the request asked %q, want nothing asked", continuity.Thinking)
	}

	// A new session over the same store: the conversation is restored,
	// and the next request carries the blocks and their provenance back.
	restored, err := session.Open(filepath.Join(dir, "session"), "test")
	if err != nil {
		t.Fatal(err)
	}
	p2 := &aliasProvider{}
	eng2 := New(restored, p2, provider.Model{ID: "claude-sonnet-4-5"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	if _, err := eng2.Respond(context.Background(), "and now the other one"); err != nil {
		t.Fatal(err)
	}
	assistant := p2.firstAssistant()
	if len(assistant.Reasoning) != 1 {
		t.Fatalf("the restored turn carries no blocks: %+v", assistant)
	}
	if assistant.Thinking != provider.ThinkingUnset {
		t.Errorf("the restored turn says the request asked %q", assistant.Thinking)
	}
}

// A record written before the thinking and the origin were kept is
// still read.
//
// It holds the blocks the provider sent, and a request that needs them
// is refused rather than sent without them. What it does not hold is
// how the request that produced them asked for thinking, and which
// endpoint produced them, and neither is guessed at.
func TestALegacyRecordIsStillRead(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(session.RoleUser, "hi", session.Turn{}); err != nil {
		t.Fatal(err)
	}
	// A record of the first version: the payload has the blocks and
	// neither the thinking nor the origin.
	record := session.NewProtocol([]byte(`{"key":"openai/reasoning-content",` +
		`"model":"deepseek-flash","blocks":[{"type":"reasoning","text":"the thought"}]}`))
	record.Version = session.ProtocolVersionLegacy
	if _, err := st.AppendTurn(session.RoleAssistant, "the answer", session.Turn{
		Protocol: record,
	}); err != nil {
		t.Fatal(err)
	}
	// A record of the version before the origin was kept: it has the
	// thinking, and no origin.
	second := session.NewProtocol(provider.EncodeReasoning(provider.Continuity{
		Key:      provider.ReasoningKey("deepseek-flash", provider.WireOpenAI),
		Model:    "deepseek-flash",
		Thinking: provider.ThinkingAsked,
		Blocks:   []provider.ReasoningBlock{{Type: provider.ReasoningPlain, Text: "the thought"}},
	}))
	second.Version = session.ProtocolVersionOriginless
	if _, err := st.AppendTurn(session.RoleAssistant, "the second answer", session.Turn{
		Protocol: second,
	}); err != nil {
		t.Fatal(err)
	}

	restored, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &reasoningProvider{reply: provider.Response{Content: "the answer"}}
	eng := New(restored, p, provider.Model{ID: "deepseek-flash"})
	history, err := restored.History()
	if err != nil {
		t.Fatal(err)
	}
	messages := eng.requestMessages(history, "", "", nil)
	var assistants []provider.Message
	for _, m := range messages {
		if m.Role == "assistant" {
			assistants = append(assistants, m)
		}
	}
	if len(assistants) != 2 {
		t.Fatalf("the assistant turns = %d", len(assistants))
	}
	for _, assistant := range assistants {
		if assistant.ReasoningIncomplete {
			t.Fatalf("a legacy record was refused: %+v", assistant)
		}
		if len(assistant.Reasoning) != 1 {
			t.Fatalf("the legacy record carries no blocks: %+v", assistant)
		}
		if assistant.Origin != "" {
			t.Errorf("the legacy record claims an origin %q", assistant.Origin)
		}
	}
	if assistants[0].Thinking != "" {
		t.Errorf("the first record claims the request asked %q", assistants[0].Thinking)
	}
	if assistants[1].Thinking != provider.ThinkingAsked {
		t.Errorf("the second record lost the thinking it kept: %q", assistants[1].Thinking)
	}
}
