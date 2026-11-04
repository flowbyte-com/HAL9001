package skills

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Routing sources. They say why a skill was chosen, for the diagnostics
// view.
const (
	// SourceAuto means the router chose the skill from the request.
	SourceAuto = "auto"
	// SourceExplicit means the operator or the request named the skill.
	SourceExplicit = "explicit"
	// SourceSticky means the request continues the writing task already
	// under way.
	SourceSticky = "sticky"
)

// Decision is the outcome of routing one request.
type Decision struct {
	// Skills are the selected skills, in order. Normally one; more only
	// when a combination was asked for explicitly.
	Skills []string
	// Source says why the skill was selected.
	Source string
	// Reason explains the choice in one line.
	Reason string
	// Trigger is the phrase that matched, when one did.
	Trigger string
	// Candidates lists the skills that were considered, best first.
	Candidates []Candidate
	// Applied lists the skills whose guidance was actually loaded. It is
	// empty when the selected skill could not be read.
	Applied []string
}

// Selected reports whether a skill was chosen.
func (d Decision) Selected() bool { return len(d.Skills) > 0 }

// Candidate is one skill's score for a request.
type Candidate struct {
	Name    string
	Score   int
	Trigger string
}

// Route decides which skill applies to a request.
//
// The decision is made here, locally, without a model call: the request
// is matched against the activation guidance MPM publishes for each
// skill. A request that matches nothing, or that matches two skills
// equally well, gets no skill at all — ordinary HAL is the safe answer.
//
// The decision is recorded so the interface can show it.
func (s *Selector) Route(input string) Decision {
	if s == nil {
		return Decision{Source: SourceAuto, Reason: "no skill store"}
	}
	s.mu.Lock()
	entries := append([]Entry(nil), s.entries...)
	forced := append([]string(nil), s.forced...)
	sticky := s.sticky
	s.mu.Unlock()

	// An operator override wins over everything.
	if len(forced) > 0 {
		d := Decision{
			Skills: forced,
			Source: SourceExplicit,
			Reason: "the operator pinned this skill",
		}
		s.record(d, input)
		return d
	}

	voice := ownVoice(input)

	// A skill the request names outright is an explicit request.
	if named, ok := namedSkill(entries, voice); ok {
		d := Decision{
			Skills:  []string{named},
			Source:  SourceExplicit,
			Reason:  "the request names the skill",
			Trigger: named,
		}
		s.record(d, input)
		return d
	}

	candidates := scoreEntries(entries, voice)
	tied := len(candidates) > 1 && candidates[1].Score == candidates[0].Score
	if len(candidates) == 0 || tied {
		// Nothing matched, or two skills matched equally well. A follow-up
		// continues the task that is already under way.
		if sticky != "" && isRefinement(voice) {
			d := Decision{
				Skills:  []string{sticky},
				Source:  SourceSticky,
				Reason:  "the request continues the writing task already under way",
				Trigger: sticky,
			}
			s.record(d, input)
			return d
		}
		reason := "no skill matched the request"
		if tied {
			reason = "the request matched " + candidates[0].Name + " and " +
				candidates[1].Name + " equally well"
		}
		d := Decision{Source: SourceAuto, Reason: reason, Candidates: candidates}
		s.record(d, input)
		return d
	}

	best := candidates[0]
	d := Decision{
		Skills:     []string{best.Name},
		Source:     SourceAuto,
		Reason:     "matched " + quote(best.Trigger),
		Trigger:    best.Trigger,
		Candidates: candidates,
	}
	s.record(d, input)
	return d
}

// record stores the decision and keeps the writing task alive.
//
// input is the request the decision was made from: whether the writing
// task continues depends on the request, not on the decision.
func (s *Selector) record(d Decision, input string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = d
	switch {
	case len(d.Skills) > 0:
		s.sticky = d.Skills[0]
	case d.Source == SourceAuto && !isRefinement(ownVoice(input)):
		// The writing task ended: the next request starts fresh.
		s.sticky = ""
	}
}

