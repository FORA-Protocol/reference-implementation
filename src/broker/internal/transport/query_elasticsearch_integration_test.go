//go:build integration

package transport_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/searchfilters"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch/essearchtest"
)

// Catalog search through Elasticsearch. The cluster is replaced at the adapter
// boundary by essearchtest, because it is an external system and the subject here
// is what the Broker does with its answer. The essearch adapter, the Broker,
// Postgres, Redis and the Connect surface are real. Rendering, ranking and
// authentication are proven against a real node in the essearch package, and a
// real node behind a Broker and a real Exchange by
// tests/e2e/harness/test_broker_es_query.py.
//
// Round-trip: signed Connect client -> RFC 9421 -> httpsig middleware ->
// BrokerService handler -> resolve core (filter admission through the search's
// own vocabulary, then the search) -> essearch -> HTTP to the stand-in -> typed
// DiscoveryResponse or Connect error. Nothing is read below the RPC except what
// the stand-in recorded, which is the Broker's own outbound request.

const (
	esHitAcme = "https://acme.example/articles/central-bank"
	esHitBeta = "https://beta.example/articles/central-bank"
)

// exampleFilters is the filter schema the operator documentation ships.
func exampleFilters(t *testing.T) *searchfilters.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "broker", "search-filters.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := searchfilters.Load(raw)
	if err != nil || schema == nil {
		t.Fatalf("load the example schema: %v, %v", schema, err)
	}
	return schema
}

func esSearcher(t *testing.T, es *essearchtest.Server, schema *searchfilters.Schema) catalogsearch.Searcher {
	t.Helper()
	cfg := es.Config()
	cfg.Filters = schema
	s, err := essearch.New(cfg)
	if err != nil {
		t.Fatalf("essearch.New: %v", err)
	}
	return s
}

// assertNoSearch fails unless the stand-in received nothing.
func assertNoSearch(t *testing.T, es *essearchtest.Server) {
	t.Helper()
	if n := len(es.Requests()); n != 0 {
		t.Errorf("searches = %d, want 0", n)
	}
}

// The hits become one group each, in the order the cluster ranked them, and the
// Broker's own search stamps them SEARCH. The two URLs sit on two publishers, so
// the order is the search's: routing sees them one at a time.
//
// Needs an e2e test: the hit order across two publishers on two Exchanges; the e2e module proves it on one.
func TestResolve_ElasticsearchHitsBecomeGroupsInHitOrder(t *testing.T) {
	ctx := context.Background()
	es := essearchtest.NewServer(t)
	es.Script(essearchtest.Reply{URLs: []string{esHitBeta, esHitAcme}})
	fx := newFixture(t, ctx, fixtureOpts{
		providerDomain: "acme.example",
		query:          esSearcher(t, es, nil),
		secondary: &secondaryProvider{
			providerDomain: "beta.example", exchangeDomain: "mp.beta.example",
			offerID: "offer-beta", offerCost: 0.02, offerCanonicalURL: esHitBeta,
		},
	})

	const requestID = "req-es-order-0001"
	pub, priv := newBrokerKeyPair(t)
	srv := startBrokerConnectServer(t, fx, pub)
	client := signingBrokerClient(srv.base, srv.server.URL, "agent-1", priv)
	req := connect.NewRequest(buildDiscoveryRequest("agent-1", reqOpts{query: "central bank", budgetMinor: 100000}))
	req.Header().Set("X-Request-ID", requestID)
	out, err := client.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	uris := make([]string, 0, len(out.Msg.GetOfferGroups()))
	for _, g := range out.Msg.GetOfferGroups() {
		uris = append(uris, g.GetUri())
		if m := g.GetDiscoveryMethod(); m != forav1.DiscoveryMethod_DISCOVERY_METHOD_SEARCH {
			t.Errorf("group %s method = %v, want SEARCH", g.GetUri(), m)
		}
	}
	if want := []string{esHitBeta, esHitAcme}; !slices.Equal(uris, want) {
		t.Fatalf("groups = %v, want the hit order %v", uris, want)
	}

	reqs := es.Requests()
	if len(reqs) != 1 || reqs[0].Path != "/catalog/_search/template" {
		t.Fatalf("requests = %v, want one search", reqs)
	}
	if got := reqs[0].OpaqueID; got != requestID {
		t.Errorf("X-Opaque-Id = %q, want the request id, so the cluster's logs correlate", got)
	}
	reqs[0].AssertBody(t, `{"id":"fora-catalog","params":{"query":"central bank","limit":10}}`)
}

