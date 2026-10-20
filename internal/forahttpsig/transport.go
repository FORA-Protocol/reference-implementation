// Package forahttpsig is the outbound signing http.RoundTripper the publisher
// console signs its calls to the Exchange with, and the key types every custodial
// signer in this repository shares.
//
// The Transport signs through the SDK (core.NewSigningTransport): the Web Bot
// Auth profile, the FORA covered components (@method, @target-uri,
// content-digest, authorization and the signer's own Signature-Agent member),
// tag="web-bot-auth", and a fresh 64-byte nonce on every signature, which is
// what keeps two identical requests apart in a verifier's replay store. The
// signature window is the caller's core.Window; production passes the SDK's
// core.ClockWindow. This package adds only what the SDK leaves to its caller:
// which key signs, and which requests are FORA traffic (/fora.* procedures plus
// any WithFORATargets admits). A request no rule claims, and a bodiless
// request, pass through unsigned. Its one production caller is the console,
// which signs with a key bound at construction and admits its console-API
// routes through WithFORATargets.
//
// AgentKey, KeySource, CompleteKey and ErrNoSigningKey are also what the
// identity service's outbound legs build their signer from. Identity resolves
// the agent's key per call (agentsign.Resolver.Source) and builds one SDK client
// per call around it, rather than signing through this Transport.
package forahttpsig

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"

	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraauth"
)

// AgentKey is the identity ONE outbound request is signed as: the signer's own
// directory origin (an https origin, the Signature-Agent member the signature
// covers), its RFC 9421 keyid, and the Ed25519 private half. KeyID may be
// left empty, in which case it is derived as the RFC 7638 thumbprint of Private's
// public key — the only value a WBA verifier will accept.
type AgentKey struct {
	Directory string
	KeyID     string
	Private   ed25519.PrivateKey
}

// KeySource resolves the key to sign the current request with. It is consulted
// once per signed request, which is what lets ONE transport sign on behalf of MANY
// agents: a registry holding custody of its users' keys returns the authenticated
// user's key here, per request, instead of binding a single key at construction.
//
// The subdomain it resolves MUST come from the authenticated identity of the
// inbound request, never from a field the caller filled in — a source that reads
// the wire will happily sign as whoever was asked for.
type KeySource func(ctx context.Context) (AgentKey, error)

// Transport is an http.RoundTripper that signs outbound FORA requests under the
// Web Bot Auth profile through the SDK. The RFC 9421 keyid is the RFC 7638
// thumbprint of the signing key's public half (proof of key possession); the
// signer's identity is its directory origin, carried in the covered
// Signature-Agent member. Construct it with New.
type Transport struct {
	keys       KeySource
	foraTarget func(*http.Request) bool
	signing    http.RoundTripper
}

// Option customizes a Transport at construction time.
type Option func(*Transport)

// WithKeySource resolves the signing key per request instead of using the one
// bound at construction, so one Transport could sign as many agents. No
// production caller uses it: the identity service, which holds custody of its
// agents' keys, builds one SDK client per call instead (see the package doc).
func WithKeySource(src KeySource) Option {
	return func(t *Transport) { t.keys = src }
}

// WithFORATargets extends which requests get the FORA signature. pred is called
// for each outgoing request and reports whether it should be signed. By default
// only requests whose path is /fora.* are signed; with this option, any request
// for which pred returns true is also signed. pred can only add targets — a
// /fora.* path is signed regardless of what pred returns.
//
// This exists for a route that is not a /fora.* path but is verified exactly
// like one — the Exchange's console API, which the publisher console signs. Its
// verifier expects the FORA signature, which covers the method, target URI,
// body digest, and authorization header; without this option the transport
// would forward the request unsigned and the verifier would refuse it.
func WithFORATargets(pred func(*http.Request) bool) Option {
	return func(t *Transport) { t.foraTarget = pred }
}

// New builds a signing Transport over base. directory is the signer's own
// directory origin, an https origin (helpers.CheckHTTPSOrigin) published as the
// covered Signature-Agent member so the verifier resolves the thumbprint keyid
// in that directory; priv is its Ed25519 private key; window supplies the
// per-request (created, expires) cutoffs (unix seconds), and production passes
// the SDK's core.ClockWindow. base defaults to http.DefaultTransport when nil.
//
// A caller that supplies WithKeySource signs with a key resolved per request; the
// directory and priv arguments are then unused and may be zero, and each
// resolved key's directory is held to the same origin rule by the SDK when it
// signs.
func New(
	base http.RoundTripper, directory string, priv ed25519.PrivateKey, window core.Window, opts ...Option,
) (*Transport, error) {
	t := &Transport{}
	for _, opt := range opts {
		opt(t)
	}
	if t.keys == nil {
		if err := helpers.CheckHTTPSOrigin(directory); err != nil {
			return nil, fmt.Errorf("%w: signing directory: %w", ErrUnusableKey, err)
		}
		static, err := CompleteKey(AgentKey{Directory: directory, Private: priv})
		if err != nil {
			return nil, err
		}
		t.keys = func(context.Context) (AgentKey, error) { return static, nil }
	}
	t.signing = core.NewSigningTransport(nil, base,
		core.WithSignerSource(t.signerFor),
		core.WithWindow(window),
		core.WithSignPredicate(t.isFORA),
	)
	return t, nil
}

