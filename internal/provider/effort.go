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

// EffortCapable reports whether reasoning_effort may be sent to this
// model.
//
// Only the MiniMax M-series documents the parameter. Other
// OpenAI-compatible providers may reject an unknown field or interpret
// it differently, so the parameter is never sent blindly: the adapter
// owns this table, and everything outside it gets no effort control at
// all.
func EffortCapable(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	return strings.HasPrefix(id, "minimax-m")
}

// DefaultEffortFor returns the level a model starts at: DefaultEffort
// for capable models, empty for everything else.
func DefaultEffortFor(modelID string) string {
	if !EffortCapable(modelID) {
		return ""
	}
	return DefaultEffort
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
