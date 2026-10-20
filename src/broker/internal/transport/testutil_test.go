//go:build integration

package transport_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	audiencetest "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/offerkeys"
)

// mustMoney renders a test-authored float amount to the canonical wire decimal
// STRING the proto money fields now carry (money-as-string), matching
// production's FormatMoney semantics (0.10 -> "0.1", 5.0 -> "5", 0 -> "0").
func mustMoney(amount float64) string {
	s, err := helpers.FormatMoney(decimal.NewFromFloat(amount))
	if err != nil {
		panic(err)
	}
	return s
}

// acquireTestDB resets the shared package Postgres to its migrated baseline (see
// TestMain) and returns a fresh pool bound to it. Each test calls it exactly once;
// it delegates to db.AcquireTestDB (once-per-test and serial-execution contract
// documented there).
func acquireTestDB(tb testing.TB, ctx context.Context) *pgxpool.Pool {
	tb.Helper()
	return sharedb.AcquireTestDB(tb, ctx, sharedPG)
}

// acquireTestRedis resets the shared package Redis to an empty keyspace (FLUSHDB,
// see TestMain) and returns the shared client. The client is owned by the suite —
// it is NOT closed per test. Each Redis-backed test calls this exactly once.
func acquireTestRedis(tb testing.TB, ctx context.Context) *redis.Client {
	tb.Helper()
	if err := sharedRedis.Reset(ctx); err != nil {
		tb.Fatalf("reset shared redis: %v", err)
	}
	return sharedRedis.Client
}

// readAndCloseBody reads a relay response body in full and closes it, failing
// the test if either step errors. Helpers that consume a response go through it
// so no caller has to close a body a helper already read.
func readAndCloseBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); cerr != nil {
		t.Fatalf("close response body: %v", cerr)
	}
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return body
}

// -----------------------------------------------------------------------------
// Mock Exchange (Connect-Go handler).
//
// Testing Doctrine point 6 forbids emulating the Exchange, and this mock does.
// It is kept only for the tests whose assertion depends on what an Exchange
// answers or receives and that no e2e test covers yet; each of those says so in
// a "Needs an e2e test" comment naming the missing coverage. Every other test
// in this package puts a wireExchange at the Exchange's address instead
// (exchange_wire_fixture_test.go): a sink, or a network condition. The
// behaviours that need a real Exchange are proven by the e2e suite under
// tests/e2e/harness/. A new test does not use this mock: it either asserts
// what the Broker does without an Exchange's answer, or belongs in that suite.

