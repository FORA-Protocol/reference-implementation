package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// The provisioning ports create the rows a deployment's operator declares in the
// Exchange's boot fixture (ADR-029): seats, payees, sites and ACTIVE listings.
// Every method creates its row unless the row's key is already stored, and
// returns the row stored under that key with whether this call created it. A
// stored row is never changed, so a boot after the first leaves alone what a
// product surface or an operator has set since. Every method runs in the
// caller's transaction, so a fixture applies whole or not at all.

// NewSeat is a seat to create: a fora.tenants row with both of its policies. A
// struct for the reason NewSite is one: four of its members are strings.
type NewSeat struct {
	TenantID      string
	Domain        string
	Ed25519KeyRef string
	// RSAKeyRef empty stores NULL: the seat signs no CloudFront URL.
	RSAKeyRef                  string
	AllowBrokerRelay           bool
	ActivateNewAgentsByDefault bool
}

// ErrSeatDomainTaken is returned by CreateSeatIfAbsent when another seat already
// holds the declared seat's domain.
var ErrSeatDomainTaken = errors.New("repo: another seat holds the seat's domain")

// SeatProvisionRepo creates seats. Nothing that serves a request creates one: a
// deployment provisions its seats, and the integration tests arrange theirs
// through this port too.
type SeatProvisionRepo interface {
	// CreateSeatIfAbsent creates seat unless a seat holds its tenant id or its
	// domain. It returns the seat stored under the tenant id, and whether this
	// call created it; a seat stored under the id with another domain comes back
	// too, for the caller to judge. ErrSeatDomainTaken means another seat holds
	// the domain.
	CreateSeatIfAbsent(ctx context.Context, tx pgx.Tx, seat NewSeat) (Tenant, bool, error)
}

// NewSeatProvisionRepo composes the seat-creation port over the fat
// sqlc.Querier, on the same concrete repository as the other tenant ports.
func NewSeatProvisionRepo(q sqlc.Querier) SeatProvisionRepo { return &tenantRepo{q: q} }

