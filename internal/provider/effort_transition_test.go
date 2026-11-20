package provider

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// The effort transitions.
//
// The effort is the operator's switch, and it may be moved between the
// turns of one conversation. What may not change is what the provider
// gets: a turn the provider thought has to come back whole when the
// provider expects it, and it must not come back when the provider
// would refuse it.
//
// The mock is the strict one. It enforces the documented rules on the
// exact payload and answers 400 when they are broken, so a transition
// that breaks a rule fails here rather than at the provider.

// TestTheEffortTransitions walks the transitions the mission names.
//
// Each case is one conversation of two turns: the first turn with one
// effort, the continuation with another. The mock refuses a request
// that breaks the replay rules, so a case that passes is one the
// provider would have accepted.
func TestTheEffortTransitions(t *testing.T) {
	cases := []struct {
		name string
		// deepSeek reports that the endpoint thinks by default.
		deepSeek bool
		// signed reports that the endpoint signs its thinking.
		signed bool
		// first is the model of the first turn.
		first Model
		// next is the model of the continuation.
		next Model
		// wantThinking says whether the continuation asks for thinking.
		wantThinking bool
		// wantReplay says whether the continuation carries the blocks
		// of the first turn back.
		wantReplay bool
		// wantBlocks says whether the provider answered with thinking
		// at all.
		wantBlocks bool
	}{
		{
			name:       "the alias keeps its blocks when the effort is taken away",
			deepSeek:   true,
			first:      Model{ID: "claude-sonnet-4-5", Effort: "high"},
			next:       Model{ID: "claude-sonnet-4-5"},
			wantReplay: true,
			wantBlocks: true,
		},
		{
			name:         "the alias keeps its blocks when the effort is added",
			deepSeek:     true,
			first:        Model{ID: "claude-sonnet-4-5"},
			next:         Model{ID: "claude-sonnet-4-5", Effort: "high"},
			wantThinking: true,
			wantReplay:   true,
			wantBlocks:   true,
		},
		{
			name:       "the alias keeps its blocks when the effort stays away",
			deepSeek:   true,
			first:      Model{ID: "claude-sonnet-4-5"},
			next:       Model{ID: "claude-sonnet-4-5"},
			wantReplay: true,
			wantBlocks: true,
		},
		{
			name:       "the DeepSeek model keeps its blocks when the effort is taken away",
			deepSeek:   true,
			first:      Model{ID: "deepseek-flash", Effort: "high"},
			next:       Model{ID: "deepseek-flash"},
			wantReplay: true,
			wantBlocks: true,
		},
		{
			name:       "the Claude model drops its blocks when the effort is taken away",
			signed:     true,
			first:      Model{ID: "claude-sonnet-4-5", Effort: "high"},
			next:       Model{ID: "claude-sonnet-4-5"},
			wantReplay: false,
			wantBlocks: true,
		},
		{
			name:         "the Claude model keeps its signed blocks when the effort stays",
			signed:       true,
			first:        Model{ID: "claude-sonnet-4-5", Effort: "high"},
			next:         Model{ID: "claude-sonnet-4-5", Effort: "high"},
			wantThinking: true,
			wantReplay:   true,
			wantBlocks:   true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := &anthropicEndpoint{t: t, deepSeek: c.deepSeek, signed: c.signed,
				steps: []string{"read", ""}}
			ts := httptest.NewServer(srv)
			defer ts.Close()

			a := srv.adapter(ts)
			tools := []Tool{{Name: "read"}}
			resp, err := a.ChatStreamTools(context.Background(),
				[]Message{{Role: "user", Content: "read the file"}},
				c.first, tools, nil)
			if err != nil {
				t.Fatal(err)
			}
			if (len(resp.ReasoningBlocks) > 0) != c.wantBlocks {
				t.Fatalf("the reasoning blocks of the first turn = %+v", resp.ReasoningBlocks)
			}
			// The continuation carries the turn back exactly as the
			// provider answered it, with the origin that says which
			// endpoint produced it.
			follow := []Message{
				{Role: "user", Content: "read the file"},
				{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
					ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks,
					Origin: resp.Origin, Thinking: resp.Thinking},
				{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
			}
			if _, err := a.ChatStreamTools(context.Background(), follow, c.next, tools, nil); err != nil {
				t.Fatalf("the continuation was refused: %v", err)
			}

			// The request itself says what the operator asked for.
			body := srv.last()
			_, asked := body["thinking"]
			if asked != c.wantThinking {
				t.Errorf("the continuation asked for thinking = %v, want %v: %v",
					asked, c.wantThinking, body["thinking"])
			}
			// And the turn carries the blocks, or none.
			blocks := srv.thinkingBlocks()
			if (len(blocks) > 0) != c.wantReplay {
				t.Errorf("the blocks were replayed = %v, want %v: %+v",
					len(blocks) > 0, c.wantReplay, srv.assistantBlocks())
			}
			if c.wantReplay && c.signed {
				if blocks[0]["signature"] != "sig-abc" {
					t.Errorf("the replayed block = %+v", blocks[0])
				}
				if blocks[0]["thinking"] != "the model weighed it" {
					t.Errorf("the thinking was changed: %+v", blocks[0])
				}
			}
			if srv.violations != 0 {
				t.Errorf("the endpoint refused %d requests", srv.violations)
			}
		})
	}
}