type mockExchange struct {
	// The embedded Unimplemented base absorbs RPCs the Broker never
	// exercises (they answer CodeUnimplemented), exactly as the production
	// ExchangeHandler embeds it — a new proto RPC must not break this stub.
	forav1connect.UnimplementedExchangeServiceHandler

	mu            sync.Mutex
	discoverCalls int
	executeCalls  int
	// lastDiscoverRequesterID is the requester.id the broker actually FORWARDED on
	// the most recent discover. The broker canonicalizes req.AgentID and writes it
	// back onto the request, so this is where a test observes that the value which
	// travelled upstream is the identity rather than the caller's spelling.
	lastDiscoverRequesterID string
	// domain is this Exchange's own published identity: the value it signs into
	// the offers it issues, the recipient it answers to, and the domain its
	// registry row carries. Empty means mockExchangeDomain, which is what the
	// single-exchange fixtures use; a fixture wiring a SECOND exchange gives that
	// one its own, so a fan-out leg addressed to the wrong sibling is refused
	// rather than served.
	domain        string
	offerUnitCost float64
	signedURL     string
	// discoveryMethod is the method the mock reports on every OfferGroup it
	// returns. It defaults to EXCHANGE in mockOfferGroup, matching the real
	// Exchange; a test sets it to another value to prove the Broker IGNORES what
	// the exchange reported and states its own, which is the contract on the
	// Resolve path.
	discoveryMethod forav1.DiscoveryMethod
	// rejectURILessQuery makes DiscoverResources refuse a ResourceQuery carrying
	// no uris, which is what the REAL Exchange does ("at least one uri
	// required"). It is opt-in and defaults false because this mock has always
	// answered a uri-less query, and the free-text-query suites are built on
	// that: turning it on globally would rewrite what sixteen pre-existing tests
	// exercise. A test sets it when the behaviour under test is what the Broker
	// does against a COMPLIANT Exchange — today, on the query path, that is
	// producing no groups at all.
	rejectURILessQuery bool
	// offerEstimatedQuantity, when > 0, is set as the offer's
	// Pricing.EstimatedQuantity, which the Broker's budget gate multiplies the
	// unit cost by.
	offerEstimatedQuantity int32
	// offerID / offerCanonicalURL, when set, override the DiscoverResources
	// offer identity so a MULTI-URI / MULTI-EXCHANGE resolve can surface a
	// DISTINCT offer per exchange (distinct offer_id + distinct canonical_url,
	// the latter keeping each offer in its own selection.Dedup identity bucket).
	// Empty fields fall back to the legacy single-exchange defaults
	// ("offer-1" / "https://acme.example/article-42") so the existing
	// single-exchange suites read unchanged.
	offerID           string
	offerCanonicalURL string

	// offerSigningKey, when non-nil, makes DiscoverResources emit TWO genuinely
	// Exchange-signed offers (both signed with offerSigningKey via
	// helpers.SignOffer): id=validOfferID and id=rejectedOfferID. Both carry a
	// real signature and a fresh expiry — the drop that a verification test
	// exercises is decided by the injected broker Deps.OfferVerifier (which id it
	// rejects), NOT by a doctored signature, so no test mocks the Exchange's
	// verification (Testing Doctrine pt 6). When offerSigningKey is nil,
	// DiscoverResources returns the legacy single-offer "sig-x" shape so every
	// existing test keeps passing unchanged.
	offerSigningKey ed25519.PrivateKey
	offerSigningPub ed25519.PublicKey
}

// mockOfferGroup builds an OfferGroup the way the REAL Exchange does
// (service/discover.go newOfferGroup): the uri plus the discovery method, which
// the Exchange states on every group it emits. method is the mock's configured
// value; UNSPECIFIED means "behave like the real Exchange" and yields EXCHANGE.
// Every group this mock returns goes through here, so the METHOD cannot drift
// from the producer it stands in for. Other parts of the shape do differ — the
// real Exchange returns one group per requested URI and this mock returns one
// for uris[0] — so this is not a general fidelity guarantee.
//
// groupURIFor resolves that single URI: the first requested one, or fallback for
// the suites that drive this mock with no URIs at all. It lives beside the
// builder because both return paths need the same resolution and had a copy of
// it each.
func groupURIFor(req *connect.Request[forav1.ResourceQuery], fallback string) string {
	if uris := req.Msg.GetUris(); len(uris) > 0 {
		return uris[0]
	}
	return fallback
}

func mockOfferGroup(uri string, method forav1.DiscoveryMethod) *forav1.OfferGroup {
	if method == forav1.DiscoveryMethod_DISCOVERY_METHOD_UNSPECIFIED {
		method = forav1.DiscoveryMethod_DISCOVERY_METHOD_EXCHANGE
	}
	return &forav1.OfferGroup{Uri: uri, DiscoveryMethod: method.Enum()}
}

