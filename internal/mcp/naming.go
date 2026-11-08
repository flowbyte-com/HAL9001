package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// The provider accepts function names from a small alphabet. MCP tool
// names are free-form, so HAL maps them onto that alphabet and keeps a
// stable name for every (server, tool) identity.

// maxToolNameLen is the longest name HAL exposes to the provider.
const maxToolNameLen = 64

// toolNameHashLen is the number of hex characters taken from the digest
// of the identity. Sixteen hex characters (64 bits) make a deliberate
// second-preimage impractical; the registry still checks for an exact
// collision and withdraws both names if one ever occurs.
const toolNameHashLen = 16

// toolNamePrefix marks every exposed name as an MCP tool, so it can
// never collide with HAL's own tools or with the MPM bridge.
const toolNamePrefix = "mcp__"

// sanitizeName replaces every character outside the provider's function
// name alphabet with an underscore.
func sanitizeName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// identityHash is a stable digest of the exact (server, tool) identity.
//
// The separator cannot appear in a valid server name, and a NUL is used
// rather than "__" so two different pairs cannot produce the same input
// by concatenation ambiguity.
func identityHash(server, tool string) string {
	sum := sha256.Sum256([]byte(server + "\x00" + tool))
	return hex.EncodeToString(sum[:])[:toolNameHashLen]
}

// ExposedName is the provider-visible name of one MCP tool.
//
// Every name carries a digest of the exact identity, including names that
// did not need sanitizing. Without that suffix a crafted tool name could
// equal another pair's rewritten name (the first server's name plus its
// hash), letting one server shadow another. The readable part is only a
// label; the digest is what makes the identity unique. If two identities
// ever did share a 64-bit digest, the registry detects the exact
// collision and withdraws the name rather than let either tool run.
func ExposedName(server, tool string) string {
	prefix := toolNamePrefix + sanitizeName(server) + "__" + sanitizeName(tool)
	suffix := "_" + identityHash(server, tool)
	if len(prefix)+len(suffix) > maxToolNameLen {
		prefix = prefix[:maxToolNameLen-len(suffix)]
	}
	return prefix + suffix
}
