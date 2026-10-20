//go:build integration

package repo_test

import (
	"context"
	"errors"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// TestCatalogUpsertRequiresARegisteredPublisher pins the constraint that closes
// the payee namespace. Before it, resource_owner_id was whatever a manifest
// attested: any domain could name any string, including somebody else's payee,
// and the row was stored. Now the id has to name a registered publisher, so a
// push naming a payee nobody registered is refused and stores nothing.
//
// Both halves are asserted through the production CatalogRepo surface: the
// refusal, and the same entry landing once its payee exists. A test that only
// showed the refusal could not tell a working foreign key from a broken upsert.
func TestCatalogUpsertRequiresARegisteredPublisher(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	q := sqlc.New(pool)
	const (
		tenantID   = "t-catalog-fk"
		siteDomain = "stoapress.example"
		ownerID    = "pub-stoa"
	)
	seedTenantRow(t, ctx, q, tenantID, siteDomain)
	catalog := repo.NewCatalogRepo(q)

	entry := repo.CatalogEntry{
		ResourceID:      tenantID + ":article",
		TenantID:        tenantID,
		URI:             "https://" + siteDomain + "/articles/one",
		URIPrefix:       "https://" + siteDomain + "/articles/one",
		Domain:          siteDomain,
		PricingJSON:     []byte(`{}`),
		DeliveryMethod:  "INSTRUCTIONS",
		ResourceOwnerID: ownerID,
	}

	// The payee is not registered yet: the write is refused as the caller's
	// mistake, not as an internal failure.
	if _, err := catalog.Upsert(ctx, entry); !errors.Is(err, repo.ErrCatalogOwnerNotRegistered) {
		t.Fatalf("Upsert with an unregistered payee = %v, want repo.ErrCatalogOwnerNotRegistered", err)
	}
	if _, err := catalog.ByID(ctx, entry.ResourceID); !errors.Is(err, repo.ErrCatalogNotFound) {
		t.Fatalf("ByID after the refused Upsert = %v, want repo.ErrCatalogNotFound", err)
	}

	// Register the payee and the same entry lands.
	if _, err := createPublisher(t, ctx, pool, ownerID, "Stoa Press"); err != nil {
		t.Fatalf("Create publisher: %v", err)
	}
	if _, err := catalog.Upsert(ctx, entry); err != nil {
		t.Fatalf("Upsert with a registered payee: %v", err)
	}
	back, err := catalog.ByID(ctx, entry.ResourceID)
	if err != nil {
		t.Fatalf("ByID after Upsert: %v", err)
	}
	if back.ResourceOwnerID != ownerID {
		t.Errorf("stored ResourceOwnerID = %q, want %q", back.ResourceOwnerID, ownerID)
	}
}
