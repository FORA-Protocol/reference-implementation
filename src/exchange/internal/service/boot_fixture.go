package service

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// The audit row of what a boot fixture creates. A boot has no caller, so the
// source names the fixture as the origin and the detail names its file.
const (
	bootFixtureSource        = "boot-fixture"
	bootFixtureActionSeat    = "BootFixtureCreateSeat"
	bootFixtureActionListing = "BootFixtureCreateListing"
)

// bootSeatAudit is the detail of the audit row a seat the fixture creates
// writes: the file it came from, and the seat as created.
type bootSeatAudit struct {
	Fixture                    string `json:"fixture"`
	FixtureSHA256              string `json:"fixture_sha256"`
	Domain                     string `json:"domain"`
	Ed25519KeyRef              string `json:"ed25519_key_ref"`
	RSAKeyRef                  string `json:"rsa_key_ref"`
	AllowBrokerRelay           bool   `json:"allow_broker_relay"`
	ActivateNewAgentsByDefault bool   `json:"activate_new_agents_by_default"`
}

// bootListingAudit is the detail of the audit row a listing the fixture creates
// writes. The signing scheme and CDN are the stored site's, which the listing
// sells under even where the file declares them otherwise. The scope, terms and
// state are the stored listing's; the terms are encoded as registration encodes
// them, and they are recorded because the fixture's sha256 covers the fixture
// file, not the terms files it names.
type bootListingAudit struct {
	Fixture          string          `json:"fixture"`
	FixtureSHA256    string          `json:"fixture_sha256"`
	Domain           string          `json:"domain"`
	PublisherID      string          `json:"publisher_id"`
	PublisherCreated bool            `json:"publisher_created"`
	SiteCreated      bool            `json:"site_created"`
	SigningScheme    string          `json:"signing_scheme"`
	CDN              string          `json:"cdn"`
	ContentPathScope []string        `json:"content_path_scope"`
	Terms            json.RawMessage `json:"terms"`
	State            string          `json:"state"`
}

// BootKeys is the one key lookup Apply makes: whether a seat's Ed25519 key
// reference names a key this Exchange holds. A seat's RSA key is resolved only
// when a CloudFront URL is first signed, on purpose, so Apply does not look it up.
type BootKeys interface {
	Ed25519(ref string) (ed25519.PublicKey, ed25519.PrivateKey, error)
}

// BootFixtureService creates the rows a boot fixture declares when they are
// absent (ADR-029). It runs once per boot, before the Exchange serves anything,
// and never updates or deletes a stored row: a row already stored is reported as
// kept, and a setting that differs from the fixture as drift.
type BootFixtureService struct {
	seats repo.SeatProvisionRepo
	sites repo.SiteProvisionRepo
	audit repo.AuditRepo
	tx    db.TxRunner
	keys  BootKeys
}

// BootFixtureDeps bundles the BootFixtureService wiring.
type BootFixtureDeps struct {
	Seats    repo.SeatProvisionRepo
	Sites    repo.SiteProvisionRepo
	Audit    repo.AuditRepo
	TxRunner db.TxRunner
	Keys     BootKeys
}

// NewBootFixtureService assembles the service.
func NewBootFixtureService(d BootFixtureDeps) *BootFixtureService {
	return &BootFixtureService{seats: d.Seats, sites: d.Sites, audit: d.Audit, tx: d.TxRunner, keys: d.Keys}
}

// NewBootFixtureServiceFromPool assembles the service from a pool and queries,
// the composition-root convenience cmd/server and the integration tests share.
func NewBootFixtureServiceFromPool(pool *pgxpool.Pool, queries *sqlc.Queries, keys BootKeys) *BootFixtureService {
	return NewBootFixtureService(BootFixtureDeps{
		Seats:    repo.NewSeatProvisionRepo(queries),
		Sites:    repo.NewSiteProvisionRepo(),
		Audit:    repo.NewAuditRepo(queries),
		TxRunner: db.PoolRunner{Pool: pool},
		Keys:     keys,
	})
}

