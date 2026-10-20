//go:build integration

package app_test

import (
	"encoding/csv"
	"html"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// The income report. Every figure on the page and in the file is compared with
// what the Exchange's totals route returns for the same publisher, period and
// resource, read with the console's own signed client: the report adds nothing
// up of its own.

// dailyTotals reads the publisher's totals this month, a bucket per day,
// narrowed to resourceID when it is not empty.
func dailyTotals(t *testing.T, f *consoleFixture, resourceID string) exchangeclient.RevenueTotals {
	t.Helper()
	return dailyTotalsOf(t, f, exchangeclient.RevenueTotalsRequest{ResourceID: resourceID})
}

// dailyTotalsOf is dailyTotals narrowed as req asks, with the fixture's
// person, publisher and this month filled in.
func dailyTotalsOf(t *testing.T, f *consoleFixture, req exchangeclient.RevenueTotalsRequest) exchangeclient.RevenueTotals {
	t.Helper()
	req.Actor, req.PublisherID, req.Period, req.Grouping = f.actor(), f.publisherID, thisMonth(), consoleapi.GroupingDay
	totals, err := f.reads().RevenueTotals(t.Context(), req)
	if err != nil {
		t.Fatalf("totals route: %v", err)
	}
	return totals
}

// wantRow is a report row as the route's figures give it.
func wantRow(date string, f consoleapi.RevenueFigures) []string {
	return []string{date, strconv.FormatInt(f.TransactionCount, 10), f.GrossRevenue, f.PlatformFee, f.NetRevenue}
}

var (
	reIncomeRow  = regexp.MustCompile(`(?s)<tr data-day="([^"]+)"[^>]*>(.*?)</tr>`)
	reIncomeCell = regexp.MustCompile(`<t[dh][^>]*>([^<]*)</t[dh]>`)
)

// incomeRows reads the report's table: its cells by row, keyed by day, and the
// total row under "total".
func incomeRows(body string) map[string][]string {
	rows := map[string][]string{}
	for _, m := range reIncomeRow.FindAllStringSubmatch(body, -1) {
		var cells []string
		for _, c := range reIncomeCell.FindAllStringSubmatch(m[2], -1) {
			cells = append(cells, html.UnescapeString(strings.TrimSpace(c[1])))
		}
		rows[m[1]] = cells
	}
	return rows
}

// shownDays are the route's days the report shows: every day, or by default
// only the days whose net is above zero.
func shownDays(t *testing.T, totals exchangeclient.RevenueTotals, allDays bool) []consoleapi.RevenueBucket {
	t.Helper()
	var out []consoleapi.RevenueBucket
	for _, b := range totals.Buckets {
		net, ok := new(big.Rat).SetString(b.NetRevenue)
		if !ok {
			t.Fatalf("the route's net %q is not a number", b.NetRevenue)
		}
		if allDays || net.Sign() > 0 {
			out = append(out, b)
		}
	}
	return out
}

// assertReportIs checks the page's table row by row against the route: a row
// for each day it shows and the total.
func assertReportIs(t *testing.T, body string, totals exchangeclient.RevenueTotals, allDays bool) {
	t.Helper()
	rows := incomeRows(body)
	days := shownDays(t, totals, allDays)
	if len(rows) != len(days)+1 {
		t.Fatalf("the table has %d rows, want a row per day shown (%d) and the total", len(rows), len(days))
	}
	for _, b := range days {
		day := b.Start.UTC().Format("2006-01-02")
		if want := wantRow(day, b.RevenueFigures); !slices.Equal(rows[day], want) {
			t.Errorf("row %s = %v, want %v", day, rows[day], want)
		}
	}
	if want := wantRow("Total", totals.Total); !slices.Equal(rows["total"], want) {
		t.Errorf("total row = %v, want %v", rows["total"], want)
	}
}

// TestDashboard_IncomeShowsEachDayAndTheTotal sells twice, today. By default
// the report shows only the days that earned, so today's row and the route's
// total; with every day asked for, it has a row for every day of the month.
// The sidebar marks it as the current section.
func TestDashboard_IncomeShowsEachDayAndTheTotal(t *testing.T) {
	f := startSeller(t)
	buyer := f.exchange.Buyer(t)
	sell(t, buyer, "/articles/one")
	sell(t, buyer, "/articles/two")
	totals := dailyTotals(t, f, "")
	if totals.Total.TransactionCount != 2 || totals.Total.GrossRevenue != "2" {
		t.Fatalf("the route's total is %+v, want the two sales", totals.Total)
	}

	body := f.get2(t, "/income")
	assertReportIs(t, body, totals, false)
	if days := len(incomeRows(body)) - 1; days != 1 {
		t.Fatalf("the default report shows %d days, want only today's", days)
	}
	mustContain(t, "the report", body, `aria-current="page" href="/income"`, "Download CSV",
		`value="profitable" checked`, "Only the days that earned something are shown")

	// What an unticked box sends: the hidden field alone.
	every := f.get2(t, "/income?days=all")
	assertReportIs(t, every, totals, true)
	mustNotContain(t, "the report of every day", every, `value="profitable" checked`)
	// What a ticked box sends: both.
	assertReportIs(t, f.get2(t, "/income?days=all&days=profitable"), totals, false)
}

// TestDashboard_IncomeHidesDaysThatEarnedNothing sells only a free access. The
// day has a transaction and no income, so by default no day is shown, and the
// total still counts the sale; asked for every day, the report shows them.
func TestDashboard_IncomeHidesDaysThatEarnedNothing(t *testing.T) {
	f := startSeller(t)
	f.exchange.Buyer(t).Buy(t, "stoa.example", "/articles/free", exchangetest.FreeTerm())
	totals := dailyTotals(t, f, "")

	body := f.get2(t, "/income")
	assertReportIs(t, body, totals, false)
	mustContain(t, "the default report", body, "No day in this period earned anything.")
	if rows := incomeRows(body); rows["total"][1] != "1" {
		t.Fatalf("the total row is %v, want the free sale counted", rows["total"])
	}
	assertReportIs(t, f.get2(t, "/income?days=all"), totals, true)
}

// TestDashboard_IncomeNarrowsToOneSite sells on both of a publisher's sites and
// asks for one. The report holds that site's sale alone, as the route narrowed
// to the site does, and the site filter says which site is chosen.
func TestDashboard_IncomeNarrowsToOneSite(t *testing.T) {
	f := twoSiteSeller(t)
	sellOn(t, f, "stoa.example", "/articles/kept")
	sellOn(t, f, secondSite, "/articles/dropped")

	body := f.get2(t, "/income?domain=stoa.example")
	totals := dailyTotalsOf(t, f, exchangeclient.RevenueTotalsRequest{Domain: "stoa.example"})
	assertReportIs(t, body, totals, false)
	if totals.Total.TransactionCount != 1 || dailyTotals(t, f, "").Total.TransactionCount != 2 {
		t.Fatalf("narrowed total = %+v, want 1 of the 2 sales", totals.Total)
	}
	mustContain(t, "the site filter", body, `<option value="stoa.example" selected>`)
}

// reIncomeLink finds a link to a filtered income report.
var reIncomeLink = regexp.MustCompile(`href="(/income\?[^"]+)"`)

// TestDashboard_IncomeNarrowsToOneResource sells one page twice and another
// once, then follows the income link on the first page's catalog entry. The
// report holds that page's two sales alone, as the route narrowed to it does.
func TestDashboard_IncomeNarrowsToOneResource(t *testing.T) {
	f := startSeller(t)
	buyer := f.exchange.Buyer(t)
	one := sell(t, buyer, "/articles/one")
	sell(t, buyer, "/articles/one")
	sell(t, buyer, "/articles/other")
	resourceID := ""
	for _, row := range allTransactions(t, f, "") {
		if row.TransactionID == one.TransactionID {
			resourceID = row.ResourceID
		}
	}

	page := f.get2(t, "/catalog/"+url.PathEscape(resourceID))
	m := reIncomeLink.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("the resource page links to no income report: %s", head(page))
	}
	link := html.UnescapeString(m[1])
	if got, _ := url.Parse(link); got.Query().Get("resource") != resourceID {
		t.Fatalf("the income link %q is not narrowed to %s", link, resourceID)
	}

	body := f.get2(t, link)
	totals := dailyTotals(t, f, resourceID)
	assertReportIs(t, body, totals, false)
	if totals.Total.TransactionCount != 2 || dailyTotals(t, f, "").Total.TransactionCount != 3 {
		t.Fatalf("narrowed total = %+v, want 2 of the 3 sales", totals.Total)
	}
	mustContain(t, "the narrowed report", body, "Showing one resource only")
}

