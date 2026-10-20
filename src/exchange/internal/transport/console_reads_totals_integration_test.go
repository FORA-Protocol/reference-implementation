//go:build integration

package transport_test

import (
	"math/big"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// Revenue totals through the console route. The money is written by
// ExecuteTransaction settling on a real TigerBeetle ledger, and read back by
// the totals route aggregating that ledger. The seat's rate is set through the
// operator's admin RPC; a publisher's own rate, which no route sets, is
// written through its repository (setFeeOverride). Nothing is asserted below
// the route.
//
// Every expected amount is computed here from the price and the rate, never
// read from the ledger, so the test checks the aggregation against the
// arithmetic rather than against itself.

// totals reads the seller's totals for the period around now.
func (p *publisherConsole) totals(t *testing.T, s seller, grouping, currency string) map[string]any {
	t.Helper()
	return p.totalsRead(t, s, consoleapi.RevenueTotalsRequest{
		Period: aroundNow(), Grouping: grouping, Currency: currency,
	})
}

// totalsRead reads the seller's totals as req asks, with the seller's actor
// and publisher filled in.
func (p *publisherConsole) totalsRead(t *testing.T, s seller, req consoleapi.RevenueTotalsRequest) map[string]any {
	t.Helper()
	req.Actor, req.PublisherID = s.actor, s.publisherID
	return p.read(t, transport.ConsoleRevenueTotalsPath, req)
}

// moneyFields are the money members of a figures object.
var moneyFields = []string{
	"gross_revenue", "platform_fee", "net_revenue", "net_payable", "refunded_amount", "refunded_platform_fee",
}

// wantTotal asserts the money and count of a totals answer's total. Amounts
// are the protocol's canonical money strings.
func wantTotal(t *testing.T, body map[string]any, gross, fee, net string, count float64) {
	t.Helper()
	got, ok := body["total"].(map[string]any)
	if !ok {
		t.Fatalf("answer has no total: %v", body)
	}
	for key, want := range map[string]string{
		"gross_revenue": gross, "platform_fee": fee, "net_revenue": net, "net_payable": net,
		"refunded_amount": "0", "refunded_platform_fee": "0",
	} {
		if g := field(t, got, key); g != want {
			t.Errorf("total %s = %s, want %s", key, g, want)
		}
	}
	if got["transaction_count"] != count {
		t.Errorf("total transaction_count = %v, want %v", got["transaction_count"], count)
	}
	if got["refund_count"] != float64(0) {
		t.Errorf("total refund_count = %v, want 0", got["refund_count"])
	}
}

// wantBucketsAddUpToTotal asserts that every money field and the count, added
// over the buckets, equal the total. Which bucket a sale's money lands in
// depends on the ledger's clock, which no test controls, so the buckets are
// checked against the total rather than one by one.
func wantBucketsAddUpToTotal(t *testing.T, body map[string]any) {
	t.Helper()
	total, _ := body["total"].(map[string]any)
	buckets := rowsOf(t, body, "buckets")
	for _, key := range moneyFields {
		sum := new(big.Rat)
		for _, b := range buckets {
			v, ok := new(big.Rat).SetString(field(t, b, key))
			if !ok {
				t.Fatalf("bucket %s = %v is not a decimal", key, b[key])
			}
			sum.Add(sum, v)
		}
		want, ok := new(big.Rat).SetString(field(t, total, key))
		if !ok || sum.Cmp(want) != 0 {
			t.Errorf("buckets add up to %s %s, total says %v", sum.FloatString(8), key, total[key])
		}
	}
	var count float64
	for _, b := range buckets {
		n, _ := b["transaction_count"].(float64)
		count += n
	}
	if count != total["transaction_count"] {
		t.Errorf("buckets count %v transactions, total says %v", count, total["transaction_count"])
	}
}

// TestConsoleReads_TotalsSplitTheFeeAtTheSeatRate sells two resources at 0.05
// per unit, one for an estimated 20 units and one for 10, so the agent is
// charged 1.00 and 0.50. At the seat's 10% the fees are 0.10 and 0.05. The
// total is gross 1.50, fee 0.15 and net 1.35, which only a sum of both
// settlements gives: keeping the first or the last would not.
func TestConsoleReads_TotalsSplitTheFeeAtTheSeatRate(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	p.sell(t, s, pathFor(1), seedPricedTermEst(20))
	p.sell(t, s, pathFor(2), seedPricedTermEst(10))

	body := p.totals(t, s, consoleapi.GroupingDay, "")
	if body["absence_reason"] != nil {
		t.Fatalf("absence_reason = %v on a period with a sale", body["absence_reason"])
	}
	if field(t, body, "currency") != harnessCurrency {
		t.Errorf("currency = %v, want the deployment's %s", body["currency"], harnessCurrency)
	}
	wantTotal(t, body, "1.5", "0.15", "1.35", 2)
	wantBucketsAddUpToTotal(t, body)
}

// TestConsoleReads_TotalsAtAZeroRateCarryNoFee sells 1.00 on a seat that
// takes no fee. That charge settles as one full post of the hold rather than a
// net and a fee, so it is its own ledger path: the whole gross is the net.
func TestConsoleReads_TotalsAtAZeroRateCarryNoFee(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s := p.newSeller(t, "stoa")
	p.sell(t, s, pathFor(1), seedPricedTermEst(20))

	wantTotal(t, p.totals(t, s, consoleapi.GroupingDay, ""), "1", "0", "1", 1)
}

// TestConsoleReads_TotalsApplyThePublisherOverride sells the same 1.00 for a
// publisher whose own rate is 25%, on a seat whose rate is 10%. The override
// wins: fee 0.25, net 0.75.
func TestConsoleReads_TotalsApplyThePublisherOverride(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "harmonia")
	setFeeOverride(t, p, s, 2500)
	p.sell(t, s, pathFor(1), seedPricedTermEst(20))

	body := p.totals(t, s, consoleapi.GroupingMonth, harnessCurrency)
	wantTotal(t, body, "1", "0.25", "0.75", 1)
	wantBucketsAddUpToTotal(t, body)
}