// The alias with the effort taken away is the failure this whole
// contract exists for.
//
// The first turn asked for thinking and the provider thought. The
// continuation says nothing about thinking — and the provider still
// thinks, because that is what the endpoint does. The turn therefore
// has to come back whole, and it does.
//
// The negative control is the same conversation with the blocks left
// out: the endpoint refuses it, which is the failure the fix removes.
func TestTheAliasWithTheEffortTakenAwayKeepsItsTurn(t *testing.T) {
	srv := &anthropicEndpoint{t: t, deepSeek: true, steps: []string{"read", ""}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		Model{ID: "claude-sonnet-4-5", Effort: "high"}, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks,
			Origin: resp.Origin, Thinking: resp.Thinking},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow,
		Model{ID: "claude-sonnet-4-5"}, tools, nil); err != nil {
		t.Fatalf("the continuation was refused: %v", err)
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
	// The exact shape of the request that is sent: the turn comes back
	// with the thinking block first, then the tool call, and the
	// request itself says nothing about thinking.
	body := srv.last()
	if _, ok := body["thinking"]; ok {
		t.Errorf("the request carried a thinking configuration: %v", body["thinking"])
	}
	if _, ok := body["output_config"]; ok {
		t.Errorf("the request carried an output configuration: %v", body["output_config"])
	}
	blocks := srv.assistantBlocks()
	if len(blocks) != 2 {
		t.Fatalf("the assistant turn = %+v", blocks)
	}
	if blocks[0]["type"] != "thinking" || blocks[0]["thinking"] != "the model weighed it" {
		t.Errorf("the first block = %+v, want the thinking block", blocks[0])
	}
	if blocks[1]["type"] != "tool_use" || blocks[1]["id"] != "call_read" {
		t.Errorf("the second block = %+v, want the tool call", blocks[1])
	}

	// The negative control: the same conversation on the same endpoint,
	// with the blocks left out, is refused by the endpoint — with the
	// message that names the failure the fix removes.
	srv.steps = append(srv.steps, "")
	_, err = a.ChatStreamTools(context.Background(),
		[]Message{
			{Role: "user", Content: "read the file"},
			{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
				ReasoningKey: resp.ReasoningKey, Origin: resp.Origin,
				Thinking: resp.Thinking},
			{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
		},
		Model{ID: "claude-sonnet-4-5"}, tools, nil)
	if err == nil {
		t.Fatal("the endpoint accepted a turn without its thinking blocks")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "no thinking block") {
		t.Errorf("the error does not name the failure: %v", err)
	}
}
