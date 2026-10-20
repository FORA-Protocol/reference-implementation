//go:build integration

package db_test

import (
	"context"
	"testing"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
)

// consoleReadColumns is every column 000046 appends to the publisher
// transactions view. The console's list, totals and income reads name each of
// them.
var consoleReadColumns = []string{"resource_title", "resource_domain", "billing_id"}

// TestPublisherConsoleReadsMigration verifies 000046 both ways, and what it does
// to rows that exist when it runs.
//
// Up: the catalog carries a domain on every row, refuses an empty one, and has
// the (tenant_id, domain) index; the view carries the three columns the
// console reads.
//
// Rows written before it: a catalog row stored under a registered domain is
// written at head, 000046 is reversed and applied again, and the row reads the
// domain its address was built from -- which is what the backfill gives every
// row that exists when the migration first runs.
//
// Down: the view is back to its 000041 shape with its original columns, and
// the column and index are gone. Migrate(45) is an explicit version rather than
// Steps(-1) so migrations stacked above 000046 later do not shift the math.
func TestPublisherConsoleReadsMigration(t *testing.T) {
	ctx := t.Context()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)
	assertConsoleReadSchema(t, ctx, dsn, true)
	resourceID := seedConsoleReadRow(t, ctx, dsn)

	m := schemaProbe.Migrator(t, dsn)
	if err := m.Migrate(45); err != nil {
		t.Fatalf("migrate to version 45 (reverse 000046): %v", err)
	}
	assertConsoleReadSchema(t, ctx, dsn, false)
	for _, col := range []string{"transaction_id", "publisher_id", "resource_uri", "unit_cost", "created_at"} {
		if !schemaProbe.HasColumn(t, ctx, dsn, "publisher_transactions", col) {
			t.Errorf("after down 000046: the view lost %s; it is 000041's, not this migration's", col)
		}
	}

	if err := m.Migrate(46); err != nil {
		t.Fatalf("migrate to version 46 (apply 000046 again): %v", err)
	}
	assertConsoleReadSchema(t, ctx, dsn, true)
	// A fresh pool: the one that wrote the row described the column 000046
	// took away, and pgx caches statement descriptions per connection.
	row, err := repo.NewCatalogRepo(sqlc.New(sharedb.OpenForTest(t, ctx, dsn))).ByID(ctx, resourceID)
	if err != nil {
		t.Fatalf("read the catalog row back: %v", err)
	}
	if row.Domain != consoleReadSite {
		t.Errorf("a row that existed when 000046 ran reads domain %q, want %q", row.Domain, consoleReadSite)
	}
}

// consoleReadSite is the domain the migration test's catalog row is stored
// under. Its address writes the host in another case, which the backfill must
// lower-case to match the registered domain.
const consoleReadSite = "stoa.example"

// seedConsoleReadRow writes one catalog row through the production write port
// and returns its resource id.
func seedConsoleReadRow(t *testing.T, ctx context.Context, dsn string) string {
	t.Helper()
	pool := sharedb.OpenForTest(t, ctx, dsn)
	seat := repotest.SeedSeat(t, ctx, pool, repotest.Seat{Domain: "exchange.example"})
	const publisherID = "pub-console-reads"
	repotest.SeedPublisher(t, ctx, pool, publisherID, "Stoa Press")
	uri := "https://Stoa.Example/articles/on-rivers"
	repotest.SeedCatalogRow(t, ctx, pool, repotest.CatalogRow{
		TenantID: seat, ResourceID: seat + ":" + uri, URI: uri, Domain: consoleReadSite, OwnerID: publisherID,
	})
	return seat + ":" + uri
}

// assertConsoleReadSchema checks that everything 000046 creates is present
// (want true) or absent (want false).
func assertConsoleReadSchema(t *testing.T, ctx context.Context, dsn string, want bool) {
	t.Helper()
	for _, col := range consoleReadColumns {
		if got := schemaProbe.HasColumn(t, ctx, dsn, "publisher_transactions", col); got != want {
			t.Errorf("publisher_transactions has %s = %v, want %v", col, got, want)
		}
	}
	if got := schemaProbe.HasColumn(t, ctx, dsn, "catalog", "domain"); got != want {
		t.Errorf("catalog has domain = %v, want %v", got, want)
	}
	if got := schemaProbe.Exists(t, ctx, dsn, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			 WHERE schemaname = 'fora' AND tablename = 'catalog'
			   AND indexname = 'catalog_tenant_domain_idx'
			   AND indexdef LIKE '%(tenant_id, domain)%'
		)`); got != want {
		t.Errorf("catalog_tenant_domain_idx on (tenant_id, domain) exists = %v, want %v", got, want)
	}
	if !want {
		return
	}
	if !schemaProbe.HasColumnNotNull(t, ctx, dsn, "catalog", "domain") {
		t.Error("catalog.domain accepts NULL")
	}
	if !schemaProbe.Exists(t, ctx, dsn, `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
			 WHERE n.nspname = 'fora' AND c.conname = 'catalog_domain_not_empty' AND c.contype = 'c'
		)`) {
		t.Error("catalog has no check refusing an empty domain")
	}
}
