package forawellknown

import (
	"errors"
	"fmt"
)

// noDocumentAt builds the ErrNoDocument every 404 in this package returns.
//
// It is a function so the three places that answer "no document here" cannot
// disagree about the shape: the direct fetch, the Cache's origin 404, and the
// Cache's remembered 404. The third used to return the bare sentinel, so a
// caller that asked twice was told less the second time — with no document name
// in the sentinel and no address in the wrapper, the error said only that
// something somewhere was missing.
func noDocumentAt(rawURL string) error {
	return fmt.Errorf("%w: %s", ErrNoDocument, rawURL)
}

// Sentinel errors. Callers branch with errors.Is; transport layers map these
// to the appropriate Connect/HTTP codes.
var (
	// ErrInvalidHost is returned when a host cannot be turned into a manifest URL.
	ErrInvalidHost = errors.New("forawellknown: invalid host")

	// ErrNoClient is returned when a fetch is attempted with no injected Client.
	// The SSRF-guarded client is SDK-owned; callers construct it once from
	// resolvers.NewGuardedClientFromEnv at their composition root and inject it
	// (Fetch/FetchWBA via FetchOptions.Client, the Cache via CacheOptions.Client).
	// This package keeps no in-package guarded default.
	ErrNoClient = errors.New("forawellknown: HTTP client is required")

	// ErrNoDocument is returned when the origin serves no document at the
	// requested well-known path (HTTP 404). Consumers branch to their absence
	// path (e.g. bare-URL fallback).
	//
	// The text names no particular document, and must not: this package fetches
	// two, and one sentinel is shared by both. Naming fora.json here reported a
	// missing WBA key directory as a missing commercial overlay, sending an
	// operator to publish keys in a document that carries none.
	//
	// Because the sentinel says nothing about which document, the URL has to.
	// EVERY error built over it comes from noDocumentAt below and carries the
	// address that was fetched — including the Cache's remembered 404s, which is
	// where this promise was broken. A consumer that wants its own vocabulary
	// translates at its boundary (see broker probe's ErrManifestMissing).
	ErrNoDocument = errors.New("forawellknown: origin serves no document at the well-known path")

	// ErrFetch wraps a transient fetch / decode / non-2xx failure. The origin
	// is not authoritative when unreachable. Document-neutral for the same
	// reason as ErrNoDocument; the wrapped error carries the URL.
	ErrFetch = errors.New("forawellknown: well-known document fetch failed")

	// ErrSchemaInvalid is returned when a document fails either of the two
	// checks a fetched document goes through: the embedded JSON Schema, which
	// decides the wire shape (malformed shape, missing required field, bad key
	// encoding), and the protocol's protovalidate constraints, which decide the
	// field and cross-field rules (a terms_digest in the wrong shape, a digest
	// published with no terms_uri).
	//
	// One sentinel covers both because there is nothing a caller would do
	// differently: either way the origin served a document this participant
	// cannot use. The Exchange's agent registry, which is the only code that
	// branches on it, turns both into the same malformed-manifest refusal. A
	// reader who needs to know WHICH has it in the wrapped error. The manifest's
	// document version is NOT one of these checks — see ErrVersionRefused.
	ErrSchemaInvalid = errors.New("forawellknown: schema validation failed")

	// ErrVersionRefused is returned when a /.well-known/fora.json carries a
	// WellKnownManifest.ver this reader does not accept: an unrecognised major
	// version, a value that is not MAJOR.MINOR, or no version at all. The rule
	// is the SDK's helpers.CheckWellKnownManifestVersion, and the wrapped error
	// carries that package's sentinel and names the value found, so an operator
	// can tell a version mismatch from a malformed document.
	//
	// Checked BEFORE the wire shape and before any other member is read: the
	// document sits at a fixed, unversioned path, so a layout this reader cannot
	// classify must not be parsed for the endpoint and keys it names. It is a
	// separate sentinel from ErrSchemaInvalid because the repair is different —
	// the origin runs a manifest layout this consumer does not implement, which
	// no correction of the document's shape resolves.
	ErrVersionRefused = errors.New("forawellknown: manifest version not accepted")

	// ErrRoleMismatch is returned by Fetch when the manifest's role differs
	// from the role the caller required.
	ErrRoleMismatch = errors.New("forawellknown: manifest role mismatch")

	// ErrKeyRevoked is returned by LookupKey when the thumbprint appears in the
	// current revocation snapshot.
	ErrKeyRevoked = errors.New("forawellknown: key revoked")

	// ErrKeyExpired is returned by LookupKey when the thumbprint resolves to a
	// key whose [not_before, not_after) window does not cover now.
	ErrKeyExpired = errors.New("forawellknown: key outside validity window")

	// ErrKeyUnknown is returned by LookupKey when no key in the WBA directory
	// carries the requested RFC 7638 thumbprint (the RFC 9421 keyid).
	ErrKeyUnknown = errors.New("forawellknown: unknown key id")
)
