package pagefetch

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/htmlmeta"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/robotstxt"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/sitemap"
)

// Where a page can come from.
const (
	// SourceArticleURL means the visitor named the page.
	SourceArticleURL = "article_url"
	// SourceSitemap means it is the first page of the site's sitemap.
	SourceSitemap = "sitemap"
	// SourceHome means nothing pointed anywhere, so the home page was read.
	SourceHome = "home"
)

// Page is the article a preview is built from.
type Page struct {
	// URL is the address that was fetched.
	URL string `json:"url"`
	// CanonicalURL is the address the page names as its own, which is the one
	// the offer carries. A site that serves one article at several addresses
	// says here which of them is the resource.
	CanonicalURL string `json:"canonical_url"`
	// Title is the page's headline, or the placeholder when the page gave
	// none; TitleSource says which.
	Title string `json:"title"`
	// TitleSource says where Title came from: TitleFromPage when it was read
	// from the site, TitleFromPlaceholder when it is the fixed stand-in. A
	// caller must not show the placeholder as if it had been read.
	TitleSource string `json:"title_source,omitempty"`
	// Source says how this page was chosen.
	Source string `json:"source"`
	// Unreadable is set when the site answered about this address but not with
	// a page. The offer is still built, with the placeholder title, because
	// it prices the address and the terms rather than the text.
	Unreadable *Unreadable `json:"unreadable,omitempty"`
}

// Where a page's title came from.
const (
	// TitleFromPage means the title was read from the site.
	TitleFromPage = "page"
	// TitleFromPlaceholder means the site gave no title and PlaceholderTitle
	// stands in for it.
	TitleFromPlaceholder = "placeholder"
)

// PlaceholderTitle stands in for a title that could not be read. The angle
// brackets are the same convention the preview uses for the account id it
// cannot know yet, so a visitor reads it as "yours goes here" rather than as
// a headline.
const PlaceholderTitle = "<article title>"

// FillTitle puts the placeholder where the page gave no title, and records
// which it was. A page with a title keeps it.
func (p *Page) FillTitle() {
	if p.Title != "" {
		p.TitleSource = TitleFromPage
		return
	}
	p.Title = PlaceholderTitle
	p.TitleSource = TitleFromPlaceholder
}

// Site is everything one preview read from a publisher's site.
type Site struct {
	Page Page
	// PageHeaders are the response headers of the page, which carry the
	// evidence of which network served it.
	PageHeaders http.Header
	// Robots is what robots.txt said, when it was read at all.
	Robots robotstxt.Directives
	// RobotsFetched says whether robots.txt was reached, so a later step knows
	// whether to ask for it again.
	RobotsFetched bool
	// SitemapURL is the sitemap the page was found through, when one was. It
	// is reported because the caller that imports a catalogue needs the same
	// address: asking a publisher to type it, when the discovery that found
	// their sample page already knows it, is asking them for something we
	// have. Empty when no sitemap was reached.
	SitemapURL string
	// Warnings record what could not be read, for the visitor.
	Warnings []string
}