func (m *mockExchange) DiscoverResources(_ context.Context, req *connect.Request[forav1.ResourceQuery]) (*connect.Response[forav1.ResourceResponse], error) {
	m.mu.Lock()
	m.discoverCalls++
	m.lastDiscoverRequesterID = req.Msg.GetRequester().GetId()
	cost := m.offerUnitCost
	signingKey := m.offerSigningKey
	method := m.discoveryMethod
	rejectURILess := m.rejectURILessQuery
	m.mu.Unlock()
	// Mirrors the real Exchange's first validation: a ResourceQuery must name at
	// least one uri (src/exchange/internal/service/exchange.go). Opt-in — see the
	// field's comment for why this mock answers a uri-less query by default.
	if rejectURILess && len(req.Msg.GetUris()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("at least one uri required"))
	}
	costStr := mustMoney(cost)
	unit := "accesses"
	pricing := &forav1.Pricing{
		Rate:     costStr,
		Currency: "USD",
		UnitCost: &costStr,
		// R7: the discovered Offer now rides on the agent-facing DiscoveryResponse
		// (offer_groups), so it must satisfy the proto's Offer validation rules
		// (pricing.model and identity.resource_mutability must be set, not the
		// zero/UNSPECIFIED enum). Before R7 the offer was consumed into a tx and
		// never serialized, so these fields were never validated. PER_UNIT
		// requires Pricing.unit (mirrors the Exchange's seedPricedTermEst fixture).
		Model: forav1.PricingModel_PRICING_MODEL_PER_UNIT,
		Unit:  &unit,
	}
	// The double's metered offer states an estimate, which the pinned protocol
	// allows and requires to be positive when stated (offer.metered.estimate_positive).
	// One unit is what the Broker's charge projection and the Exchange's biller
	// both clamp an unset estimate to, so the default changes no charge.
	est := int32(1)
	if m.offerEstimatedQuantity > 0 {
		est = m.offerEstimatedQuantity
	}
	pricing.EstimatedQuantity = &est
	url := "https://acme.example/article-42"
	if m.offerCanonicalURL != "" {
		url = m.offerCanonicalURL
	}
	offerID := "offer-1"
	if m.offerID != "" {
		offerID = m.offerID
	}
	offer := &forav1.Offer{
		OfferId:            offerID,
		Pricing:            pricing,
		DeliveryMethod:     forav1.DeliveryMethod_DELIVERY_METHOD_INSTRUCTIONS,
		Signature:          "sig-x",
		SignatureAlgorithm: "EdDSA",
		// Exchange (field 8) is the Exchange's canonical domain, signed into the
		// offer (the execute-routing target). The Exchange producer
		// sets this in buildOffer before SignOffer; the mock stands in for that
		// producer with the same domain its own registry row carries.
		Exchange: m.identity(),
		Identity: &forav1.ResourceIdentity{
			CanonicalUrl:       &url,
			ResourceMutability: forav1.ResourceMutability_RESOURCE_MUTABILITY_STATIC,
		},
		Reporting: &forav1.ReportingObligation{Required: true},
		// Terms is the full licensing payload — the broker must forward it verbatim
		// in DiscoveryResponse.offer_groups (no lossy re-type). A non-empty Terms
		// makes the verbatim-forward assertion non-vacuous.
		Terms: []*forav1.LicenseTerm{{
			// Semantics is publisher/catalog-provided data: the REAL Exchange's
			// seedPricedTerm fixture sets TERM_SEMANTICS_ENUMERATED, and the proto's
			// LicenseTerm.semantics {not_in:[0]} rule is now enforced when the broker
			// serializes offer_groups in its DiscoveryResponse. The mock stands in for
			// the Exchange producer, so it mirrors that fixture (matching the
			// ResourceMutability:STATIC mirror above) — an unset semantics here is a
			// stale mock, not a broker-production gap (the broker forwards terms
			// verbatim and never derives semantics).
			Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
			Scopes:    []string{"ai-training"},
			// The offer states its price once, in Offer.pricing: the term it
			// sells carries none (offer.terms.pricing_unset).
		}},
	}

	// Offer-verification test mode: when offerSigningKey is set, return TWO
	// genuinely Exchange-signed offers (validOfferID and rejectedOfferID), both
	// carrying a real signature and a fresh expiry. The broker's drop-wiring is
	// then exercised by injecting a Deps.OfferVerifier that rejects one id — the
	// verify/reject DECISION is mocked (a legitimate broker seam), the Exchange is
	// not (Testing Doctrine pt 6). When signingKey is nil, fall through to the
	// legacy single-offer path so every existing test stays green.
	if signingKey != nil {
		// Build the valid offer.
		validOffer := &forav1.Offer{
			OfferId:            validOfferID,
			Pricing:            pricing,
			DeliveryMethod:     forav1.DeliveryMethod_DELIVERY_METHOD_INSTRUCTIONS,
			SignatureAlgorithm: helpers.OfferSignatureAlgorithm,
			Exchange:           m.identity(),
			// The SDK Verifier is fail-closed on freshness (a missing or past
			// expires_at is EXPIRED), so mint a real future bound covered by the
			// signature — otherwise even the genuine offer is rejected.
			ExpiresAt: timestamppb.New(time.Now().Add(time.Hour)),
			Identity: &forav1.ResourceIdentity{
				CanonicalUrl:       &url,
				ResourceMutability: forav1.ResourceMutability_RESOURCE_MUTABILITY_STATIC,
			},
			Reporting: &forav1.ReportingObligation{Required: true},
			Terms: []*forav1.LicenseTerm{{
				Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
				Scopes:    []string{"ai-training"},
			}},
		}
		validSig, err := helpers.SignOffer(signingKey, validOffer)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal,
				errors.New("mockExchange: sign valid offer: "+err.Error()))
		}
		validOffer.Signature = validSig

		// Build the SECOND offer: same shape, a distinct id + canonical_url so the
		// broker keeps it in its own identity bucket, and a GENUINE Exchange
		// signature (both offers are validly signed). Whether it reaches
		// offer_groups is decided by the injected Deps.OfferVerifier, not by any
		// signature defect.
		secondCanonicalURL := "https://acme.example/article-rejected"
		secondOffer := &forav1.Offer{
			OfferId:            rejectedOfferID,
			Pricing:            pricing,
			DeliveryMethod:     forav1.DeliveryMethod_DELIVERY_METHOD_INSTRUCTIONS,
			SignatureAlgorithm: helpers.OfferSignatureAlgorithm,
			Exchange:           m.identity(),
			ExpiresAt:          timestamppb.New(time.Now().Add(time.Hour)),
			Identity: &forav1.ResourceIdentity{
				CanonicalUrl:       &secondCanonicalURL,
				ResourceMutability: forav1.ResourceMutability_RESOURCE_MUTABILITY_STATIC,
			},
			Reporting: &forav1.ReportingObligation{Required: true},
			Terms: []*forav1.LicenseTerm{{
				Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
				Scopes:    []string{"ai-training"},
			}},
		}
		secondSig, err := helpers.SignOffer(signingKey, secondOffer)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal,
				errors.New("mockExchange: sign second offer: "+err.Error()))
		}
		secondOffer.Signature = secondSig

		group := mockOfferGroup(groupURIFor(req, url), method)
		group.Offers = []*forav1.Offer{validOffer, secondOffer}
		return connect.NewResponse(&forav1.ResourceResponse{
			Ver:         helpers.ProtocolVersion,
			Offers:      []*forav1.Offer{validOffer, secondOffer},
			OfferGroups: []*forav1.OfferGroup{group},
		}), nil
	}

	// Mirror the REAL Exchange (service/discover.go): a hit returns ONE OfferGroup
	// per requested uri, keyed on OfferGroup.Uri, AND the flat Offers aggregate.
	// The broker now MERGES per-URI OfferGroups (resolve.go discoverOffers), so the
	// group MUST be keyed on the requested uri for the merge to surface it. A
	// query carrying no uris (the EXA-query suites) keys the group on the offer's
	// own canonical_url so a single-group response is still produced.
	//
	// The Exchange-facing leg (ResourceResponse) is otherwise UNAFFECTED by the
	// agent<->broker DiscoveryRequest/DiscoveryResponse rename: the broker reads
	// resp.GetOffers() (and, post per-URL fan-out, resp.GetOfferGroups()) from the
	// Exchange exactly as before.
	group := mockOfferGroup(groupURIFor(req, url), method)
	group.Offers = []*forav1.Offer{offer}
	return connect.NewResponse(&forav1.ResourceResponse{
		Ver:         helpers.ProtocolVersion,
		Offers:      []*forav1.Offer{offer},
		OfferGroups: []*forav1.OfferGroup{group},
	}), nil
}

