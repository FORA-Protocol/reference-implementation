//go:build integration

package app_test

import (
	"html"
	"regexp"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/app"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// The dashboard's tests read a publisher with real transactions. Every sale is
// made through the Exchange's public trading surface by an exchangetest Buyer,
// for a domain the publisher registered through the console's own wizard, and
// every page is compared with what the Exchange's read route returns for the
// same publisher, read with the console's own signed client.

// testFeeBps is the seat's platform fee in the dashboard's tests: 10%.
const testFeeBps = 1000

// sellingOver is the deployment of a test about sales: the Exchange sells
// through the package's ledger at testFeeBps, and deployment adjusts it
// further.
func sellingOver(deployment func(*exchangetest.Options)) func(*exchangetest.Options) {
	return func(o *exchangetest.Options) {
		o.Ledger = sharedLedger
		o.FeeRateBps = testFeeBps
		deployment(o)
	}
}

// withPageSize makes the dashboard's lists page by n rows, so a test walks
// several pages without making a page's worth of sales for each.
func withPageSize(n int) option {
	return func(c *app.Config, _ *exchangetest.Exchange) { c.DashboardPageSize = n }
}

// startSeller is a signed-in publisher with stoa.example registered through
// the wizard, in front of an Exchange that sells.
func startSeller(t *testing.T, opts ...option) *consoleFixture {
	t.Helper()
	return startSellerOver(t, func(*exchangetest.Options) {}, opts...)
}

// startSellerOver is startSeller over a deployment the test adjusts, such as
// one whose clock it moves. The publisher's site has the package in front of
// it, so the domain the wizard saves is verified and sells.
func startSellerOver(t *testing.T, deployment func(*exchangetest.Options), opts ...option) *consoleFixture {
	t.Helper()
	opts = append([]option{withPages(deployedPackage())}, opts...)
	f := startConsoleOver(t, verifiedClaims(), sellingOver(deployment), opts...)
	f.signIn(t)
	requireVerified(t, addDomain(t, f))
	return f
}

// testClock is a deployment whose clock the test moves, and the clock.
func testClock() (func(*exchangetest.Options), *clock.DeterministicClock) {
	clk := clock.NewDeterministic(clock.System{}.Now())
	return func(o *exchangetest.Options) { o.Clock = clk }, clk
}

// sell makes one sale of the page at path on stoa.example, at 0.05 USD per
// access for an estimated 20 accesses: 1 USD, of which the seat's 10% is
// 0.1.
func sell(t *testing.T, buyer *exchangetest.Buyer, path string) exchangetest.Sale {
	t.Helper()
	return buyer.Buy(t, "stoa.example", path, exchangetest.PerUnitTerm("0.05", 20))
}

// walkRoute reads every page of a list route, following each page's next
// cursor from the first, and returns the rows in order. page reads one page for
// a cursor. A route still answering pages after maxWalk fails the test rather
// than hanging it, since a cursor that does not move on would never end.
func walkRoute[T any](t *testing.T, what string, page func(cursor string) ([]T, string)) []T {
	t.Helper()
	var rows []T
	cursor := ""
	for range maxWalk {
		got, next := page(cursor)
		rows = append(rows, got...)
		if next == "" {
			return rows
		}
		cursor = next
	}
	t.Fatalf("the %s route still has pages after %d", what, maxWalk)
	return nil
}

// actor is the person this fixture signs in as, the way the console asserts
// them to the Exchange.
func (f *consoleFixture) actor() exchangeclient.Actor {
	return exchangeclient.Actor{
		OIDCIssuer: f.claims.Issuer, OIDCSubject: f.claims.Subject,
		Email: f.claims.Email, EmailVerified: f.claims.EmailVerified,
	}
}

// reads is the Exchange's publisher reads, through the console's own client.
func (f *consoleFixture) reads() *exchangeclient.Reads { return f.xclient.Reads() }

// thisMonth is the period a dashboard page reads when its address names none.
func thisMonth() exchangeclient.Period {
	now := clock.System{}.Now()
	return dashboard.MonthOf(now, now).Window()
}

// reTransactionLink finds the link each transaction row opens its detail by.
var reTransactionLink = regexp.MustCompile(`href="/transactions/([^"?]+)"`)

// transactionIDs lists the transactions a page links to, in page order.
func transactionIDs(body string) []string {
	matches := reTransactionLink.FindAllStringSubmatch(body, -1)
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, html.UnescapeString(m[1]))
	}
	return ids
}

// maxWalk bounds a walk by Next links, so a link that leads back to the same
// page fails the test rather than hanging it.
const maxWalk = 10

// reNext finds a list page's Next link.
var reNext = regexp.MustCompile(`href="([^"]+)" rel="next"`)

// nextLink is the address a page's Next link goes to, or "" on the last page.
func nextLink(body string) string {
	return linkOf(reNext, body)
}

// rePrev finds a list page's Previous link.
var rePrev = regexp.MustCompile(`href="([^"]+)" rel="prev"`)

// prevLink is the address a page's Previous link goes to, or "" on the first
// page.
func prevLink(body string) string {
	return linkOf(rePrev, body)
}

func linkOf(re *regexp.Regexp, body string) string {
	m := re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

// mustContain fails the test unless body says every one of want.
func mustContain(t *testing.T, what, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s does not say %q: %s", what, w, head(body))
		}
	}
}

// mustNotContain fails the test if body says any of unwanted.
func mustNotContain(t *testing.T, what, body string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(body, u) {
			t.Errorf("%s says %q, which it must not", what, u)
		}
	}
}

// status fetches a page and fails the test unless it answered want.
func (f *consoleFixture) status(t *testing.T, path string, want int) string {
	t.Helper()
	got, body := f.get(t, path)
	if got != want {
		t.Fatalf("GET %s = %d, want %d: %s", path, got, want, head(body))
	}
	return body
}
