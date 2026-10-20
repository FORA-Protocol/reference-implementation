//go:build integration

package app_test

import (
	"encoding/base64"
	"encoding/json"
	"html"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// allTransactions reads every one of the publisher's transactions this month
// from the Exchange's list route, page by page, newest first.
func allTransactions(t *testing.T, f *consoleFixture, resourceID string) []exchangeclient.TransactionRow {
	t.Helper()
	return walkRoute(t, "list", func(cursor string) ([]exchangeclient.TransactionRow, string) {
		page, err := f.reads().ListTransactions(t.Context(), exchangeclient.ListTransactionsRequest{
			Actor: f.actor(), PublisherID: f.publisherID, Period: thisMonth(),
			ResourceID: resourceID, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("list route: %v", err)
		}
		return page.Transactions, page.NextCursor
	})
}

func idsOf(rows []exchangeclient.TransactionRow) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.TransactionID)
	}
	return ids
}

// TestDashboard_NextWalksEveryPage sells five times and walks the list two
// rows at a time by its Next links alone. The pages together hold every
// transaction the list route returns, in the same order, once each, and each
// page numbers its rows out of the period's count.
func TestDashboard_NextWalksEveryPage(t *testing.T) {
	f := startSeller(t, withPageSize(2))
	buyer := f.exchange.Buyer(t)
	for i := range 5 {
		sell(t, buyer, "/articles/"+strconv.Itoa(i))
	}
	want := idsOf(allTransactions(t, f, ""))
	if len(want) != 5 {
		t.Fatalf("the list route holds %d transactions, want the 5 sales", len(want))
	}

	var got []string
	path, pages := dashboard.PathTransactions, 0
	for path != "" {
		body := f.get2(t, path)
		pages++
		first := len(got) + 1
		ids := transactionIDs(body)
		got = append(got, ids...)
		mustContain(t, "page "+strconv.Itoa(pages), body,
			"Transactions "+strconv.Itoa(first)+"–"+strconv.Itoa(len(got))+" of 5")
		path = nextLink(body)
		if pages > maxWalk {
			t.Fatalf("still walking after %d pages: the Next link does not move on", pages)
		}
	}
	if pages != 3 || !slices.Equal(got, want) {
		t.Fatalf("walked %d pages holding %v, want 3 pages holding %v", pages, got, want)
	}
}

// TestDashboard_PreviousWalksBackThroughEveryPage walks the list forward two
// rows at a time, then back from the last page by its Previous links alone.
// Each page on the way back holds the rows and the numbering it held on the
// way forward, and the first page has no Previous link.
func TestDashboard_PreviousWalksBackThroughEveryPage(t *testing.T) {
	f := startSeller(t, withPageSize(2))
	buyer := f.exchange.Buyer(t)
	for i := range 5 {
		sell(t, buyer, "/articles/"+strconv.Itoa(i))
	}

	var forward [][]string
	var positions []string
	body := f.get2(t, dashboard.PathTransactions)
	if prevLink(body) != "" {
		t.Fatal("the first page has a Previous link")
	}
	for {
		forward = append(forward, transactionIDs(body))
		positions = append(positions, rePosition.FindString(body))
		next := nextLink(body)
		if next == "" || len(forward) > maxWalk {
			break
		}
		body = f.get2(t, next)
	}
	if len(forward) != 3 {
		t.Fatalf("walked forward through %d pages, want 3", len(forward))
	}

	for i := len(forward) - 2; i >= 0; i-- {
		prev := prevLink(body)
		if prev == "" {
			t.Fatalf("page %d has no Previous link", i+2)
		}
		body = f.get2(t, prev)
		if got := transactionIDs(body); !slices.Equal(got, forward[i]) {
			t.Fatalf("back on page %d: rows %v, want %v", i+1, got, forward[i])
		}
		if got := rePosition.FindString(body); got != positions[i] {
			t.Fatalf("back on page %d: %q, want %q", i+1, got, positions[i])
		}
	}
	if prevLink(body) != "" {
		t.Fatal("walking back reached a first page that still has a Previous link")
	}
}

// rePosition finds the line that numbers a list page's rows.
var rePosition = regexp.MustCompile(`Transactions \d+–\d+( of \d+)?`)

