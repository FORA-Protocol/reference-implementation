//go:build integration

package transport_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sor"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// publisherConsole is one seat served twice: by the transaction harness, which
// takes catalog pushes, ExecuteTransaction and ReportUsage and charges through
// a real TigerBeetle ledger, and by the console surface, which registers
// publishers and serves their reads. Both run over one database, find the seat
// by the same domain, read site documents from one set, and read one
// deterministic clock.
//
// Everything a read test reads back is created through the public routes a
// publisher, an agent and an operator use. A publisher registers through the
// console; the site document it serves attests that publisher, so the catalog
// push attributes the resources to it; the agent's transactions against those
// resources settle to its revenue account on the ledger; the operator sets the
// seat's rate and reporting policy through the admin RPCs; and the console
// reads it all back. Two inputs are arranged below those routes, because no
// route writes them: the agent's funds are a direct ledger credit, the only
// credit path the ledger has, and a publisher's own fee rate is written through
// its production repository (setFeeOverride).
//
// The ledger is never reset. Each test gets its own ledger salt, and every
// publisher it registers has a freshly minted id, so no two tests read the same
// revenue account.
//
// exchangetest.Options.Ledger serves the same composition in one process for
// suites outside the Exchange. These tests keep the transaction harness
// instead, because they drive what that option deliberately leaves out: the
// admin RPCs that set the seat's rate and reporting policy, and revenue reads
// from a billing adapter other than the one that charged.
type publisherConsole struct {
	*consoleFixture
	h   *testHarness
	clk *clock.DeterministicClock
}

// startPublisherConsole brings up the shared seat at a platform fee of feeBps,
// with the console's revenue reads on the ledger the seat charges through.
func startPublisherConsole(t *testing.T, feeBps int32) *publisherConsole {
	t.Helper()
	return startPublisherConsoleReading(t, feeBps, nil)
}

// startPublisherConsoleReading is startPublisherConsole whose revenue reads
// come from readBilling instead. Nil is the TigerBeetle adapter the seat
// charges through. Either way the console builds its reader with
// billing.RevenueLedgerFor, as the Exchange does.
func startPublisherConsoleReading(t *testing.T, feeBps int32, readBilling billing.Adapter) *publisherConsole {
	t.Helper()
	return startPublisherConsoleWith(t, publisherConsoleOptions{feeBps: feeBps, readBilling: readBilling})
}

// publisherConsoleOptions are what one console read suite chooses differently.
// The zero value is a seat with no platform fee, revenue read from the
// TigerBeetle ledger the seat charges through, a fresh in-memory account
// registry, no log capture, a denial journal written through the seat's own
// pool, and the production settlement batch.
type publisherConsoleOptions struct {
	// feeBps is the seat's platform fee, set through the admin RPC when above 0.
	feeBps int32
	// readBilling is where the console reads revenue from; nil is the
	// TigerBeetle adapter the seat charges through.
	readBilling billing.Adapter
	// sor is the account registry the Exchange checks an agent's account in;
	// nil is a fresh in-memory one. A sorControl lets a test switch the agent's
	// account off the way an operator does.
	sor sor.Adapter
	// logger receives the Exchange's log lines; nil discards them.
	logger *slog.Logger
	// journalPool is the pool the Exchange's denial journal writes through;
	// nil is the seat's own. closedPool(t) makes every journal write fail.
	journalPool *pgxpool.Pool
	// settlementBatch is the read service's settlement batch; zero is the
	// production default.
	settlementBatch int
}

// startPublisherConsoleWith is the one bring-up every console read suite goes
// through; startPublisherConsole and startPublisherConsoleReading are its
// common cases.
func startPublisherConsoleWith(t *testing.T, o publisherConsoleOptions) *publisherConsole {
	t.Helper()
	salt := sharedTB.Salt(t)
	manifests := exchangetest.NewManifests()
	clk := clock.NewDeterministic(time.Now().UTC())
	h, adapter := newTBHarness(t, salt, 0, harnessOptions{
		clk: clk, manifests: manifests, sor: o.sor, logger: o.logger, journalPool: o.journalPool,
	})
	if o.feeBps > 0 {
		admin, _ := h.adminSurface(t)
		mustSetFee(t, h, admin, o.feeBps, nil)
	}
	fundTBAgent(t, salt, h.billingRef, "100.00")
	readBilling := o.readBilling
	if readBilling == nil {
		readBilling = adapter
	}

	base := newConsoleBase(t, h.ctx, h.pool)
	// The terms currency is left at the Exchange's default, EUR, while the
	// ledger keeps USD, as on the free and in-memory tiers. So every totals read
	// that names no currency shows it is read in the ledger's base currency: read
	// in the terms currency, it would answer UNSUPPORTED_CURRENCY.
	ex := exchangetest.Start(t, h.pool, exchangetest.Options{
		DirectoryURL:    base.directory,
		ExchangeDomain:  harnessExchangeDomain,
		ContributorHost: h.callerID,
		DirectoryClient: directoryClient(),
		// The transaction harness seeded the seat, and the console finds it by
		// the same domain.
		WithoutSeat:      true,
		Manifests:        manifests,
		Billing:          readBilling,
		LedgerCurrencies: []string{harnessCurrency},
		SettlementBatch:  o.settlementBatch,
		Clock:            clk,
	})
	// The harness's caller pushes the catalog, so it is the contributor every
	// site document has to authorize.
	f := base.over(t, ex, h.callerID)
	f.seatID = h.tenantID
	return &publisherConsole{consoleFixture: f, h: h, clk: clk}
}

