//go:build integration

package app_test

import (
	"html"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// The second site of the SAME publisher. The domain filter is only meaningful
// when one person holds more than one, which is what separates these tests
// from the access ones, where a second domain belongs to somebody else.
const secondSite = "foundry.example"

// twoSiteSeller is a publisher with stoa.example and foundry.example, each
// with one sale, so a filtered page has exactly one row to keep and one to
// drop.
func twoSiteSeller(t *testing.T, opts ...option) *consoleFixture {
	t.Helper()
	f := startSeller(t, opts...)
	requireVerified(t, addDomainWith(t, f, secondSite, map[string]any{
		"pricing_model": "per_unit", "rate": "0.02",
		"permitted_functions": []string{"search", "ai-input"},
	}))
	return f
}

// sellOn makes one sale of a page on the named site.
func sellOn(t *testing.T, f *consoleFixture, domain, path string) {
	t.Helper()
	f.exchange.Buyer(t).Buy(t, domain, path, exchangetest.PerUnitTerm("0.05", 20))
}

// TestDashboard_TheSiteFilterNarrowsTheTransactionList sells on both of the
// publisher's sites, then asks for one of them. The list holds that site's
// sale and not the other's, and the filter says which site is chosen.
func TestDashboard_TheSiteFilterNarrowsTheTransactionList(t *testing.T) {
	f := twoSiteSeller(t)
	sellOn(t, f, "stoa.example", "/articles/kept")
	sellOn(t, f, secondSite, "/articles/dropped")

	body := f.get2(t, dashboard.PathTransactions+"?domain=stoa.example")
	mustContain(t, "the filtered list", body, "/articles/kept")
	mustNotContain(t, "the filtered list", body, "/articles/dropped")

	// The control offers every site the person holds and marks the one asked
	// for, so the page says what it is showing rather than only showing it.
	mustContain(t, "the site filter", body,
		`<option value="stoa.example" selected>`,
		`<option value="`+secondSite+`">`,
		`<option value="">All domains</option>`)
}

// TestDashboard_TheSiteFilterNarrowsTheCatalog asks the catalog for one site.
// Only that site's resources are listed.
func TestDashboard_TheSiteFilterNarrowsTheCatalog(t *testing.T) {
	f := twoSiteSeller(t)
	sellOn(t, f, "stoa.example", "/articles/kept")
	sellOn(t, f, secondSite, "/articles/dropped")

	body := f.get2(t, dashboard.PathCatalog+"?domain="+secondSite)
	mustContain(t, "the filtered catalog", body, "/articles/dropped")
	mustNotContain(t, "the filtered catalog", body, "/articles/kept")
}

// TestDashboard_AnUnfilteredListHoldsEverySite is the other half of the two
// above: without the filter both sites' sales are on the page, so the tests
// prove the filter narrows rather than that the fixture only ever sold once.
func TestDashboard_AnUnfilteredListHoldsEverySite(t *testing.T) {
	f := twoSiteSeller(t)
	sellOn(t, f, "stoa.example", "/articles/one")
	sellOn(t, f, secondSite, "/articles/two")

	body := f.get2(t, dashboard.PathTransactions)
	mustContain(t, "the whole list", body, "/articles/one", "/articles/two")

	catalog := f.get2(t, dashboard.PathCatalog)
	mustContain(t, "the whole catalog", catalog, "/articles/one", "/articles/two")
}

// TestDashboard_ASiteFilteredListCountsThatSite checks the count beside the
// row numbers. "of N" comes from the period's totals, which the Exchange
// narrows to the chosen site as the list is narrowed, so the filtered list
// counts that site's transactions rather than every site's.
func TestDashboard_ASiteFilteredListCountsThatSite(t *testing.T) {
	f := twoSiteSeller(t)
	sellOn(t, f, "stoa.example", "/articles/one")
	sellOn(t, f, secondSite, "/articles/two")

	whole := f.get2(t, dashboard.PathTransactions)
	mustContain(t, "the whole list", whole, "of 2,")

	filtered := f.get2(t, dashboard.PathTransactions+"?domain=stoa.example")
	mustContain(t, "the filtered list", filtered, "Transactions 1–1 of 1, newest first")
	mustNotContain(t, "the filtered list", filtered, "of 2,")
}

// TestDashboard_TheSiteFilterSurvivesPaging walks the Next links of a filtered
// list. Every page stays on the chosen site: a filter the second page dropped
// would quietly widen the answer halfway through.
func TestDashboard_TheSiteFilterSurvivesPaging(t *testing.T) {
	f := twoSiteSeller(t, withPageSize(2))
	for i := range 3 {
		sellOn(t, f, "stoa.example", "/articles/kept-"+strconv.Itoa(i))
	}
	sellOn(t, f, secondSite, "/articles/dropped")

	path, pages, rows := dashboard.PathTransactions+"?domain=stoa.example", 0, 0
	for path != "" {
		body := f.get2(t, path)
		pages++
		// Count rows, not mentions: a row carries its address twice, once as the
		// text it shows and once as the title the truncated cell hovers.
		rows += strings.Count(body, `<tr class="row-link"`)
		mustNotContain(t, "page "+strconv.Itoa(pages), body, "/articles/dropped")
		mustContain(t, "page "+strconv.Itoa(pages), body,
			`<option value="stoa.example" selected>`)
		path = nextLink(body)
		if pages > maxWalk {
			t.Fatalf("still walking after %d pages: the Next link does not move on", pages)
		}
	}
	if pages != 2 {
		t.Fatalf("walked %d pages, want 2", pages)
	}
	if rows != 3 {
		t.Fatalf("the filtered pages held %d of the site's sales, want 3", rows)
	}
}

// TestDashboard_ASiteTheresNoSaleOnShowsNothing asks for a domain the
// publisher holds no listing under: one nobody registered, and one another
// publisher on the same Exchange sells on. The answer is the empty state, not
// the other publisher's rows and not every row.
func TestDashboard_ASiteTheresNoSaleOnShowsNothing(t *testing.T) {
	f := twoSiteSeller(t)
	sellOn(t, f, "stoa.example", "/articles/one")
	sellOn(t, f, secondSite, "/articles/two")
	r := newRival(t, f)

	for _, domain := range []string{"nowhere.example", "harmonia.example"} {
		q := "?domain=" + url.QueryEscape(domain)
		list := f.get2(t, dashboard.PathTransactions+q)
		mustContain(t, "the list on "+domain, list, "No transactions in this period.")
		mustNotContain(t, "the list on "+domain, list, "/articles/one", "/articles/two",
			r.sale.TransactionID, "/articles/rival")

		catalog := f.get2(t, dashboard.PathCatalog+q)
		mustContain(t, "the catalog on "+domain, catalog, "Nothing is listed for this domain.")
		mustNotContain(t, "the catalog on "+domain, catalog, "/articles/one", "/articles/two", "/articles/rival")

		income := f.get2(t, dashboard.PathIncome+q)
		mustContain(t, "the income report on "+domain, income, "No transactions in this period.")
		if rows := incomeRows(income); len(rows) != 0 {
			t.Errorf("the income report on %s shows rows %v, want none", domain, rows)
		}
	}
}

// TestDashboard_TheSiteFilterMatchesWhatTheRouteReturns holds the page to the
// Exchange rather than to a second opinion: the resources the catalog route
// returns for one site are exactly the ones the filtered page lists.
func TestDashboard_TheSiteFilterMatchesWhatTheRouteReturns(t *testing.T) {
	f := twoSiteSeller(t)
	sellOn(t, f, "stoa.example", "/articles/one")
	sellOn(t, f, "stoa.example", "/articles/two")
	sellOn(t, f, secondSite, "/articles/three")

	page, err := f.reads().ListCatalog(t.Context(), exchangeclient.ListCatalogRequest{
		Actor: f.actor(), PublisherID: f.publisherID, Domain: "stoa.example",
	})
	if err != nil {
		t.Fatalf("catalog route: %v", err)
	}
	if len(page.Resources) != 2 {
		t.Fatalf("the route returns %d resources for the site, want 2", len(page.Resources))
	}

	body := f.get2(t, dashboard.PathCatalog+"?domain=stoa.example")
	for _, r := range page.Resources {
		mustContain(t, "the filtered catalog", body, html.EscapeString(r.URI))
	}
	mustNotContain(t, "the filtered catalog", body, "/articles/three")
}
