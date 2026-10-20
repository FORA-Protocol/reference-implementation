package dashboard

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// Figures is how a page reads one totals answer: whether anything sold,
// whether the amounts can be shown, the sentence that explains an answer
// without them, and the period's total. The overview and the income report
// both read their totals through it, so the two cannot come to different
// conclusions about the same answer. Nothing is added up here: every figure
// is the Exchange's.
type Figures struct {
	// Empty means nothing sold in the period. A page says so rather than
	// showing zeros.
	Empty bool
	// HasAmounts is true when the answer's amounts are the ledger's: it
	// carries them, or it is an empty period, whose zeros are true. It is
	// false when the ledger could not be read, keeps no ledger in the
	// currency, or was not asked. The counts are right either way.
	HasAmounts bool
	// Notice is the sentence for an answer with an absence reason, empty
	// otherwise.
	Notice   string
	Currency string
	// Total is the period's own total, as the Exchange counted it.
	Total Row
}

// Row is one row of figures: a day of a report, or a period's total. Date is
// the day in the form a date field takes, or "Total" on the total row.
// Amounts are the Exchange's canonical money strings, in the answer's
// currency.
type Row struct {
	Date  string
	Count int64
	Gross string
	Fee   string
	Net   string
}

// Money is an amount as a page states it on its own: the amount and its
// currency, or a dash and no currency when the figures carry no amounts.
type Money struct {
	Amount   string
	Currency string
}

// noAmount is what a page shows in place of an amount it does not have.
const noAmount = "—"

// Amount is v when the figures carry amounts, and a dash when they do not.
func (f Figures) Amount(v string) string {
	if !f.HasAmounts {
		return noAmount
	}
	return v
}

// Money is v with the figures' currency, or a dash when they carry no
// amounts.
func (f Figures) Money(v string) Money {
	if !f.HasAmounts {
		return Money{Amount: noAmount}
	}
	return Money{Amount: v, Currency: f.Currency}
}

// figuresOf reads a totals answer.
func figuresOf(t exchangeclient.RevenueTotals) Figures {
	empty := t.AbsenceReason == consoleapi.AbsenceNoTransactions
	return Figures{
		Empty:      empty,
		HasAmounts: t.AbsenceReason == "" || empty,
		Notice:     absenceText(t.AbsenceReason, t.Currency),
		Currency:   t.Currency,
		Total:      rowOf("Total", t.Total),
	}
}

func rowOf(date string, f consoleapi.RevenueFigures) Row {
	return Row{
		Date: date, Count: f.TransactionCount,
		Gross: f.GrossRevenue, Fee: f.PlatformFee, Net: f.NetRevenue,
	}
}

// totalsQuery is one page's totals read: the period, the bucket it is split
// by, the resource and domain it is narrowed to, and whether only the counts
// are wanted.
type totalsQuery struct {
	Period     Period
	Grouping   string
	ResourceID string
	Domain     string
	CountOnly  bool
}

// totals reads the publisher's totals as q asks. Every page's totals request
// is built here.
func (s *Service) totals(
	ctx context.Context, actor exchangeclient.Actor, nav Nav, q totalsQuery,
) (exchangeclient.RevenueTotals, error) {
	got, err := s.reader.RevenueTotals(ctx, exchangeclient.RevenueTotalsRequest{
		Actor: actor, PublisherID: nav.PublisherID, Period: q.Period.Window(), Grouping: q.Grouping,
		ResourceID: q.ResourceID, Domain: q.Domain, CountOnly: q.CountOnly,
	})
	if err != nil {
		return exchangeclient.RevenueTotals{}, failure(err)
	}
	return got, nil
}
