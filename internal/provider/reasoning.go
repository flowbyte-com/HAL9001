package provider

import (
	"encoding/json"
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
func ReasoningKey(modelID, wire string) string {
	family := matchEffortFamily(modelID)
	if family == nil {
		return ""
	}
	switch {
	case wire == WireAnthropic && family.anthropicWire:
		return "anthropic/thinking"
	case wire == WireOpenAI && family.prefix == "deepseek":
		return "openai/reasoning-content"
	}
	return ""
}

// reasoningPayload is the stored form of continuity data.
type reasoningPayload struct {
	// Key names the protocol the blocks belong to.
	Key string `json:"key"`
	// Model names the model that produced them.
	Model string `json:"model,omitempty"`
	// Blocks are the provider's blocks, in the provider's order.
	Blocks []ReasoningBlock `json:"blocks,omitempty"`
}

// EncodeReasoning serialises continuity data for the conversation
// record.
//
// The record never mentions a provider: the payload is opaque to
// everything but the provider layer, which is what wrote it.
//
// A payload with no key or no blocks is nothing: an empty result means
// there is no continuity to keep.
func EncodeReasoning(key, model string, blocks []ReasoningBlock) []byte {
	if key == "" || len(blocks) == 0 {
		return nil
	}
	raw, err := json.Marshal(reasoningPayload{Key: key, Model: model, Blocks: blocks})
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
func DecodeReasoning(payload []byte) (key, model string, blocks []ReasoningBlock, ok bool) {
	if len(payload) == 0 {
		return "", "", nil, false
	}
	var p reasoningPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", "", nil, false
	}
	if p.Key == "" {
		return "", "", nil, false
	}
	return p.Key, p.Model, p.Blocks, true
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