// Read fetches one page of a publisher's site.
//
// When the visitor named an article, that is the page. Otherwise the sitemap
// decides: the index first, since a site with many sitemaps lists them there,
// then the sitemap itself, then the pointer in robots.txt, and the home page
// when none of those led anywhere. One page is read either way — this is a
// preview of what a publisher's content looks like, not a crawl of their site.
func (f *Fetcher) Read(ctx context.Context, domain, articleURL string) (Site, error) {
	var site Site
	target := articleURL
	site.Page.Source = SourceArticleURL
	if target == "" {
		target, site = f.discover(ctx, domain)
	}
	// An address the visitor gave follows nothing: it is the question being
	// asked, so an origin answering about a different one has answered
	// something else. An address this service chose for them — the home page,
	// or a page named by a sitemap — follows one hop within the same site,
	// because the visitor asked about the site and not about that address.
	result, err := f.readPage(ctx, target, site.Page.Source)
	if err != nil {
		return site, err
	}
	site.PageHeaders = result.Header
	if result.Status < 200 || result.Status > 299 {
		// The site answered, so the headers still say which network fronts it
		// and the onboarding path is worth showing. Only the article is
		// missing, and the caller is told why rather than losing the preview.
		site.Page.URL = result.URL
		site.Page.CanonicalURL = result.URL
		reason := classifyUnreadable(result)
		site.Page.Unreadable = &Unreadable{Reason: reason, Status: result.Status}
		site.Warnings = append(site.Warnings, unreadableWarning(reason, result.Status))
		return site, nil
	}
	// The address that actually answered, which is not the one asked for when a
	// site redirected its bare name to the www form.
	site.Page.URL = result.URL
	site.Page.CanonicalURL = result.URL
	// A bot check that answers 200 is still a bot check. Its body is a waiting
	// room, not the article, and reading it as one would put "Just a moment…"
	// into an offer as the publisher's resource.
	if IsChallengePage(result) {
		site.Page.Unreadable = &Unreadable{Reason: ReasonBotProtection, Status: result.Status}
		site.Warnings = append(site.Warnings, unreadableWarning(ReasonBotProtection, result.Status))
		return site, nil
	}
	if !isHTML(result.Header) {
		site.Warnings = append(site.Warnings,
			"that address did not serve a web page, so the preview has no title for it")
		return site, nil
	}
	meta := htmlmeta.Extract(bytes.NewReader(result.Body))
	site.Page.Title = meta.BestTitle()
	if canonical := absoluteCanonical(meta.Canonical, result.URL); canonical != "" {
		site.Page.CanonicalURL = canonical
	}
	return site, nil
}

// readPage fetches the article, under the redirect rule its address earns.
func (f *Fetcher) readPage(ctx context.Context, target, source string) (Result, error) {
	if source == SourceArticleURL {
		return f.Get(ctx, target, f.limits.pageBytes(), "text/html")
	}
	return f.getWithinSite(ctx, target, f.limits.pageBytes(), "text/html")
}

// discover finds a page to read when the visitor gave only a domain. Every leg
// is best effort: a site with no sitemap and no robots.txt still has a home
// page, and that is a preview worth showing.
//
// Every address considered here comes out of a document the site served, so
// none of them is trusted to name this site. A sitemap pointer or a <loc> on
// another host is reported to the visitor and not fetched — otherwise anyone
// who can publish a robots.txt gets this service to make a request of their
// choosing and gets the answer back in a preview that says it is about their
// own domain.
func (f *Fetcher) discover(ctx context.Context, domain string) (string, Site) {
	site := Site{Page: Page{Source: SourceSitemap}}
	base := f.limits.scheme() + "://" + domain

	if loc, ok := f.firstFromSitemap(ctx, base+"/sitemap.xml", domain, &site); ok {
		site.SitemapURL = base + "/sitemap.xml"
		return loc, site
	}
	if robots, ok := f.robots(ctx, base); ok {
		site.Robots = robots
		site.RobotsFetched = true
		for _, pointer := range robots.Sitemaps {
			address, warning, ok := f.readable(pointer, domain, "a sitemap")
			if !ok {
				site.Warnings = append(site.Warnings, warning)
				continue
			}
			if loc, ok := f.firstFromSitemap(ctx, address, domain, &site); ok {
				site.SitemapURL = address
				return loc, site
			}
		}
	}
	site.Page.Source = SourceHome
	site.Warnings = append(site.Warnings,
		"no sitemap was found, so the preview reads the home page. "+
			"Give the address of an article to see a real one")
	return base + "/", site
}

// offSiteWarning is what a visitor is told when one of their own documents
// points somewhere this preview will not follow. The same sentence covers a
// sitemap pointer, a page a sitemap names and a licensing document, so a
// publisher reads one rule rather than three.
func offSiteWarning(what, address string) string {
	return fmt.Sprintf(
		"this site points at %s on another site (%s), which this preview does not read", what, address)
}

// maxSitemapDepth bounds how far an index is followed. One level, because a
// sitemap index names sitemaps and a sitemap names pages; anything deeper is an
// index naming itself, which would otherwise be walked forever.
const maxSitemapDepth = 1

