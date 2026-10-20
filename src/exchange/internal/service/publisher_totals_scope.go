package service

import (
	"context"
	"fmt"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// DefaultSettlementBatch is how many settlements a narrowed totals read holds
// back before the log is asked which of them are inside the scope, unless
// PublisherReadConfig.SettlementBatch says otherwise.
const DefaultSettlementBatch = 500

// scopedSettlements keeps the settlements of one resource or one domain and
// drops the rest.
//
// The ledger records whose account a settlement credited and nothing about
// what was sold. What it does carry is the billing id of the hold the charge
// was reserved on, which the transaction log records beside the resource and
// its domain. So settlements are held back in batches, the log is asked which
// of a batch's billing ids are charges inside the scope, and only those are
// added: one query per batch rather than one per settlement, and never more
// than a batch in memory. The money is still the ledger's; the log only says
// which of it to count.
type scopedSettlements struct {
	activity repo.PublisherActivityRepo
	// scope names the seat, the publisher, and the resource or domain.
	scope repo.PublisherScope
	add   func(billing.Settlement) error
	batch int
	held  []billing.Settlement
}

// hold takes one settlement, and asks about the batch once it is full.
func (f *scopedSettlements) hold(ctx context.Context, st billing.Settlement) error {
	f.held = append(f.held, st)
	if len(f.held) < f.batch {
		return nil
	}
	return f.flush(ctx)
}

// flush asks the log about the settlements held back and adds those inside
// the scope.
func (f *scopedSettlements) flush(ctx context.Context) error {
	if len(f.held) == 0 {
		return nil
	}
	q := repo.BillingScopeQuery{PublisherScope: f.scope, BillingIDs: make([]string, 0, len(f.held))}
	for _, st := range f.held {
		q.BillingIDs = append(q.BillingIDs, st.BillingID)
	}
	owned, err := f.activity.ScopedBillingIDs(ctx, q)
	if err != nil {
		// Typed here, before it goes back through the ledger's callback, so
		// Totals can tell the log failing from the ledger failing: the first
		// leaves no true count to answer with.
		return exchange.Wrap(exchange.KindInternal, err, fmt.Sprintf(
			"attribute settlements to resource %q domain %q", f.scope.ResourceID, f.scope.Domain))
	}
	for _, st := range f.held {
		if !owned[st.BillingID] {
			continue
		}
		if err := f.add(st); err != nil {
			return err
		}
	}
	f.held = f.held[:0]
	return nil
}
