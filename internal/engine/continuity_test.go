package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// Reasoning continuity across turns.
//
// The continuity data a provider needs lives with the message it
// belongs to: a restored conversation replays it, a compacted one
// takes it away with the message, and a conversation that changed model
// does not replay it.

// reasoningProvider answers with the provider's own reasoning and
// records the requests it was given.
type reasoningProvider struct {
	reply    provider.Response
	requests [][]provider.Message
}

func (r *reasoningProvider) Name() string { return "reasoning" }

func (r *reasoningProvider) Chat(_ context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	r.requests = append(r.requests, append([]provider.Message(nil), messages...))
	return r.reply, nil
}

// lastAssistant returns the last assistant message of the last request.
func (r *reasoningProvider) lastAssistant() provider.Message {
	if len(r.requests) == 0 {
		return provider.Message{}
	}
	messages := r.requests[len(r.requests)-1]
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i]
		}
	}
	return provider.Message{}
}

// The reasoning a provider returned is kept with the answer it belongs
// to: the record holds it, and the record holds nothing else about it.
func TestTheReasoningIsKeptWithTheAnswer(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &reasoningProvider{reply: provider.Response{
		Content:         "the answer",
		Reasoning:       "the model weighed it",
		ReasoningBlocks: []provider.ReasoningBlock{{Type: provider.ReasoningPlain, Text: "the model weighed it"}},
		ReasoningKey:    provider.ReasoningKey("deepseek-reasoner", provider.WireOpenAI),
	}}
	eng := New(st, p, provider.Model{ID: "deepseek-reasoner"})
	if _, err := eng.Respond(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	history, err := st.History()
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
	if stored.Version != session.ProtocolVersion {
		t.Errorf("the record version = %d", stored.Version)
	}
	key, _, blocks, ok := provider.DecodeReasoning(stored.Payload)
	if !ok {
		t.Fatal("the record was not read back")
	}
	if key != provider.ReasoningKey("deepseek-reasoner", provider.WireOpenAI) {
		t.Errorf("the record names %q", key)
	}
	if len(blocks) != 1 || blocks[0].Text != "the model weighed it" {
		t.Errorf("the blocks = %+v", blocks)
	}
	// The reasoning is not conversation content: it is not in the text
	// of the answer.
	if strings.Contains(history[len(history)-1].Content, "the model weighed it") {
		t.Error("the reasoning became part of the answer")
	}
}

// A restored conversation replays the continuity data of the turn it
// belongs to.
func TestARestoredConversationReplaysTheReasoning(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &reasoningProvider{reply: provider.Response{
		Content:         "the answer",
		ReasoningBlocks: []provider.ReasoningBlock{{Type: provider.ReasoningPlain, Text: "the model weighed it"}},
		ReasoningKey:    provider.ReasoningKey("deepseek-reasoner", provider.WireOpenAI),
	}}
	eng := New(st, p, provider.Model{ID: "deepseek-reasoner"})
	if _, err := eng.Respond(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	// A new session over the same store: the conversation is restored.
	restored, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	eng2 := New(restored, p, provider.Model{ID: "deepseek-reasoner"})
	if _, err := eng2.Respond(context.Background(), "and now the other one"); err != nil {
		t.Fatal(err)
	}
	assistant := p.lastAssistant()
	if len(assistant.Reasoning) != 1 {
		t.Fatalf("the restored turn carries no reasoning: %+v", assistant)
	}
	if assistant.Reasoning[0].Text != "the model weighed it" {
		t.Errorf("the reasoning = %+v", assistant.Reasoning)
	}
	if assistant.ReasoningKey != provider.ReasoningKey("deepseek-reasoner", provider.WireOpenAI) {
		t.Errorf("the turn was not named with its protocol: %q", assistant.ReasoningKey)
	}
}

// A restored record keeps the protocol it was stored with.
//
// The engine does not re-label it and does not read it: the provider
// layer decides whether a record belongs to the model that is now
// answering, and a record of another protocol is never replayed (see
// the provider's own tests).
func TestTheRestoredRecordKeepsItsProtocol(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	key := provider.ReasoningKey("deepseek-reasoner", provider.WireOpenAI)
	p := &reasoningProvider{reply: provider.Response{
		Content:         "the answer",
		ReasoningBlocks: []provider.ReasoningBlock{{Type: provider.ReasoningPlain, Text: "the model weighed it"}},
		ReasoningKey:    key,
	}}
	eng := New(st, p, provider.Model{ID: "deepseek-reasoner"})
	if _, err := eng.Respond(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	restored, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	eng2 := New(restored, p, provider.Model{ID: "deepseek-reasoner"})
	if _, err := eng2.Respond(context.Background(), "and now the other one"); err != nil {
		t.Fatal(err)
	}
	assistant := p.lastAssistant()
	if len(assistant.Reasoning) != 1 {
		t.Fatalf("the restored turn carries no reasoning: %+v", assistant)
	}
	if assistant.ReasoningKey != key {
		t.Errorf("the record was re-labelled: %q", assistant.ReasoningKey)
	}
	if assistant.Reasoning[0].Text != "the model weighed it" {
		t.Errorf("the reasoning = %+v", assistant.Reasoning)
	}
}

// A record of another conversation is never replayed: the continuity
// data belongs to the message it was stored with.
func TestARecordOfAnotherConversationIsNotReplayed(t *testing.T) {
	first := t.TempDir()
	st, err := session.Open(first, "chat")
	if err != nil {
		t.Fatal(err)
	}
	key := provider.ReasoningKey("deepseek-reasoner", provider.WireOpenAI)
	p := &reasoningProvider{reply: provider.Response{
		Content:         "the answer",
		ReasoningBlocks: []provider.ReasoningBlock{{Type: provider.ReasoningPlain, Text: "the model weighed it"}},
		ReasoningKey:    key,
	}}
	eng := New(st, p, provider.Model{ID: "deepseek-reasoner"})
	if _, err := eng.Respond(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	// Another conversation: it has its own record and no reasoning of
	// its own.
	second := t.TempDir()
	other, err := session.Open(second, "chat")
	if err != nil {
		t.Fatal(err)
	}
	eng2 := New(other, p, provider.Model{ID: "deepseek-reasoner"})
	if _, err := eng2.Respond(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if len(p.lastAssistant().Reasoning) != 0 {
		t.Errorf("the reasoning of another conversation was replayed: %+v", p.lastAssistant())
	}
}

// A record that kept the name of the protocol but not its data is
// incomplete: the request that would need the data is refused, and
// nothing incomplete is sent.
func TestAnIncompleteRecordRefusesTheRequest(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	// A record with the key but without the data: what is left when the
	// reasoning was too large to keep.
	if _, err := st.AppendTurn(session.RoleUser, "hi", session.Turn{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(session.RoleAssistant, "the answer", session.Turn{
		Protocol: session.NewProtocol(provider.EncodeReasoningKey(
			provider.ReasoningKey("deepseek-reasoner", provider.WireOpenAI))),
	}); err != nil {
		t.Fatal(err)
	}

	restored, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &reasoningProvider{reply: provider.Response{Content: "the answer"}}
	eng := New(restored, p, provider.Model{ID: "deepseek-reasoner"})
	history, err := restored.History()
	if err != nil {
		t.Fatal(err)
	}
	messages := eng.requestMessages(history, "", "", nil)
	assistant := provider.Message{}
	for _, m := range messages {
		if m.Role == "assistant" {
			assistant = m
		}
	}
	if !assistant.ReasoningIncomplete {
		t.Fatalf("the incomplete record was not marked: %+v", assistant)
	}
	if len(assistant.Reasoning) != 0 {
		t.Errorf("the incomplete record carries data it does not have: %+v", assistant.Reasoning)
	}
}

// A record whose payload cannot be read is incomplete: the request that
// would need the data is refused, and nothing incomplete is sent.
//
// The record is opaque here, so the engine cannot know what it held.
// It knows only that it cannot be read, and a record it cannot read is
// a record it cannot send: the request that needs it is refused.
func TestADamagedRecordRefusesTheRequest(t *testing.T) {
	for _, payload := range []string{
		// A shape the provider layer does not know.
		`{"unexpected":"shape"}`,
		// Not even an object.
		`["not","an","object"]`,
	} {
		dir := t.TempDir()
		st, err := session.Open(dir, "chat")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendTurn(session.RoleUser, "hi", session.Turn{}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendTurn(session.RoleAssistant, "the answer", session.Turn{
			Protocol: session.NewProtocol([]byte(payload)),
		}); err != nil {
			t.Fatal(err)
		}

		restored, err := session.Open(dir, "chat")
		if err != nil {
			t.Fatal(err)
		}
		p := &reasoningProvider{reply: provider.Response{Content: "the answer"}}
		eng := New(restored, p, provider.Model{ID: "deepseek-reasoner"})
		history, err := restored.History()
		if err != nil {
			t.Fatal(err)
		}
		messages := eng.requestMessages(history, "", "", nil)
		assistant := provider.Message{}
		for _, m := range messages {
			if m.Role == "assistant" {
				assistant = m
			}
		}
		if !assistant.ReasoningIncomplete {
			t.Fatalf("the damaged record %s was not marked: %+v", payload, assistant)
		}
		if len(assistant.Reasoning) != 0 {
			t.Errorf("the damaged record carries data it does not have: %+v", assistant.Reasoning)
		}
	}
}

// A record of another version is incomplete as well: the engine cannot
// read a format it does not know, and it does not guess at one.
func TestARecordOfAnotherVersionRefusesTheRequest(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(session.RoleUser, "hi", session.Turn{}); err != nil {
		t.Fatal(err)
	}
	record := session.NewProtocol(provider.EncodeReasoning(
		provider.ReasoningKey("deepseek-reasoner", provider.WireOpenAI),
		"deepseek-reasoner",
		[]provider.ReasoningBlock{{Type: provider.ReasoningPlain, Text: "the thought"}}))
	record.Version = session.ProtocolVersion + 1
	if _, err := st.AppendTurn(session.RoleAssistant, "the answer", session.Turn{
		Protocol: record,
	}); err != nil {
		t.Fatal(err)
	}

	restored, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &reasoningProvider{reply: provider.Response{Content: "the answer"}}
	eng := New(restored, p, provider.Model{ID: "deepseek-reasoner"})
	history, err := restored.History()
	if err != nil {
		t.Fatal(err)
	}
	messages := eng.requestMessages(history, "", "", nil)
	assistant := provider.Message{}
	for _, m := range messages {
		if m.Role == "assistant" {
			assistant = m
		}
	}
	if !assistant.ReasoningIncomplete {
		t.Fatalf("the record of another version was not marked: %+v", assistant)
	}
	if len(assistant.Reasoning) != 0 {
		t.Errorf("the record of another version carries data: %+v", assistant.Reasoning)
	}
}

// The compaction takes the continuity data away with the message it
// belongs to: the summary replaces the exchange, and the exchange's
// protocol data goes with it.
func TestTheCompactionTakesTheReasoningWithTheMessage(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	key := provider.ReasoningKey("deepseek-reasoner", provider.WireOpenAI)
	history := []session.Message{
		{Role: session.RoleUser, Content: "hi"},
		{Role: session.RoleAssistant, Content: "the answer", Protocol: session.NewProtocol(
			provider.EncodeReasoning(key, "deepseek-reasoner", []provider.ReasoningBlock{
				{Type: provider.ReasoningPlain, Text: "the model weighed it"},
			}))},
		{Role: session.RoleUser, Content: "and now the other one"},
	}
	eng := New(st, &reasoningProvider{}, provider.Model{ID: "deepseek-reasoner"})

	// Without a compaction, the reasoning travels with its message.
	messages := eng.requestMessages(history, "", "", nil)
	var carried bool
	for _, m := range messages {
		if m.Role == "assistant" && len(m.Reasoning) == 1 {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("the reasoning did not travel with its message: %+v", messages)
	}

	// With one, the covered exchange is gone, and so is its data.
	messages = eng.requestMessages(history, "", "", &session.Compaction{CoveredMessages: 2})
	for _, m := range messages {
		if m.Role == "assistant" && len(m.Reasoning) != 0 {
			t.Errorf("the compaction left the protocol data of a covered message: %+v", m)
		}
	}
}

// The tool loop keeps the reasoning of the turn that asked for the
// tool: the next request in the same turn needs it.
func TestTheToolLoopCarriesTheReasoning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("the file"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	var requests [][]provider.Message
	p := &toolLoopProvider{requests: &requests}
	perm := permission.New()
	perm.SetSession("chat")
	_ = perm.Enable("chat")
	eng := New(st, p, provider.Model{ID: "deepseek-reasoner"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	if _, err := eng.Respond(context.Background(), "read the file"); err != nil {
		t.Fatal(err)
	}
	if len(requests) < 2 {
		t.Fatalf("the loop made %d requests", len(requests))
	}
	second := requests[1]
	var assistant provider.Message
	for _, m := range second {
		if m.Role == "assistant" {
			assistant = m
		}
	}
	if len(assistant.Reasoning) != 1 {
		t.Fatalf("the follow-up request carries no reasoning: %+v", second)
	}
	if assistant.Reasoning[0].Text != "the model weighed it" {
		t.Errorf("the reasoning = %+v", assistant.Reasoning)
	}
	if assistant.ReasoningKey != provider.ReasoningKey("deepseek-reasoner", provider.WireOpenAI) {
		t.Errorf("the turn was not named with its protocol: %q", assistant.ReasoningKey)
	}
}

// toolLoopProvider asks for a tool once and then answers.
type toolLoopProvider struct {
	requests *[][]provider.Message
	step     int
}

func (p *toolLoopProvider) Name() string { return "tool-loop" }

func (p *toolLoopProvider) Chat(_ context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	*p.requests = append(*p.requests, append([]provider.Message(nil), messages...))
	p.step++
	if p.step == 1 {
		return provider.Response{
			ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "read",
				Arguments: map[string]any{"path": "notes.md"}}},
			ReasoningBlocks: []provider.ReasoningBlock{
				{Type: provider.ReasoningPlain, Text: "the model weighed it"},
			},
			ReasoningKey: provider.ReasoningKey("deepseek-reasoner", provider.WireOpenAI),
		}, nil
	}
	return provider.Response{Content: "the answer"}, nil
}
