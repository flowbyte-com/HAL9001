package provider

import (
	"fmt"
	"strings"
)

// The context budget.
//
// A context budget is the room a request has. It is resolved before the
// request is sent, it is applied to the request as it will be sent, and
// it is enforced by dropping whole exchanges from the oldest end until
// the request fits.
//
// Three things are never done:
//
//   - the safety instructions are never dropped. They carry the
//     permission the request runs under, and a request without them
//     would be a different request;
//   - the operator's current request is never dropped to satisfy a
//     budget;
//   - a tool call is never separated from its result. The two belong to
//     the same exchange, and an exchange is dropped whole or not at all.
//
// Nothing is summarised and nothing is rewritten: older context is
// dropped, not compacted, and no retry is attempted.

// ContextBudget is the resolved budget for one request.
//
// The budget is expressed in the room the request itself may use. The
// answer's room is held back from it and reported separately, so the
// operator can see how much of the window the answer may take.
type ContextBudget struct {
	// Limit is the room the request may use, in tokens. Zero means no
	// window is known at all: the provider's own limit governs and HAL
	// does not trim.
	//
	// A window that IS known never produces a zero budget. A zero
	// budget would be read as "no budget at all", which is the
	// opposite of what a known window says.
	Limit int `json:"limit,omitempty"`
	// Total is the context window the budget was measured against, in
	// tokens. It is the whole context the model accepts: the answer's
	// room comes out of it, and what is left is the room the request
	// may use.
	//
	// Zero means no window is known at all.
	Total int `json:"total,omitempty"`
	// ReservedOutput is the room held back for the answer, in tokens.
	//
	// It is the EFFECTIVE reservation: the room the answer really
	// gets after the window was taken into account. It is the number
	// that goes on the wire, so the budget and the request can never
	// disagree.
	ReservedOutput int `json:"reserved_output,omitempty"`
	// Source says where the limit came from.
	Source ContextSource `json:"source,omitempty"`
	// Mode says how the limit was chosen.
	Mode ContextMode `json:"context_mode,omitempty"`
	// Verified reports whether the number was verified. A provisional
	// budget is never verified.
	Verified bool `json:"verified,omitempty"`
	// Note explains the budget to the operator.
	Note string `json:"note,omitempty"`
}

// Usable reports whether the budget can be enforced at all.
//
// A budget that leaves no room for the request is not a budget:
// enforcing it would mean refusing every request. The caller is told
// so rather than being given a silent no-op.
func (b ContextBudget) Usable() bool {
	return b.Limit > 0
}

// OutputAllowance is the room reserved for the answer.
//
// The window a model accepts is a TOTAL: the room for the answer comes
// out of it, and what is left is the room the request may use. The same
// interpretation applies to every source of the window — the
// operator's own limit, the provider's advertisement, a local runtime's
// allocation and a record verified earlier — so the allowance is
// measured against the same number in every case.
//
// Configured reports whether the operator configured that number. A
// configured allowance that does not fit inside the window is a
// contradiction between two numbers the operator wrote down, and it is
// refused with a clear error. A default is reduced conservatively
// instead, so that a window always leaves room for the request.
//
// Minimum is the smallest cap the protocol accepts for this request. A
// reasoning model needs room for its thinking budget, and the Messages
// API refuses a smaller cap. It is never lowered, whatever the window
// says: a cap the API rejects would fail the turn in a way the
// operator cannot act on.
type OutputAllowance struct {
	// Tokens is the room the answer may use.
	Tokens int
	// Configured reports whether the operator configured the number.
	Configured bool
	// Minimum is the smallest cap the protocol accepts.
	Minimum int
}

// ContextAllowanceError reports that the room reserved for the answer
// does not fit inside the context window.
//
// It is a contradiction between two configured numbers, not a provider
// failure: the operator asked for an answer the window cannot hold.
// The numbers are named, so the operator can see what to change.
type ContextAllowanceError struct {
	// Total is the context window the model accepts, in tokens.
	Total int
	// Reserved is the room the answer was given, in tokens.
	Reserved int
}

// Error implements the error interface.
func (e *ContextAllowanceError) Error() string {
	return fmt.Sprintf(
		"the answer is allowed %d tokens but the model's context window is %d tokens: "+
			"the answer does not fit inside the window", e.Reserved, e.Total)
}

