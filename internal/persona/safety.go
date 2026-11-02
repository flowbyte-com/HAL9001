package persona

import "strings"

// safetyFinishReasons are provider finish reasons that unambiguously mean
// "refused for safety", normalized to lowercase with separators removed.
// The list is deliberately narrow: an ordinary error, an empty response,
// or a model merely declining in prose must never trigger the refusal.
var safetyFinishReasons = map[string]struct{}{
	"contentfilter":       {},
	"contentfiltered":     {},
	"safety":              {},
	"responsibleaipolicy": {},
}

func normalizeFinishReason(reason string) string {
	lowered := strings.ToLower(strings.TrimSpace(reason))
	lowered = strings.ReplaceAll(lowered, "_", "")
	lowered = strings.ReplaceAll(lowered, "-", "")
	lowered = strings.ReplaceAll(lowered, " ", "")
	return lowered
}

// safetyErrorSubstrings are matched (case-insensitively) against transport
// errors only. They name provider-side safety blocks, not model opinions,
// and not generic failures.
var safetyErrorSubstrings = []string{
	"content filter",
	"content-filtered",
	"flagged by moderation",
	"moderation blocked",
	"safety system",
	"blocked by safety",
	"responsible ai",
	"harmful content",
}

// IsSafetyRefusal reports whether an outcome is a genuine provider safety
// refusal as opposed to an error, an empty response, uncertainty,
// disagreement, or any other failure. Conservative by design: unknown
// signals return false. Only explicit provider signals count — never
// broad refusal-text detectors.
func IsSafetyRefusal(finishReason string, err error) bool {
	if _, ok := safetyFinishReasons[normalizeFinishReason(finishReason)]; ok {
		return true
	}
	if err == nil {
		return false
	}
	lowered := strings.ToLower(err.Error())
	for _, marker := range safetyErrorSubstrings {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}
