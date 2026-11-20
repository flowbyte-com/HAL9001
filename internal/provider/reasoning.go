package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Reasoning continuity.
//
// Some providers require their own reasoning to be replayed when the
// conversation continues. DeepSeek's Thinking Mode requires the
// reasoning_content of a prior assistant turn to be sent back with
// every later request that carries tools. The Anthropic Messages API
// requires the complete, unmodified thinking blocks — signatures
// included — of an assistant turn to be sent back when a tool-use turn
// continues.
//
// The reasoning is therefore not simply dropped when the turn ends. It
// is kept as what it is: the provider's own blocks, in the provider's
// own order, with the provider's own signature.
//
// Three things stay apart. The conversation content is what the
// operator and the model read. The continuity data is what the provider
// needs and nobody reads. The interface's presentation of the
// reasoning is what the operator chose to look at.

// ReasoningBlock is one provider reasoning block, kept exactly as the
// provider sent it.
//
// The block is the unit of continuity: a provider that requires its own
// reasoning replayed must receive the same blocks, in the same order,
// with the same boundaries.
//
// The signature is the provider's own. It is never regenerated,
// truncated or re-derived: a provider that verifies it refuses a block
// whose signature was invented.
type ReasoningBlock struct {
	// Type names the block: "thinking", "redacted_thinking" or
	// "reasoning".
	Type string `json:"type"`
	// Text is the reasoning text of the block.
	Text string `json:"text,omitempty"`
	// Signature is the provider's signature for the block. It is empty
	// for a provider that does not sign its reasoning.
	Signature string `json:"signature,omitempty"`
	// Data is the opaque payload of a redacted block: the provider
	// withheld the reasoning and returned this instead.
	Data string `json:"data,omitempty"`
}

// The block types HAL knows.
const (
	// ReasoningThinking is a reasoning block the provider shows.
	ReasoningThinking = "thinking"
	// ReasoningRedacted is a reasoning block the provider withheld.
	ReasoningRedacted = "redacted_thinking"
	// ReasoningPlain is a plain reasoning field, the shape the
	// OpenAI-compatible dialect uses.
	ReasoningPlain = "reasoning"
)

// ReasoningKey names the continuity a model needs: the wire and the
// family, together.
//
// It is empty when the model needs none. The reasoning levels MiniMax
// and DeepSeek send are stateless, and a provider that requires its
// own reasoning replayed says so here.
//
// Two requests may only exchange continuity data when their keys
// match. A DeepSeek reasoning field means nothing to the Messages API,
// and a signed thinking block means nothing to the OpenAI dialect, so
// data from one family is never sent to another.
//
// A family that speaks the same wire is not enough either: DeepSeek's
// Anthropic-compatible endpoint keeps its own continuity, separate from
// Claude's on the same wire and from DeepSeek's on the OpenAI dialect.
func ReasoningKey(modelID, wire string) string {
	family := matchEffortFamily(modelID)
	if family == nil {
		return ""
	}
	switch {
	case wire == WireAnthropic && family.anthropicWire:
		return "anthropic/thinking"
	case wire == WireAnthropic && family.prefix == "deepseek":
		// DeepSeek's Anthropic-compatible endpoint keeps continuity of
		// its own: its thinking blocks are not Claude's, and they are
		// not the reasoning_content of the OpenAI dialect either. The
		// key keeps the three apart, so no block of one is ever
		// replayed as a block of another.
		return "anthropic/deepseek-thinking"
	case wire == WireOpenAI && family.prefix == "deepseek":
		return "openai/reasoning-content"
	}
	return ""
}

// The thinking a request asked for.
//
// The three states are distinct, and the difference between them is
// what decides whether the reasoning of an earlier turn is replayed.
//
// A request that asks for thinking is in thinking mode. A request that
// forbids thinking is not, whatever came before. A request that says
// nothing leaves the question to the provider: an endpoint that thinks
// by default thinks anyway, and an endpoint that thinks only when asked
// does not.
//
// The state travels with the blocks it belongs to, because the decision
// a later request has to make depends on how the request that produced
// them asked for thinking — and on nothing else.
//
// The state of a disabled request is reachable only from the API that
// takes it: the Messages API understands `thinking: {"type":
// "disabled"}`, and HAL honours the state when it reads one, but HAL
// sends no such request of its own and no operator control produces
// one. The state stays in the contract because the contract is about
// what the wire can say, and because "forbidden" and "not asked for" are
// different facts: a provider refuses an unexpected thinking block, and
// it reads "forbidden" and "not asked for" differently.
const (
	// ThinkingUnset: the request says nothing about thinking.
	ThinkingUnset = "unset"
	// ThinkingAsked: the request asks for thinking.
	ThinkingAsked = "asked"
	// ThinkingOff: the request forbids thinking.
	ThinkingOff = "off"
)