// firstFromSitemap reads a sitemap and returns its first page, following an
// index one level down. Both the sitemap's own address and the address it names
// must be on the publisher's site.
func (f *Fetcher) firstFromSitemap(ctx context.Context, address, domain string, site *Site) (string, bool) {
	for depth := maxSitemapDepth; depth >= 0; depth-- {
		result, err := f.getWithinSite(ctx, address, f.limits.docBytes(), "application/xml")
		if err != nil || result.Status != http.StatusOK {
			return "", false
		}
		doc, err := sitemap.Parse(bytes.NewReader(result.Body), 1)
		if err != nil {
			return "", false
		}
		first, ok := doc.First()
		if !ok {
			return "", false
		}
		// A distinct name, not a second `address`: shadowing the loop's own
		// variable here would leave an index pointing at itself forever.
		target, warning, ok := f.readable(first, domain, "a page")
		if !ok {
			site.Warnings = append(site.Warnings, warning)
			return "", false
		}
		if doc.Kind != sitemap.KindIndex {
			return target, true
		}
		address = target
	}
	return "", false
}

// robots reads the site's robots.txt.
func (f *Fetcher) robots(ctx context.Context, base string) (robotstxt.Directives, bool) {
	result, err := f.getWithinSite(ctx, base+"/robots.txt", f.limits.docBytes(), "text/plain")
	if err != nil || result.Status != http.StatusOK {
		return robotstxt.Directives{}, false
	}
	directives, err := robotstxt.Parse(bytes.NewReader(result.Body))
	if err != nil {
		return robotstxt.Directives{}, false
	}
	return directives, true
}

