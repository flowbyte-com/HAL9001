package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// The provider accepts function names from a small alphabet. MCP tool
// names are free-form, so HAL maps them onto that alphabet and keeps a
// stable, collision-free name for every (server, tool) pair.

// maxToolNameLen is the longest name HAL exposes to the provider.
const maxToolNameLen = 64

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

// shortHash is a short, stable digest of a name.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// ExposedName is the provider-visible name of one MCP tool.
//
// Two different tools never share a name: when the server or the tool
// name had to be rewritten, a digest of the original identity is added,
// and a name that is still too long is shortened with the same digest.
func ExposedName(server, tool string) string {
	raw := toolNamePrefix + server + "__" + tool
	name := sanitizeName(raw)
	if name != raw {
		name += "_" + shortHash(raw)
	}
	if len(name) > maxToolNameLen {
		name = name[:maxToolNameLen-9] + "_" + shortHash(raw)
	}
	return name
}
