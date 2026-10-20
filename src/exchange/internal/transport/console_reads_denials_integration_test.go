//go:build integration

package transport_test

import (
	"maps"
	"testing"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/google/uuid"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
)

// Denial counts on the console revenue totals route. A denial is written by
// ExecuteTransaction refusing an item and read back by the totals route, so
// every leg crosses the public surface: RPC → service → journal → Postgres →
// console route → service → journal. Nothing is asserted below the route.
//
// No route exposes one recorded event, so a test sees a denial as a count: per
// reason, per publisher, per period. That is enough to pin the reason, the
// publisher it is charged to, the seat, and the decision time; the offer, the
// resource and the agent on the row are not readable through any route.

// overBudgetEst prices an offer at twice the agent's funding: 0.05 per unit for
// an estimated 4000 units is 200.00, against the 100.00 the fixture credits.
const overBudgetEst = 4000

// denialsOf returns the denials member of a totals answer, keyed by reason. It
// fails the test unless the member is a list — [] when empty, never null or
// missing — of protocol DenialReason names other than UNSPECIFIED, each with a
// count, in strictly increasing enum number. That order is what the route
// promises, so every read checks it, and it also means each reason is named
// once.
func denialsOf(t *testing.T, body map[string]any) map[string]float64 {
	t.Helper()
	rows := rowsOf(t, body, "denials")
	out := make(map[string]float64, len(rows))
	var last int32 // UNSPECIFIED: every listed reason must come after it
	for _, row := range rows {
		reason := field(t, row, "reason")
		number, named := forav1.DenialReason_value[reason]
		if !named || number <= last {
			t.Fatalf("denials are not protocol reasons in increasing enum order: %v", rows)
		}
		count, ok := row["count"].(float64)
		if !ok {
			t.Fatalf("denial %s has no numeric count: %v", reason, row)
		}
		out[reason], last = count, number
	}
	return out
}

// wantDenials asserts the seller's denials around the Exchange clock are
// exactly want, and that the bystander — another publisher on the same seat —
// has none: a denial is charged to the publisher whose resource was refused,
// and to no other.
func (p *publisherConsole) wantDenials(t *testing.T, s, bystander seller, want map[string]float64) {
	t.Helper()
	period := aroundClock(p.clk)
	if got := denialsOf(t, p.totalsIn(t, s, period)); !maps.Equal(got, want) {
		t.Fatalf("%s denials = %v, want %v", s.publisherID, got, want)
	}
	if other := denialsOf(t, p.totalsIn(t, bystander, period)); len(other) != 0 {
		t.Fatalf("bystander %s was charged denials %v it had nothing to do with", bystander.publisherID, other)
	}
}

// only is the expected counts when one item was refused for reason.
func only(reason forav1.DenialReason) map[string]float64 {
	return map[string]float64{reason.String(): 1}
}

// newTxKey is a fresh request idempotency key.
func newTxKey() string { return "tx-" + uuid.NewString() }

// TestConsoleReads_TotalsCarryAnEmptyDenialList pins the shape a publisher that
// was never refused reads: denials is present and empty, a list rather than
// null, so the console never has to tell "none" from "missing".
func TestConsoleReads_TotalsCarryAnEmptyDenialList(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s := p.newSeller(t, "denial-free")

	if got := denialsOf(t, p.totals(t, s, consoleapi.GroupingDay, "")); len(got) != 0 {
		t.Fatalf("a publisher never refused reads denials %v, want none", got)
	}
}

// TestConsoleReads_DenialOfferExpired: an authentic offer presented after it
// expired is refused before the Exchange looks its resource up, and is still
// charged to the publisher, found through the offer's signed canonical URL.
func TestConsoleReads_DenialOfferExpired(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s, bystander := p.newSeller(t, "stoa"), p.newSeller(t, "harmonia")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(1))
	p.clk.Advance(10 * time.Minute)

	resp, err := executeOfferRawWithID(t, p.h, offer, newTxKey())
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_OFFER_EXPIRED)
	p.wantDenials(t, s, bystander, only(forav1.DenialReason_DENIAL_REASON_OFFER_EXPIRED))
}