// reasoningPayload is the stored form of continuity data.
type reasoningPayload struct {
	// Key names the protocol the blocks belong to.
	Key string `json:"key"`
	// Model names the model that produced them.
	Model string `json:"model,omitempty"`
	// Thinking says how the request that produced the blocks asked for
	// thinking: ThinkingAsked, ThinkingUnset or ThinkingOff.
	//
	// It is empty for a record written before the field existed. Such a
	// record says nothing about how its thinking came about, and the
	// decision that follows from it is the careful one.
	Thinking string `json:"thinking,omitempty"`
	// Origin is the fingerprint of the endpoint that produced the
	// blocks.
	//
	// It is empty for a record written before the field existed. Such a
	// record cannot be sent to any endpoint: the reasoning belongs to
	// the provider that produced it, and nothing says which one that
	// was.
	Origin string `json:"origin,omitempty"`
	// Blocks are the provider's blocks, in the provider's order.
	Blocks []ReasoningBlock `json:"blocks,omitempty"`
}

// Continuity is the continuity data of one message, as it is stored.
//
// The record is provider-owned: only the provider layer reads it, and
// only the provider layer writes it.
type Continuity struct {
	// Key names the protocol the blocks belong to.
	Key string
	// Model names the model that produced them.
	Model string
	// Thinking says how the request that produced the blocks asked for
	// thinking.
	//
	// It is empty for a record written before the field existed.
	Thinking string
	// Origin is the fingerprint of the endpoint that produced the
	// blocks.
	//
	// It is empty for a record written before the field existed.
	Origin string
	// Blocks are the provider's blocks, in the provider's order.
	Blocks []ReasoningBlock
}

// EncodeReasoning serialises continuity data for the conversation
// record.
//
// The record never mentions a provider: the payload is opaque to
// everything but the provider layer, which is what wrote it.
//
// The thinking of the producing request travels with the blocks: a later
// request has to know how the request that produced them asked for
// thinking, and that is the one fact it cannot recover on its own. So
// does the origin of the endpoint that produced them: the reasoning
// belongs to that endpoint, and a later request has to know that before
// it sends anything.
//
// A payload with no key or no blocks is nothing: an empty result means
// there is no continuity to keep.
func EncodeReasoning(continuity Continuity) []byte {
	if continuity.Key == "" || len(continuity.Blocks) == 0 {
		return nil
	}
	raw, err := json.Marshal(reasoningPayload{
		Key:      continuity.Key,
		Model:    continuity.Model,
		Thinking: continuity.Thinking,
		Origin:   continuity.Origin,
		Blocks:   continuity.Blocks,
	})
	if err != nil {
		return nil
	}
	return raw
}

// EncodeReasoningKey serialises the name of a protocol without its
// data.
//
// It is what is left when the data was too large to keep: the name
// survives, so the request that would need the data can be refused
// rather than sent without it.
func EncodeReasoningKey(key string) []byte {
	if key == "" {
		return nil
	}
	raw, err := json.Marshal(reasoningPayload{Key: key})
	if err != nil {
		return nil
	}
	return raw
}

// DecodeReasoning reads stored continuity data.
//
// It reports false for a payload it does not recognise: a malformed
// payload and one with no key are both dropped, never guessed at.
//
// The blocks are empty when the record kept the key but not the data:
// the data was too large to store, and a request that needs it must
// not be sent.
//
// The thinking of the producing request and the origin of the endpoint
// that produced it are empty for a record written before the fields
// existed: nothing is known about them, and nothing is guessed.
func DecodeReasoning(payload []byte) (Continuity, bool) {
	if len(payload) == 0 {
		return Continuity{}, false
	}
	var p reasoningPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return Continuity{}, false
	}
	if p.Key == "" {
		return Continuity{}, false
	}
	return Continuity{
		Key:      p.Key,
		Model:    p.Model,
		Thinking: p.Thinking,
		Origin:   p.Origin,
		Blocks:   p.Blocks,
	}, true
}

