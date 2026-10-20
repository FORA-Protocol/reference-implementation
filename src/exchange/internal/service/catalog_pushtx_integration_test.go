//go:build integration

package service_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/structpb"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service/servicetest"
)

// pushTestExchangeDomain is this test Exchange's own domain. The manifest stub
// attests resource_owner_id on an AuthorizedExchange entry for it, and the service
// is constructed with the same value, so the resource-owner gate accepts.
const pushTestExchangeDomain = "exchange.test"

// pushTestSiteDomain is the publisher site the entries are pushed for. It is a
// different namespace from pushTestExchangeDomain above: the seat answers on
// one, the publisher serves its own manifest on the other.
const pushTestSiteDomain = "pub.example"

// authorizeAllManifests authorizes any caller pushing for its own domain and
// attests a resource_owner_id for pushTestExchangeDomain: Get returns a publisher
// manifest whose domain equals the requested host (so AuthorizesContributor(m,
// caller) is true when caller == host) and which carries the payee attestation the
// resource-owner gate requires.
type authorizeAllManifests struct{}

func (authorizeAllManifests) Get(_ context.Context, host string) (*forawellknown.Manifest, error) {
	ext, err := structpb.NewStruct(map[string]any{"resource_owner_id": "owner-" + host})
	if err != nil {
		return nil, err
	}
	return &forawellknown.Manifest{
		Ver: forawellknown.Version, Role: forawellknown.RolePublisher, Domain: host,
		Exchanges: []*forav1.AuthorizedExchange{{Domain: pushTestExchangeDomain, Ext: ext}},
	}, nil
}

// nopRegistry satisfies agentreg.Registry; PushResources never calls it (Gate 1,
// caller-signature, runs in the transport layer, not the service).
type nopRegistry struct{}

func (nopRegistry) LookupPublicKey(context.Context, string) (ed25519.PublicKey, error) {
	return nil, errors.New("not used")
}
func (nopRegistry) RegisterFromDirectory(context.Context, string, string) error { return nil }
func (nopRegistry) RefreshDirectoryKey(context.Context, string) error           { return nil }

// failOnNthUpsertTx delegates real in-tx upserts but fails the nth call, so a
// prior committed-within-tx insert exists for the rollback to undo.
type failOnNthUpsertTx struct {
	repo.CatalogRepo
	failAt int
	calls  int
}

func (f *failOnNthUpsertTx) UpsertTx(
	ctx context.Context, tx pgx.Tx, e repo.CatalogEntry,
) (repo.CatalogEntry, error) {
	f.calls++
	if f.calls >= f.failAt {
		return repo.CatalogEntry{}, errors.New("injected upsert failure")
	}
	return f.CatalogRepo.UpsertTx(ctx, tx, e)
}

// TestPushResources_AtomicRollbackOnMidBatchFailure proves the accepted-entries
// upsert batch is transactional (Arch rule 7): when the second entry's upsert
// fails, the first entry's insert is rolled back and the catalog stays empty.
func TestPushResources_AtomicRollbackOnMidBatchFailure(t *testing.T) {
	ctx := context.Background()
	pool := servicetest.AcquireTestDB(t, ctx)

	// The seat row's domain is this Exchange's own domain: the push resolves the
	// seat from EXCHANGE_DOMAIN, not from the entry's domain.
	//
	// Documented corner: no public seat-provisioning RPC exists yet, and
	// TenantReadRepo exposes only ByID/ByDomain (no insert) — so this test arranges
	// its seat prerequisite through the sqlc InsertTenant query, the same
	// surface every sibling Exchange test uses, rather than a raw SQL string.
	if _, err := sqlc.New(pool).InsertTenant(ctx, sqlc.InsertTenantParams{
		TenantID:        "t1",
		Domain:          pushTestExchangeDomain,
		Ed25519KeyRef:   "k",
		ReportingPolicy: []byte(`{}`),
	}); err != nil {
		t.Fatalf("seed seat: %v", err)
	}
	// The site both entries are pushed for, under the payee its manifest
	// attests, listed active on the seat. Without all three the push is refused
	// before any upsert runs and the rollback under test never happens.
	repotest.SeedSite(t, ctx, pool, repotest.Site{
		SeatTenantID: "t1",
		Domain:       pushTestSiteDomain,
		PublisherID:  "owner-" + pushTestSiteDomain,
		NewPublisher: true,
	})

	realRepo := repo.NewCatalogRepo(sqlc.New(pool))
	failing := &failOnNthUpsertTx{CatalogRepo: realRepo, failAt: 2}
	queries := sqlc.New(pool)
	svc := service.NewCatalogService(service.CatalogDeps{
		Catalog:        failing,
		Tenants:        repo.NewTenantReadRepo(queries),
		Sites:          repo.NewPublisherDomainReadRepo(queries),
		Listings:       repo.NewPublisherListingReadRepo(queries),
		Registry:       nopRegistry{},
		Manifests:      authorizeAllManifests{},
		TxRunner:       sharedb.PoolRunner{Pool: pool},
		ExchangeDomain: pushTestExchangeDomain,
	})

	_, err := svc.PushResources(ctx, &forav1.PushResourcesRequest{
		Exchange: pushTestExchangeDomain,
		Ver:      helpers.ProtocolVersion,
		TenantId: "t1",
		CallerId: pushTestSiteDomain,
		Entries: []*forav1.ResourceEntry{
			{Domain: pushTestSiteDomain, Path: "/a"},
			{Domain: pushTestSiteDomain, Path: "/b"},
		},
	}, service.PushSourceOther)
	if err == nil {
		t.Fatal("expected PushResources to fail on the injected mid-batch error")
	}

	all, err := realRepo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected 0 catalog rows after rollback, got %d", len(all))
	}
}
