package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
)

// NewSite is what RegisterDomain takes: the site, the integration in front of
// it, and the per-relationship values the generated artifacts are built from.
type NewSite struct {
	PublisherID string
	Domain      string
	// CDN is the integration the console detected and the publisher confirmed.
	// Detection happens in the console, which reads DNS and response headers;
	// the Exchange validates the answer but never repeats the lookup, because a
	// registration must not depend on a network probe of a host the caller
	// chose.
	CDN string
	// ContentPathScope says which parts of the site are offered through this
	// seat. Empty offers the whole site.
	ContentPathScope []string
	// Terms is the licensing the publisher agreed: which uses they permit,
	// whether attribution is required, and at what price. Nil is a site saved
	// before its terms were chosen; CheckDomain refuses to verify it until it
	// has them.
	Terms *Terms
	// SitemapURL is where the resources under the licensed paths are listed.
	// Empty is allowed -- a publisher can register before they have one -- and
	// automatic catalogue import is what needs it.
	SitemapURL string
	// ImportReplacesManualTerms is whether the automatic import may take over
	// the site's pages another source already pushed: replace their terms and
	// keep their titles and metadata. It is recorded on the listing here, and
	// changes only with the terms, by SetDomainTerms while the site is not
	// verified.
	ImportReplacesManualTerms bool
}

// RegisterDomain saves the caller's site and lists it on this seat, not
// verified. Nothing is read from the site: CheckDomain is what verifies it,
// once the site's own document proves the caller controls it.
//
// Saving holds the hostname for the caller's payee before anything proves it is
// theirs. A hostname is deployment-wide: the first registration of it wins, and
// every later one for another payee is refused and sent to support. That is a
// decision ADR-026 records, not an oversight: it is what lets a publisher save
// a domain and finish its setup later. A stranger who saves somebody else's
// domain can never verify it -- that takes a document served from the domain --
// so nothing of it is ever sold, but the hostname stays held until an operator
// releases it with the recovery procedure in the Exchange's runbook.
//
// The listing is created PENDING_MANIFEST and left there. A listing is never
// born proven, and nothing here proves it.
//
// Retry safety comes from the uniqueness the schema already has rather than
// from an idempotency key. A repeated submit finds the site registered; when it
// belongs to the same publisher and is already listed on this seat, the
// existing rows are returned and nothing is written. When it belongs to someone
// else the request is refused, because a site has exactly one payee and moving
// it is not something a second registration may do. When it belongs to the same
// publisher but has no listing on this seat -- another seat sells it, or its
// listing here was removed -- the site row is left as it is and only the
// listing is written.
func (s *RegistrationService) RegisterDomain(
	ctx context.Context, caller ConsoleCaller, in NewSite,
) (Registration, error) {
	seat, err := s.gate.RequireGrant(ctx, caller.Actor, in.PublisherID)
	if err != nil {
		return Registration{}, err
	}
	normalized, err := normalizeNewSite(in, s.cfg.TermsDefaults)
	if err != nil {
		return Registration{}, err
	}
	prior, err := s.priorRegistration(ctx, seat, normalized)
	if err != nil {
		return Registration{}, err
	}
	if prior.listed {
		return prior.registration, nil
	}
	return s.commitRegistration(ctx, caller, seat, normalized, prior)
}

// maxRegisterAttempts bounds how often a registration re-reads and retries
// after losing an insert race. Each lost race moves the rows forward -- no
// site, then a site, then a listing -- so three attempts cover every order
// concurrent submits can arrive in; a fourth would only be reached if rows
// were being deleted under the registration.
const maxRegisterAttempts = 3

// commitRegistration writes the site's rows, recovering from a concurrent
// submit that committed between the pre-check and the insert.
//
// The pre-check is a read, so two submits in flight can both pass it. The
// loser's insert then fails on the unique key and its transaction rolls back,
// writing nothing. That is not a refusal: the winner's rows are now visible, so
// the registration reads them again and decides as it would have if the winner
// had committed first -- the same publisher's listing is returned, another
// publisher's site is refused, and the same publisher's site with no listing
// here is listed.
func (s *RegistrationService) commitRegistration(
	ctx context.Context, caller ConsoleCaller, seat string, in NewSite, prior priorState,
) (Registration, error) {
	for attempt := 1; ; attempt++ {
		out, err := s.writeRegistration(ctx, caller, seat, in, prior)
		if !lostInsertRace(err) {
			if err != nil {
				return Registration{}, err
			}
			return out, nil
		}
		if attempt == maxRegisterAttempts {
			return Registration{}, exchange.Wrap(exchange.KindInternal, err,
				"register publisher domain: concurrent writes kept conflicting")
		}
		if prior, err = s.priorRegistration(ctx, seat, in); err != nil {
			return Registration{}, err
		}
		if prior.listed {
			return prior.registration, nil
		}
	}
}

