//go:build integration

package repo_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// These are persistence round-trips: repository port -> sqlc -> Postgres and
// back, arranged and asserted through the production repository surfaces and
// nothing below them. They stop at that tier because the RPC that will own these
// tables does not exist yet -- registration ships in a later slice -- so there is
// no outermost public surface to drive. Naming that plainly is the point: a write
// that goes repo -> DB and a read that comes back the same way is a persistence
// round-trip, not a protocol one, and this file never claims the longer word.
//
// The tenant seed is one tier lower still. No production code creates a tenant
// (an operator provisions it), so there is no repository port to arrange
// through, and these tests call the generated sqlc InsertTenant query directly
// -- the same held-open corner the package's sibling suites document.

// seedTenantRow inserts the seat every tenant-scoped assertion below needs.
func seedTenantRow(t *testing.T, ctx context.Context, q sqlc.Querier, tenantID, domain string) {
	t.Helper()
	if _, err := q.InsertTenant(ctx, sqlc.InsertTenantParams{
		TenantID: tenantID, Domain: domain, Ed25519KeyRef: "k",
		ReportingPolicy: []byte(`{}`),
	}); err != nil {
		t.Fatalf("seed tenant %q: %v", tenantID, err)
	}
}

// createPublisher drives PublisherWriteRepo.Create through a real transaction,
// which is the only way to call it: the port takes a pgx.Tx because in
// production the payee commits with the grant that makes it reachable.
func createPublisher(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, displayName string,
) (repo.Publisher, error) {
	t.Helper()
	var p repo.Publisher
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var terr error
		p, terr = repo.NewPublisherWriteRepo(sqlc.New(pool)).Create(ctx, tx, id, displayName)
		return terr
	})
	return p, err
}

// createDomain drives PublisherDomainWriteRepo.Create through a real transaction.
func createDomain(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, domain, publisherID, scheme string,
) (repo.PublisherDomain, error) {
	t.Helper()
	var d repo.PublisherDomain
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var terr error
		d, terr = repo.NewPublisherDomainWriteRepo(sqlc.New(pool)).
			Create(ctx, tx, repo.NewSite{
				Domain:        domain,
				PublisherID:   publisherID,
				SigningScheme: scheme,
				CDN:           "cloudflare",
			})
		return terr
	})
	return d, err
}

// grant drives PublisherGrantWriteRepo.Grant through a real transaction.
func grant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, g repo.PublisherGrant) error {
	t.Helper()
	return db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return repo.NewPublisherGrantWriteRepo(sqlc.New(pool)).Grant(ctx, tx, g)
	})
}

// TestPublisherRegistrationRoundTrip walks the three writes a registration makes
// -- the payee, its site, and the grant that lets a person act for it -- and
// reads each one back through the read port a later consumer will hold.
func TestPublisherRegistrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	q := sqlc.New(pool)
	const (
		tenantID    = "t-registration"
		publisherID = "pub-stoa"
		site        = "stoapress.example"
		issuer      = "https://login.example"
		subject     = "sub-1"
	)
	seedTenantRow(t, ctx, q, tenantID, "exchange.example")

	created, err := createPublisher(t, ctx, pool, publisherID, "Stoa Press")
	if err != nil {
		t.Fatalf("Create publisher: %v", err)
	}
	if created.ID != publisherID || created.DisplayName != "Stoa Press" {
		t.Fatalf("Create returned (%q, %q), want (%q, %q)",
			created.ID, created.DisplayName, publisherID, "Stoa Press")
	}
	if created.CreatedAt.IsZero() {
		t.Error("Create returned a zero CreatedAt; the column defaults to NOW()")
	}
	back, err := repo.NewPublisherReadRepo(q).ByID(ctx, publisherID)
	if err != nil {
		t.Fatalf("ByID after Create: %v", err)
	}
	if back.DisplayName != "Stoa Press" {
		t.Errorf("ByID DisplayName = %q, want %q", back.DisplayName, "Stoa Press")
	}

	domain, err := createDomain(t, ctx, pool, site, publisherID, "ED25519")
	if err != nil {
		t.Fatalf("Create domain: %v", err)
	}
	if domain.PublisherID != publisherID {
		t.Errorf("Create domain PublisherID = %q, want %q", domain.PublisherID, publisherID)
	}
	if domain.CloudFrontKeyPairID != "" {
		t.Errorf("a freshly registered site carries CloudFrontKeyPairID = %q, want empty",
			domain.CloudFrontKeyPairID)
	}
	domainBack, err := repo.NewPublisherDomainReadRepo(q).ByDomain(ctx, site)
	if err != nil {
		t.Fatalf("ByDomain after Create: %v", err)
	}
	if domainBack.SigningScheme != "ED25519" {
		t.Errorf("ByDomain SigningScheme = %q, want ED25519", domainBack.SigningScheme)
	}

	grantAndAssertHeld(t, ctx, pool, repo.PublisherGrant{
		TenantID: tenantID, PublisherID: publisherID, OIDCIssuer: issuer, OIDCSubject: subject,
	})
}