// BootReport says what Apply did: the rows it created, the declared rows it
// found already stored, and the stored rows whose settings differ from what the
// fixture declares.
type BootReport struct {
	Created []string
	Kept    []string
	Drift   []BootDrift
}

// BootDrift is one stored row whose settings differ from the fixture. The
// stored values are kept; Fields names the settings that differ.
type BootDrift struct {
	Row    string
	Fields []string
}

// Apply creates the rows plan declares that are not stored yet, in one
// transaction, so a fixture applies whole or not at all. It refuses the whole
// fixture, writing nothing, when a seat names an Ed25519 key this Exchange does
// not hold, or when a declared row's identity is stored differently: a seat id
// stored with another domain, a seat domain another seat holds, or a site
// registered to another payee. A fixture must not move a domain from one owner
// to another.
func (s *BootFixtureService) Apply(ctx context.Context, plan BootPlan) (BootReport, error) {
	for _, seat := range plan.Seats {
		if _, _, err := s.keys.Ed25519(seat.Ed25519KeyRef); err != nil {
			return BootReport{}, exchange.Newf(exchange.KindFailedPrecondition,
				"seat %q names the Ed25519 key %q, which this Exchange does not hold", seat.TenantID, seat.Ed25519KeyRef)
		}
	}
	var report BootReport
	err := s.tx.WithTx(ctx, func(tx pgx.Tx) error {
		a := bootApply{svc: s, tx: tx, plan: plan, report: &BootReport{}}
		if err := a.applySeats(ctx); err != nil {
			return err
		}
		if err := a.applyPublishers(ctx); err != nil {
			return err
		}
		report = *a.report
		return nil
	})
	if err != nil {
		return BootReport{}, err
	}
	return report, nil
}

// bootApply is one Apply's transaction and what it has done so far.
type bootApply struct {
	svc    *BootFixtureService
	tx     pgx.Tx
	plan   BootPlan
	report *BootReport
}

func (a bootApply) applySeats(ctx context.Context) error {
	for _, seat := range a.plan.Seats {
		stored, created, err := a.svc.seats.CreateSeatIfAbsent(ctx, a.tx, seat)
		row := "seat " + seat.TenantID
		switch {
		case errors.Is(err, repo.ErrSeatDomainTaken):
			return exchange.Newf(exchange.KindFailedPrecondition,
				"%s: another seat already holds the domain %q", row, seat.Domain)
		case err != nil:
			return exchange.Wrap(exchange.KindInternal, err, "create "+row)
		case created:
			a.report.Created = append(a.report.Created, row)
			if err := a.auditSeat(ctx, seat); err != nil {
				return err
			}
		case stored.Domain != seat.Domain:
			return exchange.Newf(exchange.KindFailedPrecondition,
				"%s is stored with the domain %q, and the fixture declares %q", row, stored.Domain, seat.Domain)
		default:
			a.kept(row, seatDrift(seat, stored))
		}
	}
	return nil
}

