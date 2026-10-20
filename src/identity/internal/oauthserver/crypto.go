package oauthserver

import (
	"crypto/sha256"
	"encoding/base64"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
)

// verifyPKCE reports, in constant time, whether verifier hashes to challenge under
// S256 — the check /token runs against the downstream client's stored challenge.
func verifyPKCE(challenge, verifier string) bool {
	return oidcup.SameToken(challenge, oidcup.Challenge(verifier))
}

// hashCode returns the SHA-256 of an authorization code, base64url-encoded. Codes are
// stored only as this hash, so a database read cannot replay one.
func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