// lostInsertRace reports whether a registration transaction failed because a
// concurrent one inserted the same site or listing first.
func lostInsertRace(err error) bool {
	return errors.Is(err, repo.ErrPublisherDomainExists) || errors.Is(err, repo.ErrListingExists)
}

// writeRegistration writes the site (unless this publisher already holds it),
// the listing and the audit row in one transaction, after checking the payee is
// not already holding as many unverified sites as one may.
//
// A unique violation on the site or the listing is returned unwrapped, so
// commitRegistration can tell a lost race from a failure. Every other error is
// already a domain error.
func (s *RegistrationService) writeRegistration(
	ctx context.Context, caller ConsoleCaller, seat string, in NewSite, prior priorState,
) (Registration, error) {
	var out Registration
	err := s.tx.WithTx(ctx, func(tx pgx.Tx) error {
		if herr := s.checkUnverifiedHolds(ctx, tx, seat, in.PublisherID); herr != nil {
			return herr
		}
		site, werr := s.siteForRegistration(ctx, tx, in, prior)
		if werr != nil {
			return werr
		}
		terms, terr := encodeOptionalTerms(in.Terms)
		if terr != nil {
			return terr
		}
		listing, lerr := s.listings.Create(ctx, tx, repo.NewListing{
			TenantID:                  seat,
			Domain:                    in.Domain,
			ContentPathScope:          in.ContentPathScope,
			Terms:                     terms,
			SitemapURL:                in.SitemapURL,
			ImportReplacesManualTerms: in.ImportReplacesManualTerms,
		})
		if lerr != nil {
			return mapListingCreateError(lerr, in.Domain)
		}
		out = registrationFrom(site, listing)
		return s.auditRegistration(ctx, tx, caller, seat, in, site, listing)
	})
	if err != nil {
		return Registration{}, err
	}
	return out, nil
}

// checkUnverifiedHolds refuses a new listing to a payee already holding
// consoleapi.MaxUnverifiedDomains sites on this seat that are not verified.
//
// Saving holds a hostname before anything proves it is the payee's, so without
// a cap one account could hold any number of names that nobody else can
// register until an operator releases them. Verified sites do not count: the
// cap is on what is held without proof.
//
// The payee's row is locked first, inside the transaction that will write the
// listing. Two saves for one payee then count one after the other, so they
// cannot both find room for the last place.
func (s *RegistrationService) checkUnverifiedHolds(ctx context.Context, tx pgx.Tx, seat, publisherID string) error {
	if err := s.publishers.Lock(ctx, tx, publisherID); err != nil {
		return mapSiteCreateError(err)
	}
	held, err := s.listings.CountPending(ctx, tx, seat, publisherID)
	if err != nil {
		return exchange.Wrap(exchange.KindInternal, err, "count unverified publisher domains")
	}
	if held >= consoleapi.MaxUnverifiedDomains {
		return exchange.Newf(exchange.KindFailedPrecondition,
			"you already have %d domains that are not verified; finish setting one of them up "+
				"before you add another, or ask us to remove one", consoleapi.MaxUnverifiedDomains)
	}
	return nil
}

// siteForRegistration returns the site row the listing hangs off: the one this
// publisher already holds, or a new one.
//
// An existing site keeps its row as written. A site sits behind one CDN
// whichever seat sells it, so a second seat's registration does not rewrite the
// integration the first one recorded.
func (s *RegistrationService) siteForRegistration(
	ctx context.Context, tx pgx.Tx, in NewSite, prior priorState,
) (repo.PublisherDomain, error) {
	if prior.site != nil {
		return *prior.site, nil
	}
	site, err := s.sites.Create(ctx, tx, repo.NewSite{
		Domain:        in.Domain,
		PublisherID:   in.PublisherID,
		SigningScheme: string(signing.SchemeEd25519),
		CDN:           in.CDN,
	})
	if err != nil {
		return repo.PublisherDomain{}, mapSiteCreateError(err)
	}
	return site, nil
}

// auditRegistration appends the RegisterDomain audit row inside the
// registration's transaction.
func (s *RegistrationService) auditRegistration(
	ctx context.Context, tx pgx.Tx, caller ConsoleCaller, seat string, in NewSite,
	site repo.PublisherDomain, listing repo.PublisherDomainListing,
) error {
	detail, err := marshalAuditDetail(registerDomainAudit{
		PublisherID:      in.PublisherID,
		Domain:           in.Domain,
		CDN:              in.CDN,
		SigningScheme:    site.SigningScheme,
		ContentPathScope: in.ContentPathScope,
		Terms:            in.Terms,
		SitemapURL:       in.SitemapURL,
		ListingState:     listing.State,
		// Read off the listing that was written, like the listing state.
		ImportReplacesManualTerms: listing.ImportReplacesManualTerms,
	})
	if err != nil {
		return err
	}
	return appendAudit(ctx, tx, s.audit, repo.AuditEntry{
		Actor:      caller.Actor.auditActor(),
		SourceAddr: caller.SourceAddr,
		Action:     "RegisterDomain",
		Detail:     detail,
		TenantID:   seat,
		RequestID:  requestIDPtr(caller.RequestID),
	})
}