// grantAndAssertHeld writes g and reads it back through both grant read
// methods: the actor holds the grant, and it is the actor's only publisher.
func grantAndAssertHeld(t *testing.T, ctx context.Context, pool *pgxpool.Pool, g repo.PublisherGrant) {
	t.Helper()
	if err := grant(t, ctx, pool, g); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	grants := repo.NewPublisherGrantReadRepo(sqlc.New(pool))
	has, err := grants.Has(ctx, g.TenantID, g.PublisherID, g.OIDCIssuer, g.OIDCSubject)
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if !has {
		t.Error("Has reports no grant for the actor the registration just granted")
	}
	ids, err := grants.PublisherIDsForActor(ctx, g.TenantID, g.OIDCIssuer, g.OIDCSubject)
	if err != nil {
		t.Fatalf("PublisherIDsForActor: %v", err)
	}
	if len(ids) != 1 || ids[0] != g.PublisherID {
		t.Errorf("PublisherIDsForActor = %v, want [%q]", ids, g.PublisherID)
	}
}

// TestCreatePublisherRejectsDuplicateID is the negative path for the minted id.
// A collision means a broken minter, so the second Create is refused and the
// display name the first one stored is left alone -- an upsert here would let a
// later registration rename someone else's payee.
func TestCreatePublisherRejectsDuplicateID(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	const publisherID = "pub-collision"

	if _, err := createPublisher(t, ctx, pool, publisherID, "Stoa Press"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := createPublisher(t, ctx, pool, publisherID, "Harmonia Records"); !errors.Is(err, repo.ErrPublisherExists) {
		t.Fatalf("second Create error = %v, want repo.ErrPublisherExists", err)
	}
	back, err := repo.NewPublisherReadRepo(sqlc.New(pool)).ByID(ctx, publisherID)
	if err != nil {
		t.Fatalf("ByID after the refused Create: %v", err)
	}
	if back.DisplayName != "Stoa Press" {
		t.Errorf("DisplayName = %q after a refused Create, want the first one's %q",
			back.DisplayName, "Stoa Press")
	}
}

// TestCreateDomainRejectsUnknownPublisher is the negative path for the site's
// reference to its payee: an unregistered publisher id is refused, and no site
// row is left behind.
func TestCreateDomainRejectsUnknownPublisher(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	const site = "ghost.example"

	if _, err := createDomain(t, ctx, pool, site, "pub-nobody", "ED25519"); !errors.Is(err, repo.ErrPublisherNotFound) {
		t.Fatalf("Create domain error = %v, want repo.ErrPublisherNotFound", err)
	}
	if _, err := repo.NewPublisherDomainReadRepo(sqlc.New(pool)).ByDomain(ctx, site); !errors.Is(err, repo.ErrPublisherDomainNotFound) {
		t.Fatalf("ByDomain after the refused Create = %v, want repo.ErrPublisherDomainNotFound", err)
	}
}

// TestCreateDomainRejectsSecondRegistration is the negative path for the site's
// one-publisher rule. A site belongs to exactly one payee, so registering an
// already-registered site under a second payee is refused rather than moved --
// silently moving it would redirect the site's revenue.
func TestCreateDomainRejectsSecondRegistration(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	const site = "contested.example"

	if _, err := createPublisher(t, ctx, pool, "pub-first", "Stoa Press"); err != nil {
		t.Fatalf("Create first publisher: %v", err)
	}
	if _, err := createPublisher(t, ctx, pool, "pub-second", "Harmonia Records"); err != nil {
		t.Fatalf("Create second publisher: %v", err)
	}
	if _, err := createDomain(t, ctx, pool, site, "pub-first", "ED25519"); err != nil {
		t.Fatalf("first Create domain: %v", err)
	}
	if _, err := createDomain(t, ctx, pool, site, "pub-second", "ED25519"); !errors.Is(err, repo.ErrPublisherDomainExists) {
		t.Fatalf("second Create domain error = %v, want repo.ErrPublisherDomainExists", err)
	}
	back, err := repo.NewPublisherDomainReadRepo(sqlc.New(pool)).ByDomain(ctx, site)
	if err != nil {
		t.Fatalf("ByDomain after the refused Create: %v", err)
	}
	if back.PublisherID != "pub-first" {
		t.Errorf("site now belongs to %q; the refused registration moved it away from %q",
			back.PublisherID, "pub-first")
	}
}

// TestSetCloudFrontKeyPairIDOnlyOnRSASites pins the write guard on the key-pair
// id. An Ed25519 site has no CloudFront key group, so writing an id onto one
// would store a value nothing reads; the port reports the no-op instead of
// success. The AWS-fronted site takes the same call and keeps the id.
func TestSetCloudFrontKeyPairIDOnlyOnRSASites(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	q := sqlc.New(pool)
	const (
		edSite  = "cloudflare-site.example"
		rsaSite = "cloudfront-site.example"
	)
	if _, err := createPublisher(t, ctx, pool, "pub-keys", "Harmonia Records"); err != nil {
		t.Fatalf("Create publisher: %v", err)
	}
	if _, err := createDomain(t, ctx, pool, edSite, "pub-keys", "ED25519"); err != nil {
		t.Fatalf("Create ed25519 site: %v", err)
	}
	if _, err := createDomain(t, ctx, pool, rsaSite, "pub-keys", "AWS_CLOUDFRONT_RSA"); err != nil {
		t.Fatalf("Create rsa site: %v", err)
	}
	domains := repo.NewPublisherDomainWriteRepo(q)
	setKeyPairID := func(domain, publisherID, id string) int64 {
		t.Helper()
		var n int64
		if err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			var terr error
			n, terr = domains.SetCloudFrontKeyPairID(ctx, tx, domain, publisherID, id)
			return terr
		}); err != nil {
			t.Fatalf("SetCloudFrontKeyPairID(%q): %v", domain, err)
		}
		return n
	}

	if n := setKeyPairID(edSite, "pub-keys", "K-SHOULD-NOT-LAND"); n != 0 {
		t.Errorf("SetCloudFrontKeyPairID on an ED25519 site affected %d rows, want 0", n)
	}
	reads := repo.NewPublisherDomainReadRepo(q)
	edBack, err := reads.ByDomain(ctx, edSite)
	if err != nil {
		t.Fatalf("ByDomain(ed25519 site): %v", err)
	}
	if edBack.CloudFrontKeyPairID != "" {
		t.Errorf("ED25519 site holds CloudFrontKeyPairID = %q, want empty", edBack.CloudFrontKeyPairID)
	}

	if n := setKeyPairID(rsaSite, "pub-keys", "K-REAL"); n != 1 {
		t.Errorf("SetCloudFrontKeyPairID on an RSA site affected %d rows, want 1", n)
	}
	rsaBack, err := reads.ByDomain(ctx, rsaSite)
	if err != nil {
		t.Fatalf("ByDomain(rsa site): %v", err)
	}
	if rsaBack.CloudFrontKeyPairID != "K-REAL" {
		t.Errorf("RSA site CloudFrontKeyPairID = %q, want %q", rsaBack.CloudFrontKeyPairID, "K-REAL")
	}
}

