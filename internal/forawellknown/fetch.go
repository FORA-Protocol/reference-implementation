package forawellknown

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	maxDocBytes    = 64 * 1024
	defaultTimeout = 5 * time.Second
)

// HTTPDoer is the minimal contract the library needs from an *http.Client.
// Tests substitute a rewriting client that routes to a local httptest server.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// FetchOptions tunes a manifest fetch. Zero values get safe defaults.
type FetchOptions struct {
	// Client performs the GET. It is REQUIRED: the SSRF guard is SDK-owned, so a
	// caller constructs the client once from the SDK factory
	// (resolvers.NewGuardedClientFromEnv) at its composition root and injects it
	// here. A nil Client is a fail-loud ErrNoClient — this package never wraps or
	// re-exports the SDK guarded-client factory as an in-package default.
	Client HTTPDoer
	// Scheme overrides the URL scheme for bare hosts; default "https".
	Scheme string
	// Port, when set, is appended to a bare host (local/compose stacks).
	Port string
	// Timeout bounds a single fetch; default 5s.
	Timeout time.Duration
	// ExpectRole, when not RoleUnspecified, makes Fetch reject a manifest
	// whose role differs (ErrRoleMismatch).
	ExpectRole Role
}

func (o FetchOptions) client() (HTTPDoer, error) {
	if o.Client == nil {
		return nil, ErrNoClient
	}
	return o.Client, nil
}

func (o FetchOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return defaultTimeout
}

// Document is a fetched manifest together with the bytes it was served as.
//
// The decoded message answers every question but one: a rule measured over the
// BYTES an origin sent cannot be answered from a message, because protojson
// re-encodes with its own spacing and carries every JSON number as a float64.
// The registration schema's size cap is exactly such a rule — the protocol
// defines it over the data_schema member as served — so the body is kept beside
// the message rather than reconstructed from it. A consumer that re-encoded
// would reach a verdict near the cap that the publishing Exchange does not.
//
// Raw is the whole document, not the member: which member matters is the
// reader's question, and slicing it here would put one reader's interest into
// the type every reader shares.
type Document struct {
	Manifest *Manifest
	Raw      []byte
}

// Fetch GETs host's /.well-known/fora.json, applies the document version gate,
// schema-validates it, decodes it via protojson, and (optionally) asserts its
// role. A 404 yields ErrNoDocument; a transient/non-2xx failure yields ErrFetch,
// as does a body that is not JSON at all; a document version this reader does
// not accept yields ErrVersionRefused; a document that parsed and broke a rule
// yields ErrSchemaInvalid; a role mismatch yields ErrRoleMismatch.
//
// It is FetchDocument with the body dropped, and it stays because most readers
// want only the message. One fetch-and-parse path serves both, so the two
// cannot come to disagree about what a valid document is.
func Fetch(ctx context.Context, host string, opts FetchOptions) (*Manifest, error) {
	doc, err := FetchDocument(ctx, host, opts)
	if err != nil {
		return nil, err
	}
	return doc.Manifest, nil
}

// FetchDocument is Fetch, keeping the served body. Use it where a rule is
// defined over the bytes rather than over the decoded message; see Document.
func FetchDocument(ctx context.Context, host string, opts FetchOptions) (*Document, error) {
	u, err := ManifestURL(host, opts.Scheme, opts.Port)
	if err != nil {
		return nil, err
	}
	client, err := opts.client()
	if err != nil {
		return nil, err
	}
	raw, err := getDoc(ctx, client, u, opts.timeout())
	if err != nil {
		return nil, err
	}
	doc, err := ParseDocument(raw, opts.ExpectRole)
	return doc, asFetchFailure(err)
}

// asFetchFailure states a parse refusal the way a FETCH must state it.
//
// The validator is pure and cannot know a network was involved, so it reports a
// body that never parsed as ErrNotJSON and nothing more. A function that did
// reach an origin owes the caller the other half: the bytes came from somewhere
// that may answer differently next time, which is what makes this retryable
// where a document that arrived and broke a rule is not.
//
// One helper rather than the same wrap written at each boundary, so the set of
// functions that count as a fetch is a thing a reader can grep for. There are
// two, and Cache.load is the one that surprises: it does its own GET and calls
// the exported ParseManifest, so it never passes through FetchDocument.
//
// Both sentinels match through the wrap, so a consumer branching on ErrFetch
// sees no change and one branching on ErrNotJSON can still ask the narrower
// question.
func asFetchFailure(err error) error {
	if err == nil || !errors.Is(err, ErrNotJSON) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrFetch, err)
}

// ParseManifest applies the document version gate to raw, schema-validates it,
// decodes it via protojson, and asserts role when expect is not RoleUnspecified.
// A refused version yields ErrVersionRefused; a shape failure ErrSchemaInvalid.
// Exported so producers and tests can round-trip bytes without an HTTP
// round-trip.
func ParseManifest(raw []byte, expect Role) (*Manifest, error) {
	doc, err := ParseDocument(raw, expect)
	if err != nil {
		return nil, err
	}
	return doc.Manifest, nil
}

// ParseDocument is ParseManifest, keeping raw beside the decoded message.
func ParseDocument(raw []byte, expect Role) (*Document, error) {
	var m Manifest
	if err := decodeValidated(raw, &m, ValidateManifest); err != nil {
		return nil, err
	}
	if expect != RoleUnspecified && m.GetRole() != expect {
		return nil, fmt.Errorf("%w: want %s got %s", ErrRoleMismatch, expect, m.GetRole())
	}
	return &Document{Manifest: &m, Raw: raw}, nil
}

// ParseWBA schema-validates raw and decodes it via protojson into a WBAFile.
// Exported so producers and tests can round-trip bytes without an HTTP call.
func ParseWBA(raw []byte) (*WBAFile, error) {
	var f WBAFile
	if err := decodeValidated(raw, &f, ValidateWBA); err != nil {
		return nil, err
	}
	return &f, nil
}

// getDoc performs the GET and returns the (size-bounded) body. 404 →
// ErrNoDocument; any other non-2xx or transport/read error → ErrFetch. Both
// sentinels are wrapped with rawURL, because this helper serves every well-known
// document the package fetches and the sentinel alone cannot say which one.
func getDoc(ctx context.Context, client HTTPDoer, rawURL string, timeout time.Duration) ([]byte, error) {
	status, _, body, err := httpGet(ctx, client, rawURL, timeout)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, noDocumentAt(rawURL)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%w: %s: status %d", ErrFetch, rawURL, status)
	}
	return body, nil
}

// httpGet issues the GET and fully reads the size-bounded body under a single
// timeout boundary, returning the raw status, headers, and body. Transport or
// read failures return ErrFetch; HTTP status is left for the caller to branch
// on (getDoc maps 404/non-2xx; the Cache also reads Cache-Control from header).
func httpGet(
	ctx context.Context, client HTTPDoer, rawURL string, timeout time.Duration,
) (int, http.Header, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// rawURL's host can be REQUEST-DERIVED: a catalog push names the publisher
	// domains whose manifests are read. SSRF is therefore mitigated by the
	// SDK-guarded HTTP client the callers inject (constructed once from
	// resolvers.NewGuardedClientFromEnv at the composition root); callers on
	// request-influenced hosts MUST pass a guarded client, never a fail-open
	// http.DefaultClient.
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil) //nolint:noctx // ctx carried via reqCtx
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: build request: %w", ErrFetch, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %s: %w", ErrFetch, rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocBytes))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: read body: %w", ErrFetch, err)
	}
	return resp.StatusCode, resp.Header, body, nil
}
