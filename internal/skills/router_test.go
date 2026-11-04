package skills

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// entriesFor builds a catalogue from the two shipped skills.
func entriesFor(t *testing.T) []Entry {
	t.Helper()
	return []Entry{
		{
			Name:    "system-prompt-writer",
			Version: "1.0.0",
			WhenToUse: `Writing or revising instructions that a machine will follow. Use for system
  prompts, agent prompts, task briefs, reusable personas, agent workflows and
  acceptance criteria. Triggers: "system prompt", "agent prompt", "prompt for",
  "instructions for", "task brief", "agent brief", "coding agent",
  "reusable persona", "agent workflow", "acceptance criteria",
  "evaluation prompt", "improve this prompt".`,
		},
		{
			Name:    "humanize-text",
			Version: "1.0.0",
			WhenToUse: `Rewriting text that a person will read. Use for emails, letters, replies,
  direct and text messages, professional correspondence, support requests,
  announcements, cover letters, social posts, biographies and descriptions.
  Triggers: "email", "letter", "reply", "text message", "direct message",
  "announcement", "cover letter", "social post", "biography",
  "make it sound", "humanize", "less formal", "warmer".`,
		},
	}
}

// selectorFor builds a selector over a fixed catalogue.
func selectorFor(t *testing.T, entries []Entry) *Selector {
	t.Helper()
	s := NewSelector(nil)
	s.entries = entries
	s.loaded = true
	return s
}

// TestRoutingSelectsTheRightSkill covers the ordinary cases: an
// agent-directed request, a human-directed request and a request that
// belongs to neither.
func TestRoutingSelectsTheRightSkill(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		comment string
	}{
		{
			name:  "agent prompt",
			input: "Write a system prompt for a coding agent that reviews Go code.",
			want:  "system-prompt-writer",
		},
		{
			name:  "agent instructions",
			input: "I need instructions for an agent that triages incoming support tickets.",
			want:  "system-prompt-writer",
		},
		{
			name:  "task brief",
			input: "Draft a task brief for the researcher who will evaluate the vendors.",
			want:  "system-prompt-writer",
		},
		{
			name:  "reusable persona",
			input: "Create a reusable persona for our documentation bot.",
			want:  "system-prompt-writer",
		},
		{
			name:  "improve a prompt",
			input: "Can you improve this prompt for our support assistant?",
			want:  "system-prompt-writer",
		},
		{
			name:  "email",
			input: "Help me write an email to my landlord about the broken boiler.",
			want:  "humanize-text",
		},
		{
			name:  "reply",
			input: "Draft a reply to the supplier who says the invoice is wrong.",
			want:  "humanize-text",
		},
		{
			name:  "announcement",
			input: "Write an announcement for the team about the office move.",
			want:  "humanize-text",
		},
		{
			name:  "social post",
			input: "Write a social post announcing the new release.",
			want:  "humanize-text",
		},
		{
			name:  "warmer",
			input: "Rewrite this so it sounds warmer before I send it to my sister.",
			want:  "humanize-text",
		},
		{
			name:  "ordinary conversation",
			input: "What do you think about hexagonal architecture?",
			want:  "",
		},
		{
			name:  "philosophical",
			input: "Is it possible to be intentional without being in control?",
			want:  "",
		},
		{
			name:  "investigation",
			input: "Compare the trade-offs of event sourcing and change data capture.",
			want:  "",
		},
		{
			name:  "conceptual question",
			input: "What is a system prompt?",
			want:  "",
		},
	}
	s := selectorFor(t, entriesFor(t))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := s.Route(c.input)
			got := ""
			if len(d.Skills) > 0 {
				got = d.Skills[0]
			}
			if got != c.want {
				t.Errorf("Route(%q) selected %q, want %q (reason: %s)",
					c.input, got, c.want, d.Reason)
			}
		})
	}
}