// TestDashboard_TransactionRowsAreWhatTheListRouteReturns pins each row to the
// route's row: its resource, its agent, its unit price and currency, and its
// reporting status as the Exchange computed it, one of each: a report owed, a
// report received, and a sale that owes none.
func TestDashboard_TransactionRowsAreWhatTheListRouteReturns(t *testing.T) {
	f := startSeller(t)
	buyer := f.exchange.Buyer(t)
	owed := sell(t, buyer, "/articles/owed")
	reported := sell(t, buyer, "/articles/reported")
	if err := buyer.Report(t, reported, 20); err != nil {
		t.Fatalf("report: %v", err)
	}
	buyer.Buy(t, "stoa.example", "/articles/once", exchangetest.UnmeteredTerm("0.05"))

	body := f.get2(t, dashboard.PathTransactions)
	rows := allTransactions(t, f, "")
	onPage := listRows(body)
	statuses := map[string]string{}
	for _, row := range rows {
		statuses[row.TransactionID] = row.ReportingStatus
		segment, ok := onPage[row.TransactionID]
		if !ok {
			t.Fatalf("the list has no row for %s", row.TransactionID)
		}
		mustContain(t, "the row of "+row.TransactionID, segment, html.EscapeString(row.ResourceURI), row.AgentID,
			row.UnitCost+" "+row.Currency, `data-status="`+row.ReportingStatus+`"`)
	}
	if statuses[owed.TransactionID] != consoleapi.ReportingPending ||
		statuses[reported.TransactionID] != consoleapi.ReportingReceived ||
		!slices.Contains(slices.Collect(maps.Values(statuses)), consoleapi.ReportingNotRequired) {
		t.Fatalf("the route's statuses are not one of each: %v", statuses)
	}
	mustContain(t, "the list", body, buyer.ID(), "Pending", "Received", "Not required", "When (UTC)")
}

// listRows splits a transaction list page into its rows, keyed by the
// transaction each opens, so an assertion about a row reads that row alone.
func listRows(body string) map[string]string {
	out := map[string]string{}
	for _, segment := range strings.Split(body, `<tr class="row-link">`)[1:] {
		segment, _, _ = strings.Cut(segment, "</tr>")
		if m := reTransactionLink.FindStringSubmatch(segment); m != nil {
			out[m[1]] = segment
		}
	}
	return out
}

// TestDashboard_AnUnreportedSaleTurnsOverdue moves the Exchange's clock past
// the reporting window. The row then shows the overdue status the Exchange
// derives; the console computes no status of its own.
func TestDashboard_AnUnreportedSaleTurnsOverdue(t *testing.T) {
	deployment, clk := testClock()
	f := startSellerOver(t, deployment)
	sale := sell(t, f.exchange.Buyer(t), "/articles/late")
	clk.Advance(25 * time.Hour)

	rows := allTransactions(t, f, "")
	if len(rows) != 1 || rows[0].ReportingStatus != consoleapi.ReportingOverdue {
		t.Fatalf("the route does not call the sale overdue: %+v", rows)
	}
	body := f.get2(t, dashboard.PathTransactions)
	mustContain(t, "the list", body, `data-status="overdue"`, "Overdue")
	detail := f.get2(t, dashboard.PathTransactions+"/"+sale.TransactionID)
	mustContain(t, "the detail", detail, `data-status="overdue"`)
}

// TestDashboard_TheResourceFilterNarrowsTheList filters by one resource. Only
// its transactions are listed, exactly as the list route filters them, and
// the "of N" beside the row numbers counts that resource's transactions, not
// every resource's.
func TestDashboard_TheResourceFilterNarrowsTheList(t *testing.T) {
	f := startSeller(t)
	buyer := f.exchange.Buyer(t)
	one := sell(t, buyer, "/articles/one")
	sell(t, buyer, "/articles/one")
	other := sell(t, buyer, "/articles/other")
	resourceID := ""
	for _, row := range allTransactions(t, f, "") {
		if row.TransactionID == one.TransactionID {
			resourceID = row.ResourceID
		}
	}
	want := idsOf(allTransactions(t, f, resourceID))

	body := f.get2(t, dashboard.PathTransactions+"?resource="+url.QueryEscape(resourceID))
	if got := transactionIDs(body); !slices.Equal(got, want) || len(got) != 2 {
		t.Fatalf("filtered rows = %v, want the route's %v", got, want)
	}
	mustNotContain(t, "the filtered list", body, other.TransactionID, " of 3")
	mustContain(t, "the filtered list", body, "Showing one resource only", "Transactions 1–2 of 2")
}

