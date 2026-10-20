// The wizard's last step: saving the domain, with the terms chosen there or
// with none, and verifying it when the site answers both of its checks.

package wizard

import (
	"cmp"
	"context"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
)

// Registration is the last step's answers: the domain, the sitemap the
// licensing step found, the terms the publisher agreed, and whether the
// automatic import may replace the terms of the site's pages that are already
// in the catalog. Controls is nil when the licensing step was skipped.
type Registration struct {
	Draft
	Sitemap                   string
	Controls                  *termspreset.Controls
	ImportReplacesManualTerms bool
}

// Finished is what the last step did: the domain it saved, and whether that
// domain is verified now.
type Finished struct {
	Domain   string
	Verified bool
}

// Finish is what both of the last step's buttons do. "Finish setup" saves the
// domain with the terms chosen and then, when the site already answers both
// checks, verifies it. "Skip for now" saves it with no terms, and a domain
// with no terms cannot be verified, so verifying it is not asked for.
//
// Saving does not wait on the checks. A publisher who presses the last button
// keeps everything they entered: a domain whose file or package is not in place
// yet is saved and listed as not verified, rather than thrown away. Saving holds
// the hostname for this publisher, and nothing of the domain is sold until it
// is verified.
//
// What cannot be saved is refused with nothing written: terms that cannot be
// offered, refused here on the step the publisher set the price on, and
// whatever the Exchange refuses -- a domain another publisher holds among them.
func (s *Service) Finish(ctx context.Context, actor exchangeclient.Actor, in Registration) (Finished, error) {
	regs, err := s.registrations(ctx, actor)
	if err != nil {
		return Finished{}, err
	}
	site, err := draftSiteFor(regs.PublisherID, in.Draft)
	if err != nil {
		return Finished{}, err
	}
	terms, err := s.chosenTerms(ctx, in.Controls)
	if err != nil {
		return Finished{}, err
	}
	saved, err := s.save(ctx, actor, regs, site, in, terms)
	if err != nil {
		return Finished{}, err
	}
	verified := saved.Active() ||
		(terms != nil && s.activate(ctx, actor, site.PublisherID, saved.Domain, saved.ContentPathScope))
	return Finished{Domain: saved.Domain, Verified: verified}, nil
}

// chosenTerms is the terms the licensing step chose, or nil when the step was
// skipped.
//
// They are built before they are sent. The Exchange refuses the same
// arrangement for the same reason and that is the gate; this is what puts the
// refusal on the step the publisher set the price on, naming the control they
// set.
func (s *Service) chosenTerms(ctx context.Context, chosen *termspreset.Controls) (*exchangeclient.Terms, error) {
	if chosen == nil {
		return nil, nil
	}
	controls := normalizeControls(*chosen)
	denomination, err := s.registry.TermsDefaults(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := termspreset.FromControls(controls, denomination); err != nil {
		return nil, refusal.Wrap(refusal.KindInvalid, termsRefusal(err), err)
	}
	terms := termsOfControls(controls)
	return &terms, nil
}

// save writes the last step's answers, and is the one place that decides how.
// A domain the person does not hold is saved, with the terms chosen or with
// none. One they saved and have not verified keeps what it was saved with and
// takes the terms just chosen, with the sitemap and the import's answer chosen
// beside them; a sitemap the licensing step could not find this time leaves the
// saved one in place. It is left as it is when the licensing step is skipped
// again, and so is a verified one: it is sold under its terms.
//
// Keeping the saved sitemap is this console's rule rather than the Exchange's:
// the Exchange's terms route sets the sitemap it is given, and the console is
// that route's one caller.
//
// The import's answer is asked beside the terms and saved only with them. A
// domain saved with no terms is saved without it, so finishing its setup later
// does not open with the box ticked by an answer given without terms.
func (s *Service) save(
	ctx context.Context, actor exchangeclient.Actor, regs exchangeclient.Registrations,
	site setup.Site, in Registration, terms *exchangeclient.Terms,
) (exchangeclient.Registration, error) {
	sitemap := strings.TrimSpace(in.Sitemap)
	held, ok := siteNamed(regs, site.Domain)
	switch {
	case ok && (held.Active() || terms == nil):
		return held, nil
	case ok:
		return s.registry.SetDomainTerms(ctx, exchangeclient.DomainTerms{
			Actor:                     actor,
			PublisherID:               site.PublisherID,
			Domain:                    held.Domain,
			Terms:                     *terms,
			SitemapURL:                cmp.Or(sitemap, held.SitemapURL),
			ImportReplacesManualTerms: in.ImportReplacesManualTerms,
		})
	default:
		return s.registry.RegisterDomain(ctx, exchangeclient.NewSite{
			Actor:                     actor,
			PublisherID:               site.PublisherID,
			Domain:                    site.Domain,
			CDN:                       site.CDN,
			ContentPathScope:          site.ContentPathScope,
			Terms:                     terms,
			SitemapURL:                sitemap,
			ImportReplacesManualTerms: terms != nil && in.ImportReplacesManualTerms,
		})
	}
}

// activate asks the Exchange to verify a saved domain, once the console has
// seen the publisher's package answer. It reports whether the domain is
// verified now.
//
// It is the only place the console asks the Exchange to verify a domain, and
// the package check comes first on purpose. The Exchange verifies the discovery
// document itself -- it is the party that sells, so it reads the document again
// rather than trusting an earlier answer -- but it cannot see the package, which
// runs on the publisher's own CDN. So the console asks the site what it
// answers, here on the server, right before it asks the Exchange, and a domain
// whose package does not answer is left not verified. A console with no site
// reader cannot see the package answer at all, so it verifies nothing.
//
// A failure after the save is reported as not verified rather than as an
// error. The domain is saved, so an answer saying nothing was changed would be
// false, and the verification can be asked for again later.
func (s *Service) activate(
	ctx context.Context, actor exchangeclient.Actor, publisherID, domain string, paths []string,
) bool {
	if !s.inspectEdge(ctx, domain, paths).CanAdvance() {
		return false
	}
	checked, err := s.registry.CheckDomain(ctx, actor, publisherID, domain)
	if err != nil {
		reqctx.FromContext(ctx).ErrorContext(ctx, "console.verify_failed", "domain", domain, "err", err)
		return false
	}
	return checked.Passed
}