// TestQuotedTextDoesNotTrigger checks that a keyword inside quoted or
// pasted text does not select a skill on its own.
func TestQuotedTextDoesNotTrigger(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{
			name:  "double quotes",
			input: `What does this phrase mean? "you should write a system prompt for the agent"`,
		},
		{
			name:  "single quotes",
			input: `Is this good? 'draft an email to the landlord'`,
		},
		{
			name:  "curly quotes",
			input: `What is this? “write a cover letter for the role”`,
		},
		{
			name:  "block quote",
			input: "What is this for?\n> write a system prompt for a coding agent",
		},
		{
			name:  "fenced code",
			input: "What does this do?\n```\nwrite a system prompt for a coding agent\n```",
		},
	}
	s := selectorFor(t, entriesFor(t))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := s.Route(c.input)
			if d.Selected() {
				t.Errorf("Route(%q) selected %v (reason: %s)", c.input, d.Skills, d.Reason)
			}
		})
	}
}

// TestPastedTextDoesNotTrigger checks the harder case: the pasted
// document mentions a trigger word, but the request around it does not
// ask for anything to be written.
func TestPastedTextDoesNotTrigger(t *testing.T) {
	input := "Can you summarise this for me?\n\n" +
		"We were asked to write a system prompt for the finance agent last quarter, " +
		"and the letter to the auditor is still outstanding."
	s := selectorFor(t, entriesFor(t))
	d := s.Route(input)
	if d.Selected() {
		t.Errorf("Route selected %v (reason: %s)", d.Skills, d.Reason)
	}
}

// TestFollowUpKeepsTheSkill checks that a refinement continues the task
// under way.
func TestFollowUpKeepsTheSkill(t *testing.T) {
	s := selectorFor(t, entriesFor(t))
	d := s.Route("Write an email to my landlord about the broken boiler.")
	if len(d.Skills) != 1 || d.Skills[0] != "humanize-text" {
		t.Fatalf("first request selected %v", d.Skills)
	}
	for _, follow := range []string{
		"make it shorter",
		"change the tone",
		"warmer please",
		"shorter",
		"can you make it a bit more formal?",
	} {
		d = s.Route(follow)
		if len(d.Skills) != 1 || d.Skills[0] != "humanize-text" {
			t.Errorf("follow-up %q selected %v (reason: %s)", follow, d.Skills, d.Reason)
		}
		if d.Source != SourceSticky {
			t.Errorf("follow-up %q came from %q, want %q", follow, d.Source, SourceSticky)
		}
	}
}

// TestNewTaskEndsTheWritingTask checks that an unrelated request does not
// inherit the skill.
func TestNewTaskEndsTheWritingTask(t *testing.T) {
	s := selectorFor(t, entriesFor(t))
	s.Route("Write an email to my landlord about the broken boiler.")
	d := s.Route("What is the capital of Iceland?")
	if d.Selected() {
		t.Errorf("an unrelated request selected %v", d.Skills)
	}
}

// TestExplicitOverrideWins checks that the operator's pin wins.
func TestExplicitOverrideWins(t *testing.T) {
	s := selectorFor(t, entriesFor(t))
	if err := s.SetForced([]string{"humanize-text"}); err != nil {
		t.Fatal(err)
	}
	d := s.Route("Write a system prompt for a coding agent.")
	if len(d.Skills) != 1 || d.Skills[0] != "humanize-text" {
		t.Errorf("the override was ignored: %v", d.Skills)
	}
	if d.Source != SourceExplicit {
		t.Errorf("source = %q, want %q", d.Source, SourceExplicit)
	}
	s.ClearForced()
	d = s.Route("Write a system prompt for a coding agent.")
	if len(d.Skills) != 1 || d.Skills[0] != "system-prompt-writer" {
		t.Errorf("after clearing the override: %v", d.Skills)
	}
}

// TestNamedSkillIsHonoured checks that a request naming a skill selects
// it.
func TestNamedSkillIsHonoured(t *testing.T) {
	s := selectorFor(t, entriesFor(t))
	d := s.Route("Use the humanize-text skill for this draft, please.")
	if len(d.Skills) != 1 || d.Skills[0] != "humanize-text" {
		t.Errorf("selected %v (reason: %s)", d.Skills, d.Reason)
	}
	if d.Source != SourceExplicit {
		t.Errorf("source = %q, want %q", d.Source, SourceExplicit)
	}
}

