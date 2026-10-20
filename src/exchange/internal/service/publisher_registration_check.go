package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// ManifestChecker fetches one site's published manifest. It is the narrow port
// CheckDomain depends on, satisfied in production by the same cache the catalog
// push path reads through, so a site proves itself to registration and to the
// push path against one document fetched under one guarded posture.
type ManifestChecker interface {
	Get(ctx context.Context, host string) (*forawellknown.Manifest, error)
	Refresh(ctx context.Context, host string) (*forawellknown.Manifest, error)
}

// CheckReason is one rule a site's published document did not meet.
//
// It is the service's own vocabulary. The codes the console reads are a wire
// contract defined in consoleapi, and the transport maps each reason to one of
// them, so the service does not depend on the shape its caller is answered in.
// The value is also what the audit row records.
type CheckReason string

// The reasons a manifest check can fail.
//
// CheckReasonDomainTaken is reported only by PreflightDomain. RegisterDomain
// reaches the same fact through errDomainTaken, which refuses the write it was
// asked to make, and CheckDomain answers not-found, the answer a site that is
// not registered gets; preflight makes no write to refuse, so the answer is a
// reason like any other. It names no publisher, for the reason errDomainTaken
// gives.
const (
	CheckReasonManifestMissing      CheckReason = "manifest_missing"
	CheckReasonManifestUnreadable   CheckReason = "manifest_unreadable"
	CheckReasonVersionRefused       CheckReason = "manifest_version_refused"
	CheckReasonExchangeNotListed    CheckReason = "exchange_not_listed"
	CheckReasonOwnerMismatch        CheckReason = "resource_owner_id_mismatch"
	CheckReasonContributorNotListed CheckReason = "contributor_not_listed"
	CheckReasonDomainTaken          CheckReason = "domain_taken"
)

// CheckReasons lists every reason the service can report, so the transport can
// prove each one has a wire code.
func CheckReasons() []CheckReason {
	return []CheckReason{
		CheckReasonManifestMissing,
		CheckReasonManifestUnreadable,
		CheckReasonVersionRefused,
		CheckReasonExchangeNotListed,
		CheckReasonOwnerMismatch,
		CheckReasonContributorNotListed,
		CheckReasonDomainTaken,
	}
}

// CheckResult is what the console renders after a check: the listing as it now
// stands, and, when the check did not pass, every reason it did not.
//
// Reasons are plural because a manifest served with the wrong owner id and
// without the contributor has two things to fix, and reporting one at a time
// turns one edit into two round trips through a CDN cache.
type CheckResult struct {
	Registration Registration
	Reasons      []CheckReason
}

// Passed reports whether the check activated the listing.
func (r CheckResult) Passed() bool { return len(r.Reasons) == 0 }

// CheckDomain fetches the site's published manifest and decides whether the
// listing may go active.
//
// Three things have to hold, and all three are statements the site itself
// makes: its manifest names this seat, the entry for this seat attests the
// publisher id the site is registered under, and the manifest authorizes the
// operator's catalog contributor. Together they are the proof of control: only
// someone who can publish at that hostname can make the document say them.
//
// The check is not a delivery test. A passing check says the publisher
// authorized this Exchange, not that the edge is deployed, that signed URLs
// verify, or that any content has been imported. The console states that
// distinction on the page, and this is the layer it is true at.
//
// A failed check never deactivates a listing that is already active. The push
// path re-reads the manifest on every catalog push, so a site whose manifest
// stops naming the seat stops being able to push; walking the listing back from
// here would add a second mechanism that can disagree with that one.
//
// A listing whose terms are not chosen yet is refused before anything is
// fetched: an active listing is sold under its terms, so it cannot become one
// without them, and the table's CHECK says the same. The refusal is not a check
// of the document, so it writes nothing -- last_checked_at does not move and no
// audit row is written.
func (s *RegistrationService) CheckDomain(
	ctx context.Context, caller ConsoleCaller, publisherID, domain string,
) (CheckResult, error) {
	seat, err := s.gate.RequireGrant(ctx, caller.Actor, publisherID)
	if err != nil {
		return CheckResult{}, err
	}
	site, listing, err := s.siteForPublisher(ctx, seat, publisherID, domain)
	if err != nil {
		return CheckResult{}, err
	}
	if len(listing.Terms) == 0 {
		return CheckResult{}, errTermsNotSet(site.Domain)
	}
	reasons, err := s.manifestReasons(ctx, site.Domain, site.PublisherID)
	if err != nil {
		return CheckResult{}, err
	}
	updated, err := s.recordCheck(ctx, caller, seat, site, listing, reasons)
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{Registration: registrationFrom(site, updated), Reasons: reasons}, nil
}

