//go:build integration

package transport_test

import (
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// The transaction list and detail through the console routes. Every
// transaction is made by ExecuteTransaction and every report by ReportUsage;
// the reads are the console routes. The reporting status that is decided by a
// deadline is driven by the service clock the Exchange mints deadlines with.

// TestConsoleReads_TransactionPagesAreNewestFirstWithNoGapOrRepeat sells three
// resources and pages through them two at a time. The two pages together hold
// each transaction exactly once, newest first, and the last page says it is
// the last. Another publisher's sale on the same seat is on neither page.
func TestConsoleReads_TransactionPagesAreNewestFirstWithNoGapOrRepeat(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	sold := make([]string, 0, 3)
	for i := range 3 {
		sold = append(sold, p.sell(t, s, pathFor(i), seedPricedTermEst(20)).GetTransactionId())
	}
	other := p.newSeller(t, "harmonia")
	p.sell(t, other, pathFor(9), seedPricedTermEst(20))

	first := p.transactionsPage(t, s, "", 2, "")
	cursor := field(t, first, "next_cursor")
	if cursor == "" {
		t.Fatal("the first of two pages has no next_cursor")
	}
	second := p.transactionsPage(t, s, "", 2, cursor)
	if next := field(t, second, "next_cursor"); next != "" {
		t.Fatalf("the last page has next_cursor %q", next)
	}
	rows := append(rowsOf(t, first, "transactions"), rowsOf(t, second, "transactions")...)
	if len(rows) != len(sold) {
		t.Fatalf("pages hold %d rows, want %d: %v", len(rows), len(sold), rows)
	}
	listed := make([]string, 0, len(rows))
	var previous time.Time
	for i, row := range rows {
		listed = append(listed, field(t, row, "transaction_id"))
		at, err := time.Parse(time.RFC3339Nano, field(t, row, "created_at"))
		if err != nil {
			t.Fatalf("created_at does not parse: %v", row)
		}
		if i > 0 && at.After(previous) {
			t.Errorf("row %d is newer than the row before it", i)
		}
		previous = at
	}
	slices.Sort(listed)
	slices.Sort(sold)
	if !slices.Equal(listed, sold) {
		t.Fatalf("listed %v, sold %v", listed, sold)
	}
}

// TestConsoleReads_TransactionListFiltersByResource sells one resource twice
// and another once, and lists only the first.
func TestConsoleReads_TransactionListFiltersByResource(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	first := p.sell(t, s, pathFor(1), seedPricedTermEst(20))
	p.sell(t, s, pathFor(1), seedPricedTermEst(20))
	p.sell(t, s, pathFor(2), seedPricedTermEst(20))

	resourceID := ""
	for _, row := range rowsOf(t, p.transactionsPage(t, s, "", 0, ""), "transactions") {
		if field(t, row, "transaction_id") == first.GetTransactionId() {
			resourceID = field(t, row, "resource_id")
		}
	}
	if resourceID == "" {
		t.Fatal("the first sale is not in the unfiltered list")
	}
	rows := rowsOf(t, p.transactionsPage(t, s, resourceID, 0, ""), "transactions")
	if len(rows) != 2 {
		t.Fatalf("filtered list holds %d rows, want 2: %v", len(rows), rows)
	}
	for _, row := range rows {
		if field(t, row, "resource_id") != resourceID {
			t.Errorf("filtered list holds resource %v", row["resource_id"])
		}
		if !strings.HasSuffix(field(t, row, "resource_uri"), s.domain+pathFor(1)) {
			t.Errorf("resource_uri = %v, want the page at %s", row["resource_uri"], pathFor(1))
		}
		if field(t, row, "unit_cost") != "0.05" || field(t, row, "currency") != harnessCurrency {
			t.Errorf("price = %v %v, want 0.05 %s", row["unit_cost"], row["currency"], harnessCurrency)
		}
		if field(t, row, "agent_id") != p.h.callerID {
			t.Errorf("agent_id = %v, want %s", row["agent_id"], p.h.callerID)
		}
	}
}

// TestConsoleReads_TransactionListShowsEveryReportingStatus makes one
// transaction in each reporting state. The unmetered sale owes no report; one
// metered sale is reported and accepted; one gets a report refused for a wrong
// billing_id and so still owes one; the last is left alone until the clock moves
// past its deadline. The refused one is overdue after the move too. Before the
// move and after it, the list narrowed to each status holds exactly the rows
// the whole list labels with that status.
func TestConsoleReads_TransactionListShowsEveryReportingStatus(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	free := p.sell(t, s, pathFor(1), seedMeteringNoneTerm())
	reported := p.sell(t, s, pathFor(2), seedPricedTermEst(20))
	refused := p.sell(t, s, pathFor(3), seedPricedTermEst(20))
	owed := p.sell(t, s, pathFor(4), seedPricedTermEst(20))
	if err := p.report(t, reported, 20); err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := p.reportBilledAs(t, refused, "wrong-billing-id", 20); err == nil {
		t.Fatal("a report naming the wrong billing_id was accepted")
	}

	want := map[string]string{
		free.GetTransactionId():     consoleapi.ReportingNotRequired,
		reported.GetTransactionId(): consoleapi.ReportingReceived,
		refused.GetTransactionId():  consoleapi.ReportingPending,
		owed.GetTransactionId():     consoleapi.ReportingPending,
	}
	assertStatuses(t, p, s, want)
	refusedDetail := p.detail(t, s, refused.GetTransactionId())
	if o := refusedDetail.ObligationState; o == nil || o.ValidationOutcome == nil ||
		*o.ValidationOutcome != "REJECTED_BILLING_ID" {
		t.Errorf("the refused report's outcome is not shown: %+v", refusedDetail.ObligationState)
	}

	p.clk.Advance(pastReportingWindow)
	want[refused.GetTransactionId()] = consoleapi.ReportingOverdue
	want[owed.GetTransactionId()] = consoleapi.ReportingOverdue
	assertStatuses(t, p, s, want)
	if got := p.detail(t, s, owed.GetTransactionId()).ReportingStatus; got != consoleapi.ReportingOverdue {
		t.Errorf("detail reporting_status = %q, want %q", got, consoleapi.ReportingOverdue)
	}
}

// TestConsoleReads_StatusJustPastADeadlineAgreesWithTheFilter moves the clock
// to half a microsecond past an owed report's deadline. Postgres keeps the
// deadline to the microsecond, and the list query compares it with an as_of of
// the same precision, so at that instant the deadline has not passed for the
// filter. The label each row shows is decided at the same instant and agrees:
// the sale is pending in the whole list and in the list narrowed to pending,
// and absent from the list narrowed to overdue.
func TestConsoleReads_StatusJustPastADeadlineAgreesWithTheFilter(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	owed := p.sell(t, s, pathFor(1), seedPricedTermEst(20))
	obligation := p.detail(t, s, owed.GetTransactionId()).ObligationState
	if obligation == nil {
		t.Fatal("the metered sale owes no report")
	}

	p.clk.SetNow(obligation.WindowEnd.Add(500 * time.Nanosecond))
	assertStatuses(t, p, s, map[string]string{owed.GetTransactionId(): consoleapi.ReportingPending})
	if got := p.detail(t, s, owed.GetTransactionId()).ReportingStatus; got != consoleapi.ReportingPending {
		t.Errorf("detail reporting_status = %q, want %q", got, consoleapi.ReportingPending)
	}
}

func assertStatuses(t *testing.T, p *publisherConsole, s seller, want map[string]string) {
	t.Helper()
	rows := rowsOf(t, p.transactionsPage(t, s, "", 0, ""), "transactions")
	if len(rows) != len(want) {
		t.Fatalf("list holds %d rows, want %d", len(rows), len(want))
	}
	for _, row := range rows {
		id := field(t, row, "transaction_id")
		if got := field(t, row, "reporting_status"); got != want[id] {
			t.Errorf("transaction %s reporting_status = %q, want %q", id, got, want[id])
		}
	}
	for _, status := range consoleapi.AllReportingStatuses() {
		var wantIDs []string
		for id, st := range want {
			if st == status {
				wantIDs = append(wantIDs, id)
			}
		}
		filtered := rowsOf(t, p.transactionsRead(t, s, consoleapi.ListTransactionsRequest{
			Period: aroundNow(), ReportingStatus: status,
		}), "transactions")
		gotIDs := make([]string, 0, len(filtered))
		for _, row := range filtered {
			gotIDs = append(gotIDs, field(t, row, "transaction_id"))
		}
		slices.Sort(wantIDs)
		slices.Sort(gotIDs)
		if !slices.Equal(gotIDs, wantIDs) {
			t.Errorf("list narrowed to %s holds %v, want %v", status, gotIDs, wantIDs)
		}
	}
}

// detail reads one transaction's evidence through the console route.
func (p *publisherConsole) detail(t *testing.T, s seller, transactionID string) consoleapi.TransactionDetail {
	t.Helper()
	return decodeDetail(t, p.detailBody(t, s, transactionID))
}

// detailBody reads one transaction's evidence through the console route and
// returns the body as the server sent it.
func (p *publisherConsole) detailBody(t *testing.T, s seller, transactionID string) map[string]any {
	t.Helper()
	return p.read(t, transport.ConsoleTransactionDetailPath, consoleapi.TransactionDetailRequest{
		Actor: s.actor, PublisherID: s.publisherID, TransactionID: transactionID,
	})
}

// decodeDetail decodes a detail body into the evidence shape and validates it.
func decodeDetail(t *testing.T, body map[string]any) consoleapi.TransactionDetail {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("re-marshal detail: %v", err)
	}
	var out consoleapi.TransactionDetail
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("detail is not the evidence shape: %v", err)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("detail does not validate as evidence: %v", err)
	}
	return out
}

