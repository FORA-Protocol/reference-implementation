//go:build integration

package app_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
)

// The fakes below stand in for things on the public internet: DNS, and the
// publisher's own web server. Each is a fake at a port the console already
// has, because a test must not depend on a host it does not own. The Exchange
// is not among them; it runs for real (see harness_test.go).

// stubDetector stands in for the DNS and header lookup. It is a fake at the
// port rather than a real lookup because the answer depends on the public
// internet, which a test must not.
//
// It records the headers it was given, because that is a property worth
// holding: a lookup handed no headers sees only DNS, and DNS is silent at an
// apex domain -- which is most publishers' home page.
type stubDetector struct {
	provider cdn.Provider
	err      error
	mu       chan struct{}
	sawHdr   http.Header
	called   bool
}

func newStubDetector(p cdn.Provider) *stubDetector {
	return &stubDetector{provider: p, mu: make(chan struct{}, 1)}
}

func (d *stubDetector) Detect(_ context.Context, _ string, hdr http.Header) (cdn.Detection, error) {
	d.mu <- struct{}{}
	d.sawHdr, d.called = hdr, true
	<-d.mu
	return cdn.Detection{Provider: d.provider}, d.err
}

func (d *stubDetector) headers() (http.Header, bool) {
	d.mu <- struct{}{}
	defer func() { <-d.mu }()
	return d.sawHdr, d.called
}

// stubPageReader stands in for the publisher's own web server, answering with
// the headers a site behind one network would.
type stubPageReader struct {
	header   http.Header
	page     pagefetch.Page
	sitemap  string
	err      error
	probe    pagefetch.Probe
	probeErr error
	// license is the RSL document the site publishes, if it publishes one. An
	// empty one is a site that publishes none, which is the ordinary case.
	license    string
	licenseErr error
}

func (s stubPageReader) Read(_ context.Context, _ string, _ string) (pagefetch.Site, error) {
	if s.err != nil {
		return pagefetch.Site{}, s.err
	}
	return pagefetch.Site{Page: s.page, PageHeaders: s.header, SitemapURL: s.sitemap}, nil
}

func (s stubPageReader) HomeHeaders(_ context.Context, _ string) (http.Header, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.header, nil
}

func (s stubPageReader) LicenseDocument(
	_ context.Context, domain string, _ *pagefetch.Site,
) ([]byte, string, error) {
	switch {
	case s.licenseErr != nil:
		return nil, "", s.licenseErr
	case s.license == "":
		return nil, "", nil
	default:
		return []byte(s.license), "https://" + domain + "/rsl.txt", nil
	}
}

func (s stubPageReader) SiteURL(domain, path string) string {
	return "https://" + domain + path
}

func (s stubPageReader) ProbeAs(_ context.Context, _ string, _ string) (pagefetch.Probe, error) {
	if s.probeErr != nil {
		return pagefetch.Probe{}, s.probeErr
	}
	return s.probe, nil
}

// scriptedProber answers each probe from a function, so one test can give the
// agent, the ordinary visitor and the discovery document different answers.
type scriptedProber struct {
	stubPageReader
	answer func(rawURL, userAgent string) (pagefetch.Probe, error)
}

func (p scriptedProber) ProbeAs(_ context.Context, rawURL, userAgent string) (pagefetch.Probe, error) {
	return p.answer(rawURL, userAgent)
}

// asAgent reports whether a probe was made under an AI agent's name. The
// console sends GPTBot, which is what the package's deny list matches.
func asAgent(userAgent string) bool { return strings.Contains(userAgent, "GPTBot") }

// refusedByPackage is the answer our own package gives an agent it will not
// serve: a refusal naming the publisher's discovery document.
func refusedByPackage(domain string) pagefetch.Probe {
	return pagefetch.Probe{
		Status: http.StatusForbidden,
		Header: http.Header{"X-Content-Rules": []string{
			"https://" + domain + "/.well-known/fora.json",
		}},
	}
}

// servedNormally is what anything not being refused answers.
func servedNormally() pagefetch.Probe {
	return pagefetch.Probe{Status: http.StatusOK, Header: http.Header{}}
}

// deployedPackage is a site with the edge package in front of it: an agent
// without a licence is refused with the package's answer, and anything else is
// served. Its pages cannot be read, which is what a console with no site
// reader sees, so the lookup, the licensing step and the files go on as they do
// without one; only the package check is answered.
func deployedPackage() scriptedProber {
	var up atomic.Bool
	up.Store(true)
	return switchablePackage(&up)
}

// switchablePackage is deployedPackage while up holds true. Otherwise the site
// has no package in front of it, and an agent is served like anybody else.
func switchablePackage(up *atomic.Bool) scriptedProber {
	return scriptedProber{
		stubPageReader: stubPageReader{err: errUnreadable},
		answer: func(rawURL, userAgent string) (pagefetch.Probe, error) {
			if up.Load() && asAgent(userAgent) {
				return refusedByPackage(hostOf(rawURL)), nil
			}
			return servedNormally(), nil
		},
	}
}

// countedProbes is p, counting in n every probe the console sends the site.
func countedProbes(p scriptedProber, n *atomic.Int32) scriptedProber {
	answer := p.answer
	p.answer = func(rawURL, userAgent string) (pagefetch.Probe, error) {
		n.Add(1)
		return answer(rawURL, userAgent)
	}
	return p
}

// hostOf is the hostname a probe was sent to.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
