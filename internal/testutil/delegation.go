package testutil

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	wktestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

// Test-only minting of the holder-bound JWT delegation chain the pinned protocol
// module's helpers.VerifyDelegation checks. The Exchange never signs one, so the
// signer sits beside the tests: discovery, execute and Broker suites mint with
// it and the verifier is the only thing that reads what it signs.

// DelegationToken overrides one token of a chain. Zero values take the profile's
// happy-path defaults, so a test names only what it changes. A nil map value
// deletes the member.
type DelegationToken struct {
	Alg    string         // "" → EdDSA
	Header map[string]any // extra or overriding protected header members
	Claims map[string]any // extra or overriding claims
}

// DelegationChain describes a chain root-first. Keys[i] signs token i. Token i's
// cnf.jkt names Keys[i+1]'s public key, and the terminal token's names Holder:
// the key the request that carries the chain must prove. RootIssuer is the root
// token's iss, the identity the subscription record trusts. Audience is the
// Exchange's bare host. Scope is the space-separated scope claim every token
// carries; Exp is the exp every token carries. Tokens, when set, has one entry
// per key and overrides that token.
type DelegationChain struct {
	Keys       []ed25519.PrivateKey
	Holder     ed25519.PublicKey
	RootIssuer string
	Audience   string
	Scope      string
	Exp        time.Time
	Tokens     []DelegationToken
}

// MintDelegation signs the chain and returns the terminal token, the bytes
// Delegation.token carries.
func MintDelegation(tb testing.TB, c DelegationChain) []byte {
	tb.Helper()
	if len(c.Tokens) != 0 && len(c.Tokens) != len(c.Keys) {
		tb.Fatalf("MintDelegation: %d keys, %d token overrides", len(c.Keys), len(c.Tokens))
	}
	var parent string
	for i, priv := range c.Keys {
		var spec DelegationToken
		if len(c.Tokens) != 0 {
			spec = c.Tokens[i]
		}
		header, claims := c.token(tb, i, parent)
		if spec.Alg != "" {
			header["alg"] = spec.Alg
		}
		override(header, spec.Header)
		override(claims, spec.Claims)
		parent = signCompact(tb, priv, header, claims)
	}
	return []byte(parent)
}

// token is the profile's happy-path header and claims of token i: the root
// names the trusted issuer, a child carries its own key in jwk and its
// thumbprint as iss; cnf.jkt names the next key, or the holder on the last.
func (c DelegationChain) token(tb testing.TB, i int, parent string) (header, claims map[string]any) {
	tb.Helper()
	pub, _ := c.Keys[i].Public().(ed25519.PublicKey)
	holder := c.Holder
	if i+1 < len(c.Keys) {
		holder, _ = c.Keys[i+1].Public().(ed25519.PublicKey)
	}
	header = map[string]any{"alg": "EdDSA", "typ": helpers.DelegationTokenType}
	iss := c.RootIssuer
	if i > 0 {
		header["jwk"] = map[string]any{"kty": "OKP", "crv": "Ed25519", "x": base64.RawURLEncoding.EncodeToString(pub)}
		iss = wktestutil.MustThumbprint(tb, pub)
	}
	claims = map[string]any{
		"iss": iss,
		"aud": []string{c.Audience},
		"exp": float64(c.Exp.Unix()),
		"cnf": map[string]any{"jkt": wktestutil.MustThumbprint(tb, holder)},
	}
	if c.Scope != "" {
		claims["scope"] = c.Scope
	}
	if parent != "" {
		claims["fora_parent"] = parent
	}
	return header, claims
}

// override applies a test's member overrides; a nil value deletes the member.
func override(target, overrides map[string]any) {
	for k, v := range overrides {
		if v == nil {
			delete(target, k)
		} else {
			target[k] = v
		}
	}
}

// signCompact signs header and claims with priv as a compact JWS: the exact
// bytes the verifier checks, with no re-serialization in between. The SDK has no
// face that mints a delegation token (an Exchange only verifies one; the issuer
// is a party outside this repository), so the signature is made through the
// key's crypto.Signer method here, in test support code only. Signed URLs,
// offers and requests keep going through the SDK faces.
func signCompact(tb testing.TB, priv ed25519.PrivateKey, header, claims map[string]any) string {
	tb.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		tb.Fatal(err)
	}
	p, err := json.Marshal(claims)
	if err != nil {
		tb.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sig, err := priv.Sign(rand.Reader, []byte(input), crypto.Hash(0))
	if err != nil {
		tb.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}