// scoreEntries scores every skill against the user's own words, best
// first.
func scoreEntries(entries []Entry, voice string) []Candidate {
	out := make([]Candidate, 0, len(entries))
	for _, e := range entries {
		score, trigger := scoreEntry(e, voice)
		if score > 0 {
			out = append(out, Candidate{Name: e.Name, Score: score, Trigger: trigger})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// scoreEntry scores one skill against the request.
//
// A trigger only counts inside a paragraph that also asks for something
// to be written. That is what keeps a pasted document from activating a
// skill: the paste usually mentions the trigger word, but it does not
// ask for anything to be written in the same breath.
func scoreEntry(e Entry, voice string) (int, string) {
	vocab := vocabularyOf(e.WhenToUse)
	if len(vocab.terms) == 0 {
		return 0, ""
	}
	total := 0
	best := ""
	bestScore := 0
	for _, para := range instructionParagraphs(voice) {
		if !requestsWriting(para) {
			continue
		}
		score, trigger := scoreParagraph(vocab, para)
		if score == 0 {
			continue
		}
		total += score
		if score > bestScore {
			bestScore = score
			best = trigger
		}
	}
	return total, best
}

// vocabulary is the activation vocabulary of one skill.
type vocabulary struct {
	terms []term
	// precise reports that the author supplied explicit trigger phrases.
	// Without them, one incidental word is not enough to activate a
	// skill.
	precise bool
}

// term is one activation term: a phrase or a single word.
type term struct {
	text   string
	weight int
}

// vocabulary returns the activation terms of a skill.
//
// The quoted phrases in the activation guidance are the author's
// explicit triggers, and they are matched as phrases. The rest of the
// prose contributes single words with a lower weight, so a skill written
// without explicit triggers still has a chance of being found.
func vocabularyOf(whenToUse string) vocabulary {
	guidance := whenToUse
	if guidance == "" {
		return vocabulary{}
	}
	triggerText := ""
	if i := indexFold(guidance, "triggers:"); i >= 0 {
		triggerText = guidance[i+len("triggers:"):]
	}
	seen := map[string]bool{}
	var terms []term
	for _, phrase := range quotedPhrases(triggerText) {
		phrase = strings.ToLower(strings.TrimSpace(phrase))
		if phrase == "" || seen[phrase] {
			continue
		}
		seen[phrase] = true
		terms = append(terms, term{text: phrase, weight: weightOf(phrase)})
	}
	precise := len(terms) > 0
	if !precise {
		for _, word := range proseWords(guidance) {
			if seen[word] {
				continue
			}
			seen[word] = true
			terms = append(terms, term{text: word, weight: 1})
		}
	}
	return vocabulary{terms: terms, precise: precise}
}

// scoreParagraph scores the terms found in one paragraph and returns the
// trigger that produced the score.
func scoreParagraph(vocab vocabulary, para string) (int, string) {
	found := matchTerms(vocab.terms, para)
	if len(found) == 0 {
		return 0, ""
	}
	if !vocab.precise {
		// Without explicit triggers, a single incidental word is not
		// enough: two distinct terms have to agree.
		distinct := map[string]bool{}
		for _, m := range found {
			distinct[m.term.text] = true
		}
		if len(distinct) < 2 {
			return 0, ""
		}
	}
	score := 0
	best := ""
	bestWeight := 0
	for _, m := range found {
		score += m.term.weight
		if m.term.weight > bestWeight {
			bestWeight = m.term.weight
			best = m.term.text
		}
	}
	return score, best
}

// match is one accepted occurrence of a term.
type match struct {
	term  term
	start int
	end   int
}

// matchTerms finds the occurrences of the terms in text, longest first,
// and keeps the ones that do not overlap. Overlapping terms would count
// one phrase twice: "system prompt" already covers "prompt for".
func matchTerms(terms []term, text string) []match {
	lower := strings.ToLower(text)
	type occurrence struct {
		term  term
		start int
		end   int
	}
	var candidates []occurrence
	for _, t := range terms {
		for _, span := range findTerm(lower, t.text) {
			candidates = append(candidates, occurrence{term: t, start: span[0], end: span[1]})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		li := candidates[i].end - candidates[i].start
		lj := candidates[j].end - candidates[j].start
		if li != lj {
			return li > lj
		}
		return candidates[i].start < candidates[j].start
	})
	var out []match
	for _, c := range candidates {
		overlaps := false
		for _, m := range out {
			if c.start < m.end && m.start < c.end {
				overlaps = true
				break
			}
		}
		if !overlaps {
			out = append(out, match{term: c.term, start: c.start, end: c.end})
		}
	}
	return out
}

// findTerm returns the spans where term occurs in lower-cased text,
// requiring word boundaries on both sides.
func findTerm(text, term string) [][2]int {
	var out [][2]int
	if term == "" {
		return nil
	}
	from := 0
	for from <= len(text)-len(term) {
		i := strings.Index(text[from:], term)
		if i < 0 {
			return out
		}
		i += from
		end := i + len(term)
		if boundaryBefore(text, i) && boundaryAfter(text, end) {
			out = append(out, [2]int{i, end})
			from = end
			continue
		}
		from = i + 1
	}
	return out
}

// boundaryBefore reports whether what precedes the match is a word
// character.
func boundaryBefore(text string, i int) bool {
	if i <= 0 {
		return true
	}
	return !isWordByte(text[i-1])
}

// boundaryAfter reports whether what follows the match is a word
// character. A trailing "s", "es", "ed" or "ing" is allowed, so "email"
// matches "emails" and "emailed".
func boundaryAfter(text string, end int) bool {
	if end >= len(text) {
		return true
	}
	for _, suffix := range []string{"es", "ed", "ing", "s"} {
		if strings.HasPrefix(text[end:], suffix) {
			rest := end + len(suffix)
			if rest >= len(text) {
				return true
			}
			return !isWordByte(text[rest])
		}
	}
	return !isWordByte(text[end])
}

// isWordByte reports whether a byte belongs to a word.
func isWordByte(c byte) bool {
	return c == '_' ||
		(c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		c >= 0x80
}

// weightOf scores a trigger: a phrase is stronger evidence than a single
// word.
func weightOf(phrase string) int {
	if strings.ContainsAny(phrase, " \t") {
		return 2
	}
	return 1
}

// quotedPhrases returns the double-quoted spans of a text.
func quotedPhrases(text string) []string {
	var out []string
	for {
		i := strings.IndexByte(text, '"')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(text[i+1:], '"')
		if j < 0 {
			return out
		}
		out = append(out, text[i+1:i+1+j])
		text = text[i+j+2:]
	}
}

// proseWords returns the content words of the activation guidance.
func proseWords(guidance string) []string {
	fields := strings.FieldsFunc(strings.ToLower(guidance), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var out []string
	for _, f := range fields {
		if len(f) < 5 || stopwords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// stopwords are the words that carry no activation signal.
var stopwords = map[string]bool{
	"about": true, "above": true, "after": true, "again": true,
	"against": true, "along": true, "also": true, "always": true,
	"among": true, "another": true, "because": true, "been": true,
	"before": true, "being": true, "between": true, "both": true,
	"cannot": true, "could": true, "does": true, "doing": true,
	"done": true, "down": true, "during": true, "each": true,
	"either": true, "else": true, "even": true, "ever": true,
	"every": true, "from": true, "further": true, "have": true,
	"having": true, "here": true, "however": true, "into": true,
	"itself": true, "just": true, "like": true, "made": true,
	"make": true, "many": true, "more": true, "most": true,
	"much": true, "must": true, "need": true, "only": true,
	"other": true, "ours": true, "over": true, "own": true,
	"same": true, "shall": true, "should": true, "since": true,
	"some": true, "such": true, "than": true, "that": true,
	"their": true, "them": true, "then": true, "there": true,
	"these": true, "they": true, "this": true, "those": true,
	"through": true, "under": true, "until": true, "very": true,
	"were": true, "what": true, "when": true, "where": true,
	"which": true, "while": true, "with": true, "would": true,
	"your": true, "yours": true,
}

// requestSignals mark a paragraph as an instruction to write something.
//
// They gate a match; they never select a skill on their own. "please" is
// deliberately absent: pasted correspondence is full of it.
var requestSignals = []string{
	"write", "draft", "compose", "rewrite", "rephrase", "reword",
	"polish", "humanize", "edit", "improve", "generate", "produce",
	"create", "prepare", "build", "put together", "turn into", "convert",
	"shorten", "lengthen", "expand", "condense", "tighten", "soften",
	"adjust", "format", "adapt", "translate", "summarise", "summarize",
	"make it sound", "give me", "help me", "make", "sound", "give",
	"need", "want",
}

// requestsWriting reports whether a paragraph asks for something to be
// written.
func requestsWriting(para string) bool {
	lower := strings.ToLower(para)
	for _, signal := range requestSignals {
		if len(findTerm(lower, signal)) > 0 {
			return true
		}
	}
	return false
}

// refinementCues mark a short follow-up that continues the writing task
// already under way.
var refinementCues = []string{
	"it", "this", "that", "the above", "the draft", "the email",
	"the letter", "the prompt", "the text", "the message", "the post",
	"the copy", "the announcement", "the bio", "the description",
	"shorter", "longer", "tone", "warmer", "cooler", "formal", "casual",
	"concise", "expand", "rewrite", "rephrase", "adjust", "change",
	"improve", "again", "different", "version", "keep", "instead",
	"more", "less", "polish", "tighten", "soften", "nicer",
}

// isRefinement reports whether a message looks like a follow-up that
// continues the writing task already under way.
func isRefinement(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || len(trimmed) > 300 {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, cue := range refinementCues {
		if len(findTerm(lower, cue)) > 0 {
			return true
		}
	}
	return false
}

// namedSkill reports whether the request names one of the skills
// outright.
func namedSkill(entries []Entry, voice string) (string, bool) {
	lower := strings.ToLower(voice)
	for _, e := range entries {
		if e.Name == "" {
			continue
		}
		if len(findTerm(lower, strings.ToLower(e.Name))) > 0 {
			return e.Name, true
		}
	}
	return "", false
}

// quotedSpans matches inline quoted text.
//
// The single-quoted form needs at least two characters inside it, so an
// apostrophe in a contraction is left alone.
var quotedSpans = regexp.MustCompile(`"[^"\n]*"|'[^'\n]{2,}'|“[^”\n]*”|«[^»\n]*»`)

// ownVoice returns the parts of the message that are the user's own
// words.
//
// Fenced code, quoted spans and blockquoted lines are removed: a keyword
// inside them belongs to the quoted text, not to the request.
func ownVoice(text string) string {
	var kept []string
	fence := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			continue
		}
		kept = append(kept, quotedSpans.ReplaceAllString(line, " "))
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// pastedLength is the length above which a paragraph is treated as
// pasted content rather than as something the user wrote for HAL.
//
// A pasted document is long; a request is not. The first paragraph is
// always the user's, however long it is, because a request that arrives
// with a long preamble is still the user's own words.
const pastedLength = 120

// instructionParagraphs returns the paragraphs that may carry the
// request.
//
// The first paragraph is always included. A later paragraph counts only
// when it is short: a long one is pasted text, and a pasted document
// mentions the trigger words without asking for anything to be written.
func instructionParagraphs(voice string) []string {
	all := paragraphs(voice)
	out := make([]string, 0, len(all))
	for i, para := range all {
		if i == 0 || len(para) <= pastedLength {
			out = append(out, para)
		}
	}
	return out
}

// paragraphs splits text into paragraphs on blank lines.
func paragraphs(text string) []string {
	var out []string
	var current []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(current) > 0 {
				out = append(out, strings.Join(current, "\n"))
				current = nil
			}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		out = append(out, strings.Join(current, "\n"))
	}
	return out
}

// indexFold finds a substring, ignoring case.
func indexFold(text, sub string) int {
	return strings.Index(strings.ToLower(text), strings.ToLower(sub))
}

// quote renders a trigger for a human.
func quote(s string) string {
	if s == "" {
		return ""
	}
	return "\"" + s + "\""
}
