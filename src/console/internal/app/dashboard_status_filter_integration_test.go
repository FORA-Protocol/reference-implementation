//go:build integration

package app_test

import (
	"errors"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// The report status filter. The Exchange decides a transaction's status when
// it is read, from the obligation's state and whether its deadline has passed,
// so the filter runs in the Exchange's list query. These tests prove it
// narrows the list to the status each row is labelled with, keeps doing so on
// every page, and refuses a status that does not exist.

// statusPeriod is a few days around the Exchange clock's start. A sale made
// after the clock is moved on a day can fall in the next month, so these tests
// name their period rather than read the default one.
func statusPeriod(start time.Time) dashboard.Period {
	day := start.UTC().Truncate(24 * time.Hour)
	return dashboard.Period{From: day.AddDate(0, 0, -1), To: day.AddDate(0, 0, 3)}
}

// statusQuery is a transaction page's address over p, narrowed to status.
func statusQuery(p dashboard.Period, status string) string {
	q := url.Values{"from": {p.FromValue()}, "to": {p.ToValue()}}
	if status != "" {
		q.Set("status", status)
	}
	return dashboard.PathTransactions + "?" + q.Encode()
}

// listStatus reads every one of the publisher's transactions in p with status
// from the Exchange's list route, page by page.
func listStatus(t *testing.T, f *consoleFixture, p dashboard.Period, status string) []exchangeclient.TransactionRow {
	t.Helper()
	return walkRoute(t, "list", func(cursor string) ([]exchangeclient.TransactionRow, string) {
		page, err := f.reads().ListTransactions(t.Context(), exchangeclient.ListTransactionsRequest{
			Actor: f.actor(), PublisherID: f.publisherID, Period: p.Window(),
			ReportingStatus: status, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("list route with status %q: %v", status, err)
		}
		return page.Transactions, page.NextCursor
	})
}

// TestDashboard_TheStatusFilterNarrowsTheList makes one sale in each report
// status: one whose deadline has passed, one reported, one that owes nothing,
// and one made after the clock moved on, whose deadline is still ahead. That
// last one is bought by a second agent: the Exchange refuses to sell to an
// agent whose report is overdue, which the first one's now is. Each
// status then lists exactly its own sale, on the route and on the page, and
// every row it lists carries that status -- so the filter and the label follow
// the same rule.
func TestDashboard_TheStatusFilterNarrowsTheList(t *testing.T) {
	deployment, clk := testClock()
	period := statusPeriod(clk.Now())
	f := startSellerOver(t, deployment)
	buyer := f.exchange.Buyer(t)
	late := sell(t, buyer, "/articles/late")
	reported := sell(t, buyer, "/articles/reported")
	if err := buyer.Report(t, reported, 20); err != nil {
		t.Fatalf("report: %v", err)
	}
	free := buyer.Buy(t, "stoa.example", "/articles/free", exchangetest.UnmeteredTerm("0.05"))
	clk.Advance(25 * time.Hour)
	owed := sell(t, f.exchange.Buyer(t), "/articles/owed")
	want := map[string]string{
		consoleapi.ReportingOverdue:     late.TransactionID,
		consoleapi.ReportingReceived:    reported.TransactionID,
		consoleapi.ReportingNotRequired: free.TransactionID,
		consoleapi.ReportingPending:     owed.TransactionID,
	}
	if all := listStatus(t, f, period, ""); len(all) != 4 {
		t.Fatalf("the unfiltered route holds %d transactions, want the 4 sales", len(all))
	}

	for _, status := range consoleapi.AllReportingStatuses() {
		rows := listStatus(t, f, period, status)
		if len(rows) != 1 || rows[0].TransactionID != want[status] || rows[0].ReportingStatus != status {
			t.Errorf("the route with status %s holds %+v, want only %s", status, rows, want[status])
		}
		body := f.get2(t, statusQuery(period, status))
		if got := transactionIDs(body); !slices.Equal(got, []string{want[status]}) {
			t.Errorf("the page with status %s lists %v, want only %s", status, got, want[status])
		}
		// A narrowed list claims no total: the period's count is for every
		// status.
		mustContain(t, "the "+status+" list", body, `data-status="`+status+`"`,
			`<option value="`+status+`" selected>`, `<option value="">All statuses</option>`)
		mustNotContain(t, "the "+status+" list", body, " of 4")
	}
}

// TestDashboard_TheStatusFilterSurvivesEveryPage walks a status-filtered list
// one row at a time by its Next links. A Next link that dropped the status
// would widen the list halfway through, so the walk must hold exactly what the
// route returns for that status, and never the sale in another status.
func TestDashboard_TheStatusFilterSurvivesEveryPage(t *testing.T) {
	deployment, clk := testClock()
	period := statusPeriod(clk.Now())
	f := startSellerOver(t, deployment, withPageSize(1))
	buyer := f.exchange.Buyer(t)
	reported := sell(t, buyer, "/articles/reported")
	if err := buyer.Report(t, reported, 20); err != nil {
		t.Fatalf("report: %v", err)
	}
	for i := range 3 {
		sell(t, buyer, "/articles/owed-"+strconv.Itoa(i))
	}
	want := idsOf(listStatus(t, f, period, consoleapi.ReportingPending))
	if len(want) != 3 {
		t.Fatalf("the route holds %d pending transactions, want 3", len(want))
	}

	var got []string
	var body, last string
	path, pages := statusQuery(period, consoleapi.ReportingPending), 0
	for path != "" {
		body = f.get2(t, path)
		last = body
		pages++
		got = append(got, transactionIDs(body)...)
		path = nextLink(body)
		if pages > maxWalk {
			t.Fatalf("still walking after %d pages: the Next link does not move on", pages)
		}
	}
	if pages != 3 || !slices.Equal(got, want) || slices.Contains(got, reported.TransactionID) {
		t.Fatalf("walked %d pages holding %v, want 3 pages holding %v", pages, got, want)
	}

	// The way back keeps the status too: from the last page, Previous twice
	// is the first page of pending sales, not of every sale.
	var back []string
	for path = prevLink(last); path != ""; path = prevLink(body) {
		body = f.get2(t, path)
		back = append(slices.Clone(transactionIDs(body)), back...)
	}
	if !slices.Equal(back, want[:2]) {
		t.Fatalf("walking back holds %v, want the first two pending sales %v", back, want[:2])
	}

	// A status nothing is in gets the empty state, not every row.
	body = f.get2(t, statusQuery(period, consoleapi.ReportingOverdue))
	if ids := transactionIDs(body); len(ids) != 0 {
		t.Fatalf("the overdue list shows %v, want none", ids)
	}
	mustContain(t, "the overdue list", body, "No transactions in this period with this report status.")
}

// TestDashboard_AnUnknownStatusIsRefused asks for a status that does not
// exist. The page says so rather than listing nothing, which would read as
// "none of these"; and the route refuses it as a bad request rather than
// answering an empty page.
func TestDashboard_AnUnknownStatusIsRefused(t *testing.T) {
	f := startSeller(t)
	sell(t, f.exchange.Buyer(t), "/articles/one")

	body := f.status(t, dashboard.PathTransactions+"?status=disputed", 400)
	mustContain(t, "the refusal", body, "That is not a report status the list can show.")
	if ids := transactionIDs(body); len(ids) != 0 {
		t.Fatalf("the refusal lists %v", ids)
	}

	page, err := f.reads().ListTransactions(t.Context(), exchangeclient.ListTransactionsRequest{
		Actor: f.actor(), PublisherID: f.publisherID, Period: thisMonth(), ReportingStatus: "disputed",
	})
	xe, ok := errors.AsType[*exchangeclient.Error](err)
	if !ok || xe.Kind != consoleapi.ErrorKindInvalidRequest {
		t.Fatalf("the route answered %+v, %v; want invalid_request", page, err)
	}
}
