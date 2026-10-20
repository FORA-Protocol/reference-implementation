//go:build integration

package transport_test

import (
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// startSellingConsole is the console surface of an Exchange started with a
// ledger and a platform fee of feeBps: the deployment the publisher console's
// suites buy through.
func startSellingConsole(t *testing.T, feeBps int32) *consoleFixture {
	t.Helper()
	return startConsoleFixtureWith(t, func(o *exchangetest.Options) {
		o.Ledger = sharedLedger
		o.FeeRateBps = feeBps
	})
}

// TestExchangetestMarket_SalesReachTheConsoleReads proves the harness the
// console's suites create transactions through. A publisher registers a site
// through the console routes; an exchangetest Buyer buys from it through
// discovery and ExecuteTransaction and reports its usage; and the console reads
// show the sale, its settled money at the seat's fee, and the report. Every leg
// is a public route: the console routes on one side, the protocol RPCs on the
// other.
func TestExchangetestMarket_SalesReachTheConsoleReads(t *testing.T) {
	f := startSellingConsole(t, e2eFeeBps)
	actor := newActor("seller@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Seller")
	registerSite(t, f, actor, publisherID, "seller.example", "cloudflare")

	buyer := f.exchange.Buyer(t)
	sale := buyer.Buy(t, "seller.example", "/articles/one", exchangetest.PerUnitTerm("0.05", 20))
	if err := buyer.Report(t, sale, 20); err != nil {
		t.Fatalf("report: %v", err)
	}

	totals := f.read(t, transport.ConsoleRevenueTotalsPath, consoleapi.RevenueTotalsRequest{
		Actor: actor, PublisherID: publisherID, Period: aroundNow(), Grouping: consoleapi.GroupingDay,
	})
	total, _ := totals["total"].(map[string]any)
	for key, want := range map[string]any{
		"gross_revenue": "1", "platform_fee": "0.1", "net_revenue": "0.9", "transaction_count": float64(1),
	} {
		if total[key] != want {
			t.Errorf("total %s = %v, want %v (body %v)", key, total[key], want, totals)
		}
	}

	page := f.read(t, transport.ConsoleListTransactionsPath, consoleapi.ListTransactionsRequest{
		Actor: actor, PublisherID: publisherID, Period: aroundNow(),
	})
	rows := rowsOf(t, page, "transactions")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the one sale: %v", len(rows), page)
	}
	for key, want := range map[string]string{
		"transaction_id":   sale.TransactionID,
		"agent_id":         buyer.ID(),
		"resource_uri":     sale.URI,
		"reporting_status": consoleapi.ReportingReceived,
	} {
		if got := field(t, rows[0], key); got != want {
			t.Errorf("row %s = %q, want %q", key, got, want)
		}
	}
}

// TestExchangetestMarket_AHeldSiteIsNotSoldUntilChecked: a site saved through
// the console routes and not verified sells nothing. The operator's catalog
// contributor cannot list a page of it, although the site serves a document
// authorizing that contributor, until the check route verifies the site; then
// the same page is listed and a Buyer buys it. Every leg is a public route: the
// console routes, the catalog push, and the protocol RPCs.
func TestExchangetestMarket_AHeldSiteIsNotSoldUntilChecked(t *testing.T) {
	f := startSellingConsole(t, e2eFeeBps)
	actor := newActor("seller@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Seller")
	registerPending(t, f, actor, publisherID, "seller.example")
	f.manifest.serve("seller.example", publisherManifest("seller.example", publisherID, f.contributor))

	term := exchangetest.PerUnitTerm("0.05", 20)
	err := f.exchange.PushAsContributor(t, "seller.example", "/articles/held", term)
	if err == nil || !strings.Contains(err.Error(), service.RejectionReasonListingNotActive) {
		t.Fatalf("pushing a page of a site nobody verified = %v; want it refused with %s",
			err, service.RejectionReasonListingNotActive)
	}

	if checked := checkDomain(t, f, actor, publisherID, "seller.example"); checked["passed"] != true {
		t.Fatalf("CheckDomain did not verify the site: %v", checked)
	}
	if sale := f.exchange.Buyer(t).Buy(t, "seller.example", "/articles/held", term); sale.TransactionID == "" {
		t.Fatal("the verified site's page sold with no transaction id")
	}
}