// TestConsoleReads_DenialAcceptanceSignatureInvalid: the request proves the
// agent's key, but one item's own acceptance is signed by another key. The
// item is refused after its catalog entry resolved, so it is charged.
func TestConsoleReads_DenialAcceptanceSignatureInvalid(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s, bystander := p.newSeller(t, "stoa"), p.newSeller(t, "harmonia")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(1))
	key := newTxKey()

	resp, err := executeItems(t, p.h, key, &forav1.TransactionItem{
		Offer: offer, AgentAcceptance: mintWrongKeyAcceptanceFor(t, offer, agentRequester(), key),
	})
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_SIGNATURE_INVALID)
	p.wantDenials(t, s, bystander, only(forav1.DenialReason_DENIAL_REASON_SIGNATURE_INVALID))
}

// TestConsoleReads_DenialReportingOverdue: one sale's report is past its
// deadline, so the agent's next purchase is refused. The sale itself is not a
// denial, and the refusal is read over the clock the gate ran on.
func TestConsoleReads_DenialReportingOverdue(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s, bystander := p.newSeller(t, "stoa"), p.newSeller(t, "harmonia")
	p.sell(t, s, pathFor(1), seedPricedTermEst(1))
	p.clk.Advance(pastReportingWindow)
	offer := p.offerFor(t, s, pathFor(2), seedPricedTermEst(1))

	resp, err := executeOfferRawWithID(t, p.h, offer, newTxKey())
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_REPORTING_OVERDUE)
	p.wantDenials(t, s, bystander, only(forav1.DenialReason_DENIAL_REASON_REPORTING_OVERDUE))
}

// TestConsoleReads_DenialInsufficientBalance: the ledger refuses a charge
// larger than the agent's funds. With no sale in the period the answer still
// says NO_TRANSACTIONS_IN_PERIOD, which describes sales, beside the denial.
func TestConsoleReads_DenialInsufficientBalance(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s, bystander := p.newSeller(t, "stoa"), p.newSeller(t, "harmonia")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(overBudgetEst))

	resp, err := executeOfferRawWithID(t, p.h, offer, newTxKey())
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)
	p.wantDenials(t, s, bystander, only(forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE))
	if body := p.totalsIn(t, s, aroundClock(p.clk)); body["absence_reason"] != consoleapi.AbsenceNoTransactions {
		t.Fatalf("absence_reason = %v, want %s: a denial is not a sale",
			body["absence_reason"], consoleapi.AbsenceNoTransactions)
	}
}

// TestConsoleReads_DenialAccountInactive: the operator switched the agent's
// account off in the account registry, so its paid purchase is refused.
func TestConsoleReads_DenialAccountInactive(t *testing.T) {
	registry := newSorControl()
	p := startPublisherConsoleWith(t, publisherConsoleOptions{sor: registry})
	s, bystander := p.newSeller(t, "stoa"), p.newSeller(t, "harmonia")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(1))
	registry.setActive(t, p.h.billingRef, false)

	resp, err := executeOfferRawWithID(t, p.h, offer, newTxKey())
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_ACCOUNT_INACTIVE)
	p.wantDenials(t, s, bystander, only(forav1.DenialReason_DENIAL_REASON_ACCOUNT_INACTIVE))
}

// TestConsoleReads_DenialAccountNotRegistered: an agent whose key the Exchange
// knows, but which never called Register, has no account to charge.
func TestConsoleReads_DenialAccountNotRegistered(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s, bystander := p.newSeller(t, "stoa"), p.newSeller(t, "harmonia")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(1))
	stranger := p.h.addUnregisteredCaller(t, "agent-unregistered")

	resp, err := executeClaimedAs(t, p.h, stranger, newTxKey(), offer)
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_ACCOUNT_NOT_REGISTERED)
	p.wantDenials(t, s, bystander, only(forav1.DenialReason_DENIAL_REASON_ACCOUNT_NOT_REGISTERED))
}

