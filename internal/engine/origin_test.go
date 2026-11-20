package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The origin of a record, and the records that do not have one.
//
// The continuity data of a turn travels with the turn. So does the
// origin of the endpoint that produced it: the reasoning belongs to
// that endpoint, and a request has to know which endpoint that was
// before it sends anything.
//
// A record written before the origin was kept says nothing about it.
// Nothing is invented for it: the record is read as what it is, and
// the provider layer refuses to send it.

// originProvider answers with the provider's own reasoning and records
// the requests it was given.
type originProvider struct {
	reply    provider.Response
	requests [][]provider.Message
}

func (o *originProvider) Name() string { return "origin" }

func (o *originProvider) Chat(_ context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	o.requests = append(o.requests, append([]provider.Message(nil), messages...))
	return o.reply, nil
}

// lastAssistant returns the last assistant message of the last request.
func (o *originProvider) lastAssistant() provider.Message {
	if len(o.requests) == 0 {
		return provider.Message{}
	}
	messages := o.requests[len(o.requests)-1]
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i]
		}
	}
	return provider.Message{}
}

// The record of a turn keeps the origin of the endpoint that produced
// it, and a restored conversation carries it along.
func TestTheRecordKeepsTheOrigin(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	origin := string(provider.OriginFor(provider.WireAnthropic,
		"https://api.example.com/anthropic", "env:KEY"))
	p := &originProvider{reply: provider.Response{
		Content:         "the answer",
		ReasoningBlocks: []provider.ReasoningBlock{{Type: provider.ReasoningThinking, Text: "the model weighed it"}},
		ReasoningKey:    provider.ReasoningKey("claude-sonnet-4-5", provider.WireAnthropic),
		Thinking:        provider.ThinkingAsked,
		Origin:          origin,
	}}
	eng := New(st, p, provider.Model{ID: "claude-sonnet-4-5"})
	if _, err := eng.Respond(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	// The record on disk holds the origin.
	restored, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	history, err := restored.History()
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
		t.Errorf("the record version = %d, want %d", stored.Version, session.ProtocolVersion)
	}
	continuity, ok := provider.DecodeReasoning(stored.Payload)
	if !ok {
		t.Fatal("the record was not read back")
	}
	if continuity.Origin != origin {
		t.Errorf("the record names the origin %q, want %q", continuity.Origin, origin)
	}

	// And the restored conversation hands it to the provider.
	eng2 := New(restored, p, provider.Model{ID: "claude-sonnet-4-5"})
	if _, err := eng2.Respond(context.Background(), "and again"); err != nil {
		t.Fatal(err)
	}
	assistant := p.lastAssistant()
	if assistant.Origin != origin {
		t.Errorf("the restored turn names the origin %q, want %q", assistant.Origin, origin)
	}
	if len(assistant.Reasoning) != 1 {
		t.Errorf("the restored turn carries no blocks: %+v", assistant)
	}
}