// seller is one registered publisher with one active site.
type seller struct {
	actor       consoleActorBody
	publisherID string
	domain      string
}

// newSeller registers a publisher and one site for it through the console.
func (p *publisherConsole) newSeller(t *testing.T, name string) seller {
	t.Helper()
	actor := newActor(name + "@publisher.example")
	id := p.registerPublisher(t, actor, name)
	domain := name + "-" + strings.Split(uuid.NewString(), "-")[0] + ".example"
	registerSite(t, p.consoleFixture, actor, id, domain, "cloudflare")
	return seller{actor: actor, publisherID: id, domain: domain}
}

// offerFor pushes one resource at path for the seller and returns the offer
// discovery issues for it.
func (p *publisherConsole) offerFor(t *testing.T, s seller, path string, term *forav1.LicenseTerm) *forav1.Offer {
	t.Helper()
	return pushDiscoverTermOfferForTenant(t, p.h, p.h.tenantID, s.domain, path, term)
}

// sell pushes one resource at path for the seller, discovers its offer, and
// runs one transaction for it. It returns the delivered item.
func (p *publisherConsole) sell(
	t *testing.T, s seller, path string, term *forav1.LicenseTerm,
) *forav1.TransactionResultItem {
	t.Helper()
	offer := p.offerFor(t, s, path, term)
	resp, err := executeOfferRawWithID(t, p.h, offer, "tx-"+uuid.NewString())
	if err != nil {
		t.Fatalf("execute %s%s: %v", s.domain, path, err)
	}
	return exchangetest.Delivered(t, resp.Msg, s.domain+path)
}

// report files a usage report for item and returns the error the Exchange
// answered with, nil when it accepted the report.
func (p *publisherConsole) report(t *testing.T, item *forav1.TransactionResultItem, consumed int32) error {
	t.Helper()
	return p.reportBilledAs(t, item, item.GetBillingId(), consumed)
}

// reportBilledAs is report with the billing_id the report names; a billing_id
// other than the item's is a report the Exchange refuses.
func (p *publisherConsole) reportBilledAs(
	t *testing.T, item *forav1.TransactionResultItem, billingID string, consumed int32,
) error {
	t.Helper()
	_, err := p.h.exchangeClient.ReportUsage(p.h.ctx, connect.NewRequest(newUsageReport(
		"r-"+uuid.NewString(), item.GetTransactionId(), billingID,
		&forav1.Usage{ConsumedQuantity: consumed})))
	return err
}

// periodAround is the period from an hour before now to an hour after it, in
// UTC. aroundNow and aroundClock say which clock now is read from.
func periodAround(now time.Time) consoleapi.Period {
	now = now.UTC()
	return consoleapi.Period{Start: now.Add(-time.Hour), End: now.Add(time.Hour)}
}

// aroundNow is a period that holds every transaction a test just made and
// every settlement the ledger stamped for them: the database and the ledger
// stamp with the wall clock, which no test controls.
func aroundNow() consoleapi.Period { return periodAround(time.Now()) }

// aroundClock is a period that holds the Exchange clock's present. The
// Exchange stamps a recorded denial from its own clock, which a test moves, so
// a denial is read over this period rather than aroundNow's.
func aroundClock(clk *clock.DeterministicClock) consoleapi.Period { return periodAround(clk.Now()) }

// read posts one console read and fails the test unless it answers 200. It
// returns the decoded body.
func (f *consoleFixture) read(t *testing.T, path string, body any) map[string]any {
	t.Helper()
	status, out := f.post(t, consolePath(path), body)
	if status != http.StatusOK {
		t.Fatalf("POST %s status = %d, body = %v", path, status, out)
	}
	return out
}

// transactionsPage reads one page of the seller's transactions in the period
// around now.
func (p *publisherConsole) transactionsPage(
	t *testing.T, s seller, resourceID string, pageSize int, cursor string,
) map[string]any {
	t.Helper()
	return p.transactionsRead(t, s, consoleapi.ListTransactionsRequest{
		Period: aroundNow(), ResourceID: resourceID, PageSize: pageSize, Cursor: cursor,
	})
}

// transactionsRead reads the seller's transactions as req asks, with the
// seller's actor and publisher filled in.
func (p *publisherConsole) transactionsRead(
	t *testing.T, s seller, req consoleapi.ListTransactionsRequest,
) map[string]any {
	t.Helper()
	req.Actor, req.PublisherID = s.actor, s.publisherID
	return p.read(t, transport.ConsoleListTransactionsPath, req)
}

// rowsOf returns the rows under key in a page body.
func rowsOf(t *testing.T, body map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := body[key].([]any)
	if !ok {
		t.Fatalf("response has no %q list: %v", key, body)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		row, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("%q holds a non-object: %v", key, r)
		}
		out = append(out, row)
	}
	return out
}

// field reads a string member of a decoded body.
func field(t *testing.T, body map[string]any, key string) string {
	t.Helper()
	v, ok := body[key].(string)
	if !ok {
		t.Fatalf("response member %q is %T, not a string: %v", key, body[key], body)
	}
	return v
}

// pathFor is a resource path unique to this call, so a test that sells more
// than one resource does not re-push the same one.
func pathFor(i int) string { return fmt.Sprintf("/articles/read-%d", i) }
