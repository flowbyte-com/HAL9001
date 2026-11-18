package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// HAL's skill content hash is an attribution hash: SHA-256 over the body
// followed by a NUL byte and the text of each hard constraint.
//
// It is not MPM's projection_sha256, and it is not a digest of the
// original SKILL.md bytes. For a skill with no constraints it is exactly
// the digest of the body -- the same value MPM's body_sha256 carries once
// the "sha256:" prefix is normalised. With constraints it generally
// differs from every one of those, because the constraints were part of
// what shaped the answer.
//
// The expected digests below are computed independently of the
// implementation, from a plain SHA-256 of the bytes and the documented
// separator, so a change in the calculation has to be a deliberate
// change to this contract.

func TestContentHashIsAnAttributionHash(t *testing.T) {
	const body = "# A\n"

	// Verified independently:
	//   sha256("# A\n")                                  = aa1237b7…
	//   sha256("# A\n" + 0x00 + "c1" + 0x00 + "c2")      = 3215e947…
	const (
		wantBodyOnly        = "sha256:aa1237b773c38dbddef583c4868aaea7a44c5237ea7923aecca5513764b42d80"
		wantWithConstraints = "sha256:3215e9471c4bdb90dbb3d9d12754631a323fb545cf2c2f845ac22fab1530a194"
	)

	if got := ContentHash(body, nil); got != wantBodyOnly {
		t.Errorf("ContentHash(body, nil) = %q, want %q", got, wantBodyOnly)
	}
	if got := ContentHash(body, []string{"c1", "c2"}); got != wantWithConstraints {
		t.Errorf("ContentHash(body, constraints) = %q, want %q", got, wantWithConstraints)
	}

	// Without constraints the hash is precisely the body digest: the
	// value that coincides with MPM's body_sha256 after normalising the
	// prefix.
	bodyDigest := sha256.Sum256([]byte(body))
	if got, want := strings.TrimPrefix(ContentHash(body, nil), "sha256:"),
		hex.EncodeToString(bodyDigest[:]); got != want {
		t.Errorf("constraint-free hash = %q, want the body digest %q", got, want)
	}

	// With constraints it is deliberately not the body digest.
	if ContentHash(body, []string{"c1", "c2"}) == ContentHash(body, nil) {
		t.Error("constraints did not change the attribution hash")
	}

	// The hash covers what was supplied, in the order it was supplied.
	if ContentHash(body, []string{"c1", "c2"}) == ContentHash(body, []string{"c2", "c1"}) {
		t.Error("constraint order was ignored")
	}

	// A different body is a different attribution.
	if ContentHash(body, []string{"c1", "c2"}) == ContentHash("# B\n", []string{"c1", "c2"}) {
		t.Error("the body was not covered by the hash")
	}

	// The separator keeps a body and a constraint from running together:
	// body "AB" is not the same attribution as body "A" with constraint
	// "B".
	if ContentHash("AB", nil) == ContentHash("A", []string{"B"}) {
		t.Error("the body and the constraint ran together")
	}
	// Known, pre-existing property, preserved deliberately: the separator
	// is a single NUL byte, so a body that itself contains NUL is not
	// distinguishable from a constraint boundary. Skill text never
	// contains one today, and the calculation is not changed here.
}

// A loaded skill reports its own attribution hash when one was recorded,
// and derives it from the supplied instructions otherwise.
func TestSkillContentHashUsesTheRecordedAttribution(t *testing.T) {
	recorded := Skill{Body: "body", Constraints: []string{"c"}, Hash: "sha256:recorded"}
	if got := recorded.ContentHash(); got != "sha256:recorded" {
		t.Errorf("ContentHash() = %q, want the recorded value", got)
	}
	derived := Skill{Body: "body", Constraints: []string{"c"}}
	if got, want := derived.ContentHash(), ContentHash("body", []string{"c"}); got != want {
		t.Errorf("ContentHash() = %q, want %q", got, want)
	}
}
