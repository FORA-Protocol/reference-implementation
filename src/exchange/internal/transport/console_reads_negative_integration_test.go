//go:build integration

package transport_test

import (
	"net/http"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// What the publisher reads refuse. Every route is driven through the same
// signed surface the successful reads use, and every refusal is checked for
// its status, its kind, and the absence of any data in the answer.

// readRoute is one publisher read: its route, and the body it takes for an
// actor asking about a publisher. The scene names one sale: tx is its
// transaction, which the transaction detail route reads, and resource is its
// resource, which the catalog detail route reads. The other routes ignore both.
type readRoute struct {
	path string
	body func(actor consoleActorBody, publisherID string, sale saleIDs) any
}

// saleIDs names the one sale a refusal scene makes.
type saleIDs struct{ tx, resource string }

func readRoutes() []readRoute {
	return []readRoute{
		{transport.ConsoleRevenueTotalsPath, func(a consoleActorBody, id string, _ saleIDs) any {
			return consoleapi.RevenueTotalsRequest{
				Actor: a, PublisherID: id, Period: aroundNow(), Grouping: consoleapi.GroupingDay,
			}
		}},
		{transport.ConsoleListTransactionsPath, func(a consoleActorBody, id string, _ saleIDs) any {
			return consoleapi.ListTransactionsRequest{Actor: a, PublisherID: id, Period: aroundNow()}
		}},
		{transport.ConsoleTransactionDetailPath, func(a consoleActorBody, id string, sale saleIDs) any {
			return consoleapi.TransactionDetailRequest{Actor: a, PublisherID: id, TransactionID: sale.tx}
		}},
		{transport.ConsoleListCatalogPath, func(a consoleActorBody, id string, _ saleIDs) any {
			return consoleapi.ListCatalogRequest{Actor: a, PublisherID: id}
		}},
		{transport.ConsoleCatalogDetailPath, func(a consoleActorBody, id string, sale saleIDs) any {
			return consoleapi.CatalogDetailRequest{Actor: a, PublisherID: id, ResourceID: sale.resource}
		}},
		{transport.ConsolePublisherTermsPath, func(a consoleActorBody, id string, _ saleIDs) any {
			return consoleapi.PublisherTermsRequest{Actor: a, PublisherID: id}
		}},
	}
}

// assertRefused checks a refusal: its status, its kind, and that the body
// holds nothing but the refusal itself.
func assertRefused(t *testing.T, what string, status int, body map[string]any, wantStatus int, wantKind string) {
	t.Helper()
	if status != wantStatus || body["kind"] != wantKind {
		t.Fatalf("%s = %d %v, want %d %s", what, status, body, wantStatus, wantKind)
	}
	for key := range body {
		if key != "error" && key != "kind" {
			t.Errorf("%s answered with data %q beside the refusal: %v", what, key, body)
		}
	}
}

// soldTo sets up the shared refusal scene: a publisher with one sale, whose
// transaction and resource the detail routes are asked about.
func soldTo(t *testing.T, p *publisherConsole) (seller, saleIDs) {
	t.Helper()
	s := p.newSeller(t, "stoa")
	tx := p.sell(t, s, pathFor(1), seedPricedTermEst(20)).GetTransactionId()
	return s, saleIDs{tx: tx, resource: field(t, rowsOf(t, p.catalogPage(t, s, 0, ""), "resources")[0], "resource_id")}
}

// TestConsoleReads_RefuseAnUnauthenticatedOrIncompleteCaller sends every read
// unsigned, signed from another directory, and signed but for an actor with
// no subject.
func TestConsoleReads_RefuseAnUnauthenticatedOrIncompleteCaller(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s, sale := soldTo(t, p)
	unsigned, imposter := *p.consoleFixture, *p.consoleFixture
	// The server's own client, with no signing transport in front of it.
	unsigned.client = p.server.Client()
	imposter.client = p.imposterClient(t)
	incomplete := s.actor
	incomplete.OIDCSubject = ""

	for _, r := range readRoutes() {
		path := consolePath(r.path)
		status, body := unsigned.post(t, path, r.body(s.actor, s.publisherID, sale))
		assertRefused(t, "unsigned "+path, status, body, http.StatusUnauthorized, consoleapi.ErrorKindUnauthenticated)
		status, body = imposter.post(t, path, r.body(s.actor, s.publisherID, sale))
		assertRefused(t, "another directory's "+path, status, body,
			http.StatusUnauthorized, consoleapi.ErrorKindUnauthenticated)
		status, body = p.post(t, path, r.body(incomplete, s.publisherID, sale))
		assertRefused(t, "incomplete actor's "+path, status, body,
			http.StatusBadRequest, consoleapi.ErrorKindInvalidRequest)
	}
}

// TestConsoleReads_AnswerNotFoundWithoutAGrant asks every read about a
// publisher as two people who hold no grant for it: someone who registered
// nothing, and the owner of another publisher on the same seat. Both are told
// the publisher does not exist.
func TestConsoleReads_AnswerNotFoundWithoutAGrant(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s, sale := soldTo(t, p)
	stranger := newActor("stranger@elsewhere.example")
	rival := p.newSeller(t, "harmonia")

	for _, r := range readRoutes() {
		path := consolePath(r.path)
		status, body := p.post(t, path, r.body(stranger, s.publisherID, sale))
		assertRefused(t, "a stranger's "+path, status, body, http.StatusNotFound, consoleapi.ErrorKindNotFound)
		status, body = p.post(t, path, r.body(rival.actor, s.publisherID, sale))
		assertRefused(t, "another publisher's "+path, status, body, http.StatusNotFound, consoleapi.ErrorKindNotFound)
	}
}

// TestConsoleReads_DetailHidesAnotherPublishersTransaction asks for one
// publisher's transaction under another publisher the caller does hold a
// grant for. The grant passes, and the transaction is still not found.
func TestConsoleReads_DetailHidesAnotherPublishersTransaction(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	_, sale := soldTo(t, p)
	rival := p.newSeller(t, "harmonia")

	status, body := p.post(t, consolePath(transport.ConsoleTransactionDetailPath),
		consoleapi.TransactionDetailRequest{Actor: rival.actor, PublisherID: rival.publisherID, TransactionID: sale.tx})
	assertRefused(t, "another publisher's transaction", status, body, http.StatusNotFound, consoleapi.ErrorKindNotFound)
}

// TestConsoleReads_RefuseInvalidParameters sends each parameter a read
// validates in a form it refuses.
func TestConsoleReads_RefuseInvalidParameters(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s, _ := soldTo(t, p)
	now := time.Now().UTC()
	totals := func(period consoleapi.Period, grouping string) any {
		return consoleapi.RevenueTotalsRequest{
			Actor: s.actor, PublisherID: s.publisherID, Period: period, Grouping: grouping,
		}
	}
	cases := []struct {
		name string
		path string
		body any
	}{
		{
			"end before start", transport.ConsoleRevenueTotalsPath,
			totals(consoleapi.Period{Start: now, End: now.Add(-time.Hour)}, consoleapi.GroupingDay),
		},
		{"end equal to start", transport.ConsoleListTransactionsPath, consoleapi.ListTransactionsRequest{
			Actor: s.actor, PublisherID: s.publisherID, Period: consoleapi.Period{Start: now, End: now},
		}},
		{
			"more than 366 buckets", transport.ConsoleRevenueTotalsPath,
			totals(consoleapi.Period{Start: now, End: now.AddDate(2, 0, 0)}, consoleapi.GroupingDay),
		},
		{"unknown grouping", transport.ConsoleRevenueTotalsPath, totals(aroundNow(), "hour")},
		{"no period", transport.ConsoleListTransactionsPath, consoleapi.ListTransactionsRequest{
			Actor: s.actor, PublisherID: s.publisherID,
		}},
		{
			"a period before the ledger's clock starts", transport.ConsoleRevenueTotalsPath,
			totals(consoleapi.Period{Start: time.Unix(-3600, 0).UTC(), End: time.Unix(1, 0).UTC()}, consoleapi.GroupingDay),
		},
		{"page size over the limit", transport.ConsoleListTransactionsPath, consoleapi.ListTransactionsRequest{
			Actor: s.actor, PublisherID: s.publisherID, Period: aroundNow(), PageSize: consoleapi.MaxPageSize + 1,
		}},
		{"catalog page size over the limit", transport.ConsoleListCatalogPath, consoleapi.ListCatalogRequest{
			Actor: s.actor, PublisherID: s.publisherID, PageSize: consoleapi.MaxPageSize + 1,
		}},
		{"negative page size", transport.ConsoleListTransactionsPath, consoleapi.ListTransactionsRequest{
			Actor: s.actor, PublisherID: s.publisherID, Period: aroundNow(), PageSize: -1,
		}},
		{"no transaction id", transport.ConsoleTransactionDetailPath, consoleapi.TransactionDetailRequest{
			Actor: s.actor, PublisherID: s.publisherID,
		}},
		{"no resource id", transport.ConsoleCatalogDetailPath, consoleapi.CatalogDetailRequest{
			Actor: s.actor, PublisherID: s.publisherID,
		}},
		{"unknown reporting status", transport.ConsoleListTransactionsPath, consoleapi.ListTransactionsRequest{
			Actor: s.actor, PublisherID: s.publisherID, Period: aroundNow(), ReportingStatus: "disputed",
		}},
		{"malformed transaction cursor", transport.ConsoleListTransactionsPath, consoleapi.ListTransactionsRequest{
			Actor: s.actor, PublisherID: s.publisherID, Period: aroundNow(), Cursor: "not-a-cursor",
		}},
		{"malformed catalog cursor", transport.ConsoleListCatalogPath, consoleapi.ListCatalogRequest{
			Actor: s.actor, PublisherID: s.publisherID, Cursor: "not-a-cursor",
		}},
		{"totals domain that is not a hostname", transport.ConsoleRevenueTotalsPath, consoleapi.RevenueTotalsRequest{
			Actor: s.actor, PublisherID: s.publisherID, Period: aroundNow(), Grouping: consoleapi.GroupingDay,
			Domain: "https://" + s.domain + "/news",
		}},
		{"list domain that is not a hostname", transport.ConsoleListTransactionsPath, consoleapi.ListTransactionsRequest{
			Actor: s.actor, PublisherID: s.publisherID, Period: aroundNow(), Domain: s.domain + ":8443",
		}},
		{"catalog domain that is not a hostname", transport.ConsoleListCatalogPath, consoleapi.ListCatalogRequest{
			Actor: s.actor, PublisherID: s.publisherID, Domain: "localhost",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := p.post(t, consolePath(c.path), c.body)
			assertRefused(t, c.name, status, body, http.StatusBadRequest, consoleapi.ErrorKindInvalidRequest)
		})
	}
}