// TestSetCloudFrontKeyPairIDIsScopedToThePublisher is the negative path for the
// publisher predicate on the key-pair id write. Sites are deployment-wide and
// the domain is caller input, so without that predicate an actor authorized for
// one payee could overwrite another payee's site -- and the column becomes the
// Key-Pair-Id on that site's delivery URLs, so the edge would refuse content the
// Exchange had already charged for.
//
// Two publishers, one AWS-fronted site belonging to the first. The second
// publisher names the site correctly and still writes nothing.
func TestSetCloudFrontKeyPairIDIsScopedToThePublisher(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	q := sqlc.New(pool)
	const (
		site     = "owned-site.example"
		owner    = "pub-owner"
		stranger = "pub-stranger"
	)
	if _, err := createPublisher(t, ctx, pool, owner, "Stoa Press"); err != nil {
		t.Fatalf("Create owning publisher: %v", err)
	}
	if _, err := createPublisher(t, ctx, pool, stranger, "Harmonia Records"); err != nil {
		t.Fatalf("Create second publisher: %v", err)
	}
	if _, err := createDomain(t, ctx, pool, site, owner, "AWS_CLOUDFRONT_RSA"); err != nil {
		t.Fatalf("Create rsa site: %v", err)
	}
	domains := repo.NewPublisherDomainWriteRepo(q)
	setKeyPairID := func(publisherID, id string) int64 {
		t.Helper()
		var n int64
		if err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			var terr error
			n, terr = domains.SetCloudFrontKeyPairID(ctx, tx, site, publisherID, id)
			return terr
		}); err != nil {
			t.Fatalf("SetCloudFrontKeyPairID(%q, %q): %v", site, publisherID, err)
		}
		return n
	}
	reads := repo.NewPublisherDomainReadRepo(q)

	if n := setKeyPairID(owner, "K-OWNER"); n != 1 {
		t.Fatalf("the owning publisher's write affected %d rows, want 1", n)
	}
	if n := setKeyPairID(stranger, "K-STRANGER"); n != 0 {
		t.Errorf("a second publisher's write affected %d rows, want 0", n)
	}
	back, err := reads.ByDomain(ctx, site)
	if err != nil {
		t.Fatalf("ByDomain(%q): %v", site, err)
	}
	if back.CloudFrontKeyPairID != "K-OWNER" {
		t.Errorf("site CloudFrontKeyPairID = %q, want the owning publisher's %q",
			back.CloudFrontKeyPairID, "K-OWNER")
	}
}