// mockExchangeDomain is the canonical Exchange domain the mock Exchange signs
// into Offer.exchange (field 8) and that the registry row in newFixture carries.
// The discovery test asserts the broker forwards this verbatim, and the relay
// test routes execute to it via the signed-domain mechanism (no endpoint header).
const mockExchangeDomain = "mp.acme.example"

// identity is the Exchange this mock stands for. Every place that needs the
// value reads it here, so a fixture that gives a second exchange its own domain
// moves the offers it signs and the recipient it accepts together.
func (m *mockExchange) identity() string {
	if m.domain == "" {
		return mockExchangeDomain
	}
	return m.domain
}

func (m *mockExchange) ExecuteTransaction(_ context.Context, req *connect.Request[forav1.TransactionRequest]) (*connect.Response[forav1.TransactionResponse], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executeCalls++
	// Batch mode: respond with one TransactionResultItem per inbound item,
	// mirroring the real Exchange's first-class items[] path, each with its own
	// signed retrieval endpoint.
	if items := req.Msg.GetItems(); len(items) > 0 {
		return connect.NewResponse(m.batchResponse(req.Msg.GetIdempotencyKey(), items)), nil
	}
	// Items-only: the broker always relays items[]; a no-items request is
	// a contract violation the broker's empty-items guard rejects upstream, so the
	// downstream Exchange never sees one. (Single-offer mode + its top-level
	// TransactionResponse result fields were removed in C5.)
	return nil, connect.NewError(connect.CodeInvalidArgument,
		errors.New("mockExchange: ExecuteTransaction requires items[] (single-offer mode removed)"))
}

