package oauthserver

import (
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
)

// verifyPKCE is the OAuth server's code-interception defense: only the holder of the
// verifier that produced the challenge can redeem the code. The round-trip must hold,
// and a different verifier must NOT verify — the property the whole scheme rests on.
func TestVerifyPKCE_RoundTrip(t *testing.T) {
	verifier, err := oidcup.RandomToken(32)
	if err != nil {
		t.Fatalf("RandomToken: %v", err)
	}
	challenge := oidcup.Challenge(verifier)
	if !verifyPKCE(challenge, verifier) {
		t.Error("verifyPKCE rejected the verifier that produced the challenge")
	}
	other, err := oidcup.RandomToken(32)
	if err != nil {
		t.Fatalf("RandomToken: %v", err)
	}
	if verifyPKCE(challenge, other) {
		t.Error("verifyPKCE accepted a verifier that did not produce the challenge")
	}
}

// hashCode is what an authorization code is stored as, so a database read cannot
// replay it: deterministic, collision-distinct, and never the code itself.
func TestHashCode(t *testing.T) {
	h1 := hashCode("code-1")
	if h1 != hashCode("code-1") {
		t.Error("hashCode is not deterministic")
	}
	if h1 == hashCode("code-2") {
		t.Error("distinct codes hashed to the same value")
	}
	if h1 == "code-1" {
		t.Error("hashCode returned the code verbatim; a DB read could replay it")
	}
}