// PreflightDomain reports what a check would say about a domain, whether or
// not it is saved yet, and writes nothing at all.
//
// It exists because the console asks about a domain before saving it: the
// publisher reaches the step that hands over the discovery document before the
// domain is saved, and checks the document there. CheckDomain cannot answer
// that -- it needs a listing and answers not-found without one, and it writes
// what it finds -- and RegisterDomain saves the domain rather than checking it.
//
// So this is CheckDomain's question without CheckDomain's write: fetch the
// document, compare it against this seat and this publisher, and report. No
// listing is required, no row is created, last_checked_at does not move, and
// nothing is audited -- there is no state change to record. The authoritative
// gate stays where it was: CheckDomain reads the document again before it
// verifies a site, and a domain that passed here and changed since is not
// verified there.
//
// The grant is still required. The call makes an outbound fetch to a host the
// caller names, and it compares against their publisher id, so it is a
// publisher asking about their own setup rather than an open probe.
func (s *RegistrationService) PreflightDomain(
	ctx context.Context, caller ConsoleCaller, publisherID, rawDomain string,
) ([]CheckReason, error) {
	if _, err := s.gate.RequireGrant(ctx, caller.Actor, publisherID); err != nil {
		return nil, err
	}
	domain, err := normalizeSiteDomain(rawDomain)
	if err != nil {
		return nil, err
	}
	taken, err := s.registeredElsewhere(ctx, domain, publisherID)
	if err != nil {
		return nil, err
	}
	if taken {
		// Reported alone. The document may well be perfect, and telling someone
		// to go on correcting a file for a domain they cannot register would
		// send them to work that cannot end in a registration.
		return []CheckReason{CheckReasonDomainTaken}, nil
	}
	return s.manifestReasons(ctx, domain, publisherID)
}

// registeredElsewhere reports whether the hostname already belongs to another
// publisher. A domain the caller's own publisher already holds is not "taken":
// that is the retry and the second-seat case, and RegisterDomain treats both as
// legitimate.
func (s *RegistrationService) registeredElsewhere(
	ctx context.Context, domain, publisherID string,
) (bool, error) {
	site, err := s.sitesRead.ByDomain(ctx, domain)
	switch {
	case errors.Is(err, repo.ErrPublisherDomainNotFound):
		return false, nil
	case err != nil:
		return false, exchange.Wrap(exchange.KindInternal, err, "get publisher domain")
	default:
		return site.PublisherID != publisherID, nil
	}
}

// siteForPublisher resolves the site and its listing on this seat, refusing
// anything the caller's publisher does not own.
//
// A site registered to another publisher is answered not-found, the same answer
// an unregistered one gets. The caller holds a grant for their own publisher
// only, so telling them the difference would let them test which hostnames
// somebody else has registered.
func (s *RegistrationService) siteForPublisher(
	ctx context.Context, seat, publisherID, rawDomain string,
) (repo.PublisherDomain, repo.PublisherDomainListing, error) {
	domain, err := normalizeSiteDomain(rawDomain)
	if err != nil {
		return repo.PublisherDomain{}, repo.PublisherDomainListing{}, err
	}
	site, err := s.sitesRead.ByDomain(ctx, domain)
	switch {
	case errors.Is(err, repo.ErrPublisherDomainNotFound):
		return repo.PublisherDomain{}, repo.PublisherDomainListing{}, errSiteNotFound(domain)
	case err != nil:
		return repo.PublisherDomain{}, repo.PublisherDomainListing{},
			exchange.Wrap(exchange.KindInternal, err, "get publisher domain")
	case site.PublisherID != publisherID:
		return repo.PublisherDomain{}, repo.PublisherDomainListing{}, errSiteNotFound(domain)
	}
	listing, err := s.listingsRead.ByDomain(ctx, seat, domain)
	switch {
	case errors.Is(err, repo.ErrListingNotFound):
		return repo.PublisherDomain{}, repo.PublisherDomainListing{}, errSiteNotFound(domain)
	case err != nil:
		return repo.PublisherDomain{}, repo.PublisherDomainListing{},
			exchange.Wrap(exchange.KindInternal, err, "get publisher domain listing")
	}
	return site, listing, nil
}

func errSiteNotFound(domain string) error {
	return exchange.Newf(exchange.KindNotFound, "site %q is not registered here", domain)
}

// errTermsNotSet refuses to verify a site whose terms are not chosen yet. It is
// written for the publisher, because the repair is theirs: set the terms.
func errTermsNotSet(domain string) error {
	return exchange.Newf(exchange.KindFailedPrecondition,
		"set the terms for %s before it can be verified", domain)
}

