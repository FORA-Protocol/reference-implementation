package service

import (
	"context"
	"errors"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// The three registration rejections, in the order the gate applies them. They
// sit beside RejectionReasonMissingResourceOwner, which covers the case that
// comes before all of them: a manifest that attests no payee at all.
//
// They are deliberately three reasons and not one. Each names a different thing
// the publisher has to go and change, and collapsing them would send a
// publisher to the wrong place: an id to correct in a served document, a site to
// register in the console, or a listing somebody has to activate.

// RejectionReasonUnregisteredPublisherDomain is emitted when the entry's domain
// is not registered as a site on this Exchange. Nothing here knows who owns that
// domain, so nothing can attribute a catalog row to a payee. The fix is
// registration, not a change to the served manifest.
const RejectionReasonUnregisteredPublisherDomain = "publisher_domain_not_registered"

// RejectionReasonResourceOwnerMismatch is emitted when the site IS registered
// but its manifest attests a different payee than the one it was registered
// under. ADR-010 accepted any attested value because a domain can only donate
// its own revenue; ADR-010 D2 narrows that on this deployment, so attaching a
// domain to a payee takes both halves — somebody holding a grant for the payee
// registers the domain, and somebody controlling the domain serves a manifest
// naming it. This reason is one half disagreeing with the other.
const RejectionReasonResourceOwnerMismatch = "resource_owner_id_mismatch"

// RejectionReasonListingNotActive is emitted when the site is registered and
// attests the right payee, but this seat does not sell it: either the seat has
// no listing for the site, or the listing is still PENDING_MANIFEST because no
// manifest check has proved it yet. Both mean the same thing to the caller —
// this Exchange is not offering that site — and neither is fixed in the feed.
const RejectionReasonListingNotActive = "publisher_domain_listing_not_active"

// resolveSite looks the entry's site up in fora.publisher_domains and refuses an
// unregistered one, returning the row the later gates read.
//
// It runs BEFORE the manifest fetch, and that ordering is the point of having it
// as its own step. The fetch goes to the host the entry names, so without a
// database precondition in front of it any caller who clears signature
// authorization can make the Exchange issue one outbound request per entry to
// any host they choose — 256 per push, each held by the fetch timeout. Refusing
// an unregistered domain from a keyed lookup costs one indexed read and sends
// nothing.
func (s *CatalogService) resolveSite(
	ctx context.Context, domain, uri string,
) (repo.PublisherDomain, *CatalogPushRejection, error) {
	site, err := s.sites.ByDomain(ctx, domain)
	if err != nil {
		if errors.Is(err, repo.ErrPublisherDomainNotFound) {
			return repo.PublisherDomain{},
				reject(uri, RejectionReasonUnregisteredPublisherDomain), nil //nolint:nilerr // err->rejection
		}
		return repo.PublisherDomain{}, nil,
			exchange.Wrap(exchange.KindInternal, err, "resolve publisher domain")
	}
	return site, nil, nil
}

// registrationCheck names the inputs checkRegistration consumes.
type registrationCheck struct {
	// seatID is this deployment's single seat, resolved from EXCHANGE_DOMAIN.
	seatID string
	// site is the registered fora.publisher_domains row resolveSite already
	// returned for this entry. Passing the row rather than the domain keeps the
	// lookup ahead of the manifest fetch, where it bounds what the Exchange will
	// send a request to.
	site repo.PublisherDomain
	// attestedOwnerID is the payee this seat's entry in the site's manifest
	// attests. resolveManifestGates has already refused an empty one.
	attestedOwnerID string
	// uri is the entry's materialized catalog URI, carried so a rejection names
	// the resource the publisher has to look at.
	uri string
}

// checkRegistration runs the two manifest-dependent registration gates over one
// entry and returns a rejection, or nil when the entry may be stored (per
// ADR-026 D1 and D2). The site's own existence was settled by resolveSite.
//
// First the payee: the id the site's manifest attests must equal the one the
// site is registered under. Then the relationship: this seat must list that site
// in the ACTIVE state.
//
// The listing read is a lookup by key and not a write, so it needs no
// transaction of its own.
func (s *CatalogService) checkRegistration(
	ctx context.Context, in registrationCheck,
) (*CatalogPushRejection, error) {
	if in.site.PublisherID != in.attestedOwnerID {
		return reject(in.uri, RejectionReasonResourceOwnerMismatch), nil
	}
	listing, err := s.listings.ByDomain(ctx, in.seatID, in.site.Domain)
	if err != nil {
		if errors.Is(err, repo.ErrListingNotFound) {
			return reject(in.uri, RejectionReasonListingNotActive), nil //nolint:nilerr // err->rejection
		}
		return nil, exchange.Wrap(exchange.KindInternal, err, "resolve publisher domain listing")
	}
	if listing.State != repo.ListingStateActive {
		return reject(in.uri, RejectionReasonListingNotActive), nil
	}
	return nil, nil
}
