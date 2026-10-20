//go:build integration

package tigerbeetle_test

import (
	"context"
	"fmt"
	"math/big"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tigerbeetle"
)

// postedHolds is one account credited by n posted holds of amounts 1..n, made
// oldest first. The account also holds the n pending transfers themselves, so
// a scan of its credits reads 2n transfers.
type postedHolds struct {
	credit  tigerbeetle.ID
	pending []tigerbeetle.ID // in the order they were posted
}

// postHolds creates n pending transfers of amounts 1..n into one account and
// posts each in full, with the "whole pending amount" sentinel the zero-fee
// settlement uses.
func postHolds(t *testing.T, c *tigerbeetle.Client, n int) postedHolds {
	t.Helper()
	ctx := context.Background()
	debit, credit := twoAccounts(t, c)
	out := postedHolds{credit: credit, pending: make([]tigerbeetle.ID, 0, n)}
	for i := range n {
		pid := mustTransferID(t, fmt.Sprintf("%s-hold-%d", sharedTB.Salt(t), i))
		created, err := c.CreatePending(ctx, tigerbeetle.PendingTransfer{
			ID: pid, Debit: debit, Credit: credit, Amount: big.NewInt(int64(i + 1)), Ledger: testLedger,
		})
		if err != nil || created != tigerbeetle.PendingCreated {
			t.Fatalf("CreatePending %d = (%v, %v)", i, created, err)
		}
		posted, err := c.PostPending(ctx, tigerbeetle.ResolveParams{
			ID: mustTransferID(t, fmt.Sprintf("%s-post-%d", sharedTB.Salt(t), i)), PendingID: pid,
		})
		if err != nil || posted != tigerbeetle.ResolveApplied {
			t.Fatalf("PostPending %d = (%v, %v)", i, posted, err)
		}
		out.pending = append(out.pending, pid)
	}
	return out
}

// scanPosts reads every post in [minTS, maxTS] and counts the pages it came
// in.
func scanPosts(t *testing.T, q *tigerbeetle.Query, account tigerbeetle.ID, minTS, maxTS uint64) ([]tigerbeetle.PostedCredit, int) {
	t.Helper()
	var posts []tigerbeetle.PostedCredit
	pages := 0
	err := q.PostedCredits(context.Background(), account, minTS, maxTS, func(page []tigerbeetle.PostedCredit) error {
		pages++
		posts = append(posts, page...)
		return nil
	})
	if err != nil {
		t.Fatalf("PostedCredits: %v", err)
	}
	return posts, pages
}

// TestPostedCredits_ReadsEveryPostAcrossPages posts 260 holds. With the
// pending transfers the account holds 520 credits, more than two scan pages,
// so the scan has to follow its cursor to find them all. Every post comes back
// once, oldest first, carrying the amount it posted: a post made with the
// "whole pending amount" sentinel is stored with the resolved amount, which is
// what makes a zero-fee settlement read back as its gross.
func TestPostedCredits_ReadsEveryPostAcrossPages(t *testing.T) {
	c := newTestClient(t)
	holds := postHolds(t, c, 260)
	q := tigerbeetle.NewQuery(c)

	posts, pages := scanPosts(t, q, holds.credit, 0, 0)
	if len(posts) != len(holds.pending) {
		t.Fatalf("read %d posts, want %d", len(posts), len(holds.pending))
	}
	if pages < 2 {
		t.Errorf("the posts came in %d page(s); the test needs more than one to prove paging", pages)
	}
	for i, p := range posts {
		if p.PendingID != holds.pending[i] {
			t.Fatalf("post %d posts hold %s, want %s: out of order or repeated",
				i, tigerbeetle.EncodeID(p.PendingID), tigerbeetle.EncodeID(holds.pending[i]))
		}
		if p.Amount.Cmp(big.NewInt(int64(i+1))) != 0 {
			t.Errorf("post %d amount = %s, want the %d its hold reserved", i, p.Amount, i+1)
		}
		if i > 0 && p.Timestamp <= posts[i-1].Timestamp {
			t.Errorf("post %d is not after post %d", i, i-1)
		}
	}

	// The holds of one page are looked up in one call, and each reads back
	// the amount it reserved.
	amounts, err := q.TransferAmounts(context.Background(), holds.pending[:200])
	if err != nil {
		t.Fatalf("TransferAmounts: %v", err)
	}
	for i, id := range holds.pending[:200] {
		if got := amounts[id]; got == nil || got.Cmp(big.NewInt(int64(i+1))) != 0 {
			t.Errorf("hold %d amount = %v, want %d", i, got, i+1)
		}
	}
}

// TestPostedCredits_HoldsItsTimestampBounds reads between the timestamps of
// the 10th and the 20th post. Both bounds are inclusive, so exactly those 11
// posts come back.
func TestPostedCredits_HoldsItsTimestampBounds(t *testing.T) {
	c := newTestClient(t)
	holds := postHolds(t, c, 30)
	q := tigerbeetle.NewQuery(c)
	all, _ := scanPosts(t, q, holds.credit, 0, 0)

	posts, _ := scanPosts(t, q, holds.credit, all[10].Timestamp, all[20].Timestamp)
	if len(posts) != 11 {
		t.Fatalf("read %d posts between the bounds, want 11", len(posts))
	}
	if posts[0].PendingID != holds.pending[10] || posts[10].PendingID != holds.pending[20] {
		t.Errorf("the bounded read starts or ends on the wrong post")
	}
}

// TestTransferAmounts_RefusesMoreThanOnePage asks for more holds than one
// lookup carries. The caller is meant to pass one scan page at a time, so a
// longer list is refused rather than sent.
func TestTransferAmounts_RefusesMoreThanOnePage(t *testing.T) {
	q := tigerbeetle.NewQuery(newTestClient(t))
	if _, err := q.TransferAmounts(context.Background(), make([]tigerbeetle.ID, 255)); err == nil {
		t.Fatal("a lookup of 255 transfers was sent")
	}
}