// Without a schema the search applies no filter, so any key is refused before
// any search, naming the key. Results that ignored it would answer a different
// question.
func TestResolve_AnyFilterIsUnimplementedWithoutASchema(t *testing.T) {
	cases := map[string]reqOpts{
		"with a query":    {query: "calm music", budgetMinor: 10000},
		"without a query": {budgetMinor: 10000},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			es := essearchtest.NewServer(t)
			es.Script(essearchtest.Reply{URLs: []string{esHitAcme}})
			fx := newSinkFixture(t, context.Background(), fixtureOpts{
				providerDomain: "acme.example", query: esSearcher(t, es, nil),
			})

			err := signedResolveFault(t, fx, filterRequest(t, opts, map[string]any{"music.genre": "soundtrack"}))

			assertConnectCodeLocal(t, err, connect.CodeUnimplemented)
			assertBrokerErrorField(t, err, "field", "search_filters.music.genre")
			assertNoSearch(t, es)
			assertNothingSent(t, fx.wire)
		})
	}
}

// Declared filters reach the template as parameters, nested by dot, with a query
// and without one; without one the query member is absent. The hit then goes on
// to its Exchange, which here answers 503: its answer is not the subject.
func TestResolve_DeclaredFiltersReachTheTemplateNested(t *testing.T) {
	cases := map[string]struct {
		opts reqOpts
		want string
	}{
		"with a query": {
			reqOpts{query: "central bank", budgetMinor: 100000},
			`{"id":"fora-catalog","params":{"query":"central bank","limit":10,"filters":{"news":{"language":"en"}}}}`,
		},
		"without a query": {
			reqOpts{budgetMinor: 100000},
			`{"id":"fora-catalog","params":{"limit":10,"filters":{"news":{"language":"en"}}}}`,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			es := essearchtest.NewServer(t)
			es.Script(essearchtest.Reply{URLs: []string{esHitAcme}})
			fx := newFixture(t, context.Background(), fixtureOpts{
				providerDomain: "acme.example", query: esSearcher(t, es, exampleFilters(t)),
				upstream: startUnavailableExchange(t),
			})

			resolveWithFilters(t, fx, filterRequest(t, c.opts, map[string]any{"news.language": "en"}))

			if fx.wire.arrivals() == 0 {
				t.Error("the hit did not go on to its Exchange")
			}
			reqs := es.Requests()
			if len(reqs) != 1 {
				t.Fatalf("searches = %d, want 1", len(reqs))
			}
			reqs[0].AssertBody(t, c.want)
		})
	}
}

func TestResolve_UndeclaredFilterKeyIsUnimplemented(t *testing.T) {
	es := essearchtest.NewServer(t)
	fx := newSinkFixture(t, context.Background(), fixtureOpts{
		providerDomain: "acme.example", query: esSearcher(t, es, exampleFilters(t)),
	})

	err := signedResolveFault(t, fx, filterRequest(t, reqOpts{query: "central bank", budgetMinor: 10000},
		map[string]any{"news.language": "en", "news.author": "x"}))

	assertConnectCodeLocal(t, err, connect.CodeUnimplemented)
	assertBrokerErrorField(t, err, "field", "search_filters.news.author")
	assertNoSearch(t, es)
	assertNothingSent(t, fx.wire)
}