// RoundTrip signs req when it is FORA traffic carrying a body and forwards it to
// the base transport; anything else passes through unchanged. A request that
// cannot be signed — no usable key, a directory that is not an https origin —
// is never sent.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.signing.RoundTrip(req)
}

// isFORA reports whether req is signed: a /fora.* procedure, or a target
// WithFORATargets admits.
func (t *Transport) isFORA(req *http.Request) bool {
	if req.URL == nil {
		return false
	}
	return foraauth.IsFORAProcedure(req) || (t.foraTarget != nil && t.foraTarget(req))
}

// signerFor is the SDK signer source: the key this request is signed as, and its
// directory origin.
func (t *Transport) signerFor(ctx context.Context, _ *http.Request) (helpers.Signer, string, error) {
	key, err := t.resolveKey(ctx)
	if err != nil {
		return nil, "", err
	}
	signer, err := helpers.NewEd25519Signer(key.KeyID, key.Private)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrNoSigningKey, err)
	}
	return signer, key.Directory, nil
}

// CompleteKey validates key material and derives the keyid as the RFC 7638
// thumbprint of the key's public half, so the keyid on the wire always identifies
// the key that actually signed and cannot drift from it. Construction and the
// per-request path share it, which is what keeps a KeySource held to the same
// standard as a statically-supplied key.
//
// Exported because this transport is no longer the only outbound leg resolving a
// key through a KeySource: the SDK-backed legs build their signer from the same
// custody source and would otherwise skip this, leaving one service with two
// outbound paths that answer differently about whether a keyid identifies its
// key. The rule belongs to whoever owns outbound signing keys, which is here.
//
// The thumbprint is derived on EVERY call, including when the caller already
// supplied one, and a supplied keyid that disagrees is refused. Trusting the
// caller's value would make the guarantee above hold only on the branch nobody
// takes: the custody-backed KeySource always supplies a keyid, read from the
// store rather than recomputed from the key, so a rotation bug or a partial
// write there would put a keyid on the wire that resolves at the origin to a
// different public key — every signature rejected, and nothing on this side
// noticing.
func CompleteKey(key AgentKey) (AgentKey, error) {
	if len(key.Private) != ed25519.PrivateKeySize {
		return AgentKey{}, fmt.Errorf(
			"%w: key for %q is %d bytes, want an %d-byte ed25519 private key",
			ErrUnusableKey, key.Directory, len(key.Private), ed25519.PrivateKeySize,
		)
	}
	derived, err := helpers.Thumbprint(key.Private.Public().(ed25519.PublicKey))
	if err != nil {
		return AgentKey{}, fmt.Errorf("forahttpsig: derive keyid thumbprint: %w", err)
	}
	if key.KeyID != "" && key.KeyID != derived {
		return AgentKey{}, fmt.Errorf(
			"%w: keyid %q for %q does not identify the signing key (thumbprint %q)",
			ErrUnusableKey, key.KeyID, key.Directory, derived,
		)
	}
	key.KeyID = derived
	return key, nil
}

// resolveKey consults the KeySource for the key this request is signed as. A
// source that cannot answer — custody down, no active key, an unauthenticated
// caller — aborts the request: sending it unsigned would present the agent to the
// origin as an anonymous bot, which is the failure this transport exists to
// prevent.
//
// Both failures carry ErrNoSigningKey alongside their own cause, and this
// function is where that happens because it is the one point both pass through:
// a caller that wants to know "could this request be signed at all" asks one
// question rather than enumerating the ways the answer can be no.
func (t *Transport) resolveKey(ctx context.Context) (AgentKey, error) {
	key, err := t.keys(ctx)
	if err != nil {
		return AgentKey{}, fmt.Errorf("%w: resolve signing key: %w", ErrNoSigningKey, err)
	}
	key, err = CompleteKey(key)
	if err != nil {
		return AgentKey{}, fmt.Errorf("%w: %w", ErrNoSigningKey, err)
	}
	return key, nil
}