// TestConsoleReads_TransactionDetailIsVerifiableEvidence reads one sale's
// evidence. Both signatures verify over the returned bytes with the returned
// keys, which is the check the ledger tool runs, and the signed URL is present
// only as its digest.
func TestConsoleReads_TransactionDetailIsVerifiableEvidence(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	item := p.sell(t, s, pathFor(1), seedPricedTermEst(20))

	body := p.detailBody(t, s, item.GetTransactionId())
	d := decodeDetail(t, body)
	if d.Evidence.TransactionID != item.GetTransactionId() {
		t.Fatalf("evidence is for %q, want %q", d.Evidence.TransactionID, item.GetTransactionId())
	}
	assertOfferSignatureVerifies(t, d.Evidence)
	assertAcceptanceVerifies(t, p.h, d.Evidence)
	assertOfferNamesTheResource(t, d, s.domain+pathFor(1))

	wantDigest := sha256.Sum256([]byte(item.GetRetrievalEndpoint()))
	if string(d.TransactionState.SignedURLHash) != string(wantDigest[:]) {
		t.Error("signed_url_hash is not the digest of the delivered URL")
	}
	assertNoSignedURL(t, body, item.GetRetrievalEndpoint())
	if d.ReportingStatus != consoleapi.ReportingPending {
		t.Errorf("reporting_status = %q, want %q", d.ReportingStatus, consoleapi.ReportingPending)
	}
}

// assertOfferNamesTheResource decodes the signed offer bytes, which is where
// the console reads a sale's terms and price from, and checks they describe
// the resource that was sold at the price it was sold for.
func assertOfferNamesTheResource(t *testing.T, d consoleapi.TransactionDetail, resource string) {
	t.Helper()
	var offer struct {
		Identity struct {
			CanonicalURL string `json:"canonical_url"`
		} `json:"identity"`
		Pricing struct {
			Rate     string `json:"rate"`
			Currency string `json:"currency"`
		} `json:"pricing"`
	}
	if err := json.Unmarshal(d.Evidence.OfferCanonicalBytes, &offer); err != nil {
		t.Fatalf("offer canonical bytes are not JSON: %v", err)
	}
	if !strings.HasSuffix(offer.Identity.CanonicalURL, resource) {
		t.Errorf("signed offer names %q, want the page %s", offer.Identity.CanonicalURL, resource)
	}
	if offer.Pricing.Rate != "0.05" || offer.Pricing.Currency != harnessCurrency {
		t.Errorf("signed offer prices at %q %q, want the 0.05 %s the resource was sold at",
			offer.Pricing.Rate, offer.Pricing.Currency, harnessCurrency)
	}
}
