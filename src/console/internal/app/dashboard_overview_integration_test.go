//go:build integration

package app_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// TestDashboard_OverviewShowsWhatTheTotalsRouteReturns sells twice and opens
// the overview with no period in its address. It shows this month, and every
// figure on it is the one the Exchange's totals route returns for the same
// publisher and month: the count, the gross, the seat's fee and the net, in
// the ledger's currency.
func TestDashboard_OverviewShowsWhatTheTotalsRouteReturns(t *testing.T) {
	f := startSeller(t)
	buyer := f.exchange.Buyer(t)
	sell(t, buyer, "/articles/one")
	sell(t, buyer, "/articles/two")

	body := f.get2(t, dashboard.PathOverview)
	totals, err := f.reads().RevenueTotals(t.Context(), exchangeclient.RevenueTotalsRequest{
		Actor: f.actor(), PublisherID: f.publisherID, Period: thisMonth(), Grouping: consoleapi.GroupingDay,
	})
	if err != nil {
		t.Fatalf("totals route: %v", err)
	}
	if totals.Total.TransactionCount != 2 || totals.Total.GrossRevenue != "2" || totals.Total.PlatformFee != "0.2" {
		t.Fatalf("the Exchange's totals are not the two sales at 10%%: %+v", totals.Total)
	}
	money := func(amount string) string { return `stat-value">` + amount + ` <small>` + totals.Currency + `</small>` }
	mustContain(t, "the overview", body,
		`stat-value">`+strconv.FormatInt(totals.Total.TransactionCount, 10)+`<`,
		money(totals.Total.GrossRevenue), money(totals.Total.PlatformFee), money(totals.Total.NetRevenue),
		"Amounts are what the ledger settled",
		"Transactions per day (UTC)",
		dashboardMonthLabel(),
	)
	mustNotContain(t, "the overview", body, "No transactions in this period")
}

// TestDashboard_OverviewChartsEachDay pins the chart to the buckets: one day
// per bucket of the period, each with its count in the title a reader hovers
// or hears, and a column drawn only on a day that sold.
func TestDashboard_OverviewChartsEachDay(t *testing.T) {
	f := startSeller(t)
	buyer := f.exchange.Buyer(t)
	sell(t, buyer, "/articles/one")
	sell(t, buyer, "/articles/two")
	sell(t, buyer, "/articles/three")

	body := f.get2(t, dashboard.PathOverview)
	totals, err := f.reads().RevenueTotals(t.Context(), exchangeclient.RevenueTotalsRequest{
		Actor: f.actor(), PublisherID: f.publisherID, Period: thisMonth(), Grouping: consoleapi.GroupingDay,
	})
	if err != nil {
		t.Fatalf("totals route: %v", err)
	}
	drawn := 0
	for _, b := range totals.Buckets {
		day := b.Start.UTC().Format("2 Jan 2006")
		mustContain(t, "the chart", body,
			"<title>"+day+" (UTC): "+strconv.FormatInt(b.TransactionCount, 10)+" transactions</title>")
		if b.TransactionCount > 0 {
			drawn++
		}
	}
	if drawn == 0 {
		t.Fatal("no bucket of this month holds the sales")
	}
	if got := strings.Count(body, `class="bar"`); got != drawn {
		t.Fatalf("columns drawn = %d, want one per day that sold (%d)", got, drawn)
	}
}

// TestDashboard_OverviewOfAnEmptyPeriodSaysSo opens a period the publisher
// sold nothing in. The Exchange answers NO_TRANSACTIONS_IN_PERIOD, and the
// page says so instead of showing zero figures.
func TestDashboard_OverviewOfAnEmptyPeriodSaysSo(t *testing.T) {
	f := startSeller(t)
	sell(t, f.exchange.Buyer(t), "/articles/one")

	body := f.get2(t, dashboard.PathOverview+"?from=2025-01-01&to=2025-01-31")
	mustContain(t, "an empty period", body, "No transactions in this period.", "1 Jan 2025 – 31 Jan 2025 (UTC)")
	mustNotContain(t, "an empty period", body, "stat-value", `class="chart"`)
}

// refusedPeriods are periods a page does not read, each with the sentence the
// console refuses it with. The Exchange never writes these sentences, so a
// page that shows one was refused by the console before any call.
var refusedPeriods = map[string]string{
	"?from=2026-09-01":               "Choose both a first and a last day.",
	"?from=yesterday&to=2026-09-30":  "The first day is not a date. Use the form 2026-09-01.",
	"?from=2026-09-01&to=someday":    "The last day is not a date. Use the form 2026-09-30.",
	"?from=2026-09-30&to=2026-09-01": "The last day comes before the first day.",
	"?from=2024-01-01&to=2026-01-01": "Choose a period of at most a year.",
}

// TestDashboard_PagesRefuseAPeriodTheyDoNotRead asks every page that reads a
// period for each period the dashboard does not read. Each is refused with the
// console's own sentence for it.
func TestDashboard_PagesRefuseAPeriodTheyDoNotRead(t *testing.T) {
	f := startSeller(t)
	for _, path := range []string{dashboard.PathOverview, dashboard.PathTransactions, dashboard.PathIncome, dashboard.PathIncomeCSV} {
		for query, sentence := range refusedPeriods {
			body := f.status(t, path+query, http.StatusBadRequest)
			mustContain(t, "a refused period "+path+query, body, "That did not work", sentence)
		}
	}
}

// TestDashboard_OverviewWithoutAmountsShowsDashes reads a deployment whose
// ledger keeps no history, so the Exchange answers the count but cannot say
// what was settled. The overview shows the count, a dash for each amount with
// no currency beside it, and the sentence that says why, rather than zeros or
// the empty state.
func TestDashboard_OverviewWithoutAmountsShowsDashes(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	addDomain(t, f)

	body := f.get2(t, dashboard.PathOverview)
	mustContain(t, "the overview", body,
		"could not be read from the ledger",
		`<span class="stat-label">Gross</span><span class="stat-value">—</span>`,
		`<span class="stat-label">Exchange fee</span><span class="stat-value">—</span>`,
		`<span class="stat-label">Net</span><span class="stat-value">—</span>`)
	mustNotContain(t, "the overview", body, "No transactions in this period")
}

// dashboardMonthLabel is how the overview names its default period, the month
// so far.
func dashboardMonthLabel() string {
	now := clock.System{}.Now().UTC()
	first := now.AddDate(0, 0, 1-now.Day())
	return first.Format("2 Jan 2006") + " – " + now.Format("2 Jan 2006") + " (UTC)"
}
