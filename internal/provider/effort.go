package provider

import "strings"

// EffortLevels are the documented reasoning levels, ordered from least to
// most effort. MiniMax documents all five for its reasoning models.
var EffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// DefaultEffort is the level HAL sends when the operator has not chosen
// one. MiniMax-M3.1-Flash defaults to "max" when reasoning_effort is
// omitted, so sending "medium" explicitly is what makes HAL start at a
// moderate effort instead of the most expensive level.
const DefaultEffort = "medium"

// ValidEffort reports whether level is one of the documented levels.
func ValidEffort(level string) bool {
	level = strings.ToLower(strings.TrimSpace(level))
	for _, l := range EffortLevels {
		if l == level {
			return true
		}
	}
	return false
}

// effortFamily is one model family with documented reasoning control:
// the identifier that names it, the levels HAL may offer for it and the
// level it starts at.
type effortFamily struct {
	// prefix names the family. It matches at an identifier boundary so
	// "deepseek" never claims "deepseeking" and "minimax-m" never
	// claims "minimax-music".
	prefix string
	// names are the model identifiers the family documents, when the
	// family is not a prefix.
	//
	// A family with names claims those names and the ones that start
	// the same way: "deepseek-flash" claims "deepseek-flash-preview",
	// and a model the family never documented — an embedding model,
	// say — is not claimed at all.
	names  []string
	levels []string
	def    string
	// openAIWire reports whether this family's OpenAI-compatible
	// documentation names the reasoning_effort parameter. A family that
	// does not gets no effort control on that wire, even when it takes
	// one on another: the wire decides what may be sent.
	openAIWire bool
	// anthropicWire reports whether this family's reasoning control on
	// the Messages wire is the extended-thinking block.
	anthropicWire bool
}

// effortFamilies are the models whose reasoning controls HAL documents.
// Anything else is unknown to HAL: the adapter sends no effort control
// for it, and the interface says the capability is unknown instead of
// claiming the model has none.
//
// MiniMax documents all five levels for its M-series reasoning models.
// DeepSeek documents low, high and max with a high default, and maps
// the other HAL levels onto them on the wire (see MapDeepSeekEffort).
//
// The DeepSeek family names its models instead of claiming a prefix:
// the provider's catalogue holds models that do not reason — an
// embedding model, for one — and a family that claimed every name that
// started the same way would offer a level for a model that has none.
var effortFamilies = []effortFamily{
	{prefix: "minimax-m", levels: []string{"low", "medium", "high", "xhigh", "max"}, def: "medium", openAIWire: true},
	{prefix: "deepseek", names: []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v3.2", "deepseek-reasoner"},
		levels: []string{"low", "high", "max"}, def: "high", openAIWire: true},
}

// anthropicEffortFamily is the Anthropic Messages family: the models
// whose reasoning control is the extended-thinking block.
//
// Its membership is the adapter's own table (anthropicThinkingCapable)
// rather than a prefix, because which models take a thinking block is
// the Messages API's own fact and not HAL's guess.
//
// It starts at no level at all: a Claude model without a configured
// level sends no thinking block, exactly as it did before HAL could
// configure one. The operator's own level is what turns thinking on.
var anthropicEffortFamily = effortFamily{
	levels:        []string{"low", "medium", "high", "xhigh", "max"},
	def:           "",
	anthropicWire: true,
}

// matchEffortFamily returns the family a model identifier belongs to, if
// any. The match is at an identifier boundary: the character after the
// prefix must not be a letter, so a family never claims a longer name
// that merely starts the same way.
func matchEffortFamily(modelID string) *effortFamily {
	id := strings.ToLower(strings.TrimSpace(modelID))
	for i := range effortFamilies {
		if effortFamilies[i].claims(id) {
			return &effortFamilies[i]
		}
	}
	if anthropicThinkingCapable(id) {
		return &anthropicEffortFamily
	}
	return nil
}

// claims reports whether the identifier belongs to the family.
//
// A family with names claims only those names: a model the provider
// never documented as a reasoning model is not claimed, even when its
// identifier starts the same way.
func (f *effortFamily) claims(id string) bool {
	if len(f.names) > 0 {
		for _, name := range f.names {
			if matchesModelName(id, name) {
				return true
			}
		}
		return false
	}
	return matchesModelPrefix(id, f.prefix)
}

// matchesModelPrefix reports whether the identifier is the prefix, or
// starts with it at a boundary.
//
// The character after the prefix must not be a letter, so "minimax-m"
// never claims "minimax-music".
func matchesModelPrefix(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	rest := id[len(prefix):]
	return rest == "" || rest[0] < 'a' || rest[0] > 'z'
}

// matchesModelName reports whether the identifier is the name, or
// starts with it at a boundary.
//
// A name is stricter than a prefix: the character after it must not be
// a letter and not a digit, so "deepseek-v3.2" does not claim
// "deepseek-v3.20".
func matchesModelName(id, name string) bool {
	if !strings.HasPrefix(id, name) {
		return false
	}
	rest := id[len(name):]
	if rest == "" {
		return true
	}
	c := rest[0]
	return !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9')
}