// A value that breaks the schema is the caller's fault: INVALID_ARGUMENT on the
// key, and no search runs.
func TestResolve_MalformedFilterValueIsInvalidArgument(t *testing.T) {
	es := essearchtest.NewServer(t)
	fx := newSinkFixture(t, context.Background(), fixtureOpts{
		providerDomain: "acme.example", query: esSearcher(t, es, exampleFilters(t)),
	})

	err := signedResolveFault(t, fx, filterRequest(t, reqOpts{query: "central bank", budgetMinor: 10000},
		map[string]any{"news.language": "english"}))

	assertConnectCodeLocal(t, err, connect.CodeInvalidArgument)
	assertBrokerErrorField(t, err, "field", "search_filters.news.language")
	if strings.Contains(err.Error(), "english") {
		t.Errorf("refusal quotes the submitted value: %v", err)
	}
	assertNoSearch(t, es)
	assertNothingSent(t, fx.wire)
}

// A well-formed filter no document matches is an answer, not an error.
func TestResolve_WellFormedFilterWithNoHitsIsNotInCatalog(t *testing.T) {
	es := essearchtest.NewServer(t)
	fx := newSinkFixture(t, context.Background(), fixtureOpts{
		providerDomain: "acme.example", query: esSearcher(t, es, exampleFilters(t)),
	})

	out := resolveWithFilters(t, fx, filterRequest(t, reqOpts{budgetMinor: 10000}, map[string]any{"news.language": "de"}))

	assertQueryRefusal(t, fx, out, forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG)
	if n := len(es.Requests()); n != 1 {
		t.Errorf("searches = %d, want 1", n)
	}
}

// Every way the search can fail answers TEMPORARILY_UNAVAILABLE with no groups.
// The fixture's search provider would hand back acme.example, so a fallback would
// reach the sink, which fails the test; no Exchange is contacted.
func TestResolve_ElasticsearchFailureIsTemporarilyUnavailable(t *testing.T) {
	cases := map[string]essearchtest.Reply{
		"a failing cluster":   {Status: http.StatusServiceUnavailable, ErrorType: "unavailable_shards_exception"},
		"refused credentials": {Status: http.StatusUnauthorized, ErrorType: "security_exception"},
		"a missing index":     {Status: http.StatusNotFound, ErrorType: "index_not_found_exception"},
		"a timed-out search":  {URLs: []string{esHitAcme}, TimedOut: true},
		"a failed shard":      {URLs: []string{esHitAcme}, FailedShards: 1},
		"a hit without a URL at the path": {Hits: []map[string]any{
			{"_id": "doc-1", "_source": map[string]any{"title": "no url"}},
		}},
		"an unreadable answer": {RawBody: "<html>hello</html>"},
		"a template that renders invalid JSON": {Status: http.StatusBadRequest, RawBody: `{"error":{
			"root_cause":[{"type":"x_content_parse_exception","reason":"Unexpected character"}],
			"type":"x_content_parse_exception","reason":"Unexpected character"}}`},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			es := essearchtest.NewServer(t)
			es.Script(reply)
			fx := newSinkFixture(t, context.Background(), fixtureOpts{
				providerDomain: "acme.example", query: esSearcher(t, es, nil), captureLogs: true,
			})

			_, out := resolveOverConnect(t, fx, reqOpts{query: "central bank", budgetMinor: 10000})

			assertQueryRefusal(t, fx, out, forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE)
			fx.logs.AssertLoggedOnce(t, "broker.resolve.query_search_failed", "WARN")
			if lines := strings.Join(fx.logs.Lines(), "\n"); strings.Contains(lines, essearchtest.Key) {
				t.Error("a log line carries the API key")
			}
		})
	}
}

func TestResolve_ElasticsearchConnectionFailureIsTemporarilyUnavailable(t *testing.T) {
	es := essearchtest.NewServer(t)
	fx := newSinkFixture(t, context.Background(), fixtureOpts{providerDomain: "acme.example", query: esSearcher(t, es, nil)})
	es.Close()

	_, out := resolveOverConnect(t, fx, reqOpts{query: "central bank", budgetMinor: 10000})

	assertQueryRefusal(t, fx, out, forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE)
}