// BudgetFor turns a resolved context record into the budget the
// application applies before a request.
//
// The record's ContextWindow is the TOTAL context the model accepts. It
// is never an input allowance: the room for the answer is reserved from
// it first, and what is left is the room the request may use. The same
// interpretation applies to every source, so an operator-configured
// limit, a provider's advertisement, a local runtime's allocation and a
// record verified earlier are all measured the same way.
//
// allowance is the room the answer may use:
//
//   - a configured allowance that does not fit inside the window is a
//     contradiction and is refused with a clear error;
//   - a default is reduced conservatively, so that a window always
//     leaves room for the request.
//
// The reservation that comes out of this is the EFFECTIVE one: it is
// the number that goes on the wire, so the budget and the request that
// leaves the machine can never disagree.
//
// A window that is known never yields a zero or a negative budget: a
// zero budget would be read as "no budget at all", and a negative one
// would be a number nobody meant. A window that is not known at all
// leaves the provider's own limit in force, and the budget says so.
func BudgetFor(resolved ContextMetadata, allowance OutputAllowance) (ContextBudget, error) {
	budget := ContextBudget{
		Source:   resolved.Source,
		Mode:     resolved.Mode,
		Verified: resolved.Verified,
		Note:     resolved.Note,
	}
	reserved := allowance.Tokens
	if reserved <= 0 {
		reserved = DefaultMaxTokens
	}
	if resolved.Source == ContextSourceProvisional && !allowance.Configured {
		// The provisional record carries its own output cap. It is
		// the only number about this model that HAL has, and it is
		// small on purpose: the provisional window is small too, and
		// an answer that takes half of it leaves nothing for the
		// request.
		//
		// An allowance the operator configured is never replaced
		// here: their number is the one that counts.
		if provisional := resolved.MaxOutputTokens; provisional > 0 && provisional < reserved {
			reserved = provisional
		}
	}
	if allowance.Minimum > reserved {
		// The protocol refuses a smaller cap. The room for the answer
		// grows to fit it.
		reserved = allowance.Minimum
	}
	total := resolved.ContextWindow
	if total <= 0 {
		// Nothing is known about the window: the provider's own limit
		// governs and nothing is trimmed.
		budget.Limit = 0
		budget.ReservedOutput = reserved
		return budget, nil
	}
	if reserved >= total {
		if allowance.Configured {
			// Two numbers the operator wrote down contradict each
			// other. The operator is told; nothing is guessed.
			return budget, &ContextAllowanceError{Total: total, Reserved: reserved}
		}
		// A default that does not fit is reduced conservatively: a
		// window that leaves no room for the request would refuse
		// every turn.
		reserved = total / 2
		if reserved <= 0 {
			reserved = 1
		}
	}
	budget.Limit = total - reserved
	budget.Total = total
	budget.ReservedOutput = reserved
	if budget.Limit <= 0 {
		// A budget of zero would read as "no budget at all", and that
		// is the opposite of what a known window says.
		return budget, &ContextAllowanceError{Total: total, Reserved: reserved}
	}
	return budget, nil
}

// DefaultOutputTokens returns the answer cap an adapter uses when the
// model sets none, for the given wire protocol.
func DefaultOutputTokens(wire string) int {
	if wire == WireAnthropic {
		return DefaultAnthropicMaxTokens
	}
	return DefaultMaxTokens
}

// TrimReport says what the trim did.
type TrimReport struct {
	// DroppedExchanges counts the exchanges that were dropped.
	DroppedExchanges int
	// EstimatedTokens is the estimated size of the request that is left.
	EstimatedTokens int
	// Limit is the room the request had to fit into.
	Limit int
}

// ContextBudgetError reports that a request cannot be reduced to the
// budget.
//
// It is a typed error so the interface can tell it apart from a
// provider failure: nothing went wrong at the provider, the request
// simply does not fit the budget that applies to it.
type ContextBudgetError struct {
	// Estimated is the estimated size of the irreducible request.
	Estimated int
	// Limit is the room the request had to fit into.
	Limit int
	// Reason explains why the request could not be reduced.
	Reason string
}

// Error implements the error interface.
func (e *ContextBudgetError) Error() string {
	return fmt.Sprintf(
		"the request needs about %d tokens (estimated) but only %d are available in this model's context budget; %s",
		e.Estimated, e.Limit, e.Reason)
}

