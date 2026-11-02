// Package capability enforces HAL9001's 444 contract: read-only authority,
// permanently. There are no coding profiles, execution modes, or switches
// that widen it. Anything not explicitly allowed is refused by default-deny,
// including unknown future tools that no denylist could name.
package capability

import "strings"

// Allowed capabilities, grouped by purpose. These are the only operations
// HAL may perform. Everything else — shell execution, file mutation,
// deployment, orchestration, capability changes — is outside the boundary.
var allowed = []string{
	// Conversation.
	"converse",
	// Read-only investigation.
	"web_search",
	"web_fetch",
	"read_file",
	"find_skills",
	// Prompt creation for specialist agents.
	"draft_prompt",
	// Evidence and implementation-report review.
	"review_prompt",
	"review_report",
}

// Allowed reports whether name is an explicitly allowed capability.
// Matching is case- and whitespace-insensitive. Unknown names are denied.
func Allowed(name string) bool {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, a := range allowed {
		if want == a {
			return true
		}
	}
	return false
}

// List returns the allowed capabilities in stable order.
func List() []string {
	return append([]string(nil), allowed...)
}

// Refusal explains a denial in one line. It names the boundary, never the
// internals: callers must not leak allowlist contents to untrusted parties.
func Refusal() string {
	return "That is outside my read-only authority (444), so I can't do it."
}

// Check enforces the boundary for a requested operation. It returns nil
// when the operation is allowed and a descriptive error otherwise.
func Check(name string) error {
	if Allowed(name) {
		return nil
	}
	return &DeniedError{Operation: strings.TrimSpace(name)}
}

// DeniedError records a refused operation.
type DeniedError struct {
	Operation string
}

func (e *DeniedError) Error() string {
	if e.Operation == "" {
		return "444: unnamed operation refused: " + Refusal()
	}
	return "444: operation " + e.Operation + " refused: " + Refusal()
}