// A slow cluster is cut off by the configured timeout, and the agent gets its
// answer instead of waiting.
func TestResolve_ElasticsearchTimeoutIsTemporarilyUnavailable(t *testing.T) {
	es := essearchtest.NewServer(t)
	es.Script(essearchtest.Reply{Delay: time.Minute})
	cfg := es.Config()
	cfg.Timeout = 50 * time.Millisecond
	s, err := essearch.New(cfg)
	if err != nil {
		t.Fatalf("essearch.New: %v", err)
	}
	fx := newSinkFixture(t, context.Background(), fixtureOpts{providerDomain: "acme.example", query: s})

	_, out := resolveOverConnect(t, fx, reqOpts{query: "central bank", budgetMinor: 10000})

	assertQueryRefusal(t, fx, out, forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE)
}

func TestResolve_ElasticsearchWithoutHitsIsNotInCatalog(t *testing.T) {
	es := essearchtest.NewServer(t)
	fx := newSinkFixture(t, context.Background(), fixtureOpts{providerDomain: "acme.example", query: esSearcher(t, es, nil)})

	_, out := resolveOverConnect(t, fx, reqOpts{query: "quantum chromodynamics", budgetMinor: 10000})

	assertQueryRefusal(t, fx, out, forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG)
}

// A hit is only a candidate: it takes the per-URL route like a URL the agent
// named. A hit on a publisher the Broker cannot reach stops at the manifest probe,
// so a stale entry in the index reaches no Exchange and produces no offer. A
// failed search answers the same way, so the test also shows that the search ran
// once and succeeded (no search failure is logged) and that the probe of the
// hit's publisher is what failed. A stale URL on a live publisher is the
// Exchange's to refuse, which tests/e2e/harness/test_broker_es_query.py proves.
func TestResolve_ElasticsearchHitOnAnUnreachablePublisherGetsNoOffer(t *testing.T) {
	es := essearchtest.NewServer(t)
	es.Script(essearchtest.Reply{URLs: []string{"https://gone.example/articles/withdrawn"}})
	fx := newSinkFixture(t, context.Background(), fixtureOpts{
		providerDomain: "acme.example", query: esSearcher(t, es, nil), captureLogs: true,
	})

	_, out := resolveOverConnect(t, fx, reqOpts{query: "withdrawn", budgetMinor: 10000})

	assertQueryRefusal(t, fx, out, forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE)
	if n := len(es.Requests()); n != 1 {
		t.Errorf("%d requests reached the cluster, want the one search", n)
	}
	if failed := fx.logs.Find("broker.resolve.query_search_failed"); len(failed) != 0 {
		t.Errorf("the search failed, so the refusal is not the probe's: %v", failed)
	}
	if rec := fx.logs.AssertLoggedOnce(t, "broker.resolve.probe", "WARN"); rec["domain"] != "gone.example" {
		t.Errorf("probe failure domain = %v, want gone.example", rec["domain"])
	}
}

// A named URL never reaches the search, so an outage of the cluster cannot touch
// it, and search filters are not read at all: not even a key the schema does not
// declare or a value it would refuse. The upstream answers 503, which is enough
// here: the arrival shows the request went on to the Exchange instead of
// stopping at the failed search, whose absence reason it would otherwise share.
func TestResolve_NamedURIBypassesElasticsearch(t *testing.T) {
	cases := map[string]map[string]any{
		"no filters":          nil,
		"a declared filter":   {"news.language": "en"},
		"an undeclared key":   {"news.author": "x"},
		"a malformed value":   {"news.language": "english"},
		"another profile key": {"music.bpm_range": "fast"},
	}
	for name, filters := range cases {
		t.Run(name, func(t *testing.T) {
			es := essearchtest.NewServer(t)
			fx := newFixture(t, context.Background(), fixtureOpts{
				providerDomain: "acme.example", query: esSearcher(t, es, exampleFilters(t)),
				upstream: startUnavailableExchange(t),
			})
			es.Close()

			resolveWithFilters(t, fx, filterRequest(t, reqOpts{
				uri: esHitAcme, query: "central bank", budgetMinor: 10000,
			}, filters))

			assertNoSearch(t, es)
			if fx.wire.arrivals() == 0 {
				t.Error("the named URL did not reach the Exchange")
			}
		})
	}
}
