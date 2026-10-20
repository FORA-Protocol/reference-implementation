//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"maps"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/proto"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
)

// The denials the journal must NOT record, the retries it must not record
// twice, and a journal write that fails. A denial that is not recorded leaves
// no row, and no route lists rows, so each absence below is observed the only
// way the public surface allows: the publisher the refused offer names reads no
// denial through the totals route. A write that fails is also visible to the
// operator, as one ERROR line.

// wantNoDenials asserts the seller reads no denial around the Exchange clock.
func (p *publisherConsole) wantNoDenials(t *testing.T, s seller, why string) {
	t.Helper()
	if got := denialsOf(t, p.totalsIn(t, s, aroundClock(p.clk))); len(got) != 0 {
		t.Fatalf("%s, yet %s reads denials %v", why, s.publisherID, got)
	}
}

// journalFailures returns the records the Exchange logged for journal writes
// that failed. They share their msg with the request's outcome line, which
// carries no event, so the event tells them apart.
func journalFailures(t *testing.T, logs *testutil.LogCapture) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, rec := range logs.Records(t, "exchange.execute_transaction") {
		if rec["event"] == "denial_journal_failed" {
			out = append(out, rec)
		}
	}
	return out
}

// wantOneJournalFailure asserts that exactly one journal write failed, and that
// its ERROR line names what an operator needs to find the lost denial: the
// offer, the publisher it would have been charged to and the reason, with the
// driver's own error and the request id.
func wantOneJournalFailure(
	t *testing.T, logs *testutil.LogCapture, offer *forav1.Offer, s seller, reason forav1.DenialReason,
) {
	t.Helper()
	recs := journalFailures(t, logs)
	if len(recs) != 1 {
		t.Fatalf("%d denial_journal_failed records, want exactly 1: %v", len(recs), recs)
	}
	rec := recs[0]
	for key, want := range map[string]any{
		"level": "ERROR", "offer_id": offer.GetOfferId(),
		"publisher_id": s.publisherID, "denial_reason": reason.String(),
	} {
		if rec[key] != want {
			t.Errorf("denial_journal_failed %s = %v, want %v: %v", key, rec[key], want, rec)
		}
	}
	if msg, _ := rec["err"].(string); !strings.Contains(msg, "closed pool") {
		t.Errorf("denial_journal_failed err = %q, want the driver's closed-pool error", msg)
	}
	if id, _ := rec["request_id"].(string); id == "" {
		t.Errorf("denial_journal_failed carries no request_id: %v", rec)
	}
}

// TestConsoleReads_NoDenialForATamperedOffer: an offer whose Exchange signature
// fails names its resource on the agent's word alone, so the refusal is charged
// to no publisher — not even the one the offer names.
func TestConsoleReads_NoDenialForATamperedOffer(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s := p.newSeller(t, "stoa")
	tampered, ok := proto.Clone(p.offerFor(t, s, pathFor(1), seedPricedTermEst(1))).(*forav1.Offer)
	if !ok {
		t.Fatal("clone offer")
	}
	tampered.Pricing.Rate = "0.01"

	resp, err := executeOfferRawWithID(t, p.h, tampered, newTxKey())
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_SIGNATURE_INVALID)
	p.wantNoDenials(t, s, "the offer's signature did not verify")
}

// TestConsoleReads_NoDenialForWithdrawnContent: an authentic offer for a
// resource the catalog no longer holds has no catalog entry, so there is no
// publisher to charge — even though the URL is under the seller's domain.
func TestConsoleReads_NoDenialForWithdrawnContent(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s := p.newSeller(t, "stoa")
	withdrawn := p.withdrawnOffer(t, s)

	resp, err := executeOfferRawWithID(t, p.h, withdrawn, newTxKey())
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_CONTENT_UNAVAILABLE)
	p.wantNoDenials(t, s, "the offer's resource is not in the catalog")
}

// TestConsoleReads_NoDenialForAnExpiredOfferWhoseResourceIsGone: expiry is
// checked before the catalog, so the agent is told OFFER_EXPIRED. The attribution
// lookup finds no entry, so nothing is recorded: an expired offer is charged
// only when its resource is still in the catalog.
func TestConsoleReads_NoDenialForAnExpiredOfferWhoseResourceIsGone(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s := p.newSeller(t, "stoa")
	withdrawn := p.withdrawnOffer(t, s)
	p.clk.Advance(10 * time.Minute)

	resp, err := executeOfferRawWithID(t, p.h, withdrawn, newTxKey())
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_OFFER_EXPIRED)
	p.wantNoDenials(t, s, "the expired offer's resource is not in the catalog")
}

