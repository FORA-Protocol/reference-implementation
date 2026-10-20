// Package pagefetch is the one place this service reaches out to a site a
// visitor named.
//
// Every request here goes to an address an anonymous caller chose, which is the
// definition of a request forgery surface. The posture is the one the protocol
// SDK implements and this repository's architecture decision on registry-side
// content delivery requires: the address is resolved and checked before the
// dial, so a name that points inside the network is refused; the connection is
// pinned to the address that was checked, so a second lookup cannot steer it;
// only http and https are dialled; and a body over the cap is refused rather
// than truncated.
//
// Redirects follow two rules, one per kind of address. The address a visitor
// typed follows nothing: it is the question being asked, so an origin answering
// about a different one has answered something else. An address this service
// chose while looking for a page — the home page, sitemap.xml, robots.txt,
// rsl.txt, a page a sitemap named — follows at most three hops that stay on the
// same host or its www spelling. Every hop is dialled through the same guarded
// client, so a redirect toward an internal address is refused exactly as a
// direct request to it would be.
//
// The guard is not implemented here. It belongs to the SDK, and
// internal/guardedfetch composes it into the posture every reader of an
// untrusted address shares; this package composes that — a second
// implementation would be a second thing to get right.
// What this package does own is the redirect rules above, because they are a
// decision about which of a publisher's addresses to read and not about which
// addresses may be dialled at all.
package pagefetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/guardedfetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/publicerr"
)

// userAgent identifies this service to the sites it reads. A publisher looking
// at their own logs should be able to tell what asked and why.
const userAgent = "FORA-Onboarding-Preview/1.0 (+https://fora-protocol.org)"

// The causes this package reports for a refused fetch are the shared
// posture's own (internal/guardedfetch), so a caller checks one sentinel
// whichever reader refused.
var (
	// ErrRedirectRefused is the cause behind a refusal to follow a redirect.
	ErrRedirectRefused = guardedfetch.ErrRedirectRefused
	// ErrTooLarge is the cause behind a refusal to read an oversized document.
	ErrTooLarge = guardedfetch.ErrTooLarge
)

// ErrNoClient refuses a fetcher with nothing to fetch through, rather than
// letting one quietly fall back to an unguarded default.
var ErrNoClient = errors.New("pagefetch: an HTTP client is required")

// HTTPDoer is the minimal contract this package needs from a client. It is an
// argument rather than something built here, because a package that constructs
// its own transport decides its own security posture, and this one must not.
type HTTPDoer = guardedfetch.HTTPDoer

// ClientOptions tune the guarded client the composition root builds.
type ClientOptions struct {
	// Base carries the caller's own transport settings under the guard: a
	// connection pool, a pinned root set. It is never a way to replace the
	// guard, which the SDK enforces by cloning what it is given and dropping
	// the proxy and TLS dialer that would route a dial around the address
	// check.
	Base *http.Transport
	// Timeout bounds one request end to end.
	Timeout time.Duration
}

// defaultTimeout bounds a request when the caller states none.
const defaultTimeout = 10 * time.Second

// NewClient builds the guarded client. It belongs at a composition root and
// nowhere else: one client is built at startup and handed to the code that
// fetches, so the posture is visible in one place rather than decided again in
// every package that makes a request.
//
// The client itself follows nothing: the SDK's own client follows a bounded
// number of redirects, which is right for resolving a key directory whose
// address the protocol supplies, but here the address comes from a visitor.
// The page a visitor named is the page whose terms and title the preview
// reports, and an origin that answers somewhere else has answered a
// different question. The hops an address this service chose is allowed are
// made by the caller instead, one guarded request at a time, so the decision
// about where to go next is this package's and never the origin's.
func NewClient(opts ClientOptions) *http.Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return guardedfetch.NewClient(opts.Base, timeout)
}

// Limits bound what this service will read from a site.
type Limits struct {
	// Scheme is how addresses built from a bare domain are addressed. It is
	// https everywhere but a local stack, where the origin speaks plain http.
	Scheme string
	// MaxPageBytes caps an article.
	MaxPageBytes int64
	// MaxDocBytes caps the small documents: robots.txt, a sitemap, an RSL
	// document.
	MaxDocBytes int64
}

// Default caps. A page is the largest thing read, and the small documents are
// small by definition.
const (
	defaultScheme       = "https"
	defaultMaxPageBytes = 2 << 20
	defaultMaxDocBytes  = 1 << 20
)

func (l Limits) scheme() string {
	if l.Scheme == "" {
		return defaultScheme
	}
	return l.Scheme
}

func (l Limits) pageBytes() int64 {
	if l.MaxPageBytes <= 0 {
		return defaultMaxPageBytes
	}
	return l.MaxPageBytes
}

