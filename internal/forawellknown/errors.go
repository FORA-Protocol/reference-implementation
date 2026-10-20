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
	// (Fetch via FetchOptions.Client, the Cache via CacheOptions.Client,
	// NewDirectoryFetcher via its client argument).
	// This package keeps no in-package guarded default.
	ErrNoClient = errors.New("forawellknown: HTTP client is required")

	// ErrNoDocument is returned when the origin serves no document at the
	// requested well-known path (HTTP 404). Consumers branch to their absence
	// path (e.g. bare-URL fallback).
	//
	// The text names no particular document. When this package also read key
	// directories, naming fora.json here reported a missing WBA key directory as
	// a missing commercial overlay, sending an operator to publish keys in a
	// document that carries none.
	//
	// Because the sentinel says nothing about which document, the URL has to.
	// EVERY error built over it comes from noDocumentAt below and carries the
	// address that was fetched — including the Cache's remembered 404s, which is
	// where this promise was broken. A consumer that wants its own vocabulary
	// translates at its boundary (see broker probe's ErrManifestMissing).
	ErrNoDocument = errors.New("forawellknown: origin serves no document at the well-known path")

	// ErrFetch wraps a transient failure of a FETCH: the transport, a non-2xx
	// status, or a body that came back and was not JSON. The origin is not
	// authoritative when unreachable. Document-neutral for the same reason as
	// ErrNoDocument; the wrapped error carries the URL.
	//
	// The unparseable body belongs on this side because nothing was read: there
	// is no document to hold to a rule, so the failure says nothing about what
	// the origin publishes. It is what a proxy answering 200 with an error page
	// produces, and what a body truncated in transit produces, and the next
	// fetch may well return the real document.
	//
	// Every site that attaches it sits on a fetch path -- the request, the
	// transport, the status, and the boundary that states an unparseable body as
	// one. The validator never does; see ErrNotJSON. So a caller that hands this
	// package bytes it already had never sees this sentinel, and gets no claim
	// about a network it did not touch.
	ErrFetch = errors.New("forawellknown: well-known document fetch failed")

	// ErrNotJSON is a body that could not be parsed as JSON at all, said without
	// any claim about where the bytes came from.
	//
	// It exists because this package is entered two ways. The fetch functions get
	// their bytes from an origin, and there the right answer is ErrFetch: a body
	// that never parsed says nothing about what that origin publishes, and the
	// next attempt may return the real document. The Validate* and Parse*
	// functions are pure and are documented as the path for a caller that already
	// has the bytes -- a producer checking its own output, a service reading an
	// operator's file off disk. Telling that caller a fetch failed describes an
	// event that did not happen and sends them to look at a network instead of at
	// the file in front of them.
	//
	// So the fetch functions wrap this in ErrFetch and everyone else surfaces it
	// as it is. Both sentinels match through the wrap, so a consumer branching on
	// ErrFetch is unaffected.
	ErrNotJSON = errors.New("forawellknown: document body is not JSON")

	// ErrSchemaInvalid is returned when a document fails either of the two
	// checks a fetched document goes through: the embedded JSON Schema, which
	// decides the wire shape (malformed shape, missing required field, bad key
	// encoding), and the protocol's protovalidate constraints, which decide the
	// field and cross-field rules (a terms_digest in the wrong shape, a digest
	// published with no terms_uri).
	//
	// One sentinel covers both because there is nothing a caller would do
	// differently: either way the origin served a document this participant
	// cannot use. A reader who needs to know WHICH has it in the wrapped error.
	// The manifest's document version is NOT one of these checks -- see
	// ErrVersionRefused.
	//
	// What it does NOT cover is a body that is not JSON at all. That one is
	// ErrNotJSON, wrapped in ErrFetch when a fetch produced it, and the line
	// between them is whether a retry can clear the failure. Everything here is a
	// fact about a document that arrived and broke a rule, so the next fetch
	// returns the same bytes and the same answer; a caller classifying
	// retryability reads this as final. A body that never parsed establishes
	// nothing about the origin and may parse on the next try.
	//
	// The two were one sentinel until a caller had to tell them apart. The
	// identity service's registration-requirements reader plugs into an SDK seam
	// that classifies a refusal as final or retryable, and folding the two here
	// left it choosing between retrying a permanent refusal forever and giving up
	// on a CDN hiccup. Consumers had already drifted on the question, one calling
	// the whole sentinel a permanent caller fault and another naming it in the
	// same breath as a transport failure.
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
)
