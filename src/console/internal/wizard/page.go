// The wizard's licensing step: what the publisher's own site already says about
// its terms, what an agent would be shown under the answers on screen, and the
// step's Next, which saves the terms on the domain.

package wizard

import (
	"cmp"
	"context"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
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

// ReadPage reads the site of one of the person's saved domains once, when the
// licensing step opens.
//
// Three things come back from that one read, and each is used: a page worth
// previewing the offer for, the sitemap the catalogue import will read, and
// whatever the site already says about its own licensing. Reading it again on
// every price change made adjusting a number wait on somebody else's web
// server, and bought nothing -- the offer changes, the page it is for does not.
//
// The domain step saves the domain before this step opens, so this read goes
// only to the site of a domain the publisher holds.
func (s *Service) ReadPage(ctx context.Context, actor exchangeclient.Actor, rawDomain string) (PageRead, error) {
	_, reg, err := s.savedDomain(ctx, actor, rawDomain)
	if err != nil {
		return PageRead{}, err
	}
	denomination, err := s.registry.TermsDefaults(ctx)
	if err != nil {
		return PageRead{}, err
	}
	page, sitemap, site := s.readSiteFull(ctx, reg.Domain)
	terms, note := s.detectedControls(ctx, reg.Domain, site, denomination)
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

// TermsAnswers is the licensing step's answers for one saved domain: the
// terms, the sitemap the step found, and whether the automatic import may
// replace the terms of the site's pages that are already in the catalog.
type TermsAnswers struct {
	Domain string
	// Controls is nil when no terms were sent, which is refused: a domain's
	// terms are set by choosing them, never by leaving them out.
	Controls                  *termspreset.Controls
	Sitemap                   string
	ImportReplacesManualTerms bool
}

// SetTerms is the licensing step's Next: it saves the terms chosen there on a
// domain the first step saved, with the sitemap and the import's answer chosen
// beside them, and answers with the domain as it now stands. A sitemap the
// step could not find this time leaves the saved one in place.
//
// Keeping the saved sitemap is this console's rule rather than the Exchange's:
// the Exchange's terms route sets the sitemap it is given, and the console is
// that route's one caller.
//
// A verified domain is sold under the terms it was verified with, so it is
// answered as it is, and nothing is saved.
//
// What cannot be offered is refused with nothing saved: terms the builder
// refuses, here, on the step the publisher set the price on, naming the
// control they set; and whatever the Exchange refuses.
func (s *Service) SetTerms(
	ctx context.Context, actor exchangeclient.Actor, in TermsAnswers,
) (exchangeclient.Registration, error) {
	if in.Controls == nil {
		return exchangeclient.Registration{}, refusal.New(refusal.KindInvalid,
			"Choose your terms before you go on.")
	}
	regs, held, err := s.savedDomain(ctx, actor, in.Domain)
	if err != nil {
		return exchangeclient.Registration{}, err
	}
	terms, err := s.chosenTerms(ctx, *in.Controls)
	if err != nil {
		return exchangeclient.Registration{}, err
	}
	if held.Active() {
		return held, nil
	}
	return s.registry.SetDomainTerms(ctx, exchangeclient.DomainTerms{
		Actor:                     actor,
		PublisherID:               regs.PublisherID,
		Domain:                    held.Domain,
		Terms:                     terms,
		SitemapURL:                cmp.Or(strings.TrimSpace(in.Sitemap), held.SitemapURL),
		ImportReplacesManualTerms: in.ImportReplacesManualTerms,
	})
}

// chosenTerms is the terms the licensing step chose, built before they are
// sent. The Exchange refuses the same arrangement for the same reason and that
// is the gate; this is what puts the refusal on the step the publisher set the
// price on, naming the control they set.
func (s *Service) chosenTerms(ctx context.Context, chosen termspreset.Controls) (exchangeclient.Terms, error) {
	controls := normalizeControls(chosen)
	denomination, err := s.registry.TermsDefaults(ctx)
	if err != nil {
		return exchangeclient.Terms{}, err
	}
	if _, err := termspreset.FromControls(controls, denomination); err != nil {
		return exchangeclient.Terms{}, refusal.Wrap(refusal.KindInvalid, termsRefusal(err), err)
	}
	return termsOfControls(controls), nil
}