// TestConsoleReads_NoDenialWithoutProvenPossession: a request with no
// request-level proof whose only item carries an acceptance signed by another
// key proves no possession of the agent's key. Its item still runs and is
// refused after the catalog entry resolved — attributable — but a request that
// proved nothing records nothing.
func TestConsoleReads_NoDenialWithoutProvenPossession(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s := p.newSeller(t, "stoa")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(1))
	c, key := p.h.defaultCaller(), newTxKey()

	resp, err := c.client.ExecuteTransaction(p.h.ctx, connect.NewRequest(requestFor(c, key,
		&forav1.TransactionItem{Offer: offer, AgentAcceptance: mintWrongKeyAcceptanceFor(t, offer, c.requester, key)})))
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_SIGNATURE_INVALID)
	p.wantNoDenials(t, s, "the request proved no possession of the agent key")
}

// TestConsoleReads_NoDenialForARefusedRequestProof: a request-level proof that
// does not verify refuses the whole request before any item runs. No item is
// denied, so nothing is recorded, although its item would have been refused
// for its price.
func TestConsoleReads_NoDenialForARefusedRequestProof(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s := p.newSeller(t, "stoa")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(overBudgetEst))
	req := claimedItemRequest(t, p.h.defaultCaller(), newTxKey(), offer)
	_, wrong, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("wrong-key ed25519: %v", err)
	}
	req.AgentRequestAcceptance = signRequestAcceptanceFor(t, wrong, req)

	_, err = p.h.exchangeClient.ExecuteTransaction(p.h.ctx, connect.NewRequest(req))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("a request proof signed by another key: err = %v, want %s", err, connect.CodeUnauthenticated)
	}
	p.wantNoDenials(t, s, "the whole request was refused before any item ran")
}

// TestConsoleReads_ExactRetryRecordsTheDenialOnce: an SDK client's exact retry
// is answered from the response finalized onto its claim, denial included, and
// runs no item again. The count alone cannot show that second half: a retry
// that ran the item again would have its event dropped by the journal's key,
// and its own answer would lose to the finalized one, so the agent would read
// the same denial either way. So the operator switches the account back on
// between the two calls. From then on, running the item again would sell it,
// and neither the account registry nor the seller's totals may show a trace of
// that.
func TestConsoleReads_ExactRetryRecordsTheDenialOnce(t *testing.T) {
	registry := newSorControl()
	p := startPublisherConsoleWith(t, publisherConsoleOptions{sor: registry})
	s, bystander := p.newSeller(t, "stoa"), p.newSeller(t, "harmonia")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(1))
	registry.setActive(t, p.h.billingRef, false)
	key := newTxKey()

	resp, err := executeOfferRawWithID(t, p.h, offer, key)
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_ACCOUNT_INACTIVE)
	registry.setActive(t, p.h.billingRef, true)
	asked := registry.callCount()

	resp, err = executeOfferRawWithID(t, p.h, offer, key)
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_ACCOUNT_INACTIVE)
	if got := registry.callCount() - asked; got != 0 {
		t.Fatalf("the retry asked the account registry %d time(s); a replayed retry runs no item", got)
	}
	body := p.totalsIn(t, s, aroundNow())
	if body["absence_reason"] != consoleapi.AbsenceNoTransactions {
		t.Fatalf("absence_reason = %v, want %s: the retry sold the item it only had to replay",
			body["absence_reason"], consoleapi.AbsenceNoTransactions)
	}
	wantTotal(t, body, "0", "0", "0", 0)
	p.wantDenials(t, s, bystander, only(forav1.DenialReason_DENIAL_REASON_ACCOUNT_INACTIVE))
}

// TestConsoleReads_OlderClientRetryRecordsTheDenialOnce: a client that sends no
// request-level proof but signs its item proves possession, and gets no claim,
// so its retry of a denied item runs the item again and raises the same
// denial. The journal already holds it for this agent and item key, so the
// retry records nothing — and that is not a write failure: no ERROR line. The
// account registry is asked once more for the retry, which shows the item did
// run again: a retry answered from a claim would leave the journal's key
// untested.
func TestConsoleReads_OlderClientRetryRecordsTheDenialOnce(t *testing.T) {
	logs, logger := testutil.NewLogCapture()
	registry := newSorControl()
	p := startPublisherConsoleWith(t, publisherConsoleOptions{logger: logger, sor: registry})
	s, bystander := p.newSeller(t, "stoa"), p.newSeller(t, "harmonia")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(overBudgetEst))
	c, key := p.h.defaultCaller(), newTxKey()

	resp, err := executeSingleItemAs(t, p.h, c, key, offer)
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)
	asked := registry.callCount()
	resp, err = executeSingleItemAs(t, p.h, c, key, offer)
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)
	if got := registry.callCount() - asked; got != 1 {
		t.Fatalf("the retry asked the account registry %d time(s), want 1: an older client's retry runs the item again", got)
	}
	p.wantDenials(t, s, bystander, only(forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE))
	if failed := journalFailures(t, logs); len(failed) != 0 {
		t.Fatalf("the retry's denial was treated as a failed write: %v", failed)
	}
}