// batchResponse builds a TransactionResponse.items[] mirroring the real
// Exchange's first-class batch path: one TransactionResultItem per inbound
// item, each with its own signed retrieval endpoint. The signed URL carries the
// offer_id so a multi-exchange test can attribute each item to the exchange
// that minted it. Caller holds m.mu.
func (m *mockExchange) batchResponse(idem string, items []*forav1.TransactionItem) *forav1.TransactionResponse {
	results := make([]*forav1.TransactionResultItem, 0, len(items))
	for _, it := range items {
		offerID := it.GetOffer().GetOfferId()
		endpoint := m.signedURL + "&offer=" + offerID
		results = append(results, &forav1.TransactionResultItem{
			OfferId:           offerID,
			TransactionId:     "tx-" + idem + ":" + offerID,
			BillingId:         "bill-" + idem + ":" + offerID,
			RetrievalEndpoint: &endpoint,
		})
	}
	// The identity hash names this Exchange, so a purchase across two Exchanges
	// can tell which Exchange's request-level state landed in which outcome.
	return &forav1.TransactionResponse{
		Ver: helpers.ProtocolVersion, Items: results, AgentIdentityHash: "thumb-of-" + m.identity(),
	}
}

// refusingOrigin is an address that refuses a connection at once: nothing
// listens on port 1 of the loopback interface. Tests use it for an origin or an
// exchange that is down, rather than starting and closing a server, whose freed
// port another listener could take before the test dials it.
const refusingOrigin = "http://127.0.0.1:1"

// fakeEndpointResolver stands in for resolvers.WellKnownEndpointResolver in the
// broker integration tests: it maps a registered exchange DOMAIN to the mock
// Exchange's loopback URL exactly as the exchange's own /.well-known/fora.json
// would. The production broker now resolves discovery endpoints via well-known
// (Deps.Endpoints), with the registry a trust allowlist only — so the fixture
// supplies the endpoint here, NOT via the registry's Endpoint column. An
// unmapped domain returns resolvers.ErrNoEndpoint, the same not-advertised signal
// the real resolver raises (so a URL routed to it falls to typed-absence).
//
// refuse stages one of the resolver's OTHER verdicts for a domain — the one
// case the byDomain map cannot express, because the map has only "here is the
// endpoint" and "nothing advertised one". A test that needs the resolver to
// have READ a manifest and refused it puts the sentinel here. It is checked
// before byDomain, so a domain can be both registered and refused, which is
// exactly the shape under test.
type fakeEndpointResolver struct {
	byDomain map[string]string
	refuse   map[string]error
}

