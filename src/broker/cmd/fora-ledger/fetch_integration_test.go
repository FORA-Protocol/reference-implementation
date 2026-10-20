//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the database/sql "pgx" driver used by testcontainers Snapshot/Restore

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	brokerdb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// sharedPG is the package's single migrated Broker Postgres, reset per test.
var sharedPG *sharedb.SharedPostgres

func TestMain(m *testing.M) {
	os.Exit(testutil.RunIntegrationMain(
		m, brokerdb.Migrations, brokerdb.MigrationsDir, brokerdb.MigrationsTable,
		func(pg *sharedb.SharedPostgres) { sharedPG = pg },
	))
}

// The selection lookup is exercised against a real Broker database, through
// fetchSelection and then the identity assertion, so a lookup keyed on the
// wrong value fails here and not only in the assertion.
//
// The selection row is arranged through the production SelectionLogRepo. The
// Broker has no public write surface for its selection audit other than a full
// Resolve, and fetchSelection itself reads through the same repository, so the
// write and the read cross the same layer.
func TestFetchSelection_directExecuteWithUnrelatedDomainFindsTheBrokerRow(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)
	seedSelection(t, ctx, dsn, fixtureReqHost)

	f := newFixture(t)
	// A direct execute: Requester.domain is not checked against the caller, so
	// it names some other host. The transaction's agent_id is the agent.
	f.Sources.Evidence.RequesterDomain = "unrelated.example"
	resignAcceptance(t, f, &forav1.Requester{Id: fixtureReqID, Domain: "unrelated.example"})
	f.Sources.Evidence.CreatedAt = time.Now().Add(time.Minute)

	sel, absence := fetchSelection(ctx, Config{BrokerDBURL: dsn, BrokerWindow: time.Hour},
		f.Sources.Evidence, f.Sources.Transaction)
	if sel == nil {
		t.Fatalf("selection not found: %s", absence)
	}
	f.Sources.Selection, f.Sources.SelectionAbsence = sel, absence
	if a := assertionByName(t, BuildAssertions(f.Sources), "Broker's agent"); !a.OK {
		t.Errorf("identity check on the fetched row: %s", a.Detail)
	}
}

// Without the transaction's agent_id there is nothing trustworthy to look up by,
// so the lookup is reported as not read, even though a requester field would
// have matched the row.
func TestFetchSelection_absentAgentIDIsNotRead(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)
	seedSelection(t, ctx, dsn, fixtureReqHost)

	f := newFixture(t)
	f.Sources.Transaction.AgentID = ""
	f.Sources.Evidence.CreatedAt = time.Now().Add(time.Minute)

	sel, absence := fetchSelection(ctx, Config{BrokerDBURL: dsn, BrokerWindow: time.Hour},
		f.Sources.Evidence, f.Sources.Transaction)
	if sel != nil {
		t.Fatalf("selection was looked up without a transaction agent_id: %+v", sel)
	}
	if absence != noTransactionAgent {
		t.Errorf("absence = %q, want %q", absence, noTransactionAgent)
	}
}

func seedSelection(t *testing.T, ctx context.Context, dsn, agent string) {
	t.Helper()
	pool := sharedb.OpenForTest(t, ctx, dsn)
	err := repo.NewSelectionLogRepo(pool).RecordSelection(ctx, repo.SelectionLogEntry{
		LogID:           "sel-direct-execute",
		RequestID:       "req-direct-execute",
		AgentID:         agent,
		Query:           "ancient greek philosophy",
		CandidateOffers: []repo.CandidateInfo{{OfferID: fixtureOfferID, ExchangeID: "exchange.example"}},
		Rationale:       map[string]any{"outcome": "discovered"},
	})
	if err != nil {
		t.Fatalf("seed selection: %v", err)
	}
}
