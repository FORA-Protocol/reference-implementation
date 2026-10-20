//go:build integration

package transport_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the database/sql "pgx" driver used by testcontainers Snapshot/Restore

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tbtest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tigerbeetle"
	exchangedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
)

// sharedPG is the package's single Postgres container, migrated once by TestMain
// and reset to baseline between tests. sharedTB is the package's single
// TigerBeetle container and tbClient a single shared client dialing it; the
// ledger is append-only (no snapshot/restore), so TigerBeetle-touching tests
// isolate by salting business ids with sharedTB.Salt(t) rather than by a reset.
// All three are written once before m.Run and read-only thereafter, so the
// serial integration tests need no synchronization.
var (
	sharedPG *sharedb.SharedPostgres
	sharedTB *testutil.SharedTigerBeetle
	tbClient *tigerbeetle.Client
	// sharedLedger is exchangetest's handle to the same cluster, over tbClient,
	// for the suites that start an Exchange that sells.
	sharedLedger *exchangetest.Ledger
)

// TestMain brings up one Postgres and one TigerBeetle container for the whole
// package. The os.Exit is delegated to runIntegrationMain so the
// container-terminate defers (and tbClient.Close) run — os.Exit would skip them.
func TestMain(m *testing.M) {
	os.Exit(runIntegrationMain(m))
}

func runIntegrationMain(m *testing.M) int {
	ctx := context.Background()
	logger := testutil.DiscardLogger()

	pg, pgCleanup, err := sharedb.StartSharedPostgresMigrated(
		ctx, logger, exchangedb.Migrations, exchangedb.MigrationsDir, exchangedb.MigrationsTable,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start shared postgres: %v\n", err)
		return 1
	}
	defer pgCleanup()

	tb, tbCleanup, err := testutil.StartSharedTigerBeetle(ctx, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start shared tigerbeetle: %v\n", err)
		return 1
	}
	defer tbCleanup()

	client, closeClient, err := tbtest.BootClient(tb.Address)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect tigerbeetle: %v\n", err)
		return 1
	}
	defer closeClient()

	sharedPG = pg
	sharedTB = tb
	tbClient = client
	sharedLedger = exchangetest.LedgerOver(tb, client)
	return m.Run()
}

// closedPool is a pool on the shared database that is already closed. A
// repository over it fails every call with pgx's own "closed pool" error, which
// is what a lost database connection does to the port: nothing is faked. It
// never connects, because it keeps no minimum connections, so it holds no
// session and can be built before a harness's per-test reset.
func closedPool(tb testing.TB) *pgxpool.Pool {
	tb.Helper()
	pool, err := pgxpool.New(tb.Context(), sharedPG.DSN)
	if err != nil {
		tb.Fatalf("open a pool to close: %v", err)
	}
	pool.Close()
	return pool
}

// acquireTestDB resets the shared Postgres to its migrated baseline and returns a
// fresh pool. Every integration test in this package obtains its database through
// this helper (via setupExchangeTestDB or setupRegisterFixture), exactly once per
// test. See db.AcquireTestDB for the once-per-test and serial-execution contract.
//
// It also registers the payee the harness pushes under. The catch-all manifest
// stub attests harnessResourceOwner for every domain, and fora.catalog's
// resource_owner_id references fora.publishers, so without this row every push
// in the package is refused by the foreign key. Seeding it here rather than in
// each fixture keeps the one prerequisite in the one place every test already
// passes through.
func acquireTestDB(tb testing.TB, ctx context.Context) *pgxpool.Pool {
	tb.Helper()
	pool := sharedb.AcquireTestDB(tb, ctx, sharedPG)
	repotest.SeedPublisher(tb, ctx, pool, harnessResourceOwner, "Stoa Press")
	return pool
}