// EstimateSafetyMargin is the factor the raw character-based estimate
// is multiplied by.
//
// The raw estimate is four characters per token plus a small overhead,
// and it undercounts. A normal HAL request measured about 5086 real
// tokens against 4115 estimated — a factor of 1.24 — and dense code
// and non-English text undercount by more.
//
// The margin makes the estimate conservative, so a request HAL believes
// fits has room for a tokenizer that counts differently. It is a
// bounded, documented guess and not a tokenizer: HAL still does not
// claim that a request fits every provider's tokenizer, and every
// message that quotes a token count calls it an estimate.
const EstimateSafetyMargin = 1.5

// EstimateTokens approximates the size of a request in tokens.
//
// The estimate is four characters per token plus a small overhead per
// message, per tool call and per tool definition, multiplied by
// EstimateSafetyMargin. It is a rough number on purpose: HAL never
// claims to know the provider's tokenizer, so every message that quotes
// a token count calls it an estimate.
//
// The margin is what makes the number safe to budget with. Without it
// the estimate is smaller than the request that leaves the machine, and
// a budget fitted to it would be fitted to a number that is too small.
func EstimateTokens(messages []Message, tools []Tool) int {
	raw := rawEstimateTokens(messages, tools)
	if raw <= 0 {
		return 0
	}
	// The margin is rounded up: a budget fitted to a rounded-down
	// estimate would be fitted to a number that is too small.
	return (raw*int(EstimateSafetyMargin*100) + 99) / 100
}

// rawEstimateTokens is the character-based estimate before the safety
// margin is applied.
func rawEstimateTokens(messages []Message, tools []Tool) int {
	total := 0
	for _, m := range messages {
		total += len(m.Content)/4 + 4
		for _, call := range m.ToolCalls {
			total += len(call.Name)/4 + len(call.ArgumentsJSON())/4 + 4
		}
	}
	for _, t := range tools {
		total += len(t.Name)/4 + len(t.Description)/4 + len(t.Parameters)/4 + 8
	}
	return total
}

// OutputCap returns the cap that goes on the wire for a model.
//
// The budget's reservation is the effective cap: it is the room the
// answer was given, measured against the window the request was
// trimmed for. What the budget says the answer may use is what the
// provider is asked for, so the two can never disagree.
//
// Without a budget in force the model's own cap is used, and without
// that the protocol's default.
func OutputCap(model Model, fallback int) int {
	if model.Budget.Limit > 0 && model.Budget.ReservedOutput > 0 {
		return model.Budget.ReservedOutput
	}
	if model.MaxTokens > 0 {
		return model.MaxTokens
	}
	return fallback
}

// TrimToBudget drops the oldest exchanges until the request fits the
// budget.
//
// The messages are the fully assembled request: the safety
// instructions, the conversation history and the current exchange. The
// tools are the definitions that travel with every request.
//
// The rules are the ones the package documents: the safety instructions
// and the newest exchange are kept, older context is dropped only at
// exchange boundaries, and a request that still does not fit is refused
// with an explanation rather than truncated.
func TrimToBudget(messages []Message, tools []Tool, budget ContextBudget) ([]Message, TrimReport, error) {
	report := TrimReport{
		EstimatedTokens: EstimateTokens(messages, tools),
		Limit:           budget.Limit,
	}
	if budget.Limit <= 0 {
		// No budget: the provider's own window governs.
		return messages, report, nil
	}
	if report.EstimatedTokens <= report.Limit {
		return messages, report, nil
	}
	protected, exchanges := splitExchanges(messages)
	if len(exchanges) == 0 {
		// There is nothing that may be dropped.
		return nil, report, &ContextBudgetError{
			Estimated: report.EstimatedTokens,
			Limit:     report.Limit,
			Reason:    "the request holds nothing but the instructions that must never be dropped",
		}
	}
	kept := exchanges
	for len(kept) > 1 && report.EstimatedTokens > report.Limit {
		kept = kept[1:]
		report.DroppedExchanges++
		report.EstimatedTokens = EstimateTokens(joinMessages(protected, kept), tools)
	}
	if report.EstimatedTokens > report.Limit {
		return nil, report, &ContextBudgetError{
			Estimated: report.EstimatedTokens,
			Limit:     report.Limit,
			Reason: "the safety instructions, the tool definitions and the current request do not fit; " +
				"they are never dropped to satisfy a budget",
		}
	}
	return joinMessages(protected, kept), report, nil
}

