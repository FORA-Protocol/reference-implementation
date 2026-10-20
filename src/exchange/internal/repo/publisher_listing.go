package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// Listing states, mirroring the fora.listing_state enum. A listing is never born
// proven: it starts PendingManifest and reaches Active only when the site's
// served manifest names this seat with the registered payee.
const (
	ListingStatePendingManifest = "PENDING_MANIFEST"
	ListingStateActive          = "ACTIVE"
)

// PublisherDomainListing is the domain view of a fora.publisher_domain_listings
// row: one site offered on one seat, and the state that belongs to that
// relationship rather than to either side of it.
//
// ContentPathScope says which parts of the site are offered through this seat.
// Empty offers the whole site. Terms is the licensing the publisher agreed for
// it, and SitemapURL is where the resources under it are listed.
// ImportReplacesManualTerms says whether the automatic import may take over
// pages another source pushed for the site, replacing their terms. All four
// are per-relationship: the same site listed on two seats can offer different
// paths on different terms.
//
// Terms is carried as raw JSON rather than a parsed value. This package's job
// is to move the document between the database and the service, and the service
// owns what a valid document is -- a repository that parsed it would be a
// second opinion about the shape, in a layer that cannot refuse a bad one
// usefully. Terms is nil while the publisher has not chosen them yet, which
// only a PENDING_MANIFEST listing can be: the table's
// publisher_domain_listings_active_has_terms CHECK refuses an ACTIVE listing
// whose terms are not a JSON object.
//
// VerifiedAt is nil until the first manifest check passes. LastCheckedAt is nil
// until the first check runs at all, and afterwards moves on every check whether
// or not it passed -- so a listing that is Active with a LastCheckedAt well after
// its VerifiedAt is one whose recent checks have been failing.
//
// LastImport is what the automatic import's last run recorded on the listing.
// It is the zero value until the first run: the import reads SitemapURL and
// Terms, and skips a listing whose sitemap is not recorded.
type PublisherDomainListing struct {
	TenantID         string
	Domain           string
	State            string
	ContentPathScope []string
	Terms            []byte
	SitemapURL       string
	// ImportReplacesManualTerms is the publisher's registration answer; false
	// for every listing registered before the question existed.
	ImportReplacesManualTerms bool
	VerifiedAt                *time.Time
	LastCheckedAt             *time.Time
	LastImport                ListingImport
}

// NewListing is what Create takes. A struct rather than positional arguments
// for the reason NewSite is one: several of its members are strings and a
// transposed pair would compile.
type NewListing struct {
	TenantID                  string
	Domain                    string
	ContentPathScope          []string
	Terms                     []byte
	SitemapURL                string
	ImportReplacesManualTerms bool
}

// ListingTerms is what SetTerms takes: the terms of one listing that is not
// verified yet, and the two settings chosen with them.
type ListingTerms struct {
	TenantID                  string
	Domain                    string
	Terms                     []byte
	SitemapURL                string
	ImportReplacesManualTerms bool
}

// PublisherListingReadRepo is the read-only listing contract. The catalog push
// path holds it to check that the pushed site is active on this seat, and the
// console's domain list holds it to show one payee's sites. Every port is
// tenant-scoped, because the listing IS the tenant-scoped half of the model.
type PublisherListingReadRepo interface {
	// ByDomain reads one seat's listing of one site.
	ByDomain(ctx context.Context, tenantID, domain string) (PublisherDomainListing, error)
	// ByPublisher lists every site one payee has listed on one seat, ordered by
	// domain.
	ByPublisher(ctx context.Context, tenantID, publisherID string) ([]PublisherDomainListing, error)
}

