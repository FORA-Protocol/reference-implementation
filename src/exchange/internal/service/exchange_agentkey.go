package service

import (
	"crypto/ed25519"
	"encoding/base64"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
)

// The requesting agent's key material as one transaction sees it: the key
// snapshot every signature in a request verifies against, and the RFC 7638
// thumbprint binding that ties a delivery URL to that key.
//
// Split out of exchange.go, which is at the repository's file-length cap. These
// types are used only by the execute path in that file.

// agentKey is the ONE registered-agent key snapshot that every signature in one
// ExecuteTransaction is verified against: the complete request proof, each
// per-item check the broker-relay gate runs, and each item authorization at
// execution time. It is resolved once, from the agent row the request already
// had to load, and passed down; no verification site below reloads it.
//
// Threading the key rather than the agent id is what makes that structural. A
// verifier that never receives an id cannot read the registry a second time, so
// a key rotation landing mid-request cannot verify the proof and the early items
// against the old key and the later items against the new one. That matters
// because the response is finalized onto the request claim write-once: a
// response assembled under two key snapshots would be stored permanently, and
// every later retry under that idempotency key would replay its stale
// SIGNATURE_INVALID denials.
//
// It carries the public key and the directory that key was pinned from, not a
// whole agentBinding: a binding's acceptanceBytes describe ONE item's payload,
// so a shared binding would carry the wrong payload for every item after the
// first.
type agentKey struct {
	pub          ed25519.PublicKey
	discoveryURL string
}

// verifier returns the Ed25519 key a signature is checked against. A snapshot
// with no usable key means no registered agent row was resolved for this
// request — the Exchange has no agents repo wired, or the stored key is not an
// Ed25519 key. Neither is the caller's fault, so it surfaces as an internal
// error rather than a signature denial.
func (k agentKey) verifier() (ed25519.PublicKey, error) {
	if len(k.pub) != ed25519.PublicKeySize {
		return nil, exchange.Newf(exchange.KindInternal,
			"no ed25519 agent key resolved for this request (%d bytes)", len(k.pub))
	}
	return k.pub, nil
}

// agentBinding carries the requesting agent's delivery-URL identity binding:
// the RFC 7638 thumbprint of the proven caller key in both the base64url-no-pad
// wire form (URL agent_id param + TransactionResponse.agent_identity_hash) and
// the raw 32-byte digest persisted to transaction_log (ADR-013 D4/17.6/17.8).
// pub is the raw key the thumbprint was derived from, retained so the success
// path can persist it to transaction_evidence.agent_public_key for offline
// acceptance re-verification (the digest is one-way and cannot recover it), and
// discoveryURL is the anchored directory that key was pinned from, persisted
// alongside it as the provenance the registry itself does not keep.
// acceptanceBytes is the canonical payload the acceptance signature was verified
// over, carried from the verify site for the same reason resolvedOffer carries
// the offer's: the evidence row must store the bytes that were checked, not a
// later re-derivation from a separately assembled argument list.
type agentBinding struct {
	thumbprint      string
	digest          []byte
	pub             ed25519.PublicKey
	discoveryURL    string
	acceptanceBytes []byte
}

// proven reports whether this binding records a verification. Only a check of
// the agent key builds one, so admission's zero binding — a request that proved
// no possession — is the only unproven value.
func (b agentBinding) proven() bool { return b.thumbprint != "" }

// agentBindingForKey computes the RFC 7638 thumbprint binding from the request's
// agent key snapshot and the canonical acceptance payload that key was just
// verified against. R4 binds the delivery URL to the agent key proven by the
// BODY offer-acceptance signature (never the transport caller / broker key), so
// the binding source is a bare key, not a Caller. acceptanceBytes is taken as a
// parameter rather than filled in afterwards so a binding is never half-built:
// every field describes the same verification, or the value does not exist.
func agentBindingForKey(key agentKey, acceptanceBytes []byte) (agentBinding, error) {
	sum, err := helpers.ThumbprintBytes(key.pub)
	if err != nil {
		return agentBinding{}, exchange.Wrap(exchange.KindInternal, err, "compute agent thumbprint")
	}
	digest := sum[:]
	return agentBinding{
		thumbprint:      base64.RawURLEncoding.EncodeToString(digest),
		digest:          digest,
		pub:             key.pub,
		discoveryURL:    key.discoveryURL,
		acceptanceBytes: acceptanceBytes,
	}, nil
}