// TestConsoleReads_TotalsCountAFreeSaleWithNoMoney sells one priced and one
// free resource. Both are counted; only the priced one adds money.
func TestConsoleReads_TotalsCountAFreeSaleWithNoMoney(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	p.sell(t, s, pathFor(1), seedPricedTermEst(20))
	p.sell(t, s, pathFor(2), seedFreeTerm())

	body := p.totals(t, s, consoleapi.GroupingWeek, "")
	wantTotal(t, body, "1", "0.1", "0.9", 2)
	wantBucketsAddUpToTotal(t, body)
}

// TestConsoleReads_TotalsOfAnEmptyPeriodSayWhy reads a publisher that has sold
// nothing, beside one that has: the empty answer is zeros with a reason, and
// the other publisher's sale is not in it.
func TestConsoleReads_TotalsOfAnEmptyPeriodSayWhy(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	busy := p.newSeller(t, "stoa")
	p.sell(t, busy, pathFor(1), seedPricedTermEst(20))
	idle := p.newSeller(t, "harmonia")

	body := p.totals(t, idle, consoleapi.GroupingDay, "")
	if body["absence_reason"] != consoleapi.AbsenceNoTransactions {
		t.Fatalf("absence_reason = %v, want %s", body["absence_reason"], consoleapi.AbsenceNoTransactions)
	}
	wantTotal(t, body, "0", "0", "0", 0)
}

// TestConsoleReads_TotalsInACurrencyWithNoLedger asks for EUR on a deployment
// whose ledger keeps only USD. The answer says so instead of reporting zero,
// whichever billing backend the revenue is read from: the free and in-memory
// adapters keep no ledger of their own to refuse the currency, so the refusal
// has to come before the ledger is asked.
func TestConsoleReads_TotalsInACurrencyWithNoLedger(t *testing.T) {
	for name, readBilling := range map[string]billing.Adapter{
		"tigerbeetle": nil,
		"free":        billing.FreeAdapter{},
		"in-memory":   billing.NewInMemoryAdapter(billing.InMemoryOptions{}),
	} {
		t.Run(name, func(t *testing.T) {
			p := startPublisherConsoleReading(t, e2eFeeBps, readBilling)
			s := p.newSeller(t, "stoa")
			p.sell(t, s, pathFor(1), seedPricedTermEst(20))

			body := p.totals(t, s, consoleapi.GroupingDay, "EUR")
			if body["absence_reason"] != consoleapi.AbsenceUnsupportedCurrency {
				t.Fatalf("absence_reason = %v, want %s", body["absence_reason"], consoleapi.AbsenceUnsupportedCurrency)
			}
			wantTotal(t, body, "0", "0", "0", 0)
		})
	}
}