func (f *fakeEndpointResolver) ResolveEndpoint(_ context.Context, domain string) (string, error) {
	if err, ok := f.refuse[domain]; ok {
		return "", err
	}
	if u, ok := f.byDomain[domain]; ok {
		return u, nil
	}
	return "", resolvers.ErrNoEndpoint
}

// refuseDomain makes every later resolution of domain answer err, leaving the
// byDomain entry in place. It is how a test stages an exchange that is
// registered, trusted and reachable, and whose manifest the resolver read and
// refused.
func (f *fakeEndpointResolver) refuseDomain(domain string, err error) {
	if f.refuse == nil {
		f.refuse = make(map[string]error, 1)
	}
	f.refuse[domain] = err
}

// stopRefusing undoes refuseDomain, standing for the exchange republishing a
// manifest this reader accepts.
func (f *fakeEndpointResolver) stopRefusing(domain string) {
	delete(f.refuse, domain)
}

// startMockExchange serves the mock Exchange over loopback HTTP. See the note on
// mockExchange for the only tests that may use it.
func startMockExchange(tb testing.TB, m *mockExchange) string {
	tb.Helper()
	mux := http.NewServeMux()
	// Behind the recipient check, keyed to this Exchange's own identity — a real
	// Exchange mounts the same interceptor. Without it the Broker could stamp
	// its own domain on every leg, or the first exchange's domain on all of
	// them, and the whole suite would still pass.
	// A real Exchange serves through the canonical codec, so this stand-in does
	// too — otherwise the Broker's decode is only ever exercised against the
	// camelCase alias no Exchange puts on the wire.
	path, h := forav1connect.NewExchangeServiceHandler(m,
		connect.WithInterceptors(audiencetest.MustInterceptor(tb, m.identity())),
		connect.WithCodec(connectserver.EmitUnpopulatedJSONCodec()))
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	tb.Cleanup(srv.Close)
	return srv.URL
}

// rewritingHTTPClient maps probes for arbitrary domains onto a loopback address
// so tests can exercise the real fetch path.
type rewritingHTTPClient struct {
	targets map[string]string // domain -> base URL
}

func (c *rewritingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if base, ok := c.targets[strings.ToLower(req.URL.Host)]; ok {
		u := base + req.URL.Path
		if req.URL.RawQuery != "" {
			u += "?" + req.URL.RawQuery
		}
		newReq, err := http.NewRequestWithContext(req.Context(), req.Method, u, req.Body)
		if err != nil {
			return nil, err
		}
		newReq.Header = req.Header
		return http.DefaultTransport.RoundTrip(newReq)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// RoundTrip lets the rewriting client stand as an http.Client's Transport, for
// a collaborator that takes a whole *http.Client rather than a Do method.
func (c *rewritingHTTPClient) RoundTrip(req *http.Request) (*http.Response, error) {
	return c.Do(req)
}

// fixtureOfferVerifier is the per-fixture offer-verification seam. When the mock
// exchange is ARMED with an offer-signing key, it verifies STRICTLY via the SDK
// Verifier, resolving the exchange's key through the production
// offerkeys.Resolver, which reads the exchange's WBA directory through the SDK's
// directory fetcher, never an injected key. It is not production's wiring: it
// verifies at time.Now rather than an injected clock, and is strict only while
// armed. When unarmed it surfaces offers under mode Off so the legacy 'sig-x'
// fixtures stay green. The armed-key check reads the mock's pub at Sort time only
// to SELECT the mode (Strict vs Off); the key itself is resolved from the served
// directory, so a broken directory read drops the valid offer.
type fixtureOfferVerifier struct {
	ex       *mockExchange
	resolver *offerkeys.Resolver
}

func (v fixtureOfferVerifier) Sort(ctx context.Context, offers []*forav1.Offer) core.Result {
	v.ex.mu.Lock()
	pub := v.ex.offerSigningPub
	v.ex.mu.Unlock()
	if pub == nil {
		return core.NewVerifier(core.Off, nil, time.Now).Sort(ctx, offers)
	}
	return core.NewVerifier(core.Strict, v.resolver, time.Now).Sort(ctx, offers)
}