// TestAmbiguousRequestSelectsNothing checks the tie case.
func TestAmbiguousRequestSelectsNothing(t *testing.T) {
	s := selectorFor(t, entriesFor(t))
	// "letter" is a humanize trigger, "instructions for" an agent
	// trigger: both weigh the same in one paragraph only if the agent
	// phrase is a single word, so this uses two single-word triggers.
	d := s.Route("Write a reply and a letter about the announcement.")
	if d.Selected() {
		t.Logf("selected %v (reason: %s)", d.Skills, d.Reason)
	}
	// The documented rule: an ambiguous request must not pick one.
	entries := entriesFor(t)
	entries[0].WhenToUse = `Triggers: "brief".`
	entries[1].WhenToUse = `Triggers: "brief".`
	s2 := selectorFor(t, entries)
	d = s2.Route("Write me a brief about the launch.")
	if d.Selected() {
		t.Errorf("an ambiguous request selected %v", d.Skills)
	}
	if d.Reason == "" {
		t.Error("an ambiguous request must explain itself")
	}
}

// TestOnlyOneSkillIsSelected checks that the router never stacks skills.
func TestOnlyOneSkillIsSelected(t *testing.T) {
	s := selectorFor(t, entriesFor(t))
	for _, input := range []string{
		"Write a system prompt for an email assistant.",
		"Write an email about the agent system.",
		"Write a reply to the letter about the prompt.",
	} {
		d := s.Route(input)
		if len(d.Skills) > 1 {
			t.Errorf("Route(%q) stacked %v", input, d.Skills)
		}
	}
}

// TestSkillWithoutTriggersIsNeverSelected checks that a skill with no
// activation guidance stays out of the way.
func TestSkillWithoutTriggersIsNeverSelected(t *testing.T) {
	entries := append(entriesFor(t), Entry{
		Name:      "mpm-live-update-procedure",
		Version:   "1.0.0",
		WhenToUse: "",
	})
	s := selectorFor(t, entries)
	d := s.Route("Write a system prompt for a coding agent.")
	if len(d.Skills) != 1 || d.Skills[0] != "system-prompt-writer" {
		t.Errorf("selected %v", d.Skills)
	}
}

// TestVocabularyPrefersExplicitTriggers checks that the quoted trigger
// list is what activates a skill.
func TestVocabularyPrefersExplicitTriggers(t *testing.T) {
	v := vocabularyOf(`Some prose about writing. Triggers: "system prompt", "agent brief".`)
	if !v.precise {
		t.Fatal("the explicit triggers were not recognised")
	}
	if len(v.terms) != 2 {
		t.Fatalf("terms = %#v", v.terms)
	}
	if v.terms[0].text != "system prompt" || v.terms[0].weight != 2 {
		t.Errorf("term = %#v", v.terms[0])
	}
	if v.terms[1].text != "agent brief" || v.terms[1].weight != 2 {
		t.Errorf("term = %#v", v.terms[1])
	}
}

// TestVocabularyFallsBackToProse checks that a skill written without
// explicit triggers still has a vocabulary.
func TestVocabularyFallsBackToProse(t *testing.T) {
	v := vocabularyOf("Use when drafting a quarterly briefing for the board.")
	if v.precise {
		t.Error("a skill without triggers must not be precise")
	}
	if len(v.terms) == 0 {
		t.Fatal("no terms were derived from the prose")
	}
}

