package tui

// Wordmark is HAL's identity as the interface presents it: the 9001
// raised at the upper left of HAL, before the H.
//
// The digits are Unicode superscript characters. The font draws them
// small and raised, so they read as a superscript rather than as part
// of the word, and they occupy the same seven columns as the plain
// "HAL9001" they replace: the header's alignment, and the room left for
// the conversation title, do not change.
//
// This is the mark, not the product name. The executable, the
// configuration file, the installed release and the MCP server keep the
// name HAL9001, because those are identities other software reads.
const Wordmark = "⁹⁰⁰¹HAL"

// identityRow is the mark the header carries: the eye and the wordmark.
//
// The eye stays where it was, before the word, and the wordmark carries
// the 9001 with it, so the header reads the same way it always did.
func identityRow() string {
	return "🔴 " + Wordmark
}
