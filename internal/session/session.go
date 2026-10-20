// Package session encrypts and decrypts the short-lived cookies a browser-facing
// flow carries between redirects: the authorization-request context stashed
// before a redirect to the login provider and read back at the callback, and the
// signed-in session set afterwards. Both hold data the browser must not read or
// forge -- the OIDC subject, the upstream PKCE verifier -- so they are sealed as
// JWE (direct A256GCM) under a service key, with an expiry enforced against an
// injected clock.
//
// The package is payload-agnostic: callers hand it any JSON-serializable struct
// and own the concrete shapes, the cookie names, the TTLs and the cookie
// attributes. Two services use it that way -- the agent-side identity registry
// and the publisher console -- which is why it sits in the shared tree rather
// than inside either.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
)

// KeyLen is the symmetric key length A256GCM requires.
const KeyLen = 32

// ErrInvalid marks a cookie that failed to decrypt, parse, or unmarshal — a
// tampered, truncated, or wrong-key value. ErrExpired marks a well-formed cookie
// whose expiry has passed. Callers treat both as "no valid session", but the split
// lets a handler tell a stale login apart from a forged one in its logs.
var (
	ErrInvalid = errors.New("session: cookie invalid")
	ErrExpired = errors.New("session: cookie expired")
)

// Codec seals and opens cookie payloads under one symmetric key.
type Codec struct {
	enc jose.Encrypter
	key []byte
	clk clock.Clock
}

// NewCodec builds a Codec over a 32-byte key and a clock. The clock stamps and
// checks expiry, so a deterministic clock in a test controls cookie freshness.
func NewCodec(key []byte, clk clock.Clock) (*Codec, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("session: key must be %d bytes, got %d", KeyLen, len(key))
	}
	if clk == nil {
		return nil, errors.New("session: clock is required")
	}
	enc, err := jose.NewEncrypter(
		jose.A256GCM,
		jose.Recipient{Algorithm: jose.DIRECT, Key: key},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("session: new encrypter: %w", err)
	}
	return &Codec{enc: enc, key: key, clk: clk}, nil
}

// envelope wraps the caller's payload with the expiry the codec enforces, so expiry
// is sealed inside the ciphertext rather than trusted from a cookie attribute.
type envelope struct {
	Exp  int64           `json:"exp"`
	Data json.RawMessage `json:"data"`
}

// Seal encrypts payload into a compact JWE that expires ttl from now.
func (c *Codec) Seal(payload any, ttl time.Duration) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("session: marshal payload: %w", err)
	}
	raw, err := json.Marshal(envelope{Exp: c.clk.Now().Add(ttl).Unix(), Data: data})
	if err != nil {
		return "", fmt.Errorf("session: marshal envelope: %w", err)
	}
	obj, err := c.enc.Encrypt(raw)
	if err != nil {
		return "", fmt.Errorf("session: encrypt: %w", err)
	}
	return obj.CompactSerialize()
}

// Open decrypts token and unmarshals its payload into dest, returning ErrExpired
// when the sealed expiry has passed and ErrInvalid for anything malformed.
func (c *Codec) Open(token string, dest any) error {
	obj, err := jose.ParseEncrypted(token,
		[]jose.KeyAlgorithm{jose.DIRECT}, []jose.ContentEncryption{jose.A256GCM})
	if err != nil {
		return ErrInvalid
	}
	raw, err := obj.Decrypt(c.key)
	if err != nil {
		return ErrInvalid
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ErrInvalid
	}
	if c.clk.Now().Unix() >= env.Exp {
		return ErrExpired
	}
	if err := json.Unmarshal(env.Data, dest); err != nil {
		return ErrInvalid
	}
	return nil
}

// ErrWrongPurpose marks a cookie that decrypted and was fresh, but was sealed
// for a different purpose than the one it was opened for: a value minted for
// one cookie presented under another name. Every cookie a service seals shares
// one key, and two payloads can share JSON field names, so without the purpose
// one could be accepted as the other.
var ErrWrongPurpose = errors.New("session: cookie sealed for a different purpose")

// purposeEnvelope binds a purpose into the sealed payload, so that a value
// sealed for one purpose cannot be opened for another under the same key.
type purposeEnvelope struct {
	Purpose string          `json:"p"`
	Data    json.RawMessage `json:"d"`
}

// SealFor seals payload like Seal, bound to purpose: only OpenFor with the same
// purpose opens it. A caller sealing a cookie passes the cookie name.
func (c *Codec) SealFor(purpose string, payload any, ttl time.Duration) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("session: marshal payload: %w", err)
	}
	return c.Seal(purposeEnvelope{Purpose: purpose, Data: data}, ttl)
}

// OpenFor opens a value sealed by SealFor into dst. It returns the errors Open
// returns, and ErrWrongPurpose when the value was sealed for another purpose.
func (c *Codec) OpenFor(purpose, token string, dst any) error {
	var env purposeEnvelope
	if err := c.Open(token, &env); err != nil {
		return err
	}
	if env.Purpose != purpose {
		return ErrWrongPurpose
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		return ErrInvalid
	}
	return nil
}
