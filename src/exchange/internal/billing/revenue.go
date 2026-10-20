package billing

import (
	"context"
	"errors"
	"math/big"
	"time"
)

// Settlement is one charge the ledger settled to a resource owner: the gross
// the agent was charged, the net the owner was credited, and when the ledger
// posted it. Amounts are integer minor units at the ledger's asset scale. The
// platform's fee is Gross minus Net.
//
// BillingID is the hold the charge was reserved on, the same id Authorize
// returned and the transaction log records. The ledger knows nothing of what
// was sold, so this is the one way back from a settlement to its transaction.
type Settlement struct {
	SettledAt time.Time
	Gross     *big.Int
	Net       *big.Int
	BillingID string
}

// SettlementQuery selects the settlements to one resource owner in one
// currency whose ledger time is in [From, To).
type SettlementQuery struct {
	OwnerID  string
	Currency string
	From, To time.Time
}

// RevenueLedger reads what the ledger settled to one resource owner. It is the
// reporting side of the ledger, kept off Adapter because the charge path never
// reads it and a reporting read has nothing to do on a charge.
type RevenueLedger interface {
	// OwnerSettlements hands every settlement q selects to visit, oldest
	// first, one ledger page at a time, so a long period is never held in
	// memory at once. An error from visit stops the read and is returned.
	OwnerSettlements(ctx context.Context, q SettlementQuery, visit func(Settlement) error) error
}

// RevenueReporter is implemented by a billing adapter whose ledger can answer
// revenue reads. It is a capability rather than a method on Adapter: only a
// backend that keeps settlement history can implement it meaningfully.
type RevenueReporter interface {
	Revenue() RevenueLedger
}

// ErrRevenueCurrencyUnsupported reports a revenue read in a currency this
// deployment keeps no ledger for.
var ErrRevenueCurrencyUnsupported = errors.New("billing: no ledger is kept in this currency")

// ErrRevenueUnavailable reports a revenue read on a billing backend that keeps
// no settlement history, such as the in-memory adapter.
var ErrRevenueUnavailable = errors.New("billing: this billing backend keeps no settlement history")

// RevenueLedgerFor returns the revenue read of the adapter a deployment
// charges through, so the totals a publisher reads come from the same ledger
// its charges settled to. An adapter that does not implement RevenueReporter
// keeps no history, and every read of the result answers ErrRevenueUnavailable.
// A decorator around an adapter keeps its revenue read only by implementing
// RevenueReporter itself.
func RevenueLedgerFor(a Adapter) RevenueLedger {
	if r, ok := a.(RevenueReporter); ok {
		return r.Revenue()
	}
	return unavailableRevenue{}
}

// Revenue is the free adapter's revenue read: it charges nothing, so nothing
// is ever settled.
func (FreeAdapter) Revenue() RevenueLedger { return emptyRevenue{} }

type emptyRevenue struct{}

func (emptyRevenue) OwnerSettlements(context.Context, SettlementQuery, func(Settlement) error) error {
	return nil
}

type unavailableRevenue struct{}

func (unavailableRevenue) OwnerSettlements(context.Context, SettlementQuery, func(Settlement) error) error {
	return ErrRevenueUnavailable
}
