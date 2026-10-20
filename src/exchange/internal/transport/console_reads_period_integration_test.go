//go:build integration

package transport_test

import (
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
)

// The period a publisher asks for, through the console routes. A period is
// half-open: its start is inside it and its end is not. The other read tests
// ask for the hour either side of now, which holds every sale they make, so a
// read that ignored the period would pass all of them. These tests read over
// periods that leave the sale out.

// TestConsoleReads_TransactionListHoldsOnlyThePeriodAsked sells once and lists
// the seller's transactions over periods placed at the time the database
// logged the sale. The list filters on that same logged time, so the periods
// can split exactly at it. A period that starts at the sale holds it. A period
// that ends at the sale does not. A period that starts one microsecond later,
// the next instant the database can store, does not hold it either.
func TestConsoleReads_TransactionListHoldsOnlyThePeriodAsked(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	sold := p.sell(t, s, pathFor(1), seedPricedTermEst(20)).GetTransactionId()
	at := p.loggedAt(t, s, sold)

	for _, c := range []struct {
		name   string
		period consoleapi.Period
		holds  bool
	}{
		{"starts at the sale", consoleapi.Period{Start: at, End: at.Add(time.Hour)}, true},
		{"ends at the sale", consoleapi.Period{Start: at.Add(-time.Hour), End: at}, false},
		{"starts just after the sale", consoleapi.Period{Start: at.Add(time.Microsecond), End: at.Add(time.Hour)}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := p.transactionsRead(t, s, consoleapi.ListTransactionsRequest{Period: c.period})
			rows := rowsOf(t, body, "transactions")
			switch {
			case !c.holds && len(rows) != 0:
				t.Fatalf("the period holds %d rows, want none: %v", len(rows), rows)
			case c.holds && (len(rows) != 1 || field(t, rows[0], "transaction_id") != sold):
				t.Fatalf("the period holds %v, want only the sale %s", rows, sold)
			}
		})
	}
}

// TestConsoleReads_TotalsHoldOnlyThePeriodAsked sells 1.00 and reads the
// totals over the hours around the sale, the hour before it and the hour after
// it. Only the first holds the sale; the other two are empty and say why.
//
// The count comes from the database's log and the money from the ledger, and
// each stamps the sale with its own clock, a moment apart. So these periods
// stay an hour clear of the sale rather than splitting at it.
func TestConsoleReads_TotalsHoldOnlyThePeriodAsked(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	before := time.Now().UTC()
	p.sell(t, s, pathFor(1), seedPricedTermEst(20))
	after := time.Now().UTC()

	around := consoleapi.Period{Start: before.Add(-time.Hour), End: after.Add(time.Hour)}
	wantTotal(t, p.totalsIn(t, s, around), "1", "0.1", "0.9", 1)
	for name, period := range map[string]consoleapi.Period{
		"the hour before": {Start: before.Add(-2 * time.Hour), End: before.Add(-time.Hour)},
		"the hour after":  {Start: after.Add(time.Hour), End: after.Add(2 * time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			body := p.totalsIn(t, s, period)
			if body["absence_reason"] != consoleapi.AbsenceNoTransactions {
				t.Fatalf("absence_reason = %v, want %s", body["absence_reason"], consoleapi.AbsenceNoTransactions)
			}
			wantTotal(t, body, "0", "0", "0", 0)
		})
	}
}

// totalsIn reads the seller's totals over period, by day, in the deployment's
// base currency.
func (p *publisherConsole) totalsIn(t *testing.T, s seller, period consoleapi.Period) map[string]any {
	t.Helper()
	return p.totalsRead(t, s, consoleapi.RevenueTotalsRequest{Period: period, Grouping: consoleapi.GroupingDay})
}

// loggedAt returns the time the database logged transaction id, as the
// transaction list reports it.
func (p *publisherConsole) loggedAt(t *testing.T, s seller, id string) time.Time {
	t.Helper()
	for _, row := range rowsOf(t, p.transactionsPage(t, s, "", 0, ""), "transactions") {
		if field(t, row, "transaction_id") != id {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, field(t, row, "created_at"))
		if err != nil {
			t.Fatalf("created_at does not parse: %v", row)
		}
		return at
	}
	t.Fatalf("transaction %s is not in the list", id)
	return time.Time{}
}
