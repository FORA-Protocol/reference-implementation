//go:build integration

package repotest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// CatalogRow is one catalog row a suite stores directly instead of pushing it:
// a row no signed push can create today -- metadata a push would refuse, a
// resource id from an earlier identity scheme -- or one the push path never
// writes, such as a row of a second seat or under another URI scheme. The
// members no such question turns on are filled in by SeedCatalogRow.
type CatalogRow struct {
	TenantID   string
	ResourceID string
	URI        string
	// Domain is the publisher domain the row is stored under, which the push
	// would have taken from the registered site. It must not be empty.
	Domain string
	// OwnerID is the payee; it must be a registered publisher.
	OwnerID string
	// Terms is the stored terms document; nil stores an empty list.
	Terms []byte
	// Metadata is the stored metadata document; nil stores none.
	Metadata     []byte
	Title        *string
	AutoImported bool
}

// SeedCatalogRow writes row through repo.CatalogRepo, the port the catalog
// push itself writes through, with the URI as its prefix, the neutral pricing
// sentinel and instructions delivery. It fails the test if the row is refused.
func SeedCatalogRow(tb testing.TB, ctx context.Context, pool *pgxpool.Pool, row CatalogRow) {
	tb.Helper()
	if _, err := repo.NewCatalogRepo(sqlc.New(pool)).Upsert(ctx, repo.CatalogEntry{
		ResourceID: row.ResourceID, TenantID: row.TenantID, URI: row.URI, URIPrefix: row.URI,
		PricingJSON: []byte(`{}`), TermsJSON: row.Terms, DeliveryMethod: "INSTRUCTIONS",
		MetadataJSON: row.Metadata, ResourceOwnerID: row.OwnerID, Title: row.Title,
		AutoImported: row.AutoImported, Domain: row.Domain,
	}); err != nil {
		tb.Fatalf("store catalog row %s: %v", row.URI, err)
	}
}