// splitExchanges separates the messages that are never dropped from
// the conversation, and cuts the conversation into exchanges.
//
// An exchange is a user message together with the assistant and tool
// messages that answer it. Dropping an exchange therefore never
// separates a tool call from its result.
//
// A system message is protected: it is never dropped. The engine
// assembles the safety instructions as the leading message, and the
// protected messages are put back in front of the kept exchanges, so
// the instructions keep their place at the head of the request.
func splitExchanges(messages []Message) ([]Message, [][]Message) {
	var protected []Message
	var exchanges [][]Message
	for _, m := range messages {
		if m.Role == "system" {
			// The safety instructions are never dropped.
			protected = append(protected, m)
			continue
		}
		if m.Role == "user" || len(exchanges) == 0 {
			exchanges = append(exchanges, []Message{m})
			continue
		}
		exchanges[len(exchanges)-1] = append(exchanges[len(exchanges)-1], m)
	}
	return protected, exchanges
}

// joinMessages assembles the protected messages and the kept exchanges
// back into one request.
//
// The order of the exchanges is the order they were spoken in, and the
// protected messages come first.
func joinMessages(protected []Message, exchanges [][]Message) []Message {
	out := make([]Message, 0, len(protected))
	out = append(out, protected...)
	for _, exchange := range exchanges {
		out = append(out, exchange...)
	}
	return out
}

// TrimNote is the note the interface shows when context was dropped.
type TrimNote struct {
	// DroppedExchanges counts the exchanges that were dropped.
	DroppedExchanges int
	// Limit is the budget the request was fitted into.
	Limit int
	// Source says where the budget came from.
	Source string
	// Text is the sentence the interface shows.
	Text string
}

// Note builds the note for a trim.
func (r TrimReport) Note(budget ContextBudget) TrimNote {
	return TrimNote{
		DroppedExchanges: r.DroppedExchanges,
		Limit:            budget.Limit,
		Source:           string(budget.Source),
		Text:             DroppedExchangesNote(r, budget),
	}
}

// DroppedExchangesNote explains a trim to the operator.
//
// It names the number as a count of exchanges and never claims a token
// count it cannot know.
func DroppedExchangesNote(report TrimReport, budget ContextBudget) string {
	if report.DroppedExchanges == 0 {
		return ""
	}
	source := string(budget.Source)
	if source == "" {
		source = string(ContextSourceProvisional)
	}
	return fmt.Sprintf(
		"%d older %s dropped to fit the %d-token context budget (%s)",
		report.DroppedExchanges, pluralExchanges(report.DroppedExchanges), budget.Limit, source)
}

// pluralExchanges names the dropped exchanges.
func pluralExchanges(n int) string {
	if n == 1 {
		return "exchange"
	}
	return "exchanges"
}

// DescribeBudget renders the budget for the diagnostics panel.
//
// It never claims an exact token count: the numbers it quotes are the
// budget and its provenance, not a measurement of the request.
//
// The operator sees the whole picture: the total the window holds, the
// room held back for the answer, the room the request may use and where
// the numbers came from.
func DescribeBudget(budget ContextBudget) string {
	parts := []string{}
	if budget.Total > 0 {
		parts = append(parts, fmt.Sprintf("%d tokens in all", budget.Total))
	}
	if budget.Limit > 0 {
		parts = append(parts, fmt.Sprintf("%d tokens for the request", budget.Limit))
	} else {
		parts = append(parts, "the provider's own context window governs")
	}
	switch budget.Source {
	case ContextSourceOperator:
		parts = append(parts, "set by the operator")
	case ContextSourceProvider:
		parts = append(parts, "advertised by the provider")
	case ContextSourceLocal:
		parts = append(parts, "allocated by the local runtime")
	case ContextSourceCached:
		parts = append(parts, "verified earlier")
	case ContextSourceProvisional:
		parts = append(parts, "provisional: the endpoint reported no context window, "+
			"so a conservative default applies")
	default:
		parts = append(parts, "unknown source")
	}
	if budget.ReservedOutput > 0 {
		parts = append(parts, fmt.Sprintf("%d reserved for the answer", budget.ReservedOutput))
	}
	return strings.Join(parts, " · ")
}