// PublisherListingWriteRepo is the write contract held by the registration
// service: RegisterDomain lists a site, CheckDomain verifies it, and
// SetDomainTerms sets the terms of one not verified yet. Every port takes an
// explicit pgx.Tx: each runs inside the transaction that also writes the call's
// audit row, and a listing that went active without its audit row is a state
// change nobody can account for.
type PublisherListingWriteRepo interface {
	// Create lists a site on a seat. The state is not a parameter -- a listing
	// always starts PendingManifest, and letting a caller choose would let
	// registration create a proven listing without a manifest. Returns
	// ErrListingExists for a site already listed on this seat and
	// ErrPublisherDomainNotFound when the site is not registered.
	Create(ctx context.Context, tx pgx.Tx, in NewListing) (PublisherDomainListing, error)
	// Activate records a manifest check that passed: the listing goes Active and
	// both timestamps move to the moment of the check. It returns the row as
	// written, for a caller that reads it inside the same transaction;
	// CheckDomain only needs to know that a row was written, and reads the
	// listing back after it commits. Returns ErrListingNotFound when the seat
	// has no listing for the site.
	Activate(ctx context.Context, tx pgx.Tx, tenantID, domain string) (PublisherDomainListing, error)
	// RecordCheck records a manifest check that did NOT pass. Only
	// LastCheckedAt moves; the state and VerifiedAt are left alone, so the row
	// keeps saying when the site last proved itself. A failed check does not
	// revoke an active listing from here -- the push path is what stops a site
	// whose manifest has gone stale, and it re-reads the manifest every time.
	RecordCheck(ctx context.Context, tx pgx.Tx, tenantID, domain string) (int64, error)
	// SetTerms sets the terms of a listing that is still PendingManifest, with
	// the sitemap and the import's take-over answer, and returns the row as
	// written. Returns ErrListingNotPending when the seat has no such listing
	// or it is Active: an Active listing is sold under its terms, and the
	// state is in the write's own predicate so an activation that lands first
	// is never overwritten.
	SetTerms(ctx context.Context, tx pgx.Tx, in ListingTerms) (PublisherDomainListing, error)
	// CountPending counts the sites one payee has saved on one seat and not
	// verified yet: its listings still PendingManifest. It reads inside tx, so
	// a registration that locked the payee first counts what is committed and
	// nothing a concurrent registration for the same payee could add after.
	CountPending(ctx context.Context, tx pgx.Tx, tenantID, publisherID string) (int64, error)
}

// ErrListingNotFound is returned when a seat has no listing for the site.
var ErrListingNotFound = errors.New("repo: publisher domain listing not found")

// ErrListingNotPending is returned when terms are set on a listing that is not
// pending: one that is active, or that is not there.
var ErrListingNotPending = errors.New("repo: publisher domain listing is not pending")

// ErrListingExists is returned when listing a site that this seat already lists.
// Not an upsert: a second Create carries its own path scope and licensing, and
// silently overwriting the agreed ones would change what the site offers.
var ErrListingExists = errors.New("repo: publisher domain already listed on this tenant")

// NewPublisherListingReadRepo composes the read-only listing ports over the fat
// sqlc.Querier.
func NewPublisherListingReadRepo(q sqlc.Querier) PublisherListingReadRepo {
	return &publisherListingRepo{q: q}
}

// NewPublisherListingWriteRepo composes the listing write ports over the fat
// sqlc.Querier.
func NewPublisherListingWriteRepo(q sqlc.Querier) PublisherListingWriteRepo {
	return &publisherListingRepo{q: q}
}

type publisherListingRepo struct{ q sqlc.Querier }

func (r *publisherListingRepo) ByDomain(
	ctx context.Context, tenantID, domain string,
) (PublisherDomainListing, error) {
	row, err := r.q.GetPublisherDomainListing(ctx, sqlc.GetPublisherDomainListingParams{
		TenantID: tenantID,
		Domain:   domain,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublisherDomainListing{}, ErrListingNotFound
		}
		return PublisherDomainListing{}, fmt.Errorf("get publisher domain listing: %w", err)
	}
	return listingFromRow(row), nil
}

func (r *publisherListingRepo) ByPublisher(
	ctx context.Context, tenantID, publisherID string,
) ([]PublisherDomainListing, error) {
	rows, err := r.q.ListPublisherDomainListings(ctx, sqlc.ListPublisherDomainListingsParams{
		TenantID:    tenantID,
		PublisherID: publisherID,
	})
	if err != nil {
		return nil, fmt.Errorf("list publisher domain listings: %w", err)
	}
	out := make([]PublisherDomainListing, 0, len(rows))
	for _, row := range rows {
		out = append(out, listingFromRow(row))
	}
	return out, nil
}