// TestGrantIsScopedToOneSeat is the negative path that makes the many-to-many
// decision structural. A grant on one seat says nothing about another: the same
// person, the same payee, a different seat, and the answer is no.
func TestGrantIsScopedToOneSeat(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	q := sqlc.New(pool)
	const (
		seatA       = "t-seat-a"
		seatB       = "t-seat-b"
		publisherID = "pub-two-seats"
		issuer      = "https://login.example"
		subject     = "sub-1"
	)
	seedTenantRow(t, ctx, q, seatA, "exchange.example")
	seedTenantRow(t, ctx, q, seatB, "other-exchange.example")
	if _, err := createPublisher(t, ctx, pool, publisherID, "Stoa Press"); err != nil {
		t.Fatalf("Create publisher: %v", err)
	}
	if err := grant(t, ctx, pool, repo.PublisherGrant{
		TenantID: seatA, PublisherID: publisherID, OIDCIssuer: issuer, OIDCSubject: subject,
	}); err != nil {
		t.Fatalf("Grant on seat A: %v", err)
	}

	grants := repo.NewPublisherGrantReadRepo(q)
	has, err := grants.Has(ctx, seatB, publisherID, issuer, subject)
	if err != nil {
		t.Fatalf("Has on seat B: %v", err)
	}
	if has {
		t.Error("a grant on one seat answered yes on another: the grant lookup is not seat-scoped")
	}
	ids, err := grants.PublisherIDsForActor(ctx, seatB, issuer, subject)
	if err != nil {
		t.Fatalf("PublisherIDsForActor on seat B: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("PublisherIDsForActor on seat B = %v, want none", ids)
	}
}

// TestGrantRejectsUnknownPublisher is the negative path for the grant's own
// reference: nobody can be granted rights over a payee that does not exist.
func TestGrantRejectsUnknownPublisher(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	const tenantID = "t-grant-ghost"
	seedTenantRow(t, ctx, sqlc.New(pool), tenantID, "exchange.example")

	err := grant(t, ctx, pool, repo.PublisherGrant{
		TenantID: tenantID, PublisherID: "pub-nobody",
		OIDCIssuer: "https://login.example", OIDCSubject: "sub-1",
	})
	if !errors.Is(err, repo.ErrPublisherNotFound) {
		t.Fatalf("Grant error = %v, want repo.ErrPublisherNotFound", err)
	}
}