// noEffortPrefixes names model families explicitly known to have no
// reasoning control: established non-reasoning chat models whose
// providers document no reasoning parameter for them. Anything outside
// both this list and the capability table is unknown to HAL, not
// unsupported by the model: the interface reports the difference.
//
// A Claude model is not in this list: it has a reasoning control, it is
// the extended-thinking block rather than an effort parameter, and the
// Messages adapter maps a level onto it. Which of its models take one
// is the adapter's own table.
var noEffortPrefixes = []string{"gpt-4o", "gpt-4", "gpt-3.5", "llama-"}

// EffortSupport says what HAL knows about a model's reasoning control.
type EffortSupport int

const (
	// EffortUnknown means HAL has no knowledge of this model's
	// reasoning control: neither documented support nor documented
	// absence. The interface says so instead of claiming the model
	// has none.
	EffortUnknown EffortSupport = iota
	// EffortUnsupported means the model is explicitly known to take no
	// reasoning control.
	EffortUnsupported
	// EffortSupported means the model takes a reasoning control with
	// configurable levels.
	EffortSupported
)

// EffortSupportFor reports what HAL knows about a model's reasoning
// control, without consulting any endpoint: the table is documentation,
// not discovery, so a manually added model works the same as a
// discovered one.
func EffortSupportFor(modelID string) EffortSupport {
	if matchEffortFamily(modelID) != nil {
		return EffortSupported
	}
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return EffortUnsupported
	}
	for _, prefix := range noEffortPrefixes {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		rest := id[len(prefix):]
		if rest == "" || rest[0] < 'a' || rest[0] > 'z' {
			return EffortUnsupported
		}
	}
	// A Claude model from before the extended-thinking block is known
	// to take none: the API documents that the block arrived with
	// Claude 3.7.
	if anthropicNoThinking(id) {
		return EffortUnsupported
	}
	return EffortUnknown
}

// SupportedEffortLevels returns the levels HAL offers for a model, and
// whether the model is a supported one. Unknown and unsupported models
// yield no levels.
//
// The levels are the ones the model's own family documents: offering a
// level the provider does not have would be a guess it may reject.
func SupportedEffortLevels(modelID string) ([]string, bool) {
	family := matchEffortFamily(modelID)
	if family == nil {
		return nil, false
	}
	return append([]string(nil), family.levels...), true
}

// EffortCapable reports whether a reasoning effort may be sent to this
// model.
//
// Only models whose parameter HAL documents count: the adapter never
// sends an effort control blindly, and everything outside the table
// gets no effort control at all.
func EffortCapable(modelID string) bool {
	return EffortSupportFor(modelID) == EffortSupported
}

// DefaultEffortFor returns the level a model starts at: its documented
// default when supported, empty for everything else.
//
// An empty default is a fact about the model, not a gap: a Claude model
// starts with no extended thinking, and a model HAL knows nothing about
// starts with no effort control at all.
func DefaultEffortFor(modelID string) string {
	family := matchEffortFamily(modelID)
	if family == nil {
		return ""
	}
	return family.def
}

// NormalizeEffort lower-cases and trims a level, returning an empty
// string when it is not documented.
func NormalizeEffort(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	if ValidEffort(level) {
		return level
	}
	return ""
}

// MapDeepSeekEffort maps a HAL level onto the effort DeepSeek documents
// (low, high, max, default high). The mapping follows DeepSeek's own
// compatibility table: medium and xhigh ride at high, and anything
// outside the documented levels is dropped rather than forwarded.
func MapDeepSeekEffort(level string) (string, bool) {
	switch NormalizeEffort(level) {
	case "low":
		return "low", true
	case "medium", "high", "xhigh":
		return "high", true
	case "max":
		return "max", true
	}
	return "", false
}

// EffectiveEffort reports the level that reaches the provider.
//
// The level HAL stores and the level the provider receives are the same
// for a family that takes the level as written, and the same after the
// mapping for a family that documents fewer levels.
//
// The interface shows what the provider will get. A footer that names
// one level while the wire carries another is a disagreement the
// operator cannot see through, and a stored level that never existed on
// the wire is a setting that lies about what it did.
func EffectiveEffort(modelID, level string) string {
	normalized := NormalizeEffort(level)
	if normalized == "" {
		return ""
	}
	if wire, ok := OpenAIEffortFor(modelID, normalized); ok {
		return wire
	}
	return normalized
}

// OpenAIEffortFor maps a configured level onto the wire value for the
// OpenAI-compatible dialect, reporting whether anything may be sent.
// MiniMax takes the level as written; DeepSeek takes the mapped value.
// Anything else sends nothing.
func OpenAIEffortFor(modelID, level string) (string, bool) {
	family := matchEffortFamily(modelID)
	if family == nil || !family.openAIWire {
		return "", false
	}
	if family.prefix == "deepseek" {
		return MapDeepSeekEffort(level)
	}
	if out := NormalizeEffort(level); out != "" {
		return out, true
	}
	return "", false
}

// isDeepSeekModel reports whether an identifier belongs to the DeepSeek
// family, for adapters that shape DeepSeek-specific parameters.
func isDeepSeekModel(modelID string) bool {
	family := matchEffortFamily(modelID)
	return family != nil && family.prefix == "deepseek"
}