func (r *publisherListingRepo) Create(
	ctx context.Context, tx pgx.Tx, in NewListing,
) (PublisherDomainListing, error) {
	// A nil scope and an empty one mean the same thing -- the whole site is
	// offered -- and the column is NOT NULL, so nil is normalized here rather
	// than relied on to encode as an empty array.
	scope := in.ContentPathScope
	if scope == nil {
		scope = []string{}
	}
	row, err := sqlc.New(tx).CreatePublisherDomainListing(ctx, sqlc.CreatePublisherDomainListingParams{
		TenantID:         in.TenantID,
		Domain:           in.Domain,
		ContentPathScope: scope,
		Terms:            in.Terms,
		SitemapUrl:       in.SitemapURL,
		// Set with the terms: here, or by SetTerms while the listing is pending.
		ImportReplacesManualTerms: in.ImportReplacesManualTerms,
	})
	if err != nil {
		return PublisherDomainListing{}, mapInsertErr(
			err, "create publisher domain listing", ErrListingExists,
			fkSentinel{"publisher_domain_listings_domain_fk", ErrPublisherDomainNotFound},
			fkSentinel{"publisher_domain_listings_tenant_fk", ErrTenantNotFound})
	}
	return listingFromRow(row), nil
}

func (r *publisherListingRepo) Activate(
	ctx context.Context, tx pgx.Tx, tenantID, domain string,
) (PublisherDomainListing, error) {
	row, err := sqlc.New(tx).ActivatePublisherDomainListing(ctx,
		sqlc.ActivatePublisherDomainListingParams{TenantID: tenantID, Domain: domain})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublisherDomainListing{}, ErrListingNotFound
		}
		return PublisherDomainListing{}, fmt.Errorf("activate publisher domain listing: %w", err)
	}
	return listingFromRow(row), nil
}

func (r *publisherListingRepo) RecordCheck(
	ctx context.Context, tx pgx.Tx, tenantID, domain string,
) (int64, error) {
	n, err := sqlc.New(tx).RecordPublisherDomainListingCheck(ctx,
		sqlc.RecordPublisherDomainListingCheckParams{TenantID: tenantID, Domain: domain})
	if err != nil {
		return 0, fmt.Errorf("record publisher domain listing check: %w", err)
	}
	return n, nil
}

func (r *publisherListingRepo) SetTerms(
	ctx context.Context, tx pgx.Tx, in ListingTerms,
) (PublisherDomainListing, error) {
	row, err := sqlc.New(tx).SetPendingPublisherDomainListingTerms(ctx,
		sqlc.SetPendingPublisherDomainListingTermsParams{
			Terms:                     in.Terms,
			SitemapUrl:                in.SitemapURL,
			ImportReplacesManualTerms: in.ImportReplacesManualTerms,
			TenantID:                  in.TenantID,
			Domain:                    in.Domain,
		})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublisherDomainListing{}, ErrListingNotPending
		}
		return PublisherDomainListing{}, fmt.Errorf("set publisher domain listing terms: %w", err)
	}
	return listingFromRow(row), nil
}

func (r *publisherListingRepo) CountPending(
	ctx context.Context, tx pgx.Tx, tenantID, publisherID string,
) (int64, error) {
	n, err := sqlc.New(tx).CountPendingPublisherDomainListings(ctx,
		sqlc.CountPendingPublisherDomainListingsParams{TenantID: tenantID, PublisherID: publisherID})
	if err != nil {
		return 0, fmt.Errorf("count pending publisher domain listings: %w", err)
	}
	return n, nil
}

func listingFromRow(row sqlc.ForaPublisherDomainListing) PublisherDomainListing {
	return PublisherDomainListing{
		TenantID:                  row.TenantID,
		Domain:                    row.Domain,
		State:                     string(row.State),
		ContentPathScope:          row.ContentPathScope,
		Terms:                     row.Terms,
		SitemapURL:                row.SitemapUrl,
		ImportReplacesManualTerms: row.ImportReplacesManualTerms,
		VerifiedAt:                timeFromPG(row.VerifiedAt),
		LastCheckedAt:             timeFromPG(row.LastCheckedAt),
		LastImport:                lastImportFromRow(row),
	}
}
