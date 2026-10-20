// The wizard's licensing step: what the publisher's own site already says about
// its terms, and what an agent would be shown under the answers on screen.

package wizard

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

// PageRead is what one read of the publisher's site yields for the licensing
// step.
type PageRead struct {
	// Page is worth previewing the offer for.
	Page pagefetch.Page
	// Sitemap is what the catalogue import will read.
	Sitemap string
	// Terms is what the site's own licensing document amounts to, when the
	// step's controls can express it; nil otherwise.
	Terms *termspreset.Controls
	// Note says where Terms came from, or why the step opens on our defaults.
	Note string
}

// ReadPage reads the publisher's site once, when the licensing step opens.
//
// Three things come back from that one read, and each is used: a page worth
// previewing the offer for, the sitemap the catalogue import will read, and
// whatever the site already says about its own licensing. Reading it again on
// every price change made adjusting a number wait on somebody else's web
// server, and bought nothing -- the offer changes, the page it is for does not.
func (s *Service) ReadPage(ctx context.Context, rawDomain string) (PageRead, error) {
	domain, err := Domain(rawDomain)
	if err != nil {
		return PageRead{}, err
	}
	denomination, err := s.registry.TermsDefaults(ctx)
	if err != nil {
		return PageRead{}, err
	}
	page, sitemap, site := s.readSiteFull(ctx, domain)
	terms, note := s.detectedControls(ctx, domain, site, denomination)
	return PageRead{Page: page, Sitemap: sitemap, Terms: terms, Note: note}, nil
}

// readSiteFull finds a page worth previewing, the sitemap it was found through,
// and keeps the read itself so the licensing document can be looked for without
// fetching robots.txt a second time.
//
// A site that cannot be read yields the address itself and no sitemap, which is
// the ordinary state of a domain still being built. The preview then shows what
// the terms produce, which does not depend on the page's headline.
func (s *Service) readSiteFull(ctx context.Context, domain string) (pagefetch.Page, string, *pagefetch.Site) {
	if s.pages != nil {
		if site, err := s.pages.Read(ctx, domain, ""); err == nil {
			return site.Page, site.SitemapURL, &site
		}
	}
	return pagefetch.Page{CanonicalURL: "https://" + domain + "/"}, "", nil
}

// Preview is what an agent would be shown under one set of answers.
type Preview struct {
	Lines []OfferLine
	// Warning says why no offer could be built, naming the control to change.
	Warning string
	Summary string
}

// PreviewTerms answers what an agent would be shown for one page of the domain
// under the answers currently on screen.
//
// The arithmetic is not in the browser. The rules for what a price reads as and
// which uses are allowed are the ones the offer is actually built with, and a
// copy of them in JavaScript would be a second set to keep in step. This makes
// the round trip small enough not to notice instead.
func (s *Service) PreviewTerms(
	ctx context.Context, page pagefetch.Page, controls termspreset.Controls,
) (Preview, error) {
	denomination, err := s.registry.TermsDefaults(ctx)
	if err != nil {
		return Preview{}, err
	}
	controls = normalizeControls(controls)
	lines, warning := offerPreview(s.exchangeDomain, page, controls, denomination)
	return Preview{Lines: lines, Warning: warning, Summary: termsSummary(controls, denomination)}, nil
}
