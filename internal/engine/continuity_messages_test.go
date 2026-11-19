package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The continuity of the Messages wire survives the session.
//
// The DeepSeek Anthropic-compatible endpoint keeps its own continuity:
// the thinking blocks of a turn are stored with the turn, restored with
// the conversation, and replayed with the next request.

func TestTheMessagesWireContinuitySurvivesTheSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("the file"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := session.Open(filepath.Join(dir, "session"), "test")
	if err != nil {
		t.Fatal(err)
	}
	p := &deepSeekMessagesProvider{}
	perm := permission.New()
	perm.SetSession("test")
	_ = perm.Enable("test")
	eng := New(store, p, provider.Model{ID: "deepseek-flash", Effort: "high"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	if _, err := eng.Respond(context.Background(), "read the file"); err != nil {
		t.Fatal(err)
	}

	// The record is in the session file, with the protocol it belongs to.
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
	key, _, blocks, ok := provider.DecodeReasoning(stored.Payload)
	if !ok {
		t.Fatal("the record was not read back")
	}
	if key != provider.ReasoningKey("deepseek-flash", provider.WireAnthropic) {
		t.Errorf("the record names %q, want the Messages-wire protocol of DeepSeek", key)
	}
	if len(blocks) != 1 || blocks[0].Text != "the model weighed the answer too" {
		t.Fatalf("the blocks = %+v", blocks)
	}

	// A new session over the same store: the conversation is restored,
	// and the next request carries the blocks back.
	restored, err := session.Open(filepath.Join(dir, "session"), "test")
	if err != nil {
		t.Fatal(err)
	}
	p2 := &deepSeekMessagesProvider{}
	eng2 := New(restored, p2, provider.Model{ID: "deepseek-flash", Effort: "high"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	if _, err := eng2.Respond(context.Background(), "and now the other one"); err != nil {
		t.Fatal(err)
	}
	assistant := p2.firstAssistant()
	if len(assistant.Reasoning) != 1 {
		t.Fatalf("the restored turn carries no blocks: %+v", assistant)
	}
	if assistant.Reasoning[0].Text != "the model weighed the answer too" {
		t.Errorf("the block = %+v", assistant.Reasoning[0])
	}
}

// deepSeekMessagesProvider answers as the Messages wire does: a
// thinking block, a tool call, and then an answer.
type deepSeekMessagesProvider struct {
	requests [][]provider.Message
	step     int
}

func (p *deepSeekMessagesProvider) Name() string { return "deepseek-messages" }

func (p *deepSeekMessagesProvider) Chat(_ context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	p.requests = append(p.requests, append([]provider.Message(nil), messages...))
	p.step++
	key := provider.ReasoningKey("deepseek-flash", provider.WireAnthropic)
	if p.step == 1 {
		return provider.Response{
			ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "read",
				Arguments: map[string]any{"path": "notes.md"}}},
			ReasoningBlocks: []provider.ReasoningBlock{
				{Type: provider.ReasoningThinking, Text: "the model weighed it"},
			},
			ReasoningKey: key,
		}, nil
	}
	// The model thinks before it answers as well: the answer is the
	// turn the record belongs to.
	return provider.Response{
		Content: "the answer",
		ReasoningBlocks: []provider.ReasoningBlock{
			{Type: provider.ReasoningThinking, Text: "the model weighed the answer too"},
		},
		ReasoningKey: key,
	}, nil
}

// firstAssistant returns the first assistant message of the last
// request: the one the session restored.
func (p *deepSeekMessagesProvider) firstAssistant() provider.Message {
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
