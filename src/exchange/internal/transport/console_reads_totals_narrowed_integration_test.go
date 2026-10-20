//go:build integration

package transport_test

import (
	"context"
	"math/big"
	"net/http"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// TestConsoleReads_NarrowedTotalsCrossABatchBoundary sells five resources on
// the publisher's domain with the read service holding settlements back two at
// a time, so the narrowed read asks the log about two full batches and a
// partial one. Every sale is counted once: a batch that was not emptied after
// it was asked about would add its settlements again with the next one.
func TestConsoleReads_NarrowedTotalsCrossABatchBoundary(t *testing.T) {
	p := startPublisherConsoleWith(t, publisherConsoleOptions{feeBps: e2eFeeBps, settlementBatch: 2})
	s := p.newSeller(t, "stoa")
	for i := range 5 {
		p.sell(t, s, pathFor(i), seedPricedTermEst(20))
	}

	body := p.totalsRead(t, s, consoleapi.RevenueTotalsRequest{
		Period: aroundNow(), Grouping: consoleapi.GroupingDay, Domain: s.domain,
	})
	wantTotal(t, body, "5", "0.5", "4.5", 5)
	wantBucketsAddUpToTotal(t, body)
}

// TestConsoleReads_NarrowedTotalsFailWhenTheLogCannotBeAsked reads totals from
// a ledger whose one settlement names a billing id Postgres cannot hold as
// text, so the transaction log refuses the narrowed read's lookup. That read
// fails as an internal error with no figures: its counts came from the same
// log, so there is no true answer to give. The same read without a narrowing
// asks the log nothing and answers with the ledger's money, which shows the
// failure is the lookup's and not the ledger's.
func TestConsoleReads_NarrowedTotalsFailWhenTheLogCannotBeAsked(t *testing.T) {
	p := startPublisherConsoleReading(t, e2eFeeBps, unstorableBillingIDLedger{billing.NewInMemoryAdapter(billing.InMemoryOptions{})})
	s := p.newSeller(t, "stoa")
	p.sell(t, s, pathFor(1), seedPricedTermEst(20))

	status, body := p.post(t, consolePath(transport.ConsoleRevenueTotalsPath), consoleapi.RevenueTotalsRequest{
		Actor: s.actor, PublisherID: s.publisherID, Period: aroundNow(), Grouping: consoleapi.GroupingDay,
		Domain: s.domain,
	})
	assertRefused(t, "narrowed totals", status, body, http.StatusInternalServerError, consoleapi.ErrorKindInternal)

	whole := p.totals(t, s, consoleapi.GroupingDay, "")
	if reason, ok := whole["absence_reason"]; ok {
		t.Fatalf("totals without a narrowing: absence_reason = %v, want none", reason)
	}
}

// unstorableBillingIDLedger is the in-memory adapter with a revenue read that
// hands over one settlement, early in the period, whose billing id holds a NUL
// byte. The ledger is the external system here; the transaction log the lookup
// asks is the real one.
type unstorableBillingIDLedger struct{ *billing.InMemoryAdapter }

func (unstorableBillingIDLedger) Revenue() billing.RevenueLedger { return unstorableSettlements{} }

type unstorableSettlements struct{}

func (unstorableSettlements) OwnerSettlements(
	_ context.Context, q billing.SettlementQuery, visit func(billing.Settlement) error,
) error {
	return visit(billing.Settlement{
		SettledAt: q.From, Gross: big.NewInt(100), Net: big.NewInt(90), BillingID: "hold\x00id",
	})
}

// TestConsoleReads_CountOnlyTotalsLeaveTheLedgerUnread reads totals from the
// in-memory adapter, which keeps no settlement history, so a read that asks
// the ledger answers INTERNAL_ERROR. A count-only read never asks it: it
// answers the count with MONEY_NOT_REQUESTED, and a period that sold nothing
// still says so.
func TestConsoleReads_CountOnlyTotalsLeaveTheLedgerUnread(t *testing.T) {
	p := startPublisherConsoleReading(t, e2eFeeBps, billing.NewInMemoryAdapter(billing.InMemoryOptions{}))
	s := p.newSeller(t, "stoa")
	p.sell(t, s, pathFor(1), seedPricedTermEst(20))

	body := p.totalsRead(t, s, consoleapi.RevenueTotalsRequest{
		Period: aroundNow(), Grouping: consoleapi.GroupingMonth, Domain: s.domain, CountOnly: true,
	})
	if body["absence_reason"] != consoleapi.AbsenceMoneyNotRequested {
		t.Fatalf("count-only totals: absence_reason = %v, want %s",
			body["absence_reason"], consoleapi.AbsenceMoneyNotRequested)
	}
	wantTotal(t, body, "0", "0", "0", 1)

	other := p.newSeller(t, "harmonia")
	body = p.totalsRead(t, other, consoleapi.RevenueTotalsRequest{
		Period: aroundNow(), Grouping: consoleapi.GroupingMonth, CountOnly: true,
	})
	if body["absence_reason"] != consoleapi.AbsenceNoTransactions {
		t.Fatalf("count-only totals of a publisher with no sales: absence_reason = %v, want %s",
			body["absence_reason"], consoleapi.AbsenceNoTransactions)
	}
}
