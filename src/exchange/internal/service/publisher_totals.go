package service

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"math/big"
	"slices"
	"strings"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// RevenueQuery asks for one publisher's revenue in one currency over a period,
// split into buckets of Grouping. An empty Currency is the deployment's base
// currency, the first it keeps a ledger in. The narrowing, when set, narrows
// the money and the counts to one of the publisher's resources or domains.
// CountOnly asks for the counts alone, so the ledger is not read.
type RevenueQuery struct {
	PublisherID string
	Start, End  time.Time
	Grouping    Grouping
	Currency    string
	Narrowing
	CountOnly bool
}

// RevenueFigures are the money a period settled, in integer minor units at the
// ledger's asset scale, and the transactions and refunds it counted.
type RevenueFigures struct {
	Gross               *big.Int
	PlatformFee         *big.Int
	Net                 *big.Int
	Refunded            *big.Int
	RefundedPlatformFee *big.Int
	NetPayable          *big.Int
	TransactionCount    int64
	RefundCount         int64
}

func zeroFigures() RevenueFigures {
	return RevenueFigures{
		Gross: new(big.Int), PlatformFee: new(big.Int), Net: new(big.Int),
		Refunded: new(big.Int), RefundedPlatformFee: new(big.Int), NetPayable: new(big.Int),
	}
}

// addSettlement adds one settled charge: the fee is what the hold reserved
// less what the owner was credited. Refunds have no caller yet, so nothing is
// ever refunded and the net payable is the net.
func (f *RevenueFigures) addSettlement(s billing.Settlement) {
	f.Gross.Add(f.Gross, s.Gross)
	f.PlatformFee.Add(f.PlatformFee, new(big.Int).Sub(s.Gross, s.Net))
	f.Net.Add(f.Net, s.Net)
	f.NetPayable.Add(f.NetPayable, s.Net)
}

// RevenueBucket is one grouping interval and what it settled.
type RevenueBucket struct {
	Period
	RevenueFigures
}

// AbsenceReason says why a totals answer carries no figures.
type AbsenceReason string

// The absence reasons. AbsenceNone is a report with figures in it.
const (
	AbsenceNone                AbsenceReason = ""
	AbsenceNoTransactions      AbsenceReason = "NO_TRANSACTIONS_IN_PERIOD"
	AbsenceUnsupportedCurrency AbsenceReason = "UNSUPPORTED_CURRENCY"
	AbsenceInternalError       AbsenceReason = "INTERNAL_ERROR"
	AbsenceMoneyNotRequested   AbsenceReason = "MONEY_NOT_REQUESTED"
)

// DenialCount is how many of the publisher's items were refused for one
// reason in a report's period.
type DenialCount struct {
	Reason forav1.DenialReason
	Count  int64
}

// RevenueReport is a totals answer.
type RevenueReport struct {
	Period   Period
	Grouping Grouping
	Currency string
	Buckets  []RevenueBucket
	Total    RevenueFigures
	// Denials counts the publisher's refused items over the whole period, per
	// reason, in the protocol's enum order, for the whole publisher whatever
	// the narrowing. It is empty when the answer is UNSUPPORTED_CURRENCY, which
	// reads nothing, and on a count-only query, which reads the counts alone.
	Denials []DenialCount
	Absence AbsenceReason
	// bounds are the buckets' intervals alone, for the bucket search.
	bounds []Period
}