// TestConsoleReads_FailedJournalWriteKeepsTheBatch: the journal cannot be
// written, because its pool is closed, the state a lost database connection
// leaves it in. A two-item batch runs anyway: the first item sells, then the
// second is refused. The failed write must not cost the agent its purchase
// (ADR-027 D6): the response carries both items, the claim is finalized so an
// exact retry is answered with the same response, and the sale reaches the
// publisher's totals. The lost denial shows once in the operator's log and not
// in the counts, which is why the counts are a lower bound.
//
// Legs: ExecuteTransaction → service → journal port → closed pool for the
// write; the Exchange's own log for the ERROR line; the totals route → service
// → Postgres and ledger for the sale and the counts.
func TestConsoleReads_FailedJournalWriteKeepsTheBatch(t *testing.T) {
	logs, logger := testutil.NewLogCapture()
	p := startPublisherConsoleWith(t, publisherConsoleOptions{logger: logger, journalPool: closedPool(t)})
	s := p.newSeller(t, "stoa")
	sold := p.offerFor(t, s, pathFor(1), seedPricedTermEst(1))
	refused := p.offerFor(t, s, pathFor(2), seedPricedTermEst(overBudgetEst))
	c, key := p.h.defaultCaller(), newTxKey()
	items := []*forav1.TransactionItem{signedItem(t, c, key, sold), signedItem(t, c, key, refused)}

	first, err := executeItems(t, p.h, key, items...)
	if err != nil {
		t.Fatalf("a batch whose denial could not be journaled failed: %v", err)
	}
	got := first.Msg.GetItems()
	if len(got) != 2 || got[0].GetTransactionId() == "" || got[0].GetRetrievalEndpoint() == "" ||
		got[1].GetDenialReason() != forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE {
		t.Fatalf("want the first item sold and the second refused INSUFFICIENT_BALANCE, got %v", got)
	}
	wantOneJournalFailure(t, logs, refused, s, forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)

	second, err := executeItems(t, p.h, key, items...)
	if err != nil {
		t.Fatalf("the exact retry was refused, so the claim was not finalized: %v", err)
	}
	if !proto.Equal(first.Msg, second.Msg) {
		t.Fatalf("the exact retry was answered differently:\n first=%v\nsecond=%v", first.Msg, second.Msg)
	}
	wantTotal(t, p.totalsIn(t, s, aroundNow()), "0.05", "0", "0.05", 1)
	p.wantNoDenials(t, s, "the journal write failed")
}

// TestConsoleReads_SameKeyFromTwoAgentsRecordsBoth: idempotency is scoped to
// the agent, so a second agent presenting the same offer under the
// same request key is a different decision, and both are recorded.
func TestConsoleReads_SameKeyFromTwoAgentsRecordsBoth(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s, bystander := p.newSeller(t, "stoa"), p.newSeller(t, "harmonia")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(overBudgetEst))
	key := newTxKey()

	resp, err := executeOfferRawWithID(t, p.h, offer, key)
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)
	stranger := p.h.addUnregisteredCaller(t, "agent-unregistered")
	resp, err = executeClaimedAs(t, p.h, stranger, key, offer)
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_ACCOUNT_NOT_REGISTERED)

	p.wantDenials(t, s, bystander, map[string]float64{
		forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE.String():   1,
		forav1.DenialReason_DENIAL_REASON_ACCOUNT_NOT_REGISTERED.String(): 1,
	})
}

// TestConsoleReads_DenialsSurviveAnUnreadableLedger reads totals from the
// in-memory adapter, which keeps no settlement history, so the money cannot be
// read and the answer is INTERNAL_ERROR. The denials come from the database,
// not the ledger, so they are still reported, like the transaction count.
func TestConsoleReads_DenialsSurviveAnUnreadableLedger(t *testing.T) {
	p := startPublisherConsoleReading(t, 0, billing.NewInMemoryAdapter(billing.InMemoryOptions{}))
	s := p.newSeller(t, "stoa")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(overBudgetEst))

	resp, err := executeOfferRawWithID(t, p.h, offer, newTxKey())
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)

	body := p.totalsIn(t, s, aroundClock(p.clk))
	if body["absence_reason"] != consoleapi.AbsenceInternalError {
		t.Fatalf("absence_reason = %v, want %s", body["absence_reason"], consoleapi.AbsenceInternalError)
	}
	want := only(forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)
	if got := denialsOf(t, body); !maps.Equal(got, want) {
		t.Fatalf("with the ledger unreadable, denials = %v, want %v", got, want)
	}
}

// withdrawnOffer is an authentic offer for a path under the seller's domain
// that was never pushed: a genuine offer re-signed by the Exchange's own key
// onto that URL, the shape an agent holds after the publisher withdraws the
// content.
func (p *publisherConsole) withdrawnOffer(t *testing.T, s seller) *forav1.Offer {
	t.Helper()
	genuine := p.offerFor(t, s, pathFor(1), seedPricedTermEst(1))
	return resignedOffer(t, p.h, genuine, func(o *forav1.Offer) {
		u := "https://" + s.domain + "/articles/withdrawn"
		o.Identity.CanonicalUrl = &u
	})
}
