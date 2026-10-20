//go:build integration

package app_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver testcontainers Snapshot/Restore uses

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	consoledb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// sharedPG is this package's single Postgres container, migrated once and reset
// to that baseline before each test. It carries two schemas: the console's own
// and the Exchange's, because every test here talks to a real Exchange
// (exchangetest) over the same database. The two never collide: each lives in
// its own schema and records its history in its own migrations table.
var sharedPG *sharedb.SharedPostgres

// sharedLedger is this package's one TigerBeetle cluster: the ledger an
// Exchange started for a test about sales charges through and reads revenue
// from. TigerBeetle has no reset, so exchangetest salts every account id with a
// value unique to the test instead.
var sharedLedger *exchangetest.Ledger

// TestMain is bespoke rather than testutil.RunIntegrationMain because the
// baseline is two migration sets, not one, and a ledger runs beside them. The order is the one
// StartSharedPostgresMigrated documents: start, migrate (each migrator opens and
// closes its own connection), then snapshot with no session open.
func TestMain(m *testing.M) {
	os.Exit(runIntegrationMain(m))
}

func runIntegrationMain(m *testing.M) int {
	ctx := context.Background()
	logger := testutil.DiscardLogger()
	pg, cleanup, err := sharedb.StartSharedPostgres(ctx, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start shared postgres: %v\n", err)
		return 1
	}
	defer cleanup()
	if err := sharedb.Migrate(consoledb.Migrations, consoledb.MigrationsDir, pg.DSN,
		consoledb.MigrationsTable, logger); err != nil {
		fmt.Fprintf(os.Stderr, "migrate console schema: %v\n", err)
		return 1
	}
	if err := sharedb.Migrate(exchangetest.Migrations, exchangetest.MigrationsDir, pg.DSN,
		exchangetest.MigrationsTable, logger); err != nil {
		fmt.Fprintf(os.Stderr, "migrate exchange schema: %v\n", err)
		return 1
	}
	if err := pg.Snapshot(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "snapshot: %v\n", err)
		return 1
	}
	tb, tbCleanup, err := testutil.StartSharedTigerBeetle(ctx, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start shared tigerbeetle: %v\n", err)
		return 1
	}
	defer tbCleanup()
	ledger, closeLedger, err := exchangetest.BootLedger(tb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect tigerbeetle: %v\n", err)
		return 1
	}
	defer closeLedger()
	sharedPG, sharedLedger = pg, ledger
	return m.Run()
}

// acquireTestDB resets to the migrated baseline and returns a fresh pool. The
// console and the Exchange a test starts both use this one pool.
func acquireTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return sharedb.AcquireTestDB(t, t.Context(), sharedPG)
}
