package tigerbeetle

import (
	"context"
	"fmt"
	"math/big"

	tb "github.com/tigerbeetle/tigerbeetle-go"
)

// accountPageLimit is the page size of every account-transfer scan: comfortably
// inside one message at every batch-size configuration TigerBeetle ships.
const accountPageLimit uint32 = 254

// scanAccountTransfers pages through the transfers filter selects, oldest
// first, and hands each non-empty page to visit. The scan starts at
// filter.TimestampMin; its Limit is set here. An error from visit stops the
// scan and is returned.
func (c *Client) scanAccountTransfers(
	ctx context.Context, filter tb.AccountFilter, visit func([]tb.Transfer) error,
) error {
	filter.Limit = accountPageLimit
	for {
		page, err := withDeadline(ctx, c.opTimeout, func() ([]tb.Transfer, error) {
			return c.inner.GetAccountTransfers(filter)
		})
		if err != nil {
			return err
		}
		if len(page) > 0 {
			if err := visit(page); err != nil {
				return err
			}
		}
		if len(page) < int(accountPageLimit) {
			return nil
		}
		filter.TimestampMin = page[len(page)-1].Timestamp + 1
	}
}

// Query is the ledger's reporting side: reads that scan an account's history
// rather than resolve one charge. It is a separate handle from Client, over the
// same connection, because reporting and the charge path have different
// callers; putting both on Client would give the charge path methods it never
// calls.
type Query struct{ c *Client }

// NewQuery returns the reporting reads over c.
func NewQuery(c *Client) *Query { return &Query{c: c} }

// PostedCredit is one post of a pending transfer that credited an account: the
// hold it posted, the amount it posted, and the cluster time it was posted at,
// in nanoseconds since the Unix epoch.
type PostedCredit struct {
	PendingID ID
	Amount    *big.Int
	Timestamp uint64
}

// PostedCredits hands every post-pending transfer that credited account with a
// cluster timestamp in [minTS, maxTS] to visit, oldest first, one page at a
// time. Pending holds, voids and plain transfers are left out: a hold is not
// money yet, and a void never becomes money. A page that holds no post is not
// handed over.
func (q *Query) PostedCredits(
	ctx context.Context, account ID, minTS, maxTS uint64, visit func([]PostedCredit) error,
) error {
	filter := tb.AccountFilter{
		AccountID:    account,
		TimestampMin: minTS,
		TimestampMax: maxTS,
		Flags:        tb.AccountFilterFlags{Credits: true}.ToUint32(),
	}
	err := q.c.scanAccountTransfers(ctx, filter, func(page []tb.Transfer) error {
		posts := make([]PostedCredit, 0, len(page))
		for i := range page {
			if page[i].TransferFlags().PostPendingTransfer {
				posts = append(posts, PostedCredit{
					PendingID: page[i].PendingID,
					Amount:    page[i].Amount.BigInt(),
					Timestamp: page[i].Timestamp,
				})
			}
		}
		if len(posts) == 0 {
			return nil
		}
		return visit(posts)
	})
	if err != nil {
		return fmt.Errorf("tigerbeetle: account credits: %w", err)
	}
	return nil
}

// TransferAmounts returns the amount of each transfer in ids that exists. An id
// with no transfer is absent from the map. It is one lookup, so ids is at most
// one scan page of PostedCredits: accountPageLimit ids fit in one message.
func (q *Query) TransferAmounts(ctx context.Context, ids []ID) (map[ID]*big.Int, error) {
	if len(ids) > int(accountPageLimit) {
		return nil, fmt.Errorf("tigerbeetle: lookup of %d transfers exceeds one page of %d",
			len(ids), accountPageLimit)
	}
	found, err := withDeadline(ctx, q.c.opTimeout, func() ([]tb.Transfer, error) {
		return q.c.inner.LookupTransfers(ids)
	})
	if err != nil {
		return nil, fmt.Errorf("tigerbeetle: lookup transfers: %w", err)
	}
	out := make(map[ID]*big.Int, len(found))
	for i := range found {
		out[found[i].ID] = found[i].Amount.BigInt()
	}
	return out, nil
}
