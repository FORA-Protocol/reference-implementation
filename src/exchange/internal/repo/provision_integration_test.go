//go:build integration

package repo_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// Persistence round-trips for the provisioning ports, which create the rows an
// operator's boot fixture declares. Each write goes through the port in a real
// transaction and is read back through the same port's answer, because that
// answer is what the boot fixture's service acts on: whether the row was created,
// and what is stored when it was not.

func createSeat(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, seat repo.NewSeat,
) (repo.Tenant, bool, error) {
	t.Helper()
	var stored repo.Tenant
	var created bool
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		stored, created, err = repo.NewSeatProvisionRepo(sqlc.New(pool)).CreateSeatIfAbsent(ctx, tx, seat)
		return err
	})
	return stored, created, err
}

func TestCreateSeatIfAbsentCreatesASeatOnceAndKeepsItAfter(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	declared := repo.NewSeat{
		TenantID: "seat-a", Domain: "exchange.example:8081", Ed25519KeyRef: "exchange-primary",
		RSAKeyRef: "cf-rsa-primary", AllowBrokerRelay: true, ActivateNewAgentsByDefault: false,
	}

	first, created, err := createSeat(t, ctx, pool, declared)
	if err != nil || !created {
		t.Fatalf("first CreateSeatIfAbsent = created %v, err %v; want a created seat", created, err)
	}
	if first.ID != "seat-a" || first.Domain != "exchange.example:8081" || first.RSAKeyRef != "cf-rsa-primary" ||
		!first.AllowBrokerRelay || first.ActivateNewAgentsByDefault {
		t.Fatalf("created seat = %+v, want the declared settings", first)
	}

	changed := declared
	changed.AllowBrokerRelay, changed.ActivateNewAgentsByDefault, changed.RSAKeyRef = false, true, ""
	again, created, err := createSeat(t, ctx, pool, changed)
	if err != nil || created {
		t.Fatalf("second CreateSeatIfAbsent = created %v, err %v; want the stored seat", created, err)
	}
	if !reflect.DeepEqual(again, first) {
		t.Fatalf("stored seat after a second declaration = %+v, want it unchanged: %+v", again, first)
	}
}

func TestCreateSeatIfAbsentRefusesADomainAnotherSeatHolds(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	holder := repo.NewSeat{TenantID: "seat-a", Domain: "exchange.example", Ed25519KeyRef: "k"}
	if _, _, err := createSeat(t, ctx, pool, holder); err != nil {
		t.Fatalf("seed the holder: %v", err)
	}

	_, _, err := createSeat(t, ctx, pool, repo.NewSeat{TenantID: "seat-b", Domain: "exchange.example", Ed25519KeyRef: "k"})
	if !errors.Is(err, repo.ErrSeatDomainTaken) {
		t.Fatalf("a second seat on a held domain: err = %v, want ErrSeatDomainTaken", err)
	}
}

func TestCreateSeatIfAbsentReturnsASeatStoredUnderTheIDWithAnotherDomain(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	if _, _, err := createSeat(t, ctx, pool, repo.NewSeat{TenantID: "seat-a", Domain: "old.example", Ed25519KeyRef: "k"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	stored, created, err := createSeat(t, ctx, pool, repo.NewSeat{TenantID: "seat-a", Domain: "new.example", Ed25519KeyRef: "k"})
	if err != nil || created || stored.Domain != "old.example" {
		t.Fatalf("redeclared under another domain = %+v, created %v, err %v; want the stored seat", stored, created, err)
	}
}

func TestCreatePublisherIfAbsentKeepsAStoredPayee(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	provision := repo.NewSiteProvisionRepo()
	create := func(name string) (repo.Publisher, bool) {
		t.Helper()
		var p repo.Publisher
		var created bool
		if err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			var err error
			p, created, err = provision.CreatePublisherIfAbsent(ctx, tx, repo.NewPublisher{ID: "pub-1", DisplayName: name})
			return err
		}); err != nil {
			t.Fatalf("CreatePublisherIfAbsent(%q): %v", name, err)
		}
		return p, created
	}

	if p, created := create("Stoa Press"); !created || p.DisplayName != "Stoa Press" {
		t.Fatalf("first = %+v, created %v", p, created)
	}
	if p, created := create("Renamed"); created || p.DisplayName != "Stoa Press" {
		t.Fatalf("second = %+v, created %v; want the stored payee unchanged", p, created)
	}
}

// provisionSite runs CreateSiteIfAbsent in its own transaction.
func provisionSite(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, s repo.NewProvisionedSite,
) (repo.PublisherDomain, bool, error) {
	t.Helper()
	var stored repo.PublisherDomain
	var created bool
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		stored, created, err = repo.NewSiteProvisionRepo().CreateSiteIfAbsent(ctx, tx, s)
		return err
	})
	return stored, created, err
}

