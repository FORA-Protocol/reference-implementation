//go:build integration

package transport_test

import (
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// TestConsoleReads_DomainFilterReadsTheDomainInAnyCase sells two resources on
// the publisher's domain and one on another publisher's, then narrows the
// list, the totals and the catalog to the publisher's domain written three
// ways: as registered, upper-case, and with the trailing dot of a fully
// qualified name. A host is case-insensitive, so all three read the same two
// sales and resources, and none of them reads the other publisher's.
func TestConsoleReads_DomainFilterReadsTheDomainInAnyCase(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	own := map[string]bool{
		p.sell(t, s, pathFor(1), seedPricedTermEst(20)).GetTransactionId(): true,
		p.sell(t, s, pathFor(2), seedPricedTermEst(10)).GetTransactionId(): true,
	}
	other := p.newSeller(t, "harmonia")
	p.sell(t, other, pathFor(3), seedPricedTermEst(20))

	for _, domain := range []string{s.domain, strings.ToUpper(s.domain), s.domain + "."} {
		rows := rowsOf(t, p.transactionsRead(t, s, consoleapi.ListTransactionsRequest{
			Period: aroundNow(), Domain: domain,
		}), "transactions")
		if len(rows) != len(own) {
			t.Fatalf("list on %q holds %d rows, want %d: %v", domain, len(rows), len(own), rows)
		}
		for _, row := range rows {
			if id := field(t, row, "transaction_id"); !own[id] {
				t.Errorf("list on %q holds %s, which is not one of the publisher's sales on it", domain, id)
			}
		}

		body := p.totalsRead(t, s, consoleapi.RevenueTotalsRequest{
			Period: aroundNow(), Grouping: consoleapi.GroupingDay, Domain: domain,
		})
		wantTotal(t, body, "1.5", "0.15", "1.35", 2)

		resources := rowsOf(t, p.read(t, transport.ConsoleListCatalogPath, consoleapi.ListCatalogRequest{
			Actor: s.actor, PublisherID: s.publisherID, Domain: domain,
		}), "resources")
		if len(resources) != len(own) {
			t.Fatalf("catalog on %q holds %d resources, want %d: %v", domain, len(resources), len(own), resources)
		}
		for _, r := range resources {
			if uri := field(t, r, "uri"); !strings.Contains(uri, "://"+s.domain+"/") {
				t.Errorf("catalog on %q holds %s, which is not on the publisher's domain", domain, uri)
			}
		}
	}
}