// manifestReasons fetches the site's manifest and returns every rule it breaks.
// An empty slice means the site proved itself.
//
// It takes the domain and the publisher id rather than a stored site, because
// PreflightDomain asks it about a domain that may have no row yet.
//
// The fetch is a Refresh rather than a Get: the caller has just published or
// corrected the document and pressed a button that says "check again", so
// answering from a cache entry taken before that edit would report the old
// document and send them to fix something they already fixed.
func (s *RegistrationService) manifestReasons(
	ctx context.Context, domain, publisherID string,
) ([]CheckReason, error) {
	manifest, err := s.manifests.Refresh(ctx, domain)
	if err != nil {
		if reason, ok := fetchFailureReason(err); ok {
			return []CheckReason{reason}, nil
		}
		return nil, exchange.Wrap(exchange.KindInternal, err, "fetch publisher manifest")
	}
	var reasons []CheckReason
	ownerID, listed := forawellknown.ResourceOwnerID(manifest, s.cfg.ExchangeDomain)
	switch {
	case !listed:
		reasons = append(reasons, CheckReasonExchangeNotListed)
	case ownerID != publisherID:
		reasons = append(reasons, CheckReasonOwnerMismatch)
	}
	if !forawellknown.AuthorizesContributor(manifest, s.cfg.ContributorHost, agentid.FromDirectory) {
		reasons = append(reasons, CheckReasonContributorNotListed)
	}
	return reasons, nil
}

// fetchFailureReason maps a manifest fetch failure to the reason the publisher
// sees, or reports that the failure is ours rather than theirs.
//
// The split matters for what the console says. A 404, a refused version and an
// unreadable document are all verdicts on the publisher's own document, and
// each names a different edit. A transport failure that is none of those is an
// error on this side, and telling the publisher their document is wrong would
// send them to change a file that is already correct.
func fetchFailureReason(err error) (CheckReason, bool) {
	switch {
	case errors.Is(err, forawellknown.ErrNoDocument):
		return CheckReasonManifestMissing, true
	case errors.Is(err, forawellknown.ErrVersionRefused):
		return CheckReasonVersionRefused, true
	case errors.Is(err, forawellknown.ErrSchemaInvalid),
		errors.Is(err, forawellknown.ErrRoleMismatch),
		errors.Is(err, forawellknown.ErrNotJSON),
		errors.Is(err, forawellknown.ErrFetch):
		return CheckReasonManifestUnreadable, true
	default:
		return "", false
	}
}

// recordCheck writes the outcome and returns the listing as it now stands.
//
// Every check writes something, passing or not: last_checked_at is what tells
// an operator whether a pending listing is being retried or was abandoned, and
// a check that recorded nothing would leave the two looking identical. The
// write and its audit row commit together, as every state change on this plane
// does.
func (s *RegistrationService) recordCheck(
	ctx context.Context, caller ConsoleCaller, seat string,
	site repo.PublisherDomain, listing repo.PublisherDomainListing, reasons []CheckReason,
) (repo.PublisherDomainListing, error) {
	passed := len(reasons) == 0
	activating := passed && listing.State != repo.ListingStateActive
	err := s.tx.WithTx(ctx, func(tx pgx.Tx) error {
		if werr := s.writeCheckOutcome(ctx, tx, seat, site.Domain, passed); werr != nil {
			return werr
		}
		detail, derr := marshalAuditDetail(checkDomainAudit{
			PublisherID: site.PublisherID,
			Domain:      site.Domain,
			Passed:      passed,
			Activated:   activating,
			Reasons:     reasons,
		})
		if derr != nil {
			return derr
		}
		return appendAudit(ctx, tx, s.audit, repo.AuditEntry{
			Actor:      caller.Actor.auditActor(),
			SourceAddr: caller.SourceAddr,
			Action:     "CheckDomain",
			Detail:     detail,
			TenantID:   seat,
			RequestID:  requestIDPtr(caller.RequestID),
		})
	})
	if err != nil {
		return repo.PublisherDomainListing{}, err
	}
	updated, err := s.listingsRead.ByDomain(ctx, seat, site.Domain)
	if err != nil {
		return repo.PublisherDomainListing{}, exchange.Wrap(exchange.KindInternal, err,
			fmt.Sprintf("get publisher domain listing %q", site.Domain))
	}
	return updated, nil
}

// writeCheckOutcome records one check, whichever way it went.
//
// A listing that vanished between the read and the write is an operator
// removing it mid-check, which is a state change rather than a caller fault.
// Both outcomes report that the same way.
func (s *RegistrationService) writeCheckOutcome(
	ctx context.Context, tx pgx.Tx, seat, domain string, passed bool,
) error {
	gone := exchange.Newf(exchange.KindNotFound, "site %q is no longer listed here", domain)
	if !passed {
		n, err := s.listings.RecordCheck(ctx, tx, seat, domain)
		if err != nil {
			return exchange.Wrap(exchange.KindInternal, err, "record publisher domain listing check")
		}
		if n == 0 {
			return gone
		}
		return nil
	}
	// The activated row is read back after the transaction commits, so what
	// Activate returns here only tells "wrote nothing" from "wrote the row".
	if _, err := s.listings.Activate(ctx, tx, seat, domain); err != nil {
		if errors.Is(err, repo.ErrListingNotFound) {
			return gone
		}
		return exchange.Wrap(exchange.KindInternal, err, "activate publisher domain listing")
	}
	return nil
}

type checkDomainAudit struct {
	PublisherID string        `json:"publisher_id"`
	Domain      string        `json:"domain"`
	Passed      bool          `json:"passed"`
	Activated   bool          `json:"activated"`
	Reasons     []CheckReason `json:"reasons,omitempty"`
}