func (a bootApply) applyPublishers(ctx context.Context) error {
	for _, p := range a.plan.Publishers {
		stored, created, err := a.svc.sites.CreatePublisherIfAbsent(ctx, a.tx, p.Publisher)
		row := "publisher " + p.Publisher.ID
		if err != nil {
			return exchange.Wrap(exchange.KindInternal, err, "create "+row)
		}
		if created {
			a.report.Created = append(a.report.Created, row)
		} else {
			a.kept(row, publisherDrift(p.Publisher, stored))
		}
		for _, site := range p.Sites {
			if err := a.applySite(ctx, site, created); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a bootApply) applySite(ctx context.Context, planned PlannedSite, publisherCreated bool) error {
	site := planned.Site
	stored, created, err := a.svc.sites.CreateSiteIfAbsent(ctx, a.tx, site)
	row := "site " + site.Domain
	switch {
	case err != nil:
		return exchange.Wrap(exchange.KindInternal, err, "create "+row)
	case created:
		a.report.Created = append(a.report.Created, row)
	case stored.PublisherID != site.PublisherID:
		return exchange.Newf(exchange.KindFailedPrecondition,
			"%s is registered to publisher %q, and the fixture declares it under %q",
			row, stored.PublisherID, site.PublisherID)
	default:
		a.kept(row, siteDrift(site, stored))
	}
	for _, listing := range planned.Listings {
		origin := listingOrigin{publisherCreated: publisherCreated, siteCreated: created}
		if err := a.applyListing(ctx, stored, listing, origin); err != nil {
			return err
		}
	}
	return nil
}

// listingOrigin says whether this Apply created the payee and the site a new
// listing belongs to, which the listing's audit row records: neither has a
// tenant of its own to be audited under.
type listingOrigin struct {
	publisherCreated bool
	siteCreated      bool
}

// applyListing creates listing on site, the site as stored, unless it is
// stored already.
func (a bootApply) applyListing(
	ctx context.Context, site repo.PublisherDomain, listing repo.NewActiveListing, origin listingOrigin,
) error {
	stored, created, err := a.svc.sites.CreateActiveListingIfAbsent(ctx, a.tx, listing)
	row := fmt.Sprintf("listing of %s on seat %s", listing.Domain, listing.TenantID)
	if err != nil {
		return exchange.Wrap(exchange.KindInternal, err, "create "+row)
	}
	if !created {
		a.kept(row, listingDrift(listing, stored))
		return nil
	}
	a.report.Created = append(a.report.Created, row)
	return a.auditListing(ctx, site, stored, origin)
}

// kept records a declared row found already stored, and its drift if any.
func (a bootApply) kept(row string, drift []string) {
	a.report.Kept = append(a.report.Kept, row)
	if len(drift) > 0 {
		a.report.Drift = append(a.report.Drift, BootDrift{Row: row, Fields: drift})
	}
}

func (a bootApply) auditSeat(ctx context.Context, seat repo.NewSeat) error {
	detail, err := marshalAuditDetail(bootSeatAudit{
		Fixture:                    a.plan.Path,
		FixtureSHA256:              a.plan.SHA256,
		Domain:                     seat.Domain,
		Ed25519KeyRef:              seat.Ed25519KeyRef,
		RSAKeyRef:                  seat.RSAKeyRef,
		AllowBrokerRelay:           seat.AllowBrokerRelay,
		ActivateNewAgentsByDefault: seat.ActivateNewAgentsByDefault,
	})
	if err != nil {
		return err
	}
	return appendAudit(ctx, a.tx, a.svc.audit, repo.AuditEntry{
		SourceAddr: bootFixtureSource, Action: bootFixtureActionSeat, Detail: detail, TenantID: seat.TenantID,
	})
}

func (a bootApply) auditListing(
	ctx context.Context, site repo.PublisherDomain, listing repo.PublisherDomainListing, origin listingOrigin,
) error {
	detail, err := marshalAuditDetail(bootListingAudit{
		Fixture:          a.plan.Path,
		FixtureSHA256:    a.plan.SHA256,
		Domain:           listing.Domain,
		PublisherID:      site.PublisherID,
		PublisherCreated: origin.publisherCreated,
		SiteCreated:      origin.siteCreated,
		SigningScheme:    site.SigningScheme,
		CDN:              site.CDN,
		ContentPathScope: listing.ContentPathScope,
		Terms:            json.RawMessage(listing.Terms),
		State:            listing.State,
	})
	if err != nil {
		return err
	}
	return appendAudit(ctx, a.tx, a.svc.audit, repo.AuditEntry{
		SourceAddr: bootFixtureSource, Action: bootFixtureActionListing, Detail: detail, TenantID: listing.TenantID,
	})
}