// TestConsoleReads_DenialsAreListedInProtocolOrder: the route promises its
// denials in the protocol's enum order, and denialsOf checks that order on
// every read. The three refusals here are recorded in the reverse of it, their
// names sort alphabetically in another order, and the database's own
// fora.denial_reason enum lists ACCOUNT_NOT_REGISTERED second. None of those
// orders is the protocol's, so only the Exchange's own sort produces the answer
// read here.
func TestConsoleReads_DenialsAreListedInProtocolOrder(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s, bystander := p.newSeller(t, "stoa"), p.newSeller(t, "harmonia")

	stranger := p.h.addUnregisteredCaller(t, "agent-unregistered")
	unregistered := p.offerFor(t, s, pathFor(1), seedPricedTermEst(1))
	resp, err := executeClaimedAs(t, p.h, stranger, newTxKey(), unregistered)
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_ACCOUNT_NOT_REGISTERED)

	misSigned, key := p.offerFor(t, s, pathFor(2), seedPricedTermEst(1)), newTxKey()
	resp, err = executeItems(t, p.h, key, &forav1.TransactionItem{
		Offer: misSigned, AgentAcceptance: mintWrongKeyAcceptanceFor(t, misSigned, agentRequester(), key),
	})
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_SIGNATURE_INVALID)

	overBudget := p.offerFor(t, s, pathFor(3), seedPricedTermEst(overBudgetEst))
	resp, err = executeOfferRawWithID(t, p.h, overBudget, newTxKey())
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)

	p.wantDenials(t, s, bystander, map[string]float64{
		forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE.String():   1,
		forav1.DenialReason_DENIAL_REASON_SIGNATURE_INVALID.String():      1,
		forav1.DenialReason_DENIAL_REASON_ACCOUNT_NOT_REGISTERED.String(): 1,
	})
}

// TestConsoleReads_DenialIsStampedWithTheExchangeClock pins the decision time.
// The Exchange's clock is moved two days past the wall clock first, so a
// denial stamped by the database's clock instead would fall two days before
// the decision, outside both periods read here. The period is half-open: a
// denial at its end belongs to the next one.
func TestConsoleReads_DenialIsStampedWithTheExchangeClock(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s := p.newSeller(t, "stoa")
	p.clk.Advance(48 * time.Hour)
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermEst(overBudgetEst))
	decided := p.clk.Now().UTC()

	resp, err := executeOfferRawWithID(t, p.h, offer, newTxKey())
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)

	before := consoleapi.Period{Start: decided.Add(-time.Hour), End: decided}
	if got := denialsOf(t, p.totalsIn(t, s, before)); len(got) != 0 {
		t.Fatalf("the period ending at the decision counts %v; it must count none", got)
	}
	from := consoleapi.Period{Start: decided, End: decided.Add(time.Hour)}
	want := only(forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)
	if got := denialsOf(t, p.totalsIn(t, s, from)); !maps.Equal(got, want) {
		t.Fatalf("the period starting at the decision counts %v, want %v", got, want)
	}
}

// TestConsoleReads_DenialCountIsNotScopedByCurrency: the ledger keeps USD only,
// so an offer priced in EUR is refused by the ledger as INSUFFICIENT_BALANCE. A
// refused item moves no money, so the USD answer counts it; filtered by the
// offer's currency it could never be seen, because EUR is answered
// UNSUPPORTED_CURRENCY before anything is read.
func TestConsoleReads_DenialCountIsNotScopedByCurrency(t *testing.T) {
	p := startPublisherConsole(t, 0)
	s := p.newSeller(t, "stoa")
	offer := p.offerFor(t, s, pathFor(1), seedPricedTermCurrency("EUR"))

	resp, err := executeOfferRawWithID(t, p.h, offer, newTxKey())
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)

	period := aroundClock(p.clk)
	usd := p.totalsRead(t, s, consoleapi.RevenueTotalsRequest{
		Period: period, Grouping: consoleapi.GroupingDay, Currency: "USD",
	})
	want := only(forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE)
	if got := denialsOf(t, usd); !maps.Equal(got, want) {
		t.Fatalf("USD totals count denials %v, want the EUR offer's %v", got, want)
	}
	eur := p.totalsRead(t, s, consoleapi.RevenueTotalsRequest{
		Period: period, Grouping: consoleapi.GroupingDay, Currency: "EUR",
	})
	if eur["absence_reason"] != consoleapi.AbsenceUnsupportedCurrency {
		t.Fatalf("EUR absence_reason = %v, want %s", eur["absence_reason"], consoleapi.AbsenceUnsupportedCurrency)
	}
	if got := denialsOf(t, eur); len(got) != 0 {
		t.Fatalf("an UNSUPPORTED_CURRENCY answer carries denials %v; nothing is read for it", got)
	}
}