// A record of the version before the origin was kept is still read.
//
// It holds the blocks the provider sent, and it holds the thinking of
// the request that produced them. What it does not hold is the origin,
// and nothing is invented for it.
func TestARecordWithoutAnOriginIsStillRead(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(session.RoleUser, "hi", session.Turn{}); err != nil {
		t.Fatal(err)
	}
	// The version before the thinking was kept: the blocks and nothing
	// else.
	first := session.NewProtocol([]byte(`{"key":"anthropic/thinking",` +
		`"model":"claude-sonnet-4-5",` +
		`"blocks":[{"type":"thinking","text":"the thought"}]}`))
	first.Version = session.ProtocolVersionLegacy
	if _, err := st.AppendTurn(session.RoleAssistant, "the answer", session.Turn{
		Protocol: first,
	}); err != nil {
		t.Fatal(err)
	}
	// The version before the origin was kept: the thinking and the
	// blocks, and no origin.
	second := session.NewProtocol(provider.EncodeReasoning(provider.Continuity{
		Key:      provider.ReasoningKey("claude-sonnet-4-5", provider.WireAnthropic),
		Model:    "claude-sonnet-4-5",
		Thinking: provider.ThinkingAsked,
		Blocks: []provider.ReasoningBlock{
			{Type: provider.ReasoningThinking, Text: "the thought"},
		},
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
	p := &originProvider{reply: provider.Response{Content: "the answer"}}
	eng := New(restored, p, provider.Model{ID: "claude-sonnet-4-5"})
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
			t.Fatalf("a record of an older version was refused: %+v", assistant)
		}
		if len(assistant.Reasoning) != 1 {
			t.Errorf("the record carries no blocks: %+v", assistant)
		}
		if assistant.Origin != "" {
			t.Errorf("the record claims an origin %q", assistant.Origin)
		}
	}
	if assistants[0].Thinking != "" {
		t.Errorf("the first record claims the request asked %q", assistants[0].Thinking)
	}
	if assistants[1].Thinking != provider.ThinkingAsked {
		t.Errorf("the second record lost the thinking it kept: %q", assistants[1].Thinking)
	}
}

// A conversation without reasoning keeps working after a restore.
//
// Most conversations say nothing about thinking. They have no
// continuity data, so there is nothing to bind to an origin, and the
// endpoint may be changed freely.
func TestAConversationWithoutReasoningKeepsWorkingAfterARestore(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	p := &originProvider{reply: provider.Response{Content: "the answer"}}
	eng := New(st, p, provider.Model{ID: "claude-sonnet-4-5"})
	if _, err := eng.Respond(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	restored, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	eng2 := New(restored, p, provider.Model{ID: "claude-sonnet-4-5"})
	if _, err := eng2.Respond(context.Background(), "and again"); err != nil {
		t.Fatal(err)
	}
	assistant := p.lastAssistant()
	if len(assistant.Reasoning) != 0 {
		t.Errorf("the conversation carries reasoning it never had: %+v", assistant)
	}
	if assistant.Origin != "" {
		t.Errorf("the conversation claims an origin %q", assistant.Origin)
	}
}

// A record of a version HAL does not know is refused.
//
// The engine cannot read a format it does not know, and it does not
// guess at one: the record is incomplete, and the request that would
// need it is refused.
func TestARecordOfAnUnknownVersionIsRefused(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(session.RoleUser, "hi", session.Turn{}); err != nil {
		t.Fatal(err)
	}
	record := session.NewProtocol(provider.EncodeReasoning(provider.Continuity{
		Key:      provider.ReasoningKey("claude-sonnet-4-5", provider.WireAnthropic),
		Model:    "claude-sonnet-4-5",
		Thinking: provider.ThinkingAsked,
		Origin:   string(provider.OriginFor(provider.WireAnthropic, "https://api.example.com", "env:KEY")),
		Blocks: []provider.ReasoningBlock{
			{Type: provider.ReasoningThinking, Text: "the thought"},
		},
	}))
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
	p := &originProvider{reply: provider.Response{Content: "the answer"}}
	eng := New(restored, p, provider.Model{ID: "claude-sonnet-4-5"})
	history, err := restored.History()
	if err != nil {
		t.Fatal(err)
	}
	messages := eng.requestMessages(history, "", "", nil)
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		if !m.ReasoningIncomplete {
			t.Fatalf("the record of an unknown version was not refused: %+v", m)
		}
		if len(m.Reasoning) != 0 {
			t.Errorf("the record carries data: %+v", m.Reasoning)
		}
	}
}

// A record whose payload cannot be read is refused, and so is one
// whose payload was never written.
//
// The provider layer owns the payload, and the engine cannot know what
// it held. It knows only that it cannot be read, and a record it
// cannot read is a record it cannot send.
func TestADamagedRecordIsRefused(t *testing.T) {
	for _, payload := range []string{
		`{"unexpected":"shape"}`,
		`["not","an","object"]`,
		`{"key":"","blocks":[]}`,
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
		p := &originProvider{reply: provider.Response{Content: "the answer"}}
		eng := New(restored, p, provider.Model{ID: "claude-sonnet-4-5"})
		history, err := restored.History()
		if err != nil {
			t.Fatal(err)
		}
		messages := eng.requestMessages(history, "", "", nil)
		for _, m := range messages {
			if m.Role != "assistant" {
				continue
			}
			if !m.ReasoningIncomplete {
				t.Fatalf("the damaged record %s was not refused: %+v", payload, m)
			}
		}
	}
}

// The diagnostic of a refusal names the reason.
//
// The operator has to be told what happened and what to do about it:
// the reasoning belongs to another endpoint, and a new conversation is
// what starts a safe one.
func TestTheRefusalNamesTheReason(t *testing.T) {
	origin := provider.OriginFor(provider.WireAnthropic, "https://api.example.com/anthropic", "env:KEY")
	other := provider.OriginFor(provider.WireAnthropic, "https://other.example.com/anthropic", "env:KEY")
	err := provider.OriginRefusal(origin, "other.example.com/anthropic", other)
	if err == nil {
		t.Fatal("no refusal")
	}
	message := err.Error()
	for _, want := range []string{"origin", "other.example.com/anthropic", string(other)} {
		if !strings.Contains(message, want) {
			t.Errorf("the diagnostic %q does not mention %q", message, want)
		}
	}
}
