package billing

import (
	"context"
	"fmt"
	"math"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tigerbeetle"
)

// tigerBeetleRevenue reads an owner's settlements from the TigerBeetle ledger
// the adapter charges through.
//
// A settlement posts the net amount of the agent's hold to the owner's revenue
// account and, when a fee is due, moves the fee to the platform account in the
// same linked batch. So the owner account holds, per charge, one post-pending
// credit whose amount is the net and whose pending id names the hold, and the
// hold's amount is the gross. That is the whole computation: gross is the hold,
// net is the post, and the fee is their difference.
type tigerBeetleRevenue struct {
	adapter *TigerBeetleAdapter
	query   *tigerbeetle.Query
}

// Revenue is the TigerBeetle adapter's revenue read. It derives account ids
// through the adapter, so it reads the same owner account the adapter's
// settlements credit.
func (a *TigerBeetleAdapter) Revenue() RevenueLedger {
	return &tigerBeetleRevenue{adapter: a, query: tigerbeetle.NewQuery(a.tb)}
}

func (r *tigerBeetleRevenue) OwnerSettlements(
	ctx context.Context, q SettlementQuery, visit func(Settlement) error,
) (err error) {
	defer func() { err = classifyUnavailable(err) }()
	if _, ok := r.adapter.ledgerFor(q.Currency); !ok {
		return ErrRevenueCurrencyUnsupported
	}
	account, err := r.adapter.ownerAccount(q.Currency, q.OwnerID)
	if err != nil {
		return err
	}
	// Ledger timestamps are nanoseconds since the Unix epoch, and a filter
	// bound of 0 means "no bound". The window is half-open and the filter
	// inclusive, so the upper bound is one nanosecond before the end. A window
	// that ends at or before the ledger's first instant after the epoch holds
	// no ledger time at all: the cluster clock never reads 0.
	maxTS := ledgerTime(q.To)
	if !q.To.After(q.From) || maxTS <= 1 {
		return nil
	}
	return r.query.PostedCredits(ctx, account, ledgerTime(q.From), maxTS-1,
		func(posts []tigerbeetle.PostedCredit) error { return r.settle(ctx, posts, visit) })
}

// settle pairs one page of posts with the holds they posted and hands each
// settlement to visit.
func (r *tigerBeetleRevenue) settle(
	ctx context.Context, posts []tigerbeetle.PostedCredit, visit func(Settlement) error,
) error {
	holds := make([]tigerbeetle.ID, 0, len(posts))
	for _, p := range posts {
		holds = append(holds, p.PendingID)
	}
	gross, err := r.query.TransferAmounts(ctx, holds)
	if err != nil {
		return fmt.Errorf("billing: owner settlement holds: %w", err)
	}
	for _, p := range posts {
		g, ok := gross[p.PendingID]
		if !ok {
			return fmt.Errorf("billing: settlement %s posts a hold the ledger does not hold",
				tigerbeetle.EncodeID(p.PendingID))
		}
		if err := visit(Settlement{
			SettledAt: wallTime(p.Timestamp), Gross: g, Net: p.Amount,
			BillingID: tigerbeetle.EncodeID(p.PendingID),
		}); err != nil {
			return err
		}
	}
	return nil
}

// ledgerTime converts t to the ledger's clock: nanoseconds since the Unix
// epoch. A time before the epoch is clamped to 0, the ledger's first instant,
// and a time past the largest nanosecond count an int64 holds (the year 2262)
// to that count. Both clamps come before UnixNano, which is undefined outside
// that range.
func ledgerTime(t time.Time) uint64 {
	switch {
	case t.Before(time.Unix(0, 0)):
		return 0
	case t.After(time.Unix(0, math.MaxInt64)):
		return math.MaxInt64
	default:
		return uint64(t.UnixNano())
	}
}

// wallTime is the inverse of ledgerTime. A ledger time past the largest
// nanosecond count time.Time can hold (the year 2262) is clamped to it.
func wallTime(ts uint64) time.Time {
	if ts > math.MaxInt64 {
		return time.Unix(0, math.MaxInt64).UTC()
	}
	return time.Unix(0, int64(ts)).UTC()
}