// TestOwnVoiceStripsQuotedContent checks the text extraction directly.
func TestOwnVoiceStripsQuotedContent(t *testing.T) {
	voice := ownVoice("hello \"write a system prompt\" world")
	if strings.Contains(voice, "system prompt") {
		t.Errorf("quoted text survived: %q", voice)
	}
	voice = ownVoice("hello 'write a system prompt' world")
	if strings.Contains(voice, "system prompt") {
		t.Errorf("quoted text survived: %q", voice)
	}
	// An apostrophe in a contraction must survive.
	voice = ownVoice("don't write a system prompt")
	if !strings.Contains(voice, "don't") {
		t.Errorf("the contraction was mangled: %q", voice)
	}
}

// TestMatchTermsDoesNotDoubleCountOverlaps checks that a phrase inside
// another phrase is counted once.
func TestMatchTermsDoesNotDoubleCountOverlaps(t *testing.T) {
	terms := []term{
		{text: "system prompt", weight: 2},
		{text: "prompt for", weight: 2},
	}
	found := matchTerms(terms, "write a system prompt for the agent")
	if len(found) != 1 {
		t.Fatalf("matches = %#v", found)
	}
	if found[0].term.text != "system prompt" {
		t.Errorf("match = %#v", found[0])
	}
}

// TestFindTermRespectsWordBoundaries checks the matching rule.
func TestFindTermRespectsWordBoundaries(t *testing.T) {
	cases := []struct {
		text string
		term string
		want int
	}{
		{"write an email now", "email", 1},
		{"write an emails now", "email", 1},
		{"emailed the client", "email", 1},
		{"preemail value", "email", 0},
		{"the mailbox", "email", 0},
		{"email and email", "email", 2},
	}
	for _, c := range cases {
		got := len(findTerm(c.text, c.term))
		if got != c.want {
			t.Errorf("findTerm(%q, %q) = %d, want %d", c.text, c.term, got, c.want)
		}
	}
}

// TestNilSelectorIsSafe checks that a missing store cannot panic.
func TestNilSelectorIsSafe(t *testing.T) {
	var s *Selector
	d := s.Route("write an email")
	if d.Selected() {
		t.Error("a nil selector selected a skill")
	}
	if err := s.SetForced([]string{"x"}); err == nil {
		t.Error("a nil selector accepted an override")
	}
	loaded, err := s.Load(context.Background(), []string{"x"})
	if err != nil {
		t.Errorf("a nil selector reported an error: %v", err)
	}
	if len(loaded) != 0 {
		t.Error("a nil selector loaded a skill")
	}
	if s.Catalogue().Loaded {
		t.Error("a nil selector has a catalogue")
	}
}

// TestClientReadsTheRealStore exercises the MPM command line end to end.
//
// It is skipped when MPM is not installed, so the suite stays runnable
// without it.
func TestClientReadsTheRealStore(t *testing.T) {
	if _, err := exec.LookPath("mpm"); err != nil {
		if _, err := os.Stat(filepath.Join(mustHome(t), ".mpm", "bin", "mpm")); err != nil {
			t.Skip("mpm is not installed")
		}
	}
	c := &Client{}
	entries, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) == 0 {
		t.Skip("the skill store holds no skills")
	}
	byName := map[string]Entry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	skill, ok := byName["system-prompt-writer"]
	if !ok {
		t.Skip("the system-prompt-writer skill is not in the store")
	}
	if skill.Version == "" {
		t.Error("the catalogue entry has no version")
	}
	loaded, err := c.Read(context.Background(), "system-prompt-writer")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if loaded.Body == "" {
		t.Error("the skill has no body")
	}
	if len(loaded.Constraints) == 0 {
		t.Error("the skill has no constraints")
	}
}

// TestLastJSONObject checks the envelope parsing.
func TestLastJSONObject(t *testing.T) {
	out := []byte("starting up\n{\"success\":true,\"skills\":[]}\n")
	got, err := lastJSONObject(out)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal(got, &probe); err != nil {
		t.Fatal(err)
	}
	if probe["success"] != true {
		t.Errorf("envelope = %#v", probe)
	}
	if _, err := lastJSONObject([]byte("no json here\n")); err == nil {
		t.Error("expected an error")
	}
}

// mustHome returns the home directory for a test.
func mustHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	return home
}
