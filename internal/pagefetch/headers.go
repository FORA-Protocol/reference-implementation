package pagefetch

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// HomeHeaders asks the site's home page for the headers it answers with, and
// reads none of the page.
//
// It exists because Read is the wrong tool for one caller. Working out which
// network serves a site needs the response headers of one request; Read walks
// the sitemap, robots.txt and an article to find a page worth previewing. That
// is several requests against a site nobody has warmed up, and a caller that
// wants only the headers waits for all of them.
//
// The body is not read at all, and that is the second half of the same lesson.
// Reading it under the page cap made a large home page -- a news front page is
// megabytes -- run out of time, and the timeout threw away headers the origin
// had already sent. The headers arrive with the response; nothing about them
// depends on the body arriving too.
//
// A redirect is followed for one hop within the same site, because a site that
// answers http with a redirect to https is the ordinary case and its headers
// are on the other side of it. The Location is resolved against the home page,
// so a relative one -- "/en/" -- is followed like an absolute one. A redirect
// off the site is not followed: the question is which network serves THIS
// site.
//
// When the hop is not followed, or does not answer, the redirect's own headers
// are the answer. The redirect was served by this site's network, so its
// headers name that network as well as the page behind it would; a site whose
// apex does nothing but redirect is still detectable from them.
func (f *Fetcher) HomeHeaders(ctx context.Context, domain string) (http.Header, error) {
	base := f.limits.scheme() + "://" + domain + "/"
	header, location, err := f.headersAt(ctx, base)
	if err == nil {
		return header, nil
	}
	if location == "" {
		return nil, err
	}
	target, ok := sameSiteRedirect(base, location)
	if !ok {
		return header, nil
	}
	if behind, answered := f.hopHeaders(ctx, target); answered {
		return behind, nil
	}
	return header, nil
}

// hopHeaders asks a same-site redirect's target for its headers, over the
// scheme this deployment reaches the site on. It reports false when the target
// gave no answer of its own, and the caller keeps the redirect's headers.
func (f *Fetcher) hopHeaders(ctx context.Context, target string) (http.Header, bool) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, false
	}
	header, _, err := f.headersAt(ctx, f.secureForm(parsed))
	return header, err == nil
}

// Probe is one request made as a given client, and what came back: the status
// and the response headers, with no part of the body read.
type Probe struct {
	Status int
	Header http.Header
}

// SiteURL is the address of path on domain, in the scheme this fetcher reads
// sites with: https everywhere but a local stack, where the origin speaks plain
// http (Limits.Scheme). A caller that asks a site something itself, such as the
// console's check of a publisher's edge package, builds its address here, so it
// reaches the site the way every read in this package does.
func (f *Fetcher) SiteURL(domain, path string) string {
	return (&url.URL{Scheme: f.limits.scheme(), Host: domain, Path: path}).String()
}

// ProbeAs makes one request under a chosen User-Agent and reports what the site
// answered.
//
// It exists for one question a publisher cannot answer from the console
// otherwise: is the edge package actually deployed? The package identifies
// itself in the answer it gives an AI agent, so asking as one and reading the
// reply is the only evidence available from outside. The caller decides what
// the answer means; this reports it.
//
// Nothing about the response body is read, for the reason HomeHeaders gives:
// the answer is in the status and the headers, and a large page makes waiting
// for the body a timeout that throws them away.
func (f *Fetcher) ProbeAs(ctx context.Context, rawURL, agent string) (Probe, error) {
	req, err := newRequest(ctx, rawURL, agent, "text/html")
	if err != nil {
		return Probe{}, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		// A refused redirect carries its response, and a redirect IS an answer
		// to this question: the package refuses with a status of its own, so
		// anything that redirects instead did not refuse.
		if resp != nil && errors.Is(err, ErrRedirectRefused) {
			_ = resp.Body.Close()
			return Probe{Status: resp.StatusCode, Header: resp.Header}, nil
		}
		return Probe{}, classifyTransportError(err)
	}
	_ = resp.Body.Close()
	return Probe{Status: resp.StatusCode, Header: resp.Header}, nil
}

// headersAt makes one request and returns its response headers, plus where an
// origin tried to send us when it answered with a redirect.
func (f *Fetcher) headersAt(ctx context.Context, rawURL string) (http.Header, string, error) {
	req, err := newRequest(ctx, rawURL, userAgent, "text/html")
	if err != nil {
		return nil, "", err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		// A refused redirect hands back its response, whose headers are an
		// answer in their own right; any other failure has none.
		location := redirectLocation(resp, err)
		if location == "" {
			return nil, "", classifyTransportError(err)
		}
		return resp.Header, location, classifyTransportError(err)
	}
	// The body is closed without being read. Closing it lets the connection be
	// reused; reading it is what this function exists not to do.
	_ = resp.Body.Close()
	return resp.Header, "", nil
}