// TestDashboard_IncomeDownloadsAsCSV follows the report's own download link.
// The file is an attachment and holds the table as the page shows it: a line
// per day shown, by default the days that earned, and the total last, each in
// the route's currency.
func TestDashboard_IncomeDownloadsAsCSV(t *testing.T) {
	f := startSeller(t)
	sell(t, f.exchange.Buyer(t), "/articles/one")
	m := regexp.MustCompile(`href="(/income\.csv[^"]*)"`).FindStringSubmatch(f.get2(t, "/income"))
	if m == nil {
		t.Fatal("the report has no download link")
	}

	resp := f.do(t, http.MethodGet, html.UnescapeString(m[1]), nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("download = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	kind, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if err != nil || kind != "attachment" || !strings.HasPrefix(params["filename"], "income-") {
		t.Fatalf("Content-Disposition = %q", resp.Header.Get("Content-Disposition"))
	}
	lines, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("the download is not CSV: %v", err)
	}

	totals := dailyTotals(t, f, "")
	want := make([][]string, 0, len(totals.Buckets)+2)
	want = append(want, []string{"date", "transactions", "gross", "exchange_fee", "net", "currency"})
	for _, b := range shownDays(t, totals, false) {
		want = append(want, append(wantRow(b.Start.UTC().Format("2006-01-02"), b.RevenueFigures), totals.Currency))
	}
	want = append(want, append(wantRow("Total", totals.Total), totals.Currency))
	if !slices.EqualFunc(lines, want, slices.Equal) {
		t.Fatalf("the file holds\n%v\nwant\n%v", lines, want)
	}
}

