//go:build demo

// The live demonstration answers one request twice: once the way HAL
// answered it before skills existed, and once with the skill guidance
// composed into the request.
//
// It needs a provider, so it never runs by default. It is a separate
// build tag:
//
//	HAL9001_DEMO=1 go test -tags demo ./internal/engine/ -run TestLiveDemo -v
//
// The comparison uses the real routing, the real MPM store and the real
// provider: only the guidance differs between the two answers.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/skills"
)

// demoCase is one request to demonstrate.
type demoCase struct {
	label  string
	expect string
	input  string
}

// demoCases cover both skills and the ordinary conversation.
var demoCases = []demoCase{
	{
		label:  "system-prompt-writer",
		expect: "system-prompt-writer",
		input:  "Write a system prompt for a coding agent that reviews Go code for bugs.",
	},
	{
		label:  "system-prompt-writer",
		expect: "system-prompt-writer",
		input:  "I need a task brief for a research agent that compares our two vendors.",
	},
	{
		label:  "system-prompt-writer",
		expect: "system-prompt-writer",
		input:  "Draft a reusable persona for a documentation assistant that answers API questions.",
	},
	{
		label:  "humanize-text",
		expect: "humanize-text",
		input:  "Write an email to my landlord about the boiler that has been broken since Tuesday.",
	},
	{
		label:  "humanize-text",
		expect: "humanize-text",
		input:  "Draft a short reply to a customer whose order arrived two weeks late.",
	},
	{
		label:  "humanize-text",
		expect: "humanize-text",
		input:  "Write a LinkedIn post announcing that I have started a new job as a data engineer.",
	},
	{
		label:  "neither",
		expect: "",
		input:  "Is it possible to be deliberate about something without being in control of it?",
	},
	{
		label:  "neither",
		expect: "",
		input:  "What are the real trade-offs between event sourcing and change data capture?",
	},
}

// demoProvider builds a provider from the operator's own configuration.
func demoProvider(t *testing.T) provider.Provider {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	raw, err := os.ReadFile(filepath.Join(home, ".hal9001", "config.json"))
	if err != nil {
		t.Skip("no HAL9001 configuration")
	}
	var cfg struct {
		Endpoint    string `json:"endpoint"`
		APIKey      string `json:"api_key"`
		ActiveModel string `json:"active_model"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Skip("the configuration is unreadable")
	}
	key := strings.TrimSpace(os.Getenv("MINIMAX_API_KEY"))
	if key == "" {
		key = cfg.APIKey
	}
	if cfg.Endpoint == "" || key == "" {
		t.Skip("the configuration has no endpoint or key")
	}
	return &provider.OpenAICompatible{
		Endpoint: cfg.Endpoint,
		APIKey:   key,
	}
}

// TestLiveDemo compares the two answers for every demonstration case.
func TestLiveDemo(t *testing.T) {
	if os.Getenv("HAL9001_DEMO") == "" {
		t.Skip("set HAL9001_DEMO=1 to run the live demonstration")
	}
	p := demoProvider(t)

	model := provider.Model{ID: "MiniMax-M3.1-Flash"}

	for i, c := range demoCases {
		c := c
		t.Run(fmt.Sprintf("%02d-%s", i+1, c.label), func(t *testing.T) {
			// Every case is its own conversation, so the writing task of
			// one case cannot carry into the next.
			sel := skills.NewSelector(&skills.Client{})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := sel.Refresh(ctx); err != nil {
				t.Fatalf("the skill store is unavailable: %v", err)
			}
			if i == 0 {
				catalogue := sel.Catalogue()
				t.Logf("MPM catalogue: %d skills", len(catalogue.Entries))
				for _, e := range catalogue.Entries {
					t.Logf("  %s v%s", e.Name, e.Version)
				}
			}
			// The decision the router makes for this request.
			decision := sel.Route(c.input)
			got := ""
			if len(decision.Skills) > 0 {
				got = decision.Skills[0]
			}
			if got != c.expect {
				t.Errorf("routed %q, expected %q (reason: %s)", got, c.expect, decision.Reason)
			}
			t.Logf("REQUEST: %s", c.input)
			t.Logf("ROUTED: %q (source %s) — %s", got, decision.Source, decision.Reason)

			// The guidance that would be loaded.
			loaded, err := sel.Load(context.Background(), decision.Skills)
			if err != nil {
				t.Logf("LOAD: failed: %v", err)
			}
			guidance := skillBlock(loaded)
			if guidance != "" {
				t.Logf("LOADED: %s (%d bytes of guidance, %d constraints)",
					decision.Skills[0], len(guidance), len(loaded[0].Constraints))
			} else {
				t.Logf("LOADED: nothing")
			}

			plain := demoAnswer(t, p, model, c.input, "")
			guided := demoAnswer(t, p, model, c.input, guidance)
			t.Logf("WITHOUT THE SKILL:\n%s", plain)
			t.Logf("WITH THE SKILL:\n%s", guided)
		})
	}
}

// demoAnswer runs one turn with the given guidance composed into the
// request.
func demoAnswer(t *testing.T, p provider.Provider, model provider.Model, input, guidance string) string {
	t.Helper()
	dir := t.TempDir()
	store, err := session.Open(dir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	eng := New(store, p, model).WithSystemPrompt(demoPersona)
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	messages := eng.requestMessages(history, guidance)
	messages = append(messages, provider.Message{Role: "user", Content: input})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	resp, err := eng.chat(ctx, messages, nil, nil)
	if err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	return strings.TrimSpace(resp.Content)
}

// demoPersona is the identity the demonstration answers under.
const demoPersona = `# HAL9001 (demo)

You are HAL9001, a conversational thinking partner. You think *with* the user, not *for* them: explore ideas, remember what matters, disagree when warranted, and help turn developed thinking into clear instructions for others.

- **Talk first.** Respond directly and naturally. Most turns need nothing more than words.
- **Be intellectually honest.** Offer disagreement when you genuinely see a flaw, including with the user's ideas. Do not flatter, do not hedge with empty enthusiasm, and do not moralize.
- **Prompts are your craft.** When the user wants instructions for someone (or something) else — a coding agent, a researcher, a collaborator — build a portable brief: objective, background, requirements, constraints, expected deliverables, acceptance criteria. No vendor names, no proprietary assumptions.
- **Never implement.** You do not run commands, edit files, or deploy. When work needs doing, you prepare the assignment and review the outcome.
`