func (r *tenantRepo) CreateSeatIfAbsent(ctx context.Context, tx pgx.Tx, seat NewSeat) (Tenant, bool, error) {
	q := sqlc.New(tx)
	row, err := q.CreateTenantIfAbsent(ctx, sqlc.CreateTenantIfAbsentParams{
		TenantID:                   seat.TenantID,
		Domain:                     seat.Domain,
		Ed25519KeyRef:              seat.Ed25519KeyRef,
		RsaKeyRef:                  pgText(seat.RSAKeyRef),
		AllowBrokerRelay:           seat.AllowBrokerRelay,
		ActivateNewAgentsByDefault: seat.ActivateNewAgentsByDefault,
	})
	if err == nil {
		return tenantFromRow(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, false, fmt.Errorf("create seat: %w", err)
	}
	// No row: a seat holds the tenant id or the domain. Reading the id back
	// inside the same transaction tells the two apart.
	stored, err := q.GetTenantByID(ctx, seat.TenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, false, ErrSeatDomainTaken
	}
	if err != nil {
		return Tenant{}, false, fmt.Errorf("read the stored seat back: %w", err)
	}
	return tenantFromRow(stored), false, nil
}

// NewPublisher is a payee to create.
type NewPublisher struct {
	ID          string
	DisplayName string
}

// NewProvisionedSite is a site to create as an operator provisions it. Unlike
// NewSite, which registration fills, it carries the CloudFront key-pair id an
// AWS_CLOUDFRONT_RSA site needs; empty stores NULL.
type NewProvisionedSite struct {
	Domain              string
	PublisherID         string
	SigningScheme       string
	CDN                 string
	CloudFrontKeyPairID string
}

// NewActiveListing is a listing to create ACTIVE, with the terms it sells under.
// A nil ContentPathScope offers the whole site, as an empty one does.
type NewActiveListing struct {
	TenantID         string
	Domain           string
	ContentPathScope []string
	Terms            []byte
}

// SiteProvisionRepo creates a payee, its sites and their listings as an operator
// declares them. The registration ports refuse a duplicate, and refuse a state
// for a new listing, on purpose: registration mints its ids and proves each
// listing with a manifest. So this is a separate port, which only the boot
// fixture's service holds.
type SiteProvisionRepo interface {
	CreatePublisherIfAbsent(ctx context.Context, tx pgx.Tx, p NewPublisher) (Publisher, bool, error)
	// CreateSiteIfAbsent returns the stored site whichever payee owns it; the
	// caller refuses one owned by another payee.
	CreateSiteIfAbsent(ctx context.Context, tx pgx.Tx, s NewProvisionedSite) (PublisherDomain, bool, error)
	// CreateActiveListingIfAbsent returns the stored listing in whatever state it
	// is in; a PENDING_MANIFEST listing stays pending.
	CreateActiveListingIfAbsent(
		ctx context.Context, tx pgx.Tx, l NewActiveListing,
	) (PublisherDomainListing, bool, error)
}

// NewSiteProvisionRepo returns the site-provisioning port. It keeps no querier:
// every method runs in the transaction its caller passes.
func NewSiteProvisionRepo() SiteProvisionRepo { return siteProvisionRepo{} }

type siteProvisionRepo struct{}

func (siteProvisionRepo) CreatePublisherIfAbsent(
	ctx context.Context, tx pgx.Tx, p NewPublisher,
) (Publisher, bool, error) {
	q := sqlc.New(tx)
	row, err := q.CreatePublisherIfAbsent(ctx, sqlc.CreatePublisherIfAbsentParams{
		PublisherID: p.ID,
		DisplayName: p.DisplayName,
	})
	if err == nil {
		return publisherFromRow(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Publisher{}, false, fmt.Errorf("create publisher: %w", err)
	}
	stored, err := q.GetPublisher(ctx, p.ID)
	if err != nil {
		return Publisher{}, false, fmt.Errorf("read the stored publisher back: %w", err)
	}
	return publisherFromRow(stored), false, nil
}

func (siteProvisionRepo) CreateSiteIfAbsent(
	ctx context.Context, tx pgx.Tx, s NewProvisionedSite,
) (PublisherDomain, bool, error) {
	q := sqlc.New(tx)
	row, err := q.CreatePublisherDomainIfAbsent(ctx, sqlc.CreatePublisherDomainIfAbsentParams{
		Domain:              s.Domain,
		PublisherID:         s.PublisherID,
		SigningScheme:       sqlc.ForaSigningScheme(s.SigningScheme),
		Cdn:                 s.CDN,
		CloudfrontKeyPairID: pgText(s.CloudFrontKeyPairID),
	})
	if err == nil {
		return publisherDomainFromRow(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PublisherDomain{}, false, mapInsertErr(err, "create site", nil,
			fkSentinel{"publisher_domains_publisher_fk", ErrPublisherNotFound})
	}
	stored, err := q.GetPublisherDomain(ctx, s.Domain)
	if err != nil {
		return PublisherDomain{}, false, fmt.Errorf("read the stored site back: %w", err)
	}
	return publisherDomainFromRow(stored), false, nil
}

func (siteProvisionRepo) CreateActiveListingIfAbsent(
	ctx context.Context, tx pgx.Tx, l NewActiveListing,
) (PublisherDomainListing, bool, error) {
	q := sqlc.New(tx)
	row, err := q.CreateActivePublisherDomainListingIfAbsent(ctx, sqlc.CreateActivePublisherDomainListingIfAbsentParams{
		TenantID:         l.TenantID,
		Domain:           l.Domain,
		ContentPathScope: listingScope(l.ContentPathScope),
		Terms:            l.Terms,
	})
	if err == nil {
		return listingFromRow(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PublisherDomainListing{}, false, mapInsertErr(err, "create active listing", nil,
			fkSentinel{"publisher_domain_listings_tenant_fk", ErrTenantNotFound},
			fkSentinel{"publisher_domain_listings_domain_fk", ErrPublisherDomainNotFound})
	}
	stored, err := q.GetPublisherDomainListing(ctx, sqlc.GetPublisherDomainListingParams{
		TenantID: l.TenantID,
		Domain:   l.Domain,
	})
	if err != nil {
		return PublisherDomainListing{}, false, fmt.Errorf("read the stored listing back: %w", err)
	}
	return listingFromRow(stored), false, nil
}
