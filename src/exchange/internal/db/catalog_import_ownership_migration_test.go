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

// ownershipSite is the site whose catalog row and listing the ownership
// migration test carries across the down and up migrations.
const ownershipSite = "stoa.example"

// TestCatalogImportOwnershipMigration verifies 000043 both ways, and what it
// does to rows that exist when it runs.
//
// Up: the catalog carries auto_imported and the listing carries
// import_replaces_manual_terms and last_import_skipped, all NOT NULL with a
// default, so no row ever reads unknown.
//
// Rows written before it: a catalog row and a listing are written at head
// through the production write ports with every new column set, and 000043 is
// then reversed and applied again. They come back reading false, false and 0
// -- which is what every row that exists when the migration first runs reads.
// A row whose source is unknown is protected, never assumed to be the
// importer's.
//
// Down: the three columns are gone and 000042's outcome columns, with 000039's
// sitemap_url and terms, are left alone. Migrate(42) is an explicit version
// rather than Steps(-1) so migrations stacked above 000043 later do not shift
// the math.
func TestCatalogImportOwnershipMigration(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)
	assertOwnershipColumns(t, ctx, dsn)
	resourceID, seat := seedOwnershipRows(t, ctx, dsn)

	m := schemaProbe.Migrator(t, dsn)
	if err := m.Migrate(42); err != nil {
		t.Fatalf("migrate to version 42 (reverse 000043): %v", err)
	}
	for table, cols := range map[string][]string{
		"catalog":                   {"auto_imported"},
		"publisher_domain_listings": {"import_replaces_manual_terms", "last_import_skipped"},
	} {
		for _, col := range cols {
			if schemaProbe.HasColumn(t, ctx, dsn, table, col) {
				t.Errorf("after down 000043: fora.%s still has %s", table, col)
			}
		}
	}
	for _, col := range []string{"last_import_outcome", "last_import_entries", "sitemap_url", "terms"} {
		if !schemaProbe.HasColumn(t, ctx, dsn, "publisher_domain_listings", col) {
			t.Errorf("after down 000043: %s is gone; it is an earlier migration's, not this one's", col)
		}
	}

	// Back to head, not just to 43: the rows are read through the production
	// repositories, which read the head schema, and later migrations change the
	// catalog too.
	if err := m.Up(); err != nil {
		t.Fatalf("migrate to head (apply 000043 and later again): %v", err)
	}
	// A fresh pool: the one that wrote the rows described the columns 000043
	// took away, and pgx caches statement descriptions per connection.
	q := sqlc.New(sharedb.OpenForTest(t, ctx, dsn))
	row, err := repo.NewCatalogRepo(q).ByID(ctx, resourceID)
	if err != nil {
		t.Fatalf("read the catalog row back: %v", err)
	}
	if row.AutoImported {
		t.Error("a catalog row that existed when 000043 ran reads auto_imported; it must read protected")
	}
	listing, err := repo.NewPublisherListingReadRepo(q).ByDomain(ctx, seat, ownershipSite)
	if err != nil {
		t.Fatalf("read the listing back: %v", err)
	}
	if listing.ImportReplacesManualTerms || listing.LastImport.Skipped != 0 {
		t.Errorf("a listing that existed when 000043 ran reads replace=%v skipped=%d; want false and 0",
			listing.ImportReplacesManualTerms, listing.LastImport.Skipped)
	}
}

// assertOwnershipColumns checks the columns 000043 adds at head: their type,
// that they are NOT NULL, and the default an existing row reads.
func assertOwnershipColumns(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	for _, c := range []struct{ table, col, typ, def string }{
		{"catalog", "auto_imported", "boolean", "false"},
		{"publisher_domain_listings", "import_replaces_manual_terms", "boolean", "false"},
		{"publisher_domain_listings", "last_import_skipped", "integer", "0"},
	} {
		if !schemaProbe.HasColumnType(t, ctx, dsn, c.table, c.col, c.typ) {
			t.Errorf("after up: fora.%s.%s is missing or not %s", c.table, c.col, c.typ)
		}
		if !schemaProbe.HasColumnNotNull(t, ctx, dsn, c.table, c.col) {
			t.Errorf("after up: fora.%s.%s is nullable; a row must never read unknown", c.table, c.col)
		}
		if !schemaProbe.HasColumnDefault(t, ctx, dsn, c.table, c.col, c.def) {
			t.Errorf("after up: fora.%s.%s does not default to %s", c.table, c.col, c.def)
		}
	}
}

// seedOwnershipRows writes, through the production write ports, a listing that
// answered yes to the take-over question and recorded skipped pages, and a
// catalog row the importer wrote. It returns the row's resource id and the
// seat.
func seedOwnershipRows(t *testing.T, ctx context.Context, dsn string) (resourceID, seat string) {
	t.Helper()
	pool := sharedb.OpenForTest(t, ctx, dsn)
	seat = repotest.SeedSeat(t, ctx, pool, repotest.Seat{Domain: "exchange.example"})
	const publisherID = "pub-ownership"
	repotest.SeedSite(t, ctx, pool, repotest.Site{
		SeatTenantID: seat, Domain: ownershipSite, PublisherID: publisherID, NewPublisher: true,
		SitemapURL: "https://" + ownershipSite + "/sitemap.xml", ImportReplacesManualTerms: true,
	})
	uri := "https://" + ownershipSite + "/articles/on-rivers"
	resourceID = seat + ":" + uri
	repotest.SeedCatalogRow(t, ctx, pool, repotest.CatalogRow{
		TenantID: seat, ResourceID: resourceID, URI: uri, Domain: ownershipSite, OwnerID: publisherID,
		AutoImported: true,
	})
	n, err := repo.NewListingImportRepo(sqlc.New(pool)).RecordOutcome(ctx, seat, ownershipSite, repo.ImportOutcome{
		Outcome: repo.ImportOutcomeComplete, Entries: 5, Skipped: 5,
	})
	if err != nil || n != 1 {
		t.Fatalf("record an outcome with skipped pages = %d, %v; want 1 row", n, err)
	}
	return resourceID, seat
}