// TestDashboard_TransactionDetailShowsTheSignedOffer opens one sale. The page
// shows what the agent accepted, read back from the offer bytes the Exchange
// signed, and carries those bytes with both signatures and keys for the
// browser to verify: the same values the detail route returns. The page is
// sold again at a higher price after the sale, which changes its catalog
// entry, and the sale's page still shows the price the agent accepted.
func TestDashboard_TransactionDetailShowsTheSignedOffer(t *testing.T) {
	f := startSeller(t)
	buyer := f.exchange.Buyer(t)
	sale := sell(t, buyer, "/articles/one")
	buyer.Buy(t, "stoa.example", "/articles/one", exchangetest.PerUnitTerm("0.07", 20))

	body := f.get2(t, dashboard.PathTransactions+"/"+sale.TransactionID)
	mustContain(t, "the detail", body, sale.TransactionID, buyer.ID(), sale.URI,
		"0.05 USD per accesses", "The agent owes a usage report by", `data-status="pending"`, "Verify")
	mustNotContain(t, "the detail", body, "0.07")

	detail, err := f.reads().TransactionDetail(t.Context(), exchangeclient.TransactionDetailRequest{
		Actor: f.actor(), PublisherID: f.publisherID, TransactionID: sale.TransactionID,
	})
	if err != nil {
		t.Fatalf("detail route: %v", err)
	}
	evidence := evidenceOf(t, body)
	b64 := base64.StdEncoding.EncodeToString
	for key, want := range map[string]string{
		"offer_canonical_bytes":            b64(detail.Evidence.OfferCanonicalBytes),
		"offer_sig":                        detail.Evidence.OfferSig,
		"exchange_signing_public_key":      b64(detail.Evidence.ExchangeSigningPublicKey),
		"agent_acceptance_canonical_bytes": b64(detail.Evidence.AgentAcceptanceCanonicalBytes),
		"agent_acceptance_signature":       detail.Evidence.AgentAcceptanceSignature,
		"agent_public_key":                 b64(detail.Evidence.AgentPublicKey),
	} {
		if evidence[key] != want || want == "" {
			t.Errorf("evidence %s = %q, want the route's %q", key, evidence[key], want)
		}
	}
	if len(evidence) != 6 {
		t.Errorf("the page hands the browser %d evidence fields, want exactly the 6 it verifies", len(evidence))
	}
}

// reEvidence finds the evidence the detail page hands its Verify button.
var reEvidence = regexp.MustCompile(`data-evidence="([^"]*)"`)

// evidenceOf reads that evidence the way the browser does: the attribute,
// unescaped, as JSON.
func evidenceOf(t *testing.T, body string) map[string]string {
	t.Helper()
	m := reEvidence.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the detail page carries no evidence: %s", head(body))
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &out); err != nil {
		t.Fatalf("the evidence is not JSON: %v", err)
	}
	return out
}

// TestDashboard_AMalformedCursorIsRefused hands the list a cursor the Exchange
// never issued. The Exchange refuses it and the page says so.
func TestDashboard_AMalformedCursorIsRefused(t *testing.T) {
	f := startSeller(t)
	body := f.status(t, dashboard.PathTransactions+"?cursor=not-a-cursor", http.StatusBadRequest)
	mustContain(t, "a refused cursor", body, "That did not work")
}

// TestDashboard_ATitledResourceIsShownByItsTitle sells a page listed with a
// headline and one listed without. The transaction list and the catalog show
// the first by its headline, with its address in the cell's tooltip, and the
// second by its address.
func TestDashboard_ATitledResourceIsShownByItsTitle(t *testing.T) {
	f := startSeller(t)
	buyer := f.exchange.Buyer(t)
	titled := buyer.BuyTitled(t, "stoa.example", "/articles/rivers", "On rivers", exchangetest.PerUnitTerm("0.05", 20))
	plain := sell(t, buyer, "/articles/plain")
	cell := func(uri, shown string) *regexp.Regexp {
		return regexp.MustCompile(`class="cell-clip" title="` + regexp.QuoteMeta(uri) + `">\s*` + regexp.QuoteMeta(shown) + `\s*<`)
	}
	for _, path := range []string{dashboard.PathTransactions, dashboard.PathCatalog} {
		body := f.get2(t, path)
		if !cell(titled.URI, "On rivers").MatchString(body) {
			t.Errorf("%s does not show %s by its title with its address beside it", path, titled.URI)
		}
		if !cell(plain.URI, plain.URI).MatchString(body) {
			t.Errorf("%s does not show the untitled %s by its address", path, plain.URI)
		}
	}
}

// TestDashboard_TheListReadsThePeriodAsked sells once today, then opens the
// list over last month and over this month. Last month shows the empty state
// and not the sale; this month lists it: the period in the address is the
// period the Exchange is asked for.
func TestDashboard_TheListReadsThePeriodAsked(t *testing.T) {
	f := startSeller(t)
	sale := sell(t, f.exchange.Buyer(t), "/articles/one")
	now := time.Now().UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	period := func(from, to time.Time) string {
		return dashboard.PathTransactions + "?from=" + from.Format("2006-01-02") + "&to=" + to.Format("2006-01-02")
	}

	lastMonth := f.get2(t, period(first.AddDate(0, -1, 0), first.AddDate(0, 0, -1)))
	mustContain(t, "last month", lastMonth, "No transactions in this period.")
	mustNotContain(t, "last month", lastMonth, sale.TransactionID)
	mustContain(t, "this month", f.get2(t, period(first, now)), sale.TransactionID)
}
