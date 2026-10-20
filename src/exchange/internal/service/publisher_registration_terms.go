package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// DomainTerms is what SetDomainTerms takes: one of the caller's sites, and the
// terms chosen for it on the console's licensing step with the two settings
// chosen there beside them.
type DomainTerms struct {
	PublisherID               string
	Domain                    string
	Terms                     Terms
	SitemapURL                string
	ImportReplacesManualTerms bool
}

// SetDomainTerms sets the terms of one of the caller's sites that is saved and
// not verified yet, and answers with the site as it now stands.
//
// A site can be saved before its terms are chosen, and it cannot be verified
// without them, so this is how a publisher who saved one finishes it. It is
// also how one who saved it with terms changes them before the site is
// verified: nothing is sold from it yet, so nothing has been agreed under them.
// A verified site's terms are not changed here; it is sold under them.
//
// The checks run in the order the other publisher routes use: the grant, then
// the input, then the site, which another publisher's site and one that is not
// registered fail alike, so the answer tells nobody which hostnames someone
// else holds. The write and its audit row commit together.
func (s *RegistrationService) SetDomainTerms(
	ctx context.Context, caller ConsoleCaller, in DomainTerms,
) (Registration, error) {
	seat, err := s.gate.RequireGrant(ctx, caller.Actor, in.PublisherID)
	if err != nil {
		return Registration{}, err
	}
	domain, err := normalizeSiteDomain(in.Domain)
	if err != nil {
		return Registration{}, err
	}
	terms, err := validateTerms(in.Terms, s.cfg.TermsDefaults)
	if err != nil {
		return Registration{}, err
	}
	sitemap, err := normalizeSitemapURL(in.SitemapURL, domain)
	if err != nil {
		return Registration{}, err
	}
	site, listing, err := s.siteForPublisher(ctx, seat, in.PublisherID, domain)
	if err != nil {
		return Registration{}, err
	}
	if listing.State == repo.ListingStateActive {
		return Registration{}, errTermsOfAVerifiedSite(domain)
	}
	encoded, err := encodeTerms(terms)
	if err != nil {
		return Registration{}, err
	}
	var updated repo.PublisherDomainListing
	err = s.tx.WithTx(ctx, func(tx pgx.Tx) error {
		var werr error
		updated, werr = s.listings.SetTerms(ctx, tx, repo.ListingTerms{
			TenantID:                  seat,
			Domain:                    domain,
			Terms:                     encoded,
			SitemapURL:                sitemap,
			ImportReplacesManualTerms: in.ImportReplacesManualTerms,
		})
		if errors.Is(werr, repo.ErrListingNotPending) {
			// Verified, or removed, since the read above.
			return errTermsOfAVerifiedSite(domain)
		}
		if werr != nil {
			return exchange.Wrap(exchange.KindInternal, werr, "set publisher domain listing terms")
		}
		return s.auditDomainTerms(ctx, tx, caller, seat, in.PublisherID, domain, terms, updated)
	})
	if err != nil {
		return Registration{}, err
	}
	return registrationFrom(site, updated), nil
}

// auditDomainTerms appends the SetDomainTerms audit row inside the write's
// transaction, recording what was written.
func (s *RegistrationService) auditDomainTerms(
	ctx context.Context, tx pgx.Tx, caller ConsoleCaller, seat, publisherID, domain string,
	terms Terms, written repo.PublisherDomainListing,
) error {
	detail, err := marshalAuditDetail(setDomainTermsAudit{
		PublisherID:               publisherID,
		Domain:                    domain,
		Terms:                     terms,
		SitemapURL:                written.SitemapURL,
		ImportReplacesManualTerms: written.ImportReplacesManualTerms,
	})
	if err != nil {
		return err
	}
	return appendAudit(ctx, tx, s.audit, repo.AuditEntry{
		Actor:      caller.Actor.auditActor(),
		SourceAddr: caller.SourceAddr,
		Action:     "SetDomainTerms",
		Detail:     detail,
		TenantID:   seat,
		RequestID:  requestIDPtr(caller.RequestID),
	})
}

// setDomainTermsAudit is what the audit row records.
type setDomainTermsAudit struct {
	PublisherID               string `json:"publisher_id"`
	Domain                    string `json:"domain"`
	Terms                     Terms  `json:"terms"`
	SitemapURL                string `json:"sitemap_url,omitempty"`
	ImportReplacesManualTerms bool   `json:"import_replaces_manual_terms"`
}

// errTermsOfAVerifiedSite refuses to change the terms of a site that is
// verified, and so sold under them. It is written for the publisher.
func errTermsOfAVerifiedSite(domain string) error {
	return exchange.Newf(exchange.KindFailedPrecondition,
		"%s is verified, and the terms it is sold under cannot be changed here", domain)
}
