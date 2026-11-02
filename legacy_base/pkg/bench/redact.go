package bench

import "regexp"

var (
	jwtSecret = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)
	skSecret  = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`)
	authValue = regexp.MustCompile(`(?i)\b(api[_-]?key|authorization|access[_-]?token|refresh[_-]?token)\b\s*[:=]\s*["']?[A-Za-z0-9._~+/=-]{8,}["']?`)
	bearer    = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`)
)

// RedactSecrets makes raw benchmark artifacts safe to retain. It removes
// credential-shaped values while leaving normal prompt and response text
// intact.
func RedactSecrets(text string) string {
	text = jwtSecret.ReplaceAllString(text, "[REDACTED_JWT]")
	text = skSecret.ReplaceAllString(text, "[REDACTED_KEY]")
	text = authValue.ReplaceAllString(text, "$1=[REDACTED]")
	text = bearer.ReplaceAllString(text, "Bearer [REDACTED]")
	return text
}

// RedactBytes is RedactSecrets for log files.
func RedactBytes(data []byte) []byte {
	return []byte(RedactSecrets(string(data)))
}