// Totals reports what the ledger settled to the publisher in the period, per
// bucket and in total, beside how many transactions the log recorded.
//
// The two sources are read separately on purpose. The money is what the ledger
// settled, so a transaction whose settlement failed is counted but adds no
// money. The ledger stamps a settlement a few seconds after the log stamps its
// transaction, so a transaction logged just before a bucket boundary can have
// its money in the next bucket.
//
// A currency the deployment keeps no ledger in is answered before anything is
// read. A count-only query stops after the counts. A failed ledger read is not
// an error: the counts are still true, so the answer carries them, no money,
// and AbsenceInternalError. A failed lookup of a narrowed read's settlements
// in the transaction log is an internal error.
//
// Beside the money, the answer counts the publisher's refused items by reason,
// read from the transaction event journal for the whole period. A refused item
// moves no money, so the count is the same whichever currency is asked for, and
// it never changes the absence reason, which describes sales. The count is the
// whole publisher's: a resource or domain narrowing does not narrow it, and a
// count-only query does not read it.
func (s *PublisherReadService) Totals(
	ctx context.Context, caller ConsoleCaller, q RevenueQuery,
) (RevenueReport, error) {
	scope, err := s.scope(ctx, caller, q.PublisherID, q.Narrowing)
	if err != nil {
		return RevenueReport{}, err
	}
	period, err := NewPeriod(q.Start, q.End)
	if err != nil {
		return RevenueReport{}, err
	}
	buckets, err := Buckets(period, q.Grouping)
	if err != nil {
		return RevenueReport{}, err
	}
	report := newRevenueReport(period, q.Grouping,
		strings.ToUpper(cmp.Or(q.Currency, billing.BaseCurrency(s.cfg.Currencies))), buckets)
	if !slices.Contains(s.cfg.Currencies, report.Currency) {
		return report.absent(AbsenceUnsupportedCurrency), nil
	}

	counts, err := s.activity.TransactionCounts(ctx, repo.TransactionCountQuery{
		PublisherScope: scope, Currency: report.Currency,
		From: period.Start, To: period.End, BucketStarts: report.starts(),
	})
	if err != nil {
		return RevenueReport{}, exchange.Wrap(exchange.KindInternal, err, "count publisher transactions")
	}
	report.count(counts)
	if q.CountOnly {
		report.Absence = AbsenceMoneyNotRequested
		if report.Total.TransactionCount == 0 {
			report.Absence = AbsenceNoTransactions
		}
		return report, nil
	}
	report.Denials, err = s.denialCounts(ctx, repo.DenialCountQuery{
		TenantID: scope.TenantID, PublisherID: q.PublisherID, From: period.Start, To: period.End,
	})
	if err != nil {
		return RevenueReport{}, err
	}
	tally, err := s.settled(ctx, scope, &report)
	// The ledger's own errors carry no domain kind; one that does is the
	// narrowing's lookup in the transaction log, which the answer's counts
	// came from too. That is a failure of the read, not a ledger outage.
	if lookup, ok := errors.AsType[*exchange.Error](err); ok {
		return RevenueReport{}, lookup
	}
	switch {
	case errors.Is(err, billing.ErrRevenueCurrencyUnsupported):
		return report.absent(AbsenceUnsupportedCurrency), nil
	case err != nil:
		reqctx.FromContext(ctx).ErrorContext(ctx, "publisher revenue ledger read failed",
			slog.String("publisher_id", q.PublisherID), slog.String("currency", report.Currency),
			slog.Any("err", err))
		report.Absence = AbsenceInternalError
		return report, nil
	}
	report.addMoney(tally)
	if report.Total.TransactionCount == 0 && tally.settlements == 0 {
		report.Absence = AbsenceNoTransactions
	}
	return report, nil
}

// denialCounts reads the publisher's refused items in the query's period from
// the journal and puts them in the protocol's enum order, so every answer lists
// its reasons the same way.
func (s *PublisherReadService) denialCounts(ctx context.Context, q repo.DenialCountQuery) ([]DenialCount, error) {
	rows, err := s.denials.DenialCounts(ctx, q)
	if err != nil {
		return nil, exchange.Wrap(exchange.KindInternal, err, "count publisher denials")
	}
	out := make([]DenialCount, 0, len(rows))
	for _, row := range rows {
		reason, ok := denialFromLabel(row.Reason)
		if !ok {
			return nil, exchange.Newf(exchange.KindInternal,
				"the journal holds denial reason %q, which the protocol does not name", row.Reason)
		}
		out = append(out, DenialCount{Reason: reason, Count: row.Count})
	}
	slices.SortFunc(out, func(a, b DenialCount) int { return cmp.Compare(a.Reason, b.Reason) })
	return out, nil
}

