// Package sitemapwalk enumerates a site's pages from one sitemap or
// sitemap-index URL, within limits, and says whether it saw everything.
//
// Every address it dials comes out of a document the publisher served, which
// makes this a request-forgery surface: the client it is given must be the SDK's
// guarded transport (NewClient builds one), so a sitemap naming an internal
// address is refused before the dial, and redirects are refused so the guard's
// decision is never handed to whatever an origin names next.
//
// The walk is bounded in five ways, and each bound is reported by name when it
// stops the walk: a per-document byte cap, a cap on sitemap documents fetched,
// a cap on unique page URLs, the client's per-request timeout, and the
// caller's deadline. A walk that stops for
// any reason is INCOMPLETE, and the caller must not treat the partial list as
// the site: the automatic importer records such a walk as a failed run and
// submits nothing, so the catalog is never rebuilt from a partial reading.
package sitemapwalk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/guardedfetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/sitemap"
)

// userAgent identifies the importer to the sites it reads.
const userAgent = "FORA-Catalog-Importer/1.0 (+https://fora-protocol.org)"

// HTTPDoer is the minimal contract the walk needs from a client. It is an
// argument rather than something built here so the composition root decides
// the security posture once, in one place.
type HTTPDoer = guardedfetch.HTTPDoer

// The causes a walk reports for a refused fetch are the shared posture's own,
// so a caller checks one sentinel whichever reader refused.
var (
	// ErrRedirectRefused is the cause behind a refusal to follow a redirect.
	ErrRedirectRefused = guardedfetch.ErrRedirectRefused
	// ErrTooLarge is the cause behind a refusal to read an oversized document.
	ErrTooLarge = guardedfetch.ErrTooLarge
	// ErrCredentialsInURL refuses a sitemap address carrying userinfo, which
	// nothing else would stop a sitemap entry from naming.
	ErrCredentialsInURL = guardedfetch.ErrCredentialsInURL
)

// NewClient builds the guarded client every document is fetched through:
// the shared posture under the caller's timeout, following nothing.
func NewClient(timeout time.Duration) *http.Client {
	return guardedfetch.NewClient(nil, timeout)
}

// Limits bound one walk. Zero values take the defaults below.
type Limits struct {
	// MaxDocBytes caps one sitemap document.
	MaxDocBytes int64
	// MaxDocuments caps the sitemap documents fetched, the root included.
	MaxDocuments int
	// MaxResources caps the unique page URLs collected across the walk.
	MaxResources int
}

// Default limits, the ones the importer ships with.
const (
	DefaultMaxDocBytes  = 1 << 20
	DefaultMaxDocuments = 100
	DefaultMaxResources = 10_000
)

func (l Limits) withDefaults() Limits {
	if l.MaxDocBytes <= 0 {
		l.MaxDocBytes = DefaultMaxDocBytes
	}
	if l.MaxDocuments <= 0 {
		l.MaxDocuments = DefaultMaxDocuments
	}
	if l.MaxResources <= 0 {
		l.MaxResources = DefaultMaxResources
	}
	return l
}

// Reason names what stopped an incomplete walk.
type Reason string

const (
	// ReasonSource is a document that could not be fetched: a refused
	// destination, a refused redirect, a non-200 answer, credentials in the
	// address, an address that is not a well-formed http or https URL, or a
	// network failure. A site too slow to answer is not one of these: that is
	// ReasonRequestTimeout, or ReasonDeadline once the caller's time is up.
	ReasonSource Reason = "source"
	// ReasonParser is a document that is not a well-formed sitemap.
	ReasonParser Reason = "parser"
	// ReasonSize is a document over the per-document byte cap.
	ReasonSize Reason = "size"
	// ReasonDocuments is the document-count cap reached with children still
	// unread.
	ReasonDocuments Reason = "documents"
	// ReasonResources is a unique page URL past the resource cap.
	ReasonResources Reason = "resources"
	// ReasonRequestTimeout is a document the site did not deliver within the
	// client's per-request timeout (the automatic import's
	// IMPORT_REQUEST_TIMEOUT) while the caller still had time left.
	ReasonRequestTimeout Reason = "request-timeout"
	// ReasonDeadline is the caller's deadline or cancellation.
	ReasonDeadline Reason = "deadline"
)

// Result is what one walk yielded.
type Result struct {
	// URLs are the unique page URLs in first-seen order. For an incomplete
	// walk they are a partial reading and must not be treated as the site.
	URLs []string
	// Documents is how many sitemap documents were fetched.
	Documents int
	// Complete is true when every document reachable from the root was read
	// whole within the limits.
	Complete bool
	// Reason and Err say why an incomplete walk stopped.
	Reason Reason
	Err    error
}

// walker carries the state of one traversal.
type walker struct {
	client  HTTPDoer
	limits  Limits
	visited map[string]bool
	seen    map[string]bool
	queue   []pending
	result  Result
}

// pending is a sitemap the walk has yet to read, and the index that listed it
// ("" for the root).
type pending struct {
	address  string
	listedIn string
}