// absoluteCanonical resolves a page's declared canonical address against the
// address that answered, since a page may write a relative one. It has to be
// the address that answered rather than the one that was asked for: a site
// redirecting its bare name to the www form, or a path to its trailing-slash
// spelling, serves the page from somewhere other than where the request
// started, and resolving "story" against the pre-redirect address produces an
// address the site does not serve.
//
// A canonical address on another site is dropped: a page a visitor named does
// not get to redirect the offer's identity to somewhere else. Same site is the
// same rule the redirect path uses, so a page reached at the www form may
// declare its canonical on the bare name and be believed.
func absoluteCanonical(declared, fetchedFrom string) string {
	if declared == "" {
		return ""
	}
	base, err := url.Parse(fetchedFrom)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(declared)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(ref)
	if !SameSite(resolved.Host, base.Host) {
		return ""
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	if resolved.User != nil {
		// The canonical address is what the offer names as the resource, so it
		// is rendered into the answer and read by whoever the publisher shows
		// it to. A page declaring credentials in its own canonical link is not
		// a resource identity this preview will repeat.
		return ""
	}
	return resolved.String()
}

// SameSite reports whether two hosts are the site a visitor named, written two
// ways. The bare name and the www form of it are one site: a publisher who
// types the bare name and a site that serves from the www form are talking
// about the same place.
//
// This is the only definition of "the same site" in the service. Every question
// that turns on it — is this article on the domain the visitor gave, is this
// sitemap pointer the publisher's own, does this page's declared canonical
// address belong to it, does this redirect stay put — is asked here, so the
// four cannot answer differently.
func SameSite(a, b string) bool {
	a = strings.TrimPrefix(strings.ToLower(a), "www.")
	b = strings.TrimPrefix(strings.ToLower(b), "www.")
	return a != "" && a == b
}

// readable reports whether an address this service found inside one of the
// publisher's own documents may be fetched, and returns the sentence to tell
// the visitor when it may not.
//
// Two rules, both about not letting a document a site serves turn into a
// request this service would not otherwise make. The address must be on the
// site the visitor named, so a sitemap cannot point the preview at a third
// party. And it must carry no credentials: Go's client puts userinfo on the
// wire as an Authorization header, so an entry reading
// "https://user:pass@host/x" passes a host comparison and still sends a
// password. The warning for that case reports the address redacted, because
// repeating the credentials to the visitor is the same leak one step further
// along.
func (f *Fetcher) readable(address, domain, what string) (string, string, bool) {
	parsed, err := url.Parse(address)
	if err != nil {
		return "", offSiteWarning(what, address), false
	}
	if parsed.User != nil {
		return "", fmt.Sprintf(
			"this site points at %s carrying credentials (%s), which this preview does not read",
			what, parsed.Redacted()), false
	}
	if !SameSite(parsed.Host, domain) {
		return "", offSiteWarning(what, address), false
	}
	return f.secureForm(parsed), "", true
}

// secureForm returns the address to fetch, over the scheme this deployment
// reaches the site on.
//
// A robots.txt written before the site moved to https still names its sitemap
// as http, and plenty never went back to change the line: it is boilerplate the
// site's own server answers with a redirect to https. The guarded client dials
// https only, so taking that line at its word means refusing a document that is
// plainly there. Asking the same host for the same path over https is the same
// resource.
//
// The upgrade follows the configured scheme rather than being unconditional. A
// local stack is configured for plain http and its origin really does speak it;
// rewriting a pointer there would send the request somewhere nothing answers.
//
// Only http rises to https, never the reverse, and only for an address already
// shown to belong to this site: a pointer at another host is refused above and
// never reaches here.
func (f *Fetcher) secureForm(parsed *url.URL) string {
	if parsed.Scheme == "http" && f.limits.scheme() == "https" {
		upgraded := *parsed
		upgraded.Scheme = "https"
		return upgraded.String()
	}
	return parsed.String()
}

// isHTML reports whether a response carries a web page.
func isHTML(h http.Header) bool {
	return strings.Contains(strings.ToLower(h.Get("Content-Type")), "text/html")
}

// maxSiteRedirects bounds how many redirects are followed within one site.
//
// Three, because that is what the arrangements sites actually use add up to: a
// bare name that answers 301 for every path and serves from the www form is
// one, a locale or trailing-slash redirect on top of it is two, and the third
// is room for a site that does both plus something else. It is deliberately
// short of the number a general-purpose client allows, since a long chain
// inside one site is something to report rather than to chase.
const maxSiteRedirects = 3

// getWithinSite fetches one of the addresses this service chose while looking
// for a page, following redirects that stay within the same site.
//
// This is the one place a redirect is followed, and it is not a relaxation of
// the address guard: every hop is dialled through the same guarded client, so a
// redirect toward an internal address is refused exactly as a direct request to
// it would be. What these hops cross is the difference between a site's bare
// name and its www form, and the locale or trailing-slash redirect that often
// sits behind it. That is how a large share of the web is arranged; refusing it
// meant answering "that site could not be read" for sites that read perfectly
// well one name over.
//
// The page a visitor named is deliberately not fetched this way. There the
// address is the question being asked, so an origin answering about a different
// one is a different answer, not a detour.
func (f *Fetcher) getWithinSite(ctx context.Context, address string, cap int64, accept string) (Result, error) {
	var result Result
	var err error
	for range maxSiteRedirects + 1 {
		var location string
		result, location, err = f.get(ctx, address, cap, accept)
		if err == nil || location == "" {
			return result, err
		}
		next, ok := sameSiteRedirect(address, location)
		if !ok {
			return result, err
		}
		address = next
	}
	return result, err
}

// sameSiteRedirect reports where to retry, when an origin's redirect stays
// within one site. Same site means the same host, or the two spellings of it
// that differ only by a leading "www." — and http or https, never another
// scheme.
func sameSiteRedirect(from, location string) (string, bool) {
	base, err := url.Parse(from)
	if err != nil {
		return "", false
	}
	target, err := url.Parse(location)
	if err != nil {
		return "", false
	}
	next := base.ResolveReference(target)
	if next.Scheme != "http" && next.Scheme != "https" {
		return "", false
	}
	if next.User != nil {
		return "", false
	}
	if !SameSite(base.Host, next.Host) {
		return "", false
	}
	return next.String(), true
}
