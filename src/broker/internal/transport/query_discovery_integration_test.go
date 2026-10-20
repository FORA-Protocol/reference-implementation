//go:build integration

package transport_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/bedrock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

// Catalog search: with query discovery enabled, the Broker turns a free-text
// query into catalog URIs before any candidate-domain probing, and those URIs
// follow the per-URL route. The model is replaced at the adapter boundary by
// fakeExtractor; the catalog index, the Broker, Postgres, Redis and the
// Connect surface are real.
//
// The tests here are the Broker's refusals, answered before any Exchange is
// contacted, so the Exchange address is a sink that fails the test if anything
// reaches it. A matched query routed to a real Exchange, and a named URI taking
// priority over the query, are proven end to end by
// tests/e2e/harness/test_mcp_query_discovery.py.
//
// Round-trip: signed Connect client -> RFC 9421 -> httpsig middleware ->
// BrokerService handler -> resolve core (catalog search) -> typed
// DiscoveryResponse. Nothing is read below the RPC.

// queryFeed holds four tracks on the fixture's publisher domain. For
// {mood:calm, genre:soundtrack} the calm orchestral and the calm soundtrack
// score two facets; the calm electronica and the epic soundtrack score one.
var queryFeed = strings.Join([]string{
	`{"domain":"acme.example","path":"/tracks/calm-electronica.mp3","terms":[],` +
		`"ext":{"music.genre":"electronica","music.mood":["calm"],"music.bpm":124}}`,
	`{"domain":"acme.example","path":"/tracks/calm-orchestral.mp3","terms":[],` +
		`"ext":{"music.isrc":"USAAA2600002","music.genre":"soundtrack","music.mood":["calm"],"music.bpm":90}}`,
	`{"domain":"acme.example","path":"/tracks/calm-soundtrack.mp3","terms":[],` +
		`"ext":{"music.genre":"soundtrack","music.mood":["calm"],"music.bpm":70}}`,
	`{"domain":"acme.example","path":"/tracks/epic-soundtrack.mp3","terms":[],` +
		`"ext":{"music.genre":"soundtrack","music.mood":["epic"],"music.bpm":85}}`,
}, "\n")

// fakeExtractor stands in for the model: it returns a fixed extraction or
// error, and records the queries it received.
type fakeExtractor struct {
	mu      sync.Mutex
	result  tagsearch.Extraction
	err     error
	queries []string
}

func (f *fakeExtractor) Extract(_ context.Context, query string, _ []string) (tagsearch.Extraction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, query)
	return f.result, f.err
}

func (f *fakeExtractor) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

// queryFixture is the resolve fixture with catalog search enabled over
// queryFeed, in front of a sink.
func queryFixture(t *testing.T, x *fakeExtractor) *fixture {
	t.Helper()
	index, err := tagsearch.Load(strings.NewReader(queryFeed), "https")
	if err != nil {
		t.Fatalf("load feed: %v", err)
	}
	return newSinkFixture(t, context.Background(), fixtureOpts{
		providerDomain: "acme.example",
		query:          tagsearch.NewSearcher(index, x),
	})
}

// A query the catalog cannot answer is NOT_IN_CATALOG with no groups, and no
// Exchange is queried: neither for tags outside the vocabulary nor for known
// tags that match no track.
func TestResolve_QueryWithoutMatchIsNotInCatalog(t *testing.T) {
	minBPM := 60
	cases := map[string]tagsearch.Extraction{
		"no known tag": {Include: []string{"mood:joyful", "license:personal"}},
		"no match":     {Include: []string{"mood:epic"}, Exclude: []string{"genre:soundtrack"}},
		"range only":   {MinBPM: &minBPM},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			fx := queryFixture(t, &fakeExtractor{result: e})
			_, out := resolveOverConnect(t, fx, reqOpts{query: "q", budgetMinor: 10000})
			assertQueryRefusal(t, fx, out, forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG)
		})
	}
}

// A failed extraction is TEMPORARILY_UNAVAILABLE with no groups. The search
// provider is not consulted, and no Exchange is queried.
func TestResolve_QueryExtractionFailureIsTemporarilyUnavailable(t *testing.T) {
	fx := queryFixture(t, &fakeExtractor{err: errors.Join(bedrock.ErrUnavailable, errors.New("throttled"))})
	_, out := resolveOverConnect(t, fx, reqOpts{query: "calm music", budgetMinor: 10000})
	assertQueryRefusal(t, fx, out, forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE)
}

func assertQueryRefusal(
	t *testing.T, fx *fixture, out *forav1.DiscoveryResponse, want forav1.OfferAbsenceReason,
) {
	t.Helper()
	if got := out.GetAbsenceReason(); got != want {
		t.Errorf("absence_reason = %v, want %v", got, want)
	}
	if n := len(out.GetOfferGroups()); n != 0 {
		t.Errorf("offer_groups = %d, want 0", n)
	}
	assertNothingSent(t, fx.wire)
}