func (l Limits) docBytes() int64 {
	if l.MaxDocBytes <= 0 {
		return defaultMaxDocBytes
	}
	return l.MaxDocBytes
}

// Fetcher reads a publisher's site through the guarded client.
type Fetcher struct {
	client HTTPDoer
	limits Limits
}

// New builds a fetcher over an already-guarded client. A nil client is refused
// loudly rather than replaced with a default, because the default would be an
// unguarded one and nothing about the resulting requests would look wrong.
func New(client HTTPDoer, limits Limits) (*Fetcher, error) {
	if client == nil {
		return nil, ErrNoClient
	}
	return &Fetcher{client: client, limits: limits}, nil
}

// Result is one fetched document.
type Result struct {
	URL    string
	Status int
	Header http.Header
	Body   []byte
}

// Get fetches one address, reading at most cap bytes.
//
// Everything that can go wrong is reported as an upstream failure with a
// message written for the visitor. The cause is attached for the log, and the
// upstream's own words are never repeated: an error from a site a visitor
// named is text that site chose, and this service does not pass it on.
func (f *Fetcher) Get(ctx context.Context, rawURL string, cap int64, accept string) (Result, error) {
	result, _, err := f.get(ctx, rawURL, cap, accept)
	return result, err
}

// get is Get, and also reports where an origin tried to send us when it
// answered with a redirect. The caller decides what to do with that: the page a
// visitor named follows nothing, and the documents looked for while finding a
// page follow one hop within the same site.
func (f *Fetcher) get(ctx context.Context, rawURL string, cap int64, accept string) (Result, string, error) {
	req, err := newRequest(ctx, rawURL, userAgent, accept)
	if err != nil {
		return Result{}, "", err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		// A refused redirect is the one failure that also hands back the
		// response, so the address the origin named is readable here and
		// nowhere else.
		return Result{}, redirectLocation(resp, err), classifyTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := guardedfetch.ReadCapped(resp.Body, cap)
	if err != nil {
		return Result{}, "", publicerr.Wrap(publicerr.KindUpstream, err,
			fmt.Sprintf("that document is larger than this preview reads (%d bytes)", cap))
	}
	// URL is what the request reached, which is the request's own address: this
	// client follows no redirect, so the two are always the same here. The
	// caller that retries a hop passes the address it retried with.
	return Result{URL: rawURL, Status: resp.StatusCode, Header: resp.Header, Body: body}, "", nil
}

// newRequest builds the one GET every request in this package makes, as the
// given client and asking for the given media type. An empty accept sends no
// Accept header.
//
// It refuses an address that carries credentials. Go's client turns userinfo in
// an address into an Authorization header, so an address carrying credentials
// sends them to the origin. Only one of the addresses reaching here is typed by
// a visitor; the rest come out of documents the site served, where nothing else
// would stop a sitemap entry naming "https://user:pass@host/x" and getting
// those credentials put on the wire. This preview presents none, to anyone, and
// the refusal does not repeat the address, so the password is not echoed back.
func newRequest(ctx context.Context, rawURL, agent, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, publicerr.Wrap(publicerr.KindInvalidRequest, err,
			"that address cannot be requested")
	}
	// The shared posture decides what is refused. Its cause is not attached,
	// because it names the address and this refusal must not repeat it.
	if guardedfetch.CheckAddress(req.URL) != nil {
		return nil, publicerr.Newf(publicerr.KindInvalidRequest,
			"that address carries credentials, and this preview sends none")
	}
	req.Header.Set("User-Agent", agent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return req, nil
}

// redirectLocation reads where an origin tried to send us, for the one failure
// that carries a response alongside its error. It reports nothing for any other
// failure, so a caller cannot mistake an unrelated error for a redirect.
func redirectLocation(resp *http.Response, err error) string {
	if resp == nil || !errors.Is(err, ErrRedirectRefused) {
		return ""
	}
	return resp.Header.Get("Location")
}

// classifyTransportError turns a failed request into an answer the visitor can
// act on, without repeating anything the upstream said.
func classifyTransportError(err error) error {
	switch {
	case errors.Is(err, ErrRedirectRefused):
		return publicerr.Wrap(publicerr.KindUpstream, err,
			"that address redirects elsewhere, and this preview reads the page it was given. "+
				"Try the address it redirects to")
	case errors.Is(err, context.DeadlineExceeded):
		return publicerr.Wrap(publicerr.KindTimeout, err, "that site did not answer in time")
	case errors.Is(err, context.Canceled):
		return publicerr.Wrap(publicerr.KindTimeout, err, "the preview ran out of time")
	default:
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			return publicerr.Wrap(publicerr.KindTimeout, err, "that site did not answer in time")
		}
		return publicerr.Wrap(publicerr.KindUpstream, err, "that site could not be reached")
	}
}