// ledgerTally is the money the ledger settled, per bucket and in total, kept
// apart from the report until the whole read has succeeded, so a read that
// fails partway leaves no money in the answer.
type ledgerTally struct {
	buckets     []RevenueFigures
	total       RevenueFigures
	settlements int
}

// settled reads the publisher's settlements in the report's period and adds
// each to the bucket that holds its ledger time. The ledger hands them over
// one page at a time, so a long period is never held in memory at once. When
// scope names a resource or a domain, only the settlements inside it are added.
func (s *PublisherReadService) settled(
	ctx context.Context, scope repo.PublisherScope, r *RevenueReport,
) (ledgerTally, error) {
	tally := ledgerTally{buckets: make([]RevenueFigures, len(r.bounds)), total: zeroFigures()}
	for i := range tally.buckets {
		tally.buckets[i] = zeroFigures()
	}
	add := func(st billing.Settlement) error {
		// Both sources are read with the period as their bounds, so a
		// settlement outside every bucket is outside the period, and it is
		// dropped rather than put in the wrong bucket.
		if i := bucketIndex(r.bounds, st.SettledAt); i >= 0 {
			tally.buckets[i].addSettlement(st)
			tally.total.addSettlement(st)
			tally.settlements++
		}
		return nil
	}
	visit, flush := add, func() error { return nil }
	if scope.Narrowed() {
		f := &scopedSettlements{activity: s.activity, scope: scope, add: add, batch: s.cfg.SettlementBatch}
		visit = func(st billing.Settlement) error { return f.hold(ctx, st) }
		flush = func() error { return f.flush(ctx) }
	}
	err := s.ledger.OwnerSettlements(ctx, billing.SettlementQuery{
		OwnerID: scope.PublisherID, Currency: r.Currency, From: r.Period.Start, To: r.Period.End,
	}, visit)
	if err == nil {
		err = flush()
	}
	return tally, err
}

func newRevenueReport(p Period, g Grouping, currency string, buckets []Period) RevenueReport {
	out := RevenueReport{
		Period: p, Grouping: g, Currency: currency,
		Buckets: make([]RevenueBucket, len(buckets)),
		Total:   zeroFigures(),
		bounds:  buckets,
	}
	for i, b := range buckets {
		out.Buckets[i] = RevenueBucket{Period: b, RevenueFigures: zeroFigures()}
	}
	return out
}

// absent returns the report with no figures at all and reason as its absence.
func (r RevenueReport) absent(reason AbsenceReason) RevenueReport {
	out := newRevenueReport(r.Period, r.Grouping, r.Currency, r.bounds)
	out.Absence = reason
	return out
}

// starts returns the start of every bucket, in order.
func (r RevenueReport) starts() []time.Time {
	out := make([]time.Time, 0, len(r.bounds))
	for _, b := range r.bounds {
		out = append(out, b.Start)
	}
	return out
}

// count sets each bucket's transaction count and the total. counts holds one
// entry per bucket.
func (r *RevenueReport) count(counts []int64) {
	for i, n := range counts {
		r.Buckets[i].TransactionCount = n
		r.Total.TransactionCount += n
	}
}

// addMoney moves a completed ledger read into the report.
func (r *RevenueReport) addMoney(t ledgerTally) {
	for i := range r.Buckets {
		count := r.Buckets[i].TransactionCount
		r.Buckets[i].RevenueFigures = t.buckets[i]
		r.Buckets[i].TransactionCount = count
	}
	count := r.Total.TransactionCount
	r.Total = t.total
	r.Total.TransactionCount = count
}
