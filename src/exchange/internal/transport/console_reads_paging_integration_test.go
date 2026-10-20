//go:build integration

package transport_test

import (
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// Each list is read two ways: forward, from the first page by next_cursor, and
// back by prev_cursor. The two are separate queries whose filters must agree,
// so these tests walk every filtered list both ways and hold that the pages
// come back the same.

// walkBothWays pages through a list two rows at a time, forward to its end and
// back to its start, and returns the ids on each page of each walk, the back
// walk in the order it was read. read answers one page for a cursor.
func walkBothWays(
	t *testing.T, read func(cursor string) map[string]any, rowsKey, idKey string,
) (forward, back [][]string) {
	t.Helper()
	ids := func(body map[string]any) []string {
		rows := rowsOf(t, body, rowsKey)
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, field(t, row, idKey))
		}
		return out
	}
	body := read("")
	if prev, ok := body["prev_cursor"]; ok {
		t.Fatalf("the first page has prev_cursor %v", prev)
	}
	for walked := 0; ; walked++ {
		if walked == 10 {
			t.Fatal("the forward walk did not end within 10 pages")
		}
		forward = append(forward, ids(body))
		next, _ := body["next_cursor"].(string)
		if next == "" {
			break
		}
		body = read(next)
	}
	for walked := 0; ; walked++ {
		if walked == 10 {
			t.Fatal("the walk back did not end within 10 pages")
		}
		prev, _ := body["prev_cursor"].(string)
		if prev == "" {
			break
		}
		body = read(prev)
		back = append(back, ids(body))
	}
	return forward, back
}

// assertRetraced holds the walk back to the pages of the walk forward, in
// reverse, down to the first page.
func assertRetraced(t *testing.T, what string, forward, back [][]string, wantRows int) {
	t.Helper()
	if got := len(slices.Concat(forward...)); got != wantRows {
		t.Fatalf("%s: the forward walk holds %d rows, want %d: %v", what, got, wantRows, forward)
	}
	if len(back) != len(forward)-1 {
		t.Fatalf("%s: the walk back read %d pages, want %d: forward %v, back %v",
			what, len(back), len(forward)-1, forward, back)
	}
	for i, page := range back {
		if want := forward[len(forward)-2-i]; !slices.Equal(page, want) {
			t.Errorf("%s: page %d back = %v, want %v", what, i+1, page, want)
		}
	}
}

// TestConsoleReads_PreviousRetracesTheTransactionList sells five resources on
// the publisher's domain, reports one sale, and sells one on another
// publisher's domain. The list, unfiltered and narrowed by domain, by resource
// and by report status, walks back through exactly the pages it walked
// forward.
func TestConsoleReads_PreviousRetracesTheTransactionList(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	var resource string
	for i := range 5 {
		item := p.sell(t, s, pathFor(i), seedPricedTermEst(20))
		if i == 0 {
			if err := p.report(t, item, 20); err != nil {
				t.Fatalf("report: %v", err)
			}
		}
	}
	for _, row := range rowsOf(t, p.transactionsPage(t, s, "", 0, ""), "transactions") {
		resource = field(t, row, "resource_id")
	}
	p.sell(t, p.newSeller(t, "harmonia"), pathFor(9), seedPricedTermEst(20))

	for _, c := range []struct {
		name string
		req  consoleapi.ListTransactionsRequest
		rows int
	}{
		{"unfiltered", consoleapi.ListTransactionsRequest{}, 5},
		{"by domain", consoleapi.ListTransactionsRequest{Domain: s.domain}, 5},
		{"by resource", consoleapi.ListTransactionsRequest{ResourceID: resource}, 1},
		{"pending", consoleapi.ListTransactionsRequest{ReportingStatus: consoleapi.ReportingPending}, 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			forward, back := walkBothWays(t, func(cursor string) map[string]any {
				req := c.req
				req.Period, req.PageSize, req.Cursor = aroundNow(), 2, cursor
				return p.transactionsRead(t, s, req)
			}, "transactions", "transaction_id")
			assertRetraced(t, c.name, forward, back, c.rows)
		})
	}
}

// TestConsoleReads_PreviousRetracesTheCatalog pushes five resources for the
// publisher and one for another publisher, and walks the catalog, unfiltered
// and narrowed by domain, forward and back.
func TestConsoleReads_PreviousRetracesTheCatalog(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	p.pushAtRates(t, s, "0.01", "0.02", "0.03", "0.04", "0.05")
	other := p.newSeller(t, "harmonia")
	pushDiscoverTermOfferForTenant(t, p.h, p.h.tenantID, other.domain, pathFor(0), seedPricedTermEst(20))

	for name, domain := range map[string]string{"unfiltered": "", "by domain": s.domain} {
		t.Run(name, func(t *testing.T) {
			forward, back := walkBothWays(t, func(cursor string) map[string]any {
				return p.read(t, transport.ConsoleListCatalogPath, consoleapi.ListCatalogRequest{
					Actor: s.actor, PublisherID: s.publisherID, Domain: domain, PageSize: 2, Cursor: cursor,
				})
			}, "resources", "resource_id")
			assertRetraced(t, name, forward, back, 5)
		})
	}
}
