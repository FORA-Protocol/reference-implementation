//go:build integration

package app_test

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// rival is a second publisher on the same console and the same Exchange, with
// harmonia.example registered through the wizard and one sale made on it: a
// real domain, transaction and figures that are not the first person's. Its
// sale is 2 USD, twice the first person's usual one, so a page that showed the
// other publisher's figures would show numbers that differ.
type rival struct {
	*consoleFixture
	sale exchangetest.Sale
}

func newRival(t *testing.T, f *consoleFixture) rival {
	t.Helper()
	other := f.otherPerson(t, claimsFor("sub-2", "desk@harmonia.example", "Grace"))
	requireVerified(t, addDomainWith(t, other, "harmonia.example", map[string]any{
		"pricing_model": "per_unit", "rate": "0.02", "permitted_functions": []string{"search"},
	}))
	sale := other.exchange.Buyer(t).Buy(t, "harmonia.example", "/articles/rival",
		exchangetest.PerUnitTerm("0.05", 40))
	return rival{consoleFixture: other, sale: sale}
}

// TestDashboard_EachPersonSeesTheirOwnPublisher has two publishers sell on one
// Exchange, 1 USD and 2 USD. Each person's pages show their own publisher's
// sales and nothing of the other's, though no address names a publisher: the
// console asks the Exchange which publisher the signed-in person holds. The
// overview and the income report show each person's own figures, the ones the
// Exchange's totals route returns for their publisher.
func TestDashboard_EachPersonSeesTheirOwnPublisher(t *testing.T) {
	f := startSeller(t)
	own := sell(t, f.exchange.Buyer(t), "/articles/own")
	r := newRival(t, f)

	sections := []string{dashboard.PathOverview, dashboard.PathTransactions, dashboard.PathCatalog, dashboard.PathIncome}
	for _, path := range sections {
		mustNotContain(t, "my "+path, f.get2(t, path), r.sale.TransactionID, "harmonia.example")
		mustNotContain(t, "the rival's "+path, r.get2(t, path), own.TransactionID, "stoa.example")
	}
	mustContain(t, "my transactions", f.get2(t, dashboard.PathTransactions), own.TransactionID)
	mustContain(t, "the rival's transactions", r.get2(t, dashboard.PathTransactions), r.sale.TransactionID)

	mine, theirs := dailyTotals(t, f, ""), dailyTotals(t, r.consoleFixture, "")
	if mine.Total.GrossRevenue == theirs.Total.GrossRevenue {
		t.Fatalf("both publishers grossed %s; the test needs figures that differ", mine.Total.GrossRevenue)
	}
	assertOwnFigures(t, "mine", f, mine)
	assertOwnFigures(t, "the rival's", r.consoleFixture, theirs)
}

// assertOwnFigures holds a person's overview and income report to the totals
// the Exchange returns for their own publisher: the count and the three
// amounts.
func assertOwnFigures(t *testing.T, who string, f *consoleFixture, totals exchangeclient.RevenueTotals) {
	t.Helper()
	stat := func(label, value string) string {
		return `<span class="stat-label">` + label + `</span><span class="stat-value">` + value
	}
	mustContain(t, who+" overview", f.get2(t, dashboard.PathOverview),
		stat("Transactions", strconv.FormatInt(totals.Total.TransactionCount, 10)+"<"),
		stat("Gross", totals.Total.GrossRevenue+" <small>"),
		stat("Exchange fee", totals.Total.PlatformFee+" <small>"),
		stat("Net", totals.Total.NetRevenue+" <small>"))
	total := incomeRows(f.get2(t, dashboard.PathIncome))["total"]
	want := []string{
		"Total", strconv.FormatInt(totals.Total.TransactionCount, 10),
		totals.Total.GrossRevenue, totals.Total.PlatformFee, totals.Total.NetRevenue,
	}
	if !slices.Equal(total, want) {
		t.Fatalf("%s income total = %v, want %v", who, total, want)
	}
}

// TestDashboard_WithNoDomainThereAreNoSections opens the dashboard as a person
// registered nowhere, and as one whose business is registered but has no
// domain yet. Neither has anything in any section, so every section sends them
// to add their first domain, and no page shows the sidebar.
func TestDashboard_WithNoDomainThereAreNoSections(t *testing.T) {
	for name, arrange := range map[string]func(*testing.T, *consoleFixture){
		"no publisher":           func(*testing.T, *consoleFixture) {},
		"a publisher, no domain": func(t *testing.T, f *consoleFixture) { f.registerBusiness(t) },
	} {
		t.Run(name, func(t *testing.T) {
			f := startConsole(t, verifiedClaims())
			f.signIn(t)
			arrange(t, f)
			for _, path := range []string{"/", "/domains", dashboard.PathOverview, dashboard.PathTransactions, dashboard.PathCatalog} {
				resp := f.do(t, http.MethodGet, path, nil)
				body := readAll(t, resp)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("GET %s = %d", path, resp.StatusCode)
				}
				mustContain(t, "GET "+path, body, "Add your first domain")
				mustNotContain(t, "GET "+path, body, `aria-label="Sections"`, "Your terms with the exchange")
			}
		})
	}
}

