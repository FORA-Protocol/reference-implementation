//go:build integration

package transport_test

import (
	"context"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

// fora-music-v1 search filters over the catalog search. The setup is the one
// query_discovery_integration_test.go uses: the model is replaced at the
// adapter boundary by fakeExtractor, and the catalog index, the Broker,
// Postgres, Redis and the Connect surface are real.
//
// The tests here are the Broker's refusals, answered before any Exchange is
// contacted, so the Exchange address is a sink that fails the test if anything
// reaches it. Filtered searches routed to a real Exchange, and a named URI
// ignoring a malformed filter, are proven end to end by
// tests/e2e/harness/test_mcp_query_discovery.py; the filter semantics case by
// case are unit-tested in src/broker/internal/tagsearch.
//
// Round-trip: signed Connect client -> RFC 9421 -> httpsig middleware ->
// BrokerService handler (filter validation) -> resolve core (catalog search)
// -> typed DiscoveryResponse or Connect error. Nothing is read below the RPC.
//
// queryFeed (in query_discovery_integration_test.go) holds:
//   calm-electronica  electronica, calm, 124 bpm
//   calm-orchestral   soundtrack,  calm,  90 bpm, ISRC USAAA2600002
//   calm-soundtrack   soundtrack,  calm,  70 bpm
//   epic-soundtrack   soundtrack,  epic,  85 bpm

// filterRequest builds a signed-caller DiscoveryRequest carrying filters.
func filterRequest(t *testing.T, o reqOpts, filters map[string]any) *forav1.DiscoveryRequest {
	t.Helper()
	req := buildDiscoveryRequest("agent-1", o)
	s, err := structpb.NewStruct(filters)
	if err != nil {
		t.Fatalf("build search_filters: %v", err)
	}
	req.SearchFilters = s
	return req
}

// resolveWithFilters sends the request and returns the decoded response.
func resolveWithFilters(t *testing.T, fx *fixture, req *forav1.DiscoveryRequest) *forav1.DiscoveryResponse {
	t.Helper()
	pub, priv := newBrokerKeyPair(t)
	srv := startBrokerConnectServer(t, fx, pub)
	client := signingBrokerClient(srv.base, srv.server.URL, "agent-1", priv)
	resp, err := client.Resolve(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("Resolve returned transport error: %v", err)
	}
	return resp.Msg
}

// Filtered searches that find nothing are NOT_IN_CATALOG with no groups, and
// no Exchange is queried. The filter is never dropped to produce a result.
func TestResolve_SearchFiltersWithoutMatchAreNotInCatalog(t *testing.T) {
	maxBPM := 80
	cases := map[string]struct {
		extraction tagsearch.Extraction
		query      string
		filters    map[string]any
	}{
		"a valid value that no track has": {filters: map[string]any{"music.genre": "funk"}},
		"filter and query bounds that do not overlap": {
			query:      "slow music",
			extraction: tagsearch.Extraction{MaxBPM: &maxBPM},
			filters:    map[string]any{"music.bpm_range": "100-"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fx := queryFixture(t, &fakeExtractor{result: c.extraction})
			out := resolveWithFilters(t, fx, filterRequest(t, reqOpts{query: c.query, budgetMinor: 10000}, c.filters))
			assertQueryRefusal(t, fx, out, forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG)
		})
	}
}

// A malformed filter value rejects the request before any search: the model
// is not asked and no Exchange is queried.
func TestResolve_MalformedSearchFilterIsInvalidArgument(t *testing.T) {
	x := &fakeExtractor{result: tagsearch.Extraction{Include: []string{"mood:calm"}}}
	fx := queryFixture(t, x)

	err := signedResolveFault(t, fx,
		filterRequest(t, reqOpts{query: "calm music", budgetMinor: 10000}, map[string]any{"music.bpm_range": "fast"}))

	assertConnectCodeLocal(t, err, connect.CodeInvalidArgument)
	assertBrokerErrorField(t, err, "field", "search_filters.music.bpm_range")
	if got := x.calls(); len(got) != 0 {
		t.Errorf("extractor was asked %v, want no search for a malformed filter", got)
	}
	assertNothingSent(t, fx.wire)
}

// A request whose only search input is a filter key the profile does not
// define has no search input at all, like a request with neither query nor
// uri.
func TestResolve_OnlyUnknownSearchFiltersAreInvalidArgument(t *testing.T) {
	x := &fakeExtractor{}
	fx := queryFixture(t, x)

	err := signedResolveFault(t, fx,
		filterRequest(t, reqOpts{budgetMinor: 10000}, map[string]any{"music.tempo": "fast"}))

	assertConnectCodeLocal(t, err, connect.CodeInvalidArgument)
	assertBrokerErrorField(t, err, "required_one_of", "query,uri,search_filters")
	if len(x.calls()) != 0 {
		t.Errorf("rejected request reached the search: extractor=%v", x.calls())
	}
	assertNothingSent(t, fx.wire)
}

// With query discovery switched off, search filters are refused. The Broker
// must not answer through the search-provider fallback, which would ignore
// them.
func TestResolve_SearchFiltersWithoutCatalogSearchAreUnimplemented(t *testing.T) {
	fx := newSinkFixture(t, context.Background(), fixtureOpts{providerDomain: "acme.example"})

	err := signedResolveFault(t, fx,
		filterRequest(t, reqOpts{query: "calm music", budgetMinor: 10000}, map[string]any{"music.genre": "soundtrack"}))

	assertConnectCodeLocal(t, err, connect.CodeUnimplemented)
	assertBrokerErrorField(t, err, "field", "search_filters")
	assertNothingSent(t, fx.wire)
}
