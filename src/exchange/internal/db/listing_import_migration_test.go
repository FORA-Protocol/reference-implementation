//go:build integration

package db_test

import (
	"context"
	"testing"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
)

// listingImportColumns is every column 000042 adds to
// fora.publisher_domain_listings, in the order the up file declares them.
var listingImportColumns = []string{
	"last_import_attempt_at", "last_import_success_at", "last_import_outcome",
	"last_import_failure_reason", "last_import_entries", "last_import_accepted",
	"last_import_refused", "last_import_unconfirmed", "last_import_unsent",
}

// TestListingImportOutcomeMigration verifies 000042 both ways.
//
// Up: the listing carries the last-outcome columns, the outcome column is the
// fora.import_outcome enum, and the five counts are NOT NULL so a row that was
// never imported reads as zero rather than as unknown.
//
// Down: every column and the enum type are gone, and the two settings the
// import reads -- sitemap_url and terms, which 000039 adds -- are left alone.
// Migrate(41) is an explicit version rather than Steps(-1) so migrations
// stacked above 000042 later do not shift the math.
func TestListingImportOutcomeMigration(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)

	for _, col := range listingImportColumns {
		if !schemaProbe.HasColumn(t, ctx, dsn, "publisher_domain_listings", col) {
			t.Errorf("after up: fora.publisher_domain_listings lacks %s", col)
		}
	}
	for _, col := range []string{
		"last_import_entries", "last_import_accepted", "last_import_refused",
		"last_import_unconfirmed", "last_import_unsent",
	} {
		if !schemaProbe.HasColumnNotNull(t, ctx, dsn, "publisher_domain_listings", col) {
			t.Errorf("after up: %s is nullable; a never-imported listing must read as zero", col)
		}
	}
	if !hasImportOutcomeEnum(t, ctx, dsn) {
		t.Error("after up: last_import_outcome is not the fora.import_outcome enum")
	}

	m := schemaProbe.Migrator(t, dsn)
	if err := m.Migrate(41); err != nil {
		t.Fatalf("migrate to version 41 (reverse 000042): %v", err)
	}
	for _, col := range listingImportColumns {
		if schemaProbe.HasColumn(t, ctx, dsn, "publisher_domain_listings", col) {
			t.Errorf("after down 000042: fora.publisher_domain_listings still has %s", col)
		}
	}
	for _, col := range []string{"sitemap_url", "terms"} {
		if !schemaProbe.HasColumn(t, ctx, dsn, "publisher_domain_listings", col) {
			t.Errorf("after down 000042: %s is gone; it is 000039's, not this migration's", col)
		}
	}
	if schemaProbe.Exists(t, ctx, dsn, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_type ty JOIN pg_namespace n ON n.oid = ty.typnamespace
		   WHERE n.nspname = 'fora' AND ty.typname = 'import_outcome'
		)`) {
		t.Error("after down 000042: the fora.import_outcome enum type survived")
	}
}

// hasImportOutcomeEnum reports whether last_import_outcome is the
// fora.import_outcome enum, read through udt_schema/udt_name because data_type
// renders every enum as "USER-DEFINED".
func hasImportOutcomeEnum(t *testing.T, ctx context.Context, dsn string) bool {
	t.Helper()
	return schemaProbe.Exists(t, ctx, dsn, `
		SELECT EXISTS (
		  SELECT 1 FROM information_schema.columns
		   WHERE table_schema = 'fora' AND table_name = 'publisher_domain_listings'
		     AND column_name = 'last_import_outcome'
		     AND udt_schema = 'fora' AND udt_name = 'import_outcome'
		)`)
}