// Walk enumerates the pages reachable from root: a URL set yields its pages, an
// index yields further sitemaps, read breadth-first. A sitemap already fetched
// is not fetched again, which is what ends an index that names itself or a
// child that names the index.
//
// Every document is parsed whole within its byte cap, so malformed XML after the
// last location fails the walk rather than hiding behind the locations already
// collected. The document cap counts fetches; the resource cap counts unique
// pages after deduplication, so a page listed twice takes one slot. Landing
// exactly on the resource cap is complete; one page past it is not.
func Walk(ctx context.Context, client HTTPDoer, root string, limits Limits) Result {
	w := &walker{
		client:  client,
		limits:  limits.withDefaults(),
		visited: map[string]bool{root: true},
		seen:    map[string]bool{},
		queue:   []pending{{address: root}},
		result:  Result{URLs: []string{}},
	}
	for len(w.queue) > 0 {
		next := w.queue[0]
		w.queue = w.queue[1:]
		if reason, err := w.visit(ctx, next.address); err != nil {
			w.result.Reason, w.result.Err = reason, located(err, next.listedIn)
			return w.result
		}
	}
	w.result.Complete = true
	return w.result
}

// visit fetches and folds one sitemap document into the result.
func (w *walker) visit(ctx context.Context, address string) (Reason, error) {
	if err := ctx.Err(); err != nil {
		return ReasonDeadline, err
	}
	if w.result.Documents >= w.limits.MaxDocuments {
		return ReasonDocuments, fmt.Errorf(
			"sitemapwalk: the document limit of %d was reached before %s",
			w.limits.MaxDocuments, guardedfetch.Redacted(address))
	}
	body, reason, err := fetch(ctx, w.client, address, w.limits.MaxDocBytes)
	if err != nil {
		return reason, err
	}
	w.result.Documents++
	// math.MaxInt: the parser stops early once it holds that many locations,
	// and a document read only as far as the cap could hide malformed XML, or
	// more pages, behind the ones already collected. The byte cap above is what
	// bounds the read; the walk counts what it collected afterwards.
	doc, err := sitemap.Parse(bytes.NewReader(body), math.MaxInt)
	if err != nil {
		return ReasonParser, fmt.Errorf("sitemapwalk: %s: %w", guardedfetch.Redacted(address), err)
	}
	switch doc.Kind {
	case sitemap.KindIndex:
		for _, child := range doc.Locs {
			if !w.visited[child] {
				w.visited[child] = true
				w.queue = append(w.queue, pending{address: child, listedIn: address})
			}
		}
	case sitemap.KindURLSet, sitemap.KindUnknown:
		for _, page := range doc.Locs {
			if w.seen[page] {
				continue
			}
			if len(w.result.URLs) >= w.limits.MaxResources {
				return ReasonResources, fmt.Errorf(
					"sitemapwalk: the resource limit of %d was exceeded at %s",
					w.limits.MaxResources, guardedfetch.Redacted(address))
			}
			w.seen[page] = true
			w.result.URLs = append(w.result.URLs, page)
		}
	}
	return "", nil
}

// fetch reads one document through the guarded client, at most cap bytes.
func fetch(ctx context.Context, client HTTPDoer, address string, cap int64) ([]byte, Reason, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, ReasonSource, fmt.Errorf("sitemapwalk: %s cannot be requested: %w",
			guardedfetch.Redacted(address), urlCause(err))
	}
	if err := guardedfetch.CheckAddress(req.URL); err != nil {
		return nil, ReasonSource, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/xml, text/xml;q=0.9, */*;q=0.1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, classify(ctx, err), fmt.Errorf("sitemapwalk: fetch %s: %w",
			guardedfetch.Redacted(address), urlCause(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ReasonSource, fmt.Errorf("sitemapwalk: fetch %s: status %d",
			guardedfetch.Redacted(address), resp.StatusCode)
	}
	body, err := guardedfetch.ReadCapped(resp.Body, cap)
	if errors.Is(err, ErrTooLarge) {
		return nil, ReasonSize, fmt.Errorf("%w: %s is over %d bytes", err, guardedfetch.Redacted(address), cap)
	}
	if err != nil {
		return nil, classify(ctx, err), fmt.Errorf("sitemapwalk: read %s: %w", guardedfetch.Redacted(address), err)
	}
	return body, "", nil
}

// classify names a failed request by who ran out of time, if anyone. The error
// alone cannot tell: http.Client reports its own per-request Timeout with an
// error that matches context.DeadlineExceeded and reports Timeout(), exactly as
// the caller's deadline does. So the caller's context decides first -- when it
// is done, the caller's budget for the whole walk is spent (ReasonDeadline) --
// and only then does a timeout in the error mean the site was too slow for one
// request (ReasonRequestTimeout). Anything else is the source's (ReasonSource).
func classify(ctx context.Context, err error) Reason {
	if ctx.Err() != nil {
		return ReasonDeadline
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ReasonRequestTimeout
	}
	return ReasonSource
}

// located names, in an error about a child sitemap, the index that listed it,
// so a child whose own address is too malformed to show can still be found.
// The sentinels in err still match.
func located(err error, listedIn string) error {
	if listedIn == "" {
		return err
	}
	return fmt.Errorf("%w, listed in %s", err, guardedfetch.Redacted(listedIn))
}

// urlCause is the cause inside a *url.Error, or err itself. A url.Error's own
// text quotes its URL field as given -- the raw address, or after a refused
// redirect the raw Location the origin sent -- so the walk wraps only what is
// inside it and names the address itself, through guardedfetch.Redacted.
//
// One path stays open: a redirect whose Location does not parse. net/http
// quotes that Location in the cause it builds, before any hook the client
// offers runs, and keeping it out would take a wrapper around the SDK's
// guarded transport, which internal/guards rules out.
func urlCause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