func TestCreateSiteIfAbsentStoresTheKeyPairIDAndKeepsAStoredOwner(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	for _, id := range []string{"pub-1", "pub-2"} {
		if _, err := createPublisher(t, ctx, pool, id, id); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	declared := repo.NewProvisionedSite{
		Domain: "media.publisher.example", PublisherID: "pub-1",
		SigningScheme: "AWS_CLOUDFRONT_RSA", CDN: "cloudfront", CloudFrontKeyPairID: "KEXAMPLE",
	}

	site, created, err := provisionSite(t, ctx, pool, declared)
	if err != nil || !created {
		t.Fatalf("first = created %v, err %v", created, err)
	}
	if site.PublisherID != "pub-1" || site.SigningScheme != "AWS_CLOUDFRONT_RSA" || site.CDN != "cloudfront" ||
		site.CloudFrontKeyPairID != "KEXAMPLE" {
		t.Fatalf("created site = %+v, want the declared one", site)
	}

	claimed := declared
	claimed.PublisherID, claimed.SigningScheme, claimed.CloudFrontKeyPairID = "pub-2", "ED25519", ""
	stored, created, err := provisionSite(t, ctx, pool, claimed)
	if err != nil || created || stored.PublisherID != "pub-1" || stored.CloudFrontKeyPairID != "KEXAMPLE" {
		t.Fatalf("declared under another payee = %+v, created %v, err %v; want the stored site", stored, created, err)
	}
}

func TestCreateSiteIfAbsentForAnUnknownPayeeIsNotFound(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	_, _, err := provisionSite(t, ctx, pool, repo.NewProvisionedSite{
		Domain: "www.publisher.example", PublisherID: "nobody", SigningScheme: "ED25519",
	})
	if !errors.Is(err, repo.ErrPublisherNotFound) {
		t.Fatalf("a site of an unknown payee: err = %v, want ErrPublisherNotFound", err)
	}
}

// provisionListing runs CreateActiveListingIfAbsent in its own transaction.
func provisionListing(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, l repo.NewActiveListing,
) (repo.PublisherDomainListing, bool, error) {
	t.Helper()
	var stored repo.PublisherDomainListing
	var created bool
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		stored, created, err = repo.NewSiteProvisionRepo().CreateActiveListingIfAbsent(ctx, tx, l)
		return err
	})
	return stored, created, err
}

func TestCreateActiveListingIfAbsentCreatesItActiveAndUnverified(t *testing.T) {
	ctx := context.Background()
	pool := listingFixture(t, ctx, "seat-a", "pub-1", "www.publisher.example")
	declared := repo.NewActiveListing{
		TenantID: "seat-a", Domain: "www.publisher.example",
		ContentPathScope: []string{"/articles/"}, Terms: []byte(termsFree),
	}

	listing, created, err := provisionListing(t, ctx, pool, declared)
	if err != nil || !created {
		t.Fatalf("first = created %v, err %v", created, err)
	}
	if listing.State != repo.ListingStateActive || listing.VerifiedAt != nil ||
		!reflect.DeepEqual(listing.ContentPathScope, []string{"/articles/"}) || !sameJSON(t, listing.Terms, []byte(termsFree)) {
		t.Fatalf("created listing = %+v, want it ACTIVE, unverified, with the declared scope and terms", listing)
	}

	changed := declared
	changed.ContentPathScope, changed.Terms = []string{}, []byte(termsPerAccess)
	again, created, err := provisionListing(t, ctx, pool, changed)
	if err != nil || created || !reflect.DeepEqual(again.ContentPathScope, []string{"/articles/"}) ||
		!sameJSON(t, again.Terms, []byte(termsFree)) {
		t.Fatalf("redeclared = %+v, created %v, err %v; want the stored listing unchanged", again, created, err)
	}
}

// A listing declared with no scope offers the whole site: the row stores an
// empty scope, as registration stores one, where a nil one would break the
// column's NOT NULL.
func TestCreateActiveListingIfAbsentStoresANilScopeAsTheWholeSite(t *testing.T) {
	ctx := context.Background()
	pool := listingFixture(t, ctx, "seat-a", "pub-1", "www.publisher.example")
	listing, created, err := provisionListing(t, ctx, pool, repo.NewActiveListing{
		TenantID: "seat-a", Domain: "www.publisher.example", Terms: []byte(termsFree),
	})
	if err != nil || !created || len(listing.ContentPathScope) != 0 {
		t.Fatalf("a listing with no scope = %+v, created %v, err %v; want it created, offering the whole site",
			listing, created, err)
	}
}

func TestCreateActiveListingIfAbsentLeavesAPendingListingPending(t *testing.T) {
	ctx := context.Background()
	pool := listingFixture(t, ctx, "seat-a", "pub-1", "www.publisher.example")
	if _, err := createListing(t, ctx, pool, "seat-a", "www.publisher.example", nil, termsFree); err != nil {
		t.Fatalf("seed the pending listing: %v", err)
	}

	stored, created, err := provisionListing(t, ctx, pool, repo.NewActiveListing{
		TenantID: "seat-a", Domain: "www.publisher.example", ContentPathScope: []string{}, Terms: []byte(termsFree),
	})
	if err != nil || created || stored.State != repo.ListingStatePendingManifest {
		t.Fatalf("declared over a pending listing = %+v, created %v, err %v; want it still pending", stored, created, err)
	}
}

func TestCreateActiveListingIfAbsentOnAnUnknownSeatIsNotFound(t *testing.T) {
	ctx := context.Background()
	pool := listingFixture(t, ctx, "seat-a", "pub-1", "www.publisher.example")
	_, _, err := provisionListing(t, ctx, pool, repo.NewActiveListing{
		TenantID: "no-such-seat", Domain: "www.publisher.example", ContentPathScope: []string{}, Terms: []byte(termsFree),
	})
	if !errors.Is(err, repo.ErrTenantNotFound) {
		t.Fatalf("a listing on an unknown seat: err = %v, want ErrTenantNotFound", err)
	}
}