// ReasoningText joins the text of a set of blocks.
//
// It is the plain reasoning field the OpenAI-compatible dialect uses:
// one string, the concatenation of what the provider sent.
func ReasoningText(blocks []ReasoningBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		b.WriteString(block.Text)
	}
	return b.String()
}

// replaysThinking reports whether a request in this thinking mode
// replays the reasoning of an earlier turn.
//
// The three states of the request are apart on purpose. A request that
// forbids thinking replays nothing, whatever came before: the provider
// refuses an unexpected block. A request that asks for thinking
// replays everything of its own protocol: the provider refuses a
// continued thinking turn whose blocks are missing.
//
// A request that says nothing is the difficult one, and the answer is
// not HAL's to guess. It is the endpoint's own behaviour: an endpoint
// that thinks by default is in thinking mode whatever the request says,
// and an endpoint that thinks only when asked is not. An endpoint HAL
// knows nothing about is decided by the turn itself: a turn that asked
// for nothing and came back with thinking says that this provider
// thinks by default.
//
// An omitted reasoning configuration is never read as "thinking is off":
// that is the provider's decision, and only the provider's own answer
// settles it.
func replaysThinking(mode string, endpoint ThinkingDefault, turnThinking string) bool {
	switch mode {
	case ThinkingOff:
		return false
	case ThinkingAsked:
		return true
	default:
		switch endpoint {
		case ThinkingDefaultOn:
			return true
		case ThinkingDefaultOff:
			return false
		default:
			// The endpoint is not known: the turn that produced the
			// blocks says whether this provider thinks by default.
			return turnThinking == ThinkingUnset
		}
	}
}

// checkOrigin refuses a conversation whose reasoning belongs to
// another endpoint.
//
// The reasoning of a turn belongs to the provider that produced it.
// Sending it to another provider would hand one provider the private
// reasoning of another, so the request is refused here, before
// anything goes out.
//
// The check is on the turn, not on the protocol: a record of another
// endpoint is refused whatever its protocol is, because the protocol
// says how the reasoning is written and the origin says whose it is.
// A record that names no origin is refused too: nothing is invented
// for it.
func checkOrigin(messages []Message, origin Origin, endpoint string) error {
	for _, m := range messages {
		if len(m.Reasoning) == 0 {
			continue
		}
		if m.Origin != string(origin) {
			return OriginRefusal(origin, endpoint, Origin(m.Origin))
		}
	}
	return nil
}

// OriginRefusal explains why the reasoning of an earlier turn is not
// sent to this endpoint.
//
// The reasoning belongs to the provider that produced it. Sending it
// to another provider would hand one provider the private reasoning of
// another, so the request is refused here, before anything goes out.
//
// The diagnostic names the endpoint and the two origins. The origins
// are fingerprints: they name the endpoints without naming a
// credential, a key or an address, and they are what an operator
// compares to see which endpoint the reasoning came from.
func OriginRefusal(origin Origin, endpoint string, conversation Origin) error {
	if conversation == "" {
		return fmt.Errorf(
			"provider: this conversation's reasoning has no endpoint identity, "+
				"so it cannot be sent to %s safely; start a new conversation, "+
				"or continue with the endpoint the reasoning came from", endpoint)
	}
	return fmt.Errorf(
		"provider: the provider origin changed: this conversation's reasoning came from "+
			"another endpoint (origin %s) and is not sent to %s (origin %s); "+
			"start a new conversation, or switch back to the endpoint the reasoning came from",
		conversation, endpoint, origin)
}

// openAIReasoningBlocks is the continuity data of a plain reasoning
// field.
//
// The dialect sends one string, so the turn is one block: the text
// exactly as the provider sent it, which is what a replay has to
// carry back.
func openAIReasoningBlocks(text string) []ReasoningBlock {
	if text == "" {
		return nil
	}
	return []ReasoningBlock{{Type: ReasoningPlain, Text: text}}
}