// TestConsoleReads_TotalsOnALedgerWithNoHistory sells through TigerBeetle but
// reads totals from the in-memory adapter, which keeps no settlement history.
// The read does not fail and does not claim the publisher sold nothing: it
// answers INTERNAL_ERROR, still counts the sale, and reports no money.
func TestConsoleReads_TotalsOnALedgerWithNoHistory(t *testing.T) {
	p := startPublisherConsoleReading(t, e2eFeeBps, billing.NewInMemoryAdapter(billing.InMemoryOptions{}))
	s := p.newSeller(t, "stoa")
	p.sell(t, s, pathFor(1), seedPricedTermEst(20))

	body := p.totals(t, s, consoleapi.GroupingDay, "")
	if body["absence_reason"] != consoleapi.AbsenceInternalError {
		t.Fatalf("absence_reason = %v, want %s", body["absence_reason"], consoleapi.AbsenceInternalError)
	}
	wantTotal(t, body, "0", "0", "0", 1)
	wantBucketsAddUpToTotal(t, body)
}

// setFeeOverride gives the seller its own platform fee rate on the seat.
//
// It writes through the production repository because no public route sets a
// publisher's rate: the operator admin surface sets the seat's default and the
// reporting policy, but not a per-publisher override. The read under test is
// still the public route.
func setFeeOverride(t *testing.T, p *publisherConsole, s seller, bps int) {
	t.Helper()
	if err := repo.NewFeeOverrideRepo(sqlc.New(p.pool)).Set(p.ctx, p.seatID, s.publisherID, bps); err != nil {
		t.Fatalf("set fee override: %v", err)
	}
}

// TestConsoleReads_TotalsNarrowToOneResourceOrDomain sells two resources,
// charged 1.00 and 0.50, and a third for another publisher on the same seat.
// Narrowed to each of the first two, the totals hold that resource's charge
// and count alone, which the ledger cannot tell apart by itself: it is traced
// from each settlement's billing id to the sale in the log. Narrowed to the
// publisher's own domain they hold both; narrowed to the other publisher's
// resource or domain, the answer is the empty one, not that sale.
func TestConsoleReads_TotalsNarrowToOneResourceOrDomain(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	dear := p.sell(t, s, pathFor(1), seedPricedTermEst(20))
	cheap := p.sell(t, s, pathFor(2), seedPricedTermEst(10))
	other := p.newSeller(t, "harmonia")
	elsewhere := p.sell(t, other, pathFor(3), seedPricedTermEst(20))

	resourceOf := func(s seller, transactionID string) string {
		t.Helper()
		for _, row := range rowsOf(t, p.transactionsPage(t, s, "", 0, ""), "transactions") {
			if field(t, row, "transaction_id") == transactionID {
				return field(t, row, "resource_id")
			}
		}
		t.Fatalf("transaction %s is not on the list", transactionID)
		return ""
	}
	narrowed := func(resourceID string) map[string]any {
		return p.totalsRead(t, s, consoleapi.RevenueTotalsRequest{
			Period: aroundNow(), Grouping: consoleapi.GroupingDay, ResourceID: resourceID,
		})
	}
	onDomain := func(domain string) map[string]any {
		return p.totalsRead(t, s, consoleapi.RevenueTotalsRequest{
			Period: aroundNow(), Grouping: consoleapi.GroupingDay, Domain: domain,
		})
	}

	body := narrowed(resourceOf(s, dear.GetTransactionId()))
	wantTotal(t, body, "1", "0.1", "0.9", 1)
	wantBucketsAddUpToTotal(t, body)
	body = narrowed(resourceOf(s, cheap.GetTransactionId()))
	wantTotal(t, body, "0.5", "0.05", "0.45", 1)
	wantBucketsAddUpToTotal(t, body)
	wantTotal(t, p.totals(t, s, consoleapi.GroupingDay, ""), "1.5", "0.15", "1.35", 2)

	body = onDomain(s.domain)
	wantTotal(t, body, "1.5", "0.15", "1.35", 2)
	wantBucketsAddUpToTotal(t, body)

	for name, body := range map[string]map[string]any{
		"resource": narrowed(resourceOf(other, elsewhere.GetTransactionId())),
		"domain":   onDomain(other.domain),
	} {
		if body["absence_reason"] != consoleapi.AbsenceNoTransactions {
			t.Fatalf("another publisher's %s: absence_reason = %v, want %s",
				name, body["absence_reason"], consoleapi.AbsenceNoTransactions)
		}
		wantTotal(t, body, "0", "0", "0", 0)
	}
}
