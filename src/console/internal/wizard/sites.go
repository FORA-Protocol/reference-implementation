package wizard

import (
	"context"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
)

// Sites reads back what a person has registered: the list, one site and the
// files it was set up with, and its licensing in a line.
//
// Everything is read from the Exchange on every call. Nothing about where a
// person is in the setup is kept in the session: a publisher who signed out
// halfway through, or who is on a second device, sees the same state, because
// the state is the Exchange's and is read back rather than remembered.
type Sites struct {
	reader  Reader
	bundles Bundler
	// exchangeDomain is the Exchange a site's offers are sold by, which the
	// offer preview names.
	exchangeDomain string
}

// NewSites builds the registered-site reads for sites sold by exchangeDomain.
func NewSites(reader Reader, bundles Bundler, exchangeDomain string) *Sites {
	return &Sites{reader: reader, bundles: bundles, exchangeDomain: exchangeDomain}
}

// Registrations lists what this person has registered.
func (s *Sites) Registrations(
	ctx context.Context, actor exchangeclient.Actor,
) (exchangeclient.Registrations, error) {
	return s.reader.ListRegistrations(ctx, actor)
}

// Registered is one of a person's sites, the payee it is registered under,
// and the files it was set up with.
type Registered struct {
	PublisherID  string
	Registration exchangeclient.Registration
	Bundle       setup.Bundle
}

// Registered finds one of this person's sites by name and builds its files
// from the registration the Exchange holds, never from the request. A publisher
// who signs back in a month later downloads the files their site was
// registered with, because the registration is what they are built from.
//
// The lookup runs over what the Exchange returned for this actor, so a site
// somebody else registered is simply not in the list: there is no way to ask
// about one, and a guessed hostname is answered the same way an unregistered
// one is.
func (s *Sites) Registered(ctx context.Context, actor exchangeclient.Actor, domain string) (Registered, error) {
	regs, err := s.reader.ListRegistrations(ctx, actor)
	if err != nil {
		return Registered{}, err
	}
	reg, ok := siteNamed(regs, domain)
	if !ok {
		return Registered{}, notYours()
	}
	site := setup.Site{
		Domain:           reg.Domain,
		PublisherID:      regs.PublisherID,
		CDN:              reg.CDN,
		ContentPathScope: reg.ContentPathScope,
		Terms:            controlsOrDefault(reg.Terms),
		SitemapURL:       reg.SitemapURL,
	}
	bundle, err := build(ctx, s.bundles, site,
		"Something went wrong on our side. Your registration is unaffected.")
	if err != nil {
		return Registered{}, err
	}
	return Registered{PublisherID: regs.PublisherID, Registration: reg, Bundle: bundle}, nil
}

// siteNamed finds one of the person's sites by name, whatever its case.
func siteNamed(regs exchangeclient.Registrations, domain string) (exchangeclient.Registration, bool) {
	for _, reg := range regs.Sites {
		if strings.EqualFold(reg.Domain, domain) {
			return reg, true
		}
	}
	return exchangeclient.Registration{}, false
}

// notYours is the answer for a domain that is not one of this person's. A
// domain somebody else holds gets the same answer as one nobody holds, so the
// answer tells nobody which hostnames somebody else has registered.
func notYours() error {
	return refusal.New(refusal.KindNotFound, "That domain is not one of yours.")
}

// SiteTerms is a registered site's licensing as a page shows it back: in one
// line, and laid out as the licensing step's own controls, which the domain
// page draws disabled because the terms are not changed there.
type SiteTerms struct {
	// Set is false for a site saved before its terms were chosen. Only Summary
	// is filled in then: there is nothing to lay out.
	Set      bool
	Summary  string
	Charging bool
	Rate     string
	// Currency and Unit are the Exchange's denomination, the one the rate is
	// in.
	Currency    string
	Unit        string
	Uses        []SiteUse
	Attribution bool
	// Offer is what an agent is offered for a page of the site under these
	// terms; OfferWarning says why there is none.
	Offer        []OfferLine
	OfferWarning string
}

// SiteUse is one use the licensing step offers, and whether the site permits
// it.
type SiteUse struct {
	Label     string
	Note      string
	Permitted bool
}

// Terms reads a registered site's licensing back for its page, in the
// Exchange's denomination, with the uses in the order and words the licensing
// step offers them, and the offer they produce. The offer is for the page the
// site's sample was built from, so it shows the publisher's own content without
// reading their site again; without that page it is built from the address.
func (s *Sites) Terms(ctx context.Context, site Registered) (SiteTerms, error) {
	if site.Registration.Terms == nil {
		return SiteTerms{Summary: "terms not set yet"}, nil
	}
	d, err := s.reader.TermsDefaults(ctx)
	if err != nil {
		return SiteTerms{}, err
	}
	c := controlsOf(*site.Registration.Terms)
	page := pagefetch.Page{CanonicalURL: "https://" + site.Registration.Domain + "/"}
	if site.Bundle.SamplePage != nil {
		page = *site.Bundle.SamplePage
	}
	offer, warning := offerPreview(s.exchangeDomain, page, c, d)
	choices := functionChoices()
	uses := make([]SiteUse, 0, len(choices))
	for _, choice := range choices {
		uses = append(uses, SiteUse{
			Label: choice.Label, Note: choice.Note, Permitted: containsFold(c.PermittedFunctions, choice.Value),
		})
	}
	return SiteTerms{
		Set:          true,
		Summary:      termsSummary(c, d),
		Charging:     strings.EqualFold(c.PricingModel, termspreset.ModelPerUnit),
		Rate:         strings.TrimSpace(c.Rate),
		Currency:     d.Vocab().Currency,
		Unit:         d.Vocab().Unit,
		Uses:         uses,
		Attribution:  c.AttributionRequired,
		Offer:        offer,
		OfferWarning: warning,
	}, nil
}
