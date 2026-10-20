//go:build integration

package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
)

// The sites the terms migration test lists: one saved before its terms were
// chosen, one saved with them, and one whose terms are a JSON null.
const (
	untermedSite = "harmonia.example"
	termedSite   = "stoa.example"
	jsonNullSite = "foundry.example"
)

// activeHasTerms is the constraint 000048 adds.
const activeHasTerms = "publisher_domain_listings_active_has_terms"

// TestListingTermsOptionalMigration verifies 000048 both ways.
//
// Up: terms is nullable, so a listing created through the production write
// port with no terms reads back with none. The new CHECK refuses to make such
// a listing ACTIVE, and refuses a listing whose terms are a JSON null as well,
// which the shape check 000039 added lets through. A listing with terms
// activates as before.
//
// Down: the listing without terms is deleted, the listings with terms stay,
// and terms is NOT NULL again. Migrate(47) is an explicit version rather than
// Steps(-1) so migrations stacked above 000048 later do not shift the math.
func TestListingTermsOptionalMigration(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)
	if schemaProbe.HasColumnNotNull(t, ctx, dsn, "publisher_domain_listings", "terms") {
		t.Fatal("after up: fora.publisher_domain_listings.terms is NOT NULL; a listing saved before its terms are chosen cannot exist")
	}
	pool := sharedb.OpenForTest(t, ctx, dsn)
	seat := repotest.SeedSeat(t, ctx, pool, repotest.Seat{Domain: "exchange.example"})
	listUnlisted(t, ctx, pool, seat, untermedSite, nil)
	listUnlisted(t, ctx, pool, seat, jsonNullSite, []byte("null"))
	repotest.SeedSite(t, ctx, pool, repotest.Site{
		SeatTenantID: seat, Domain: termedSite, PublisherID: "pub-termed", NewPublisher: true, Pending: true,
	})

	listings := repo.NewPublisherListingReadRepo(sqlc.New(pool))
	got, err := listings.ByDomain(ctx, seat, untermedSite)
	if err != nil {
		t.Fatalf("read the listing saved without terms: %v", err)
	}
	if got.Terms != nil {
		t.Fatalf("a listing saved without terms reads back terms %q; want none", got.Terms)
	}
	for _, domain := range []string{untermedSite, jsonNullSite} {
		if err := activate(ctx, pool, seat, domain); !violates(err, activeHasTerms) {
			t.Errorf("activating %s = %v; want the %s constraint to refuse it", domain, err, activeHasTerms)
		}
	}
	if err := activate(ctx, pool, seat, termedSite); err != nil {
		t.Fatalf("activating a listing with terms: %v", err)
	}

	m := schemaProbe.Migrator(t, dsn)
	if err := m.Migrate(47); err != nil {
		t.Fatalf("migrate to version 47 (reverse 000048): %v", err)
	}
	if !schemaProbe.HasColumnNotNull(t, ctx, dsn, "publisher_domain_listings", "terms") {
		t.Error("after down 000048: terms is still nullable")
	}
	// Back to head before reading: the production repositories read the head
	// schema, and a fresh pool, because the one above cached the old one.
	if err := m.Up(); err != nil {
		t.Fatalf("migrate to head (apply 000048 and later again): %v", err)
	}
	listings = repo.NewPublisherListingReadRepo(sqlc.New(sharedb.OpenForTest(t, ctx, dsn)))
	if _, err := listings.ByDomain(ctx, seat, untermedSite); !errors.Is(err, repo.ErrListingNotFound) {
		t.Errorf("the listing saved without terms survived the down migration: %v", err)
	}
	for _, domain := range []string{termedSite, jsonNullSite} {
		if _, err := listings.ByDomain(ctx, seat, domain); err != nil {
			t.Errorf("the listing of %s, which has terms, did not survive the down migration: %v", domain, err)
		}
	}
}

// listUnlisted registers a site under a payee of its own, through the
// production write ports, and lists it on the seat with the given terms.
// repotest.SeedSite gives every listing terms, so the listing is created here.
func listUnlisted(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seat, domain string, terms []byte) {
	t.Helper()
	repotest.SeedSite(t, ctx, pool, repotest.Site{
		SeatTenantID: seat, Domain: domain, PublisherID: "pub-" + domain, NewPublisher: true, Unlisted: true,
	})
	err := sharedb.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := repo.NewPublisherListingWriteRepo(sqlc.New(pool)).Create(ctx, tx, repo.NewListing{
			TenantID: seat, Domain: domain, Terms: terms,
		})
		return err
	})
	if err != nil {
		t.Fatalf("list %s with terms %q: %v", domain, terms, err)
	}
}

// activate makes one listing ACTIVE through the production write port.
func activate(ctx context.Context, pool *pgxpool.Pool, seat, domain string) error {
	return sharedb.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := repo.NewPublisherListingWriteRepo(sqlc.New(pool)).Activate(ctx, tx, seat, domain)
		return err
	})
}

// violates reports whether err is the database refusing a row on the named
// CHECK constraint.
func violates(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == checkViolationSQLState && pgErr.ConstraintName == constraint
}
