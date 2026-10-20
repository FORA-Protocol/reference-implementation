//go:build integration

package transport_test

// The Exchange registry fixture the purchase suite seeds through, and the
// registry's endpoint normalization pinned at its source.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// seedExchangeRegistry brings up a real exchange registry (testcontainers
// Postgres) and registers the Exchange address as a VERIFIED Exchange via the
// production repository surface (repo.UpsertFromBootstrap). It also returns the
// registry's pool, so a test can take the registry away by closing it: a
// genuine database fault, with no stand-in involved.
func seedExchangeRegistry(
	t *testing.T, ctx context.Context, domain, exchangeURL string,
) (*repo.PgxExchangeRepo, *pgxpool.Pool) {
	t.Helper()
	pool := acquireTestDB(t, ctx)
	exchangeRepo := repo.NewExchangeRepo(pool)
	if _, err := exchangeRepo.UpsertFromBootstrap(ctx, repo.Exchange{
		ID:                "mp-" + domain,
		Domain:            domain,
		Endpoint:          exchangeURL,
		TrustLevel:        "VERIFIED",
		SupportedProfiles: []string{"fora-news-v1"},
		Priority:          10,
	}); err != nil {
		t.Fatalf("seed exchange: %v", err)
	}
	return exchangeRepo, pool
}

// TestExchangeRepo_NormalizesEndpointOnStore pins the endpoint normalization at
// its source of truth: an Exchange configured with a trailing slash is stored
// canonicalized, so every reader of the registry sees one form: the refresher
// compares the address an Exchange advertises against the stored one, and a
// trailing slash left in place would read as a change of address.
func TestExchangeRepo_NormalizesEndpointOnStore(t *testing.T) {
	ctx := context.Background()
	pool := acquireTestDB(t, ctx)
	exchangeRepo := repo.NewExchangeRepo(pool)

	const want = "https://exchange.example:8443"
	stored, err := exchangeRepo.UpsertFromBootstrap(ctx, repo.Exchange{
		ID:                "mp-trailing",
		Domain:            "mp.trailing.example",
		Endpoint:          want + "/", // operator configured a trailing slash
		TrustLevel:        "VERIFIED",
		SupportedProfiles: []string{"fora-news-v1"},
		Priority:          10,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if stored.Endpoint != want {
		t.Errorf("UpsertFromBootstrap returned endpoint %q, want normalized %q", stored.Endpoint, want)
	}

	list, err := exchangeRepo.ListUnblocked(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got string
	for _, ex := range list {
		if ex.ID == "mp-trailing" {
			got = ex.Endpoint
		}
	}
	if got != want {
		t.Errorf("List returned endpoint %q, want normalized %q", got, want)
	}
}