// TestDashboard_AnotherPublishersTransactionRendersNotFound opens the rival's
// transaction. The detail read is scoped to the person's own publisher, so the
// transaction is not found and nothing of it shows. The page leads back to the
// domains list, the one page every signed-in person has.
func TestDashboard_AnotherPublishersTransactionRendersNotFound(t *testing.T) {
	f := startSeller(t)
	r := newRival(t, f)

	body := f.status(t, dashboard.PathTransactions+"/"+r.sale.TransactionID, http.StatusNotFound)
	mustContain(t, "the rival's transaction", body, "Not found",
		`<a class="btn btn-primary" href="/domains">Back to your domains</a>`)
	mustNotContain(t, "the rival's transaction", body, "harmonia.example", "data-evidence")
}

// TestDashboard_SidebarMarksTheCurrentSection checks the sidebar on each
// section: it links every section, and only the current one is marked
// current.
func TestDashboard_SidebarMarksTheCurrentSection(t *testing.T) {
	f := startSeller(t)
	sections := map[string]string{
		"/domains":                 `aria-current="page" href="/domains"`,
		"/domains/stoa.example":    `aria-current="page" href="/domains"`,
		dashboard.PathOverview:     `aria-current="page" href="` + dashboard.PathOverview + `"`,
		dashboard.PathTransactions: `aria-current="page" href="` + dashboard.PathTransactions + `"`,
		dashboard.PathCatalog:      `aria-current="page" href="` + dashboard.PathCatalog + `"`,
	}
	for path, current := range sections {
		body := f.get2(t, path)
		mustContain(t, "the sidebar on "+path, body, current,
			`href="`+dashboard.PathOverview+`"`, `href="`+dashboard.PathTransactions+`"`, `href="`+dashboard.PathCatalog+`"`)
		// Two copies of the list, the sidebar's and the phone menu's, each
		// marking one section.
		if got := countCurrent(body); got != 2 {
			t.Errorf("%s marks %d sections current, want the one section in both lists", path, got)
		}
	}
}

// TestDashboard_DomainsListShowsTheExchangesTerms reads the fee and reporting
// policy the Exchange applies, beside the domains list.
func TestDashboard_DomainsListShowsTheExchangesTerms(t *testing.T) {
	f := startSeller(t)
	body := f.get2(t, "/domains")
	mustContain(t, "the domains list", body, "My domains", "Your terms with the exchange",
		"10% of each sale", "the exchange&#39;s standard rate", "due within 1 day", "up to 20%")
}

// TestDashboard_AFractionalFeeIsShownAsItIs sets the seat's fee to 1250 basis
// points. The domains list says 12.5%, not a rounded figure.
func TestDashboard_AFractionalFeeIsShownAsItIs(t *testing.T) {
	f := startSellerOver(t, func(o *exchangetest.Options) { o.FeeRateBps = 1250 })
	mustContain(t, "the domains list", f.get2(t, "/domains"), "12.5% of each sale")
}

// countCurrent is how many section links a page marks as the current one.
func countCurrent(body string) int {
	return strings.Count(body, `aria-current="page" href=`)
}

// TestDashboard_NothingVerifiedOpensOnMyDomains opens the console's root as a
// publisher whose one domain is saved and not verified. Nothing of it can have
// sold, so they land on their domains rather than on an overview of sales: the
// list says the domain is not verified, and the dashboard's sections are drawn,
// because a domain is saved.
func TestDashboard_NothingVerifiedOpensOnMyDomains(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	if out := addDomain(t, f); out["verified"] != false {
		t.Fatalf("a console that cannot see the package verified a domain: %v", out)
	}
	resp := f.do(t, http.MethodGet, "/", nil)
	body := readAll(t, resp)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/" {
		t.Fatalf("GET / ended at %s with %d, want the domains list", resp.Request.URL.Path, resp.StatusCode)
	}
	mustContain(t, "the landing page", body, "My domains", "stoa.example", "Not verified", `aria-label="Sections"`)
}

// TestDashboard_TheConsoleOpensOnTheOverview opens the console's root as a
// publisher with a verified domain. They land on their own overview, and the
// domains list is one section of the sidebar.
func TestDashboard_TheConsoleOpensOnTheOverview(t *testing.T) {
	f := startSeller(t)
	resp := f.do(t, http.MethodGet, "/", nil)
	body := readAll(t, resp)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != dashboard.PathOverview {
		t.Fatalf("GET / ended at %s with %d, want the overview", resp.Request.URL.Path, resp.StatusCode)
	}
	mustContain(t, "the landing page", body, `href="/domains" title="My domains"`)
}
