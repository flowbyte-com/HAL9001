package bench

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/session"
)

func TestRedactSecrets(t *testing.T) {
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.c2lnbmF0dXJl"
	input := "authorization: Bearer abcdefghijklmnop\napi_key=supersecretvalue\nkey " + jwt + " and sk-abcdef1234567890"
	got := RedactSecrets(input)
	for _, secret := range []string{"abcdefghijklmnop", "supersecretvalue", jwt, "sk-abcdef1234567890"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret %q survived redaction: %s", secret, got)
		}
	}
}

func TestValidateSessionKeyRejectsFallbacks(t *testing.T) {
	if err := ValidateSessionKey("bench-1"); err == nil {
		t.Fatal("non-explicit key accepted")
	}
	if err := ValidateSessionKey(session.BuildMainSessionKey("main")); err == nil {
		t.Fatal("canonical fallback key accepted")
	}
	key := NewSessionKey("run", "trial")
	if err := ValidateSessionKey(key); err != nil {
		t.Fatalf("generated key rejected: %v", err)
	}
	if !strings.HasPrefix(key, "sk_v1_") {
		t.Fatalf("generated key is not opaque: %s", key)
	}
}