// registerDomainAudit is what the audit row records: what the caller asked for
// AND what was written. The signing scheme and the listing state are derived
// rather than supplied, and recording them is what lets an operator reading the
// log later tell a site that registered normally from one written before the
// integration set changed.
type registerDomainAudit struct {
	PublisherID      string   `json:"publisher_id"`
	Domain           string   `json:"domain"`
	CDN              string   `json:"cdn"`
	SigningScheme    string   `json:"signing_scheme"`
	ContentPathScope []string `json:"content_path_scope"`
	// Terms is null for a site saved before its terms were chosen.
	Terms        *Terms `json:"terms"`
	SitemapURL   string `json:"sitemap_url,omitempty"`
	ListingState string `json:"listing_state"`
	// ImportReplacesManualTerms is recorded whichever way it went: false is
	// an answer too.
	ImportReplacesManualTerms bool `json:"import_replaces_manual_terms"`
}

// priorState is what the tables already hold for a registration.
//
// Three states are possible. Nothing: the site is unregistered, site is nil
// and listed is false. The site is this publisher's but has no listing on this
// seat: site is set and listed is false. The site is this publisher's and
// listed here: listed is true and registration is the existing one. A site
// that belongs to another publisher is not a state; it is refused.
type priorState struct {
	site         *repo.PublisherDomain
	listed       bool
	registration Registration
}

// priorRegistration reads what already exists for this registration, which is
// what makes a retry safe. It is a read before the write rather than a
// recovery after it, so the common retry costs two lookups and no rolled-back
// transaction.
//
// A site registered to a DIFFERENT publisher is refused here, not returned:
// answering with the other payee's listing would disclose it, and answering
// success would tell the caller they own a site they do not.
func (s *RegistrationService) priorRegistration(
	ctx context.Context, seat string, in NewSite,
) (priorState, error) {
	site, err := s.sitesRead.ByDomain(ctx, in.Domain)
	if errors.Is(err, repo.ErrPublisherDomainNotFound) {
		return priorState{}, nil
	}
	if err != nil {
		return priorState{}, exchange.Wrap(exchange.KindInternal, err, "get publisher domain")
	}
	if site.PublisherID != in.PublisherID {
		return priorState{}, errDomainTaken(in.Domain)
	}
	listing, err := s.listingsRead.ByDomain(ctx, seat, in.Domain)
	if errors.Is(err, repo.ErrListingNotFound) {
		return priorState{site: &site}, nil
	}
	if err != nil {
		return priorState{}, exchange.Wrap(exchange.KindInternal, err, "get publisher domain listing")
	}
	return priorState{site: &site, listed: true, registration: registrationFrom(site, listing)}, nil
}

// mapSiteCreateError translates the site insert's sentinels. A duplicate is
// returned unwrapped: it means a concurrent registration inserted the site
// after this one's pre-check, and commitRegistration re-reads to learn whether
// that was this publisher or another.
func mapSiteCreateError(err error) error {
	switch {
	case errors.Is(err, repo.ErrPublisherDomainExists):
		return err
	case errors.Is(err, repo.ErrPublisherNotFound):
		// The grant check passed, so the publisher existed a moment ago. It is
		// gone now, which is an operator action mid-registration rather than a
		// caller fault.
		return exchange.Wrap(exchange.KindNotFound, err,
			"your publisher account is no longer registered on this exchange")
	default:
		return exchange.Wrap(exchange.KindInternal, err, "create publisher domain")
	}
}

// mapListingCreateError translates the listing insert's sentinels. A duplicate
// is returned unwrapped for the reason mapSiteCreateError gives: a concurrent
// registration listed the site on this seat first, and the re-read returns the
// listing it wrote.
func mapListingCreateError(err error, domain string) error {
	switch {
	case errors.Is(err, repo.ErrListingExists):
		return err
	case errors.Is(err, repo.ErrPublisherDomainNotFound):
		gone := exchange.Newf(exchange.KindNotFound,
			"site %q was removed while it was being registered; please try again", domain)
		gone.Err = err
		return gone
	case errors.Is(err, repo.ErrTenantNotFound):
		// The seat went away mid-registration. That is the operator's state to
		// repair, as a missing seat is on every other path.
		return exchange.Wrap(exchange.KindUnavailable, err, "exchange seat missing while creating a listing")
	default:
		return exchange.Wrap(exchange.KindInternal, err, "create publisher domain listing")
	}
}

// errDomainTaken is the one answer a site registered to another payee gets. It
// names no publisher: the caller holds no grant for that one, and naming it
// would turn registration into a lookup of who owns which site.
func errDomainTaken(domain string) error {
	return exchange.Newf(exchange.KindInvalidRequest,
		"site %q is registered to another publisher; contact support to transfer it", domain)
}