// TestDashboard_IncomeDownloadKeepsTheFilters sells once on one of the
// publisher's domains and twice on the other, opens the report narrowed to the
// second, and follows its download link. The file holds that domain's sales
// alone, as the totals route narrowed to it counts them: the filter reaches
// the file through the link, not only the page.
func TestDashboard_IncomeDownloadKeepsTheFilters(t *testing.T) {
	f := twoSiteSeller(t)
	sellOn(t, f, "stoa.example", "/articles/one")
	sellOn(t, f, secondSite, "/articles/two")
	sellOn(t, f, secondSite, "/articles/three")

	m := regexp.MustCompile(`href="(/income\.csv[^"]*)"`).FindStringSubmatch(
		f.get2(t, "/income?domain="+url.QueryEscape(secondSite)))
	if m == nil {
		t.Fatal("the narrowed report has no download link")
	}
	resp := f.do(t, http.MethodGet, html.UnescapeString(m[1]), nil)
	defer func() { _ = resp.Body.Close() }()
	lines, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil || len(lines) < 2 {
		t.Fatalf("the download is not a report: %v %v", lines, err)
	}
	narrowed := dailyTotalsOf(t, f, exchangeclient.RevenueTotalsRequest{Domain: secondSite})
	if narrowed.Total.TransactionCount != 2 {
		t.Fatalf("the route narrowed to %s counts %d sales, want 2", secondSite, narrowed.Total.TransactionCount)
	}
	if got, want := lines[len(lines)-1], append(wantRow("Total", narrowed.Total), narrowed.Currency); !slices.Equal(got, want) {
		t.Fatalf("the file's total is %v, want the narrowed route's %v", got, want)
	}
}

// TestDashboard_IncomeWithoutAmountsCannotBeDownloaded reads a deployment whose
// ledger keeps no history, so the Exchange cannot say what was settled. The
// report says so and offers no file, and asking for the file directly is
// refused rather than answered with zeros a spreadsheet would add up.
func TestDashboard_IncomeWithoutAmountsCannotBeDownloaded(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	addDomain(t, f)

	body := f.get2(t, "/income")
	mustContain(t, "the report", body, "could not be read from the ledger", "—")
	mustNotContain(t, "the report", body, "Download CSV", "Only the days that earned something are shown")
	// With no amounts there is no telling which days earned, so every day is
	// shown rather than none.
	if rows, days := len(incomeRows(body)), len(dailyTotals(t, f, "").Buckets); rows != days+1 {
		t.Fatalf("the report shows %d rows, want every day (%d) and the total", rows, days)
	}
	refused := f.status(t, "/income.csv", http.StatusServiceUnavailable)
	mustContain(t, "the refused download", refused, "could not be read from the ledger")
}
