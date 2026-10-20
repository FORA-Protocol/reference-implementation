package oidcup_test

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
)

// Challenge must be exactly base64url(SHA-256(verifier)) with no padding (RFC
// 7636 §4.2), so it interoperates with any conformant provider's own check.
func TestChallenge_IsBase64URLSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte("test-verifier"))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := oidcup.Challenge("test-verifier"); got != want {
		t.Errorf("Challenge = %q, want %q", got, want)
	}
	if oidcup.Challenge("a") == oidcup.Challenge("b") {
		t.Error("distinct verifiers produced the same challenge")
	}
}

// SameToken is exact-match: a prefix, a differing value, or an empty expected
// value never matches. The constant-time property is not observable in a test;
// the equality contract is.
func TestSameToken(t *testing.T) {
	cases := []struct {
		name      string
		want, got string
		match     bool
	}{
		{"equal", "abc123", "abc123", true},
		{"differing", "abc123", "abc124", false},
		{"prefix", "abc123", "abc12", false},
		{"empty presented", "abc123", "", false},
		{"both empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := oidcup.SameToken(tc.want, tc.got); got != tc.match {
				t.Errorf("SameToken(%q, %q) = %v, want %v", tc.want, tc.got, got, tc.match)
			}
		})
	}
}

// NewLeg mints three distinct values, each long enough to be a PKCE verifier
// (RFC 7636 §4.1 requires 43 to 128 characters).
func TestNewLeg_MintsDistinctVerifierLengthValues(t *testing.T) {
	leg, err := oidcup.NewLeg()
	if err != nil {
		t.Fatalf("NewLeg: %v", err)
	}
	seen := map[string]bool{}
	for _, v := range []string{leg.State, leg.Nonce, leg.Verifier} {
		if len(v) < 43 || len(v) > 128 {
			t.Errorf("minted value %q has length %d, want 43..128", v, len(v))
		}
		seen[v] = true
	}
	if len(seen) != 3 {
		t.Errorf("NewLeg reused a value: %+v", leg)
	}
	again, err := oidcup.NewLeg()
	if err != nil {
		t.Fatalf("NewLeg: %v", err)
	}
	if again.State == leg.State {
		t.Error("two legs share a state")
	}
}
