//go:build integration

package transport_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/offerkeys"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/budget"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/exa"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/probe"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/purchase"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/resolve"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/xclient"
)

type fixture struct {
	resolveHandler *resolve.Service
	purchase       *purchase.Service
	// exchange is the package's mock Exchange at mp.acme.example. It is set only
	// when fixtureOpts.upstream is nil, for the tests whose assertion depends on
	// an Exchange's answer and that no e2e test covers yet.
	exchange *mockExchange
	// wire is fixtureOpts.upstream: the network endpoint at mp.acme.example for a
	// test whose subject is what the Broker does before or instead of contacting
	// an Exchange. nil when the mock is wired.
	wire *wireExchange
	// exchange2 is the SECOND exchange, wired only when fixtureOpts.secondary is
	// set (the multi-publisher / cross-group selection-audit scenario). nil for
	// the single-exchange suites.
	exchange2 *mockExchange
	logger    *slog.Logger
	// logs is non-nil only when fixtureOpts.captureLogs is set. It holds every
	// record the fixture's logger emitted, including the request-scoped ones the
	// handler writes through reqctx, so a test can assert on a log line the
	// service is REQUIRED to leave behind (e.g. why an exchange was skipped).
	logs *testutil.LogCapture
	// endpoints is the SAME resolver instance the resolve handler routes
	// through. The health refresher must probe the endpoint routing will use,
	// so a test that drives both halves has to hand them one resolver; two
	// instances could disagree and the test would not notice.
	endpoints *fakeEndpointResolver
	// pool is the live pgx pool bound to the shared migrated Postgres the
	// resolve handler writes through. The selection-audit test reuses it to
	// build a SelectionLogRepo for its tier-2 read leg (same DB, same
	// connection the WRITE leg persisted through) — observing the audit row
	// through the production repository interface, never a second/raw handle.
	pool *pgxpool.Pool
}

type fixtureOpts struct {
	providerDomain string
	unlicensed     bool
	// providerVer, when non-nil, makes the publisher serve its manifest at that
	// document version instead of the one a working publisher serves. It is a
	// POINTER because the empty string is a meaningful value here — it stages a
	// manifest carrying no ver member at all, which is a distinct refusal from a
	// ver this reader does not recognise. nil serves the working version.
	providerVer *string
	// providerRole, when non-empty, makes the publisher serve a manifest
	// describing that role instead of ROLE_PUBLISHER. It stages the other
	// content-based refusal: a document that arrived and broke a rule, which is
	// as settled as a refused version and must not read as an outage.
	providerRole string
	// providerDown maps the publisher's domain to a loopback port nothing
	// listens on: the origin that did not answer, an outage rather than a verdict.
	providerDown bool
	// secondary, when non-nil, wires a SECOND publisher domain + SECOND exchange
	// into the resolve fixture so a route-per-URL resolve fans a multi-URI batch
	// across two distinct provider domains (one OfferGroup per domain). Each
	// exchange surfaces a DISTINCT offer (offerID + canonicalURL) at its own
	// unit cost, so selection.Rank can deterministically place the GLOBAL winner
	// in EITHER group. Used by the cross-group selection-audit regression test.
	secondary *secondaryProvider
	// verifier, when non-nil, OVERRIDES the default fixtureOfferVerifier wired
	// into Deps.Verifier. It lets a test inject a broker-side offer-verification
	// DECISION (the legitimate Deps.OfferVerifier seam) — e.g. a verifier that
	// rejects a specific offer — WITHOUT mocking the Exchange (Testing Doctrine
	// pt 6): the offers still come from the real Exchange/mock, only the
	// verify/reject verdict is controlled. nil keeps the default (Off-mode
	// pass-through for the legacy 'sig-x' fixtures).
	verifier resolve.OfferVerifier
	// captureLogs swaps the fixture's discard logger for a capturing one and
	// exposes it as fixture.logs. Off by default so the suite stays quiet; a
	// test turns it on when the log record itself is the behavior under test.
	captureLogs bool
	// query, when non-nil, enables the Broker's catalog search (Deps.Query).
	query resolve.QuerySearcher
	// upstream, when non-nil, is what sits at mp.acme.example's address instead
	// of the mock Exchange: a sink, or a network condition
	// (exchange_wire_fixture_test.go). A test whose subject is what the Broker
	// does before or instead of contacting an Exchange sets it, and no Exchange
	// is emulated for that test.
	upstream *wireExchange
	// relayKey, when non-nil, makes the Broker sign every request it sends an
	// Exchange with this key, through the production relay transport
	// (xclient.NewSigningTransport). nil sends them unsigned, which is all a
	// test about what the Broker routes where needs.
	relayKey *xclient.RelayKey
}

// fixtureRelayClient is the HTTP client the fixture's Broker sends Exchange
// requests through: signed with key when one is given, plain otherwise.
func fixtureRelayClient(tb testing.TB, key *xclient.RelayKey) *http.Client {
	tb.Helper()
	if key == nil {
		return &http.Client{}
	}
	rt, err := xclient.NewSigningTransport(nil, key, "https://broker.local", 30*time.Second, clock.System{})
	if err != nil {
		tb.Fatalf("broker relay signing transport: %v", err)
	}
	return &http.Client{Transport: rt}
}

// newSinkFixture is newFixture with a sink at mp.acme.example: the test fails if
// the Broker sends that Exchange anything.
func newSinkFixture(tb testing.TB, ctx context.Context, opts fixtureOpts) *fixture {
	tb.Helper()
	opts.upstream = startExchangeSink(tb)
	return newFixture(tb, ctx, opts)
}

// secondaryProvider parameterises the second publisher domain + its exchange's
// discovered offer for the multi-publisher resolve fixture. providerDomain is
// the publisher whose /.well-known/fora.json the broker probes; exchangeDomain
// is the (distinct) exchange that publisher names and the broker queries;
// offerID / offerCost / offerCanonicalURL parameterise the single offer that
// exchange returns for its routed URI.
type secondaryProvider struct {
	providerDomain    string
	exchangeDomain    string
	offerID           string
	offerCost         float64
	offerCanonicalURL string
}

func newFixture(tb testing.TB, ctx context.Context, opts fixtureOpts) *fixture {
	tb.Helper()
	logger := testutil.DiscardLogger()
	var logs *testutil.LogCapture
	if opts.captureLogs {
		logs, logger = testutil.NewLogCapture()
	}
	pool := acquireTestDB(tb, ctx)
	redisClient := acquireTestRedis(tb, ctx)

	// Seed exchange row (domain must match the manifest payload).
	exchangeRepo := repo.NewExchangeRepo(pool)
	logRepo := repo.NewSelectionLogRepo(pool)

	exSrv, exchangeURL := startFixtureExchange(tb, opts.upstream)

	// Upsert exchange row pointing at the mock Exchange.
	if _, err := exchangeRepo.UpsertFromBootstrap(ctx, repo.Exchange{
		ID:                "mp-acme",
		Domain:            "mp.acme.example",
		Endpoint:          exchangeURL,
		TrustLevel:        "VERIFIED",
		SupportedProfiles: []string{"fora-news-v1"},
		Priority:          10,
	}); err != nil {
		tb.Fatalf("upsert exchange: %v", err)
	}

	// Provider fixture hosts /.well-known/fora.json.
	var providerBase string
	switch {
	case opts.unlicensed:
		providerBase = startUnlicensedProvider(tb).URL
	case opts.providerDown:
		providerBase = refusingOrigin
	case opts.providerRole != "":
		providerBase = startProviderFixtureWith(tb, exchangeURL, "1.0", opts.providerRole).URL
	case opts.providerVer != nil:
		providerBase = startProviderFixtureAtVersion(tb, exchangeURL, *opts.providerVer).URL
	default:
		providerBase = startProviderFixture(tb, exchangeURL).URL
	}
	providerURL, err := url.Parse(providerBase)
	if err != nil {
		tb.Fatalf("parse provider url: %v", err)
	}

	// Wire probe to hit the provider via loopback.
	probeTargets := map[string]string{
		opts.providerDomain: providerBase,
		// also handle :port case from http.Request (host may include port).
		opts.providerDomain + ":" + providerURL.Port(): providerBase,
	}

	// Discovery: static client with our provider domain.
	exaCandidates := []exa.Candidate{
		{URL: "https://" + opts.providerDomain + "/x", Domain: opts.providerDomain, Score: 0.9},
	}

	endpointsByDomain := map[string]string{
		"mp.acme.example": exchangeURL,
	}

	// Multi-publisher wiring (cross-group selection-audit scenario): a SECOND
	// provider domain hosting its OWN well-known (naming a SECOND exchange) plus
	// the second exchange registered + endpoint-resolvable, and the probe client
	// taught to reach the second provider over loopback. The second exchange
	// surfaces a DISTINCT offer at its own cost so selection can place the global
	// winner in either group.
	var exchange2 *mockExchange
	if sec := opts.secondary; sec != nil {
		exchange2 = &mockExchange{
			// Its own identity, not the primary's: the second exchange is what
			// makes "each leg names the exchange it is going to" testable, and it
			// can only refuse a leg meant for its sibling if it knows who it is.
			domain:            sec.exchangeDomain,
			offerUnitCost:     sec.offerCost,
			signedURL:         "https://edge2.example/signed?tok=def",
			offerID:           sec.offerID,
			offerCanonicalURL: sec.offerCanonicalURL,
		}
		exchange2URL := startMockExchange(tb, exchange2)
		if _, err := exchangeRepo.UpsertFromBootstrap(ctx, repo.Exchange{
			ID:                "mp-" + sec.exchangeDomain,
			Domain:            sec.exchangeDomain,
			Endpoint:          exchange2URL,
			TrustLevel:        "VERIFIED",
			SupportedProfiles: []string{"fora-news-v1"},
			Priority:          10,
		}); err != nil {
			tb.Fatalf("upsert exchange2: %v", err)
		}
		provider2 := startProviderFixtureFor(tb, sec.providerDomain, sec.exchangeDomain, exchange2URL)
		provider2URL, err := url.Parse(provider2.URL)
		if err != nil {
			tb.Fatalf("parse provider2 url: %v", err)
		}
		probeTargets[sec.providerDomain] = provider2.URL
		probeTargets[sec.providerDomain+":"+provider2URL.Port()] = provider2.URL
		endpointsByDomain[sec.exchangeDomain] = exchange2URL
		exaCandidates = append(exaCandidates, exa.Candidate{
			URL: "https://" + sec.providerDomain + "/x", Domain: sec.providerDomain, Score: 0.8,
		})
	}

	probeClient := &rewritingHTTPClient{targets: probeTargets}
	prober := probe.New(probeClient, logger, probe.Options{Scheme: "http"})
	discovery := &exa.StaticClient{Candidates: exaCandidates}

	// Production offer-signing-key resolver: the SAME constructor the broker
	// wires (offerkeys.New), reading through the rewriting client as an
	// http.Client Transport. It backs the default fixtureOfferVerifier, which runs
	// in mode Off for the legacy 'sig-x' fixtures (offerSigningPub unset), so the
	// resolver is never called by these suites — verification-drop behaviour is
	// exercised instead by injecting a fixtureOpts.verifier (the Deps.OfferVerifier
	// seam), not by mock-Exchange WBA key resolution.
	offerKeyResolver := offerkeys.New(offerkeys.Config{Client: &http.Client{Transport: probeClient}})

	// Default offer-verification seam: pass-through (mode Off) for the legacy
	// fixtures. A test may OVERRIDE it via fixtureOpts.verifier to inject a
	// specific verify/reject decision without mocking the Exchange (pt 6).
	verifier := fixtureVerifier(opts.verifier, exSrv, offerKeyResolver)

	xpool := xclient.NewPool(fixtureRelayClient(tb, opts.relayKey))
	// Anchor the budget period bucket at a fixed instant so the per-agent
	// accumulation tests land every resolve in one deterministic period
	// ("2026-04") regardless of wall clock — no month-rollover flake. Redis is
	// FLUSHDB-reset per fixture, so each test starts from an empty counter.
	budgetSvc := budget.NewRedis(redisClient, 0,
		clock.NewDeterministic(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)))

	endpoints := &fakeEndpointResolver{byDomain: endpointsByDomain}
	resolver := resolve.NewService(resolve.Deps{
		Exchanges: exchangeRepo,
		Log:       logRepo,
		Discovery: discovery,
		Prober:    prober,
		Exchange:  xpool,
		Budget:    budgetSvc,
		// Discovery now resolves the exchange endpoint via the exchange's OWN
		// well-known (registry = trust allowlist only). The fake maps the registered
		// exchange domain to the mock Exchange URL, as the real resolver would from
		// the exchange's /.well-known/fora.json.
		Endpoints: endpoints,
		Verifier:  verifier,
		Query:     opts.query,
	})
	return &fixture{
		resolveHandler: resolver, purchase: purchase.New(exchangeRepo, endpoints, xpool),
		exchange: exSrv, wire: opts.upstream, exchange2: exchange2,
		logger: logger, logs: logs, endpoints: endpoints, pool: pool,
	}
}

// startFixtureExchange puts what the test chose at mp.acme.example: its
// wireExchange when it set one, otherwise the mock Exchange, returned with its
// URL. The mock surfaces an offer with EstimatedQuantity=42, so the budget
// projection unit_cost x quantity is non-trivial in every test that leaves it
// unset.
func startFixtureExchange(tb testing.TB, upstream *wireExchange) (*mockExchange, string) {
	tb.Helper()
	if upstream != nil {
		return nil, upstream.url
	}
	exSrv := &mockExchange{
		offerUnitCost:          0.10,
		signedURL:              "https://edge.example/signed?tok=abc",
		offerEstimatedQuantity: 42,
	}
	return exSrv, startMockExchange(tb, exSrv)
}

// fixtureVerifier picks the offer-verification seam: the test's own when it
// injected one, pass-through when no mock is wired (there is no armed offer to
// verify), and otherwise the mock-aware fixtureOfferVerifier.
func fixtureVerifier(
	injected resolve.OfferVerifier, exSrv *mockExchange, keys *offerkeys.Resolver,
) resolve.OfferVerifier {
	switch {
	case injected != nil:
		return injected
	case exSrv == nil:
		return core.NewVerifier(core.Off, nil, time.Now)
	default:
		return fixtureOfferVerifier{ex: exSrv, resolver: keys}
	}
}

// requesterDomain is the agent domain every DiscoveryRequest in these tests
// carries. It names where an agent's key directory would be fetched from, and
// nothing here exercises that fetch, so one value serves the whole package.
const requesterDomain = "agent.example"

// reqOpts carries the optional DiscoveryRequest fields the resolve tests vary.
type reqOpts struct {
	query string
	uri   string
	// uris is the full requested batch (batch fan-out). When set it populates
	// DiscoveryRequest.uris verbatim, so a multi-publisher resolve can drive the
	// route-per-URL path that produces one OfferGroup per requested URI. uri
	// (the single-URI convenience) and uris are mutually exclusive at call
	// sites; uris takes precedence when both are set.
	uris        []string
	budgetMinor int64
	maxHops     *int32
	// scopes, when set, is sent as requester.scopes.
	scopes []string
}

// buildDiscoveryRequest maps budgetMinor from minor to major currency units.
func buildDiscoveryRequest(agentID string, o reqOpts) *forav1.DiscoveryRequest {
	req := &forav1.DiscoveryRequest{
		Ver: helpers.ProtocolVersion,
		Requester: &forav1.Requester{
			Id: agentID,
			// A fixed, valid value, independent of agentID. The domain is a bare
			// host on the wire and an empty one is refused, so every request needs
			// one; the identity-gate tests vary the ID and would otherwise be
			// refused for the shape of a field they are not testing.
			Domain: requesterDomain,
			Type:   forav1.RequesterType_REQUESTER_TYPE_AGENT,
			Scopes: o.scopes,
		},
	}
	if o.query != "" {
		q := o.query
		req.Query = &q
	}
	switch {
	case len(o.uris) > 0:
		req.Uris = o.uris
	case o.uri != "":
		req.Uris = []string{o.uri}
	}
	if o.budgetMinor > 0 || o.maxHops != nil {
		c := &forav1.RequestConstraints{MaxHops: o.maxHops}
		if o.budgetMinor > 0 {
			// budgetMinor is the test's notion of the cap in cents; the wire
			// PeriodBudget.amount is the canonical decimal STRING in major units
			// (money-as-string). The production handler scales it to 1e8
			// fixed-point internally; here we only render the major-unit string.
			c.PeriodBudget = &forav1.Cost{Amount: mustMoney(float64(o.budgetMinor) / 100), Currency: "USD"}
		}
		req.Constraints = c
	}
	return req
}

// resolveCaller is the agent every resolveOverConnect test resolves as; none of
// those tests is about who the caller is.
const resolveCaller = "agent-1"

// resolveOverConnect spins up the real Connect+RFC9421 harness for a SIGNED
// caller acting on its own behalf (keyID == requester.id), issues the Resolve
// through the generated client, and returns the server fixture + decoded
// response. Every leg is a genuine protocol round-trip: signing client → RFC
// 9421 signature → httpsig middleware → registered Connect handler → shared
// resolve core → toDiscoveryResponse → typed *forav1.DiscoveryResponse. The 200-OK
// happy/refusal tests share this setup to avoid duplicated harness wiring.
func resolveOverConnect(t *testing.T, fx *fixture, o reqOpts) (*brokerConnectFixture, *forav1.DiscoveryResponse) {
	t.Helper()
	pub, priv := newBrokerKeyPair(t)
	srv := startBrokerConnectServer(t, fx, pub)
	client := signingBrokerClient(srv.base, srv.server.URL, resolveCaller, priv)
	resp, err := client.Resolve(context.Background(), connect.NewRequest(buildDiscoveryRequest(resolveCaller, o)))
	if err != nil {
		t.Fatalf("Resolve returned transport error: %v", err)
	}
	return srv, resp.Msg
}

// TestResolve_RefusalKeepsZeroValuedFieldsOnJSONWire pins the wire-codec half of
// production parity: the harness must emit the SAME JSON shape production does.
// Production wires connectserver.WithEmitUnpopulated so zero-valued response
// fields stay on the JSON wire; a refusal DiscoveryResponse has an EMPTY
// offer_groups, which protojson OMITS by default and RENDERS as "offer_groups":[]
// only under EmitUnpopulated. Driven over the Connect-JSON protocol (the wire the
// codec governs — the gRPC client the other tests use would never surface this),
// so if the harness dropped WithEmitUnpopulated the field would vanish and this
// test would fail. That absent assertion is exactly what let the harness silently
// exercise a different response shape than production.
func TestResolve_RefusalKeepsZeroValuedFieldsOnJSONWire(t *testing.T) {
	ctx := context.Background()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "bare.example", unlicensed: true})

	pub, priv := newBrokerKeyPair(t)
	srv := startBrokerConnectServer(t, fx, pub)
	client, capture := signingBrokerJSONClient(srv.base, srv.server.URL, "agent-1", priv)

	// Refusal path: 200-OK DiscoveryResponse carrying only ver + absence_reason,
	// with empty offer_groups.
	resp, err := client.Resolve(ctx, connect.NewRequest(buildDiscoveryRequest("agent-1", reqOpts{uri: "https://bare.example/item"})))
	if err != nil {
		t.Fatalf("Resolve returned transport error: %v", err)
	}
	if got := resp.Msg.GetAbsenceReason(); got != forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG {
		t.Fatalf("absence_reason = %v, want NOT_IN_CATALOG (refusal path)", got)
	}
	// The codec decides two things at once, and each assertion below reads one.
	//
	// AssertWireCarries reads PRESENCE: the empty offer_groups survives on the
	// JSON wire as an explicit [], which is what EmitUnpopulated does and what
	// default protojson omits entirely. It resolves the name against the
	// descriptor first, so renaming the field in the contract fails as a test
	// naming a field that does not exist rather than as a codec problem.
	//
	// AssertCanonicalWireNames reads the field NAMES, and on this path the field
	// that carries it is absence_reason — not offer_groups. Measured both ways
	// against this mount: with the codec the body is
	//
	//	{"ver":"1.0","offer_groups":[],"absence_reason":"...","ext":null,"ext_critical":[]}
	//
	// and with the stock codec it is {"ver":"1.0","absenceReason":"..."}. A refusal
	// leaves offer_groups empty, so losing the codec drops it from the wire
	// entirely and it has no spelling left to report — which is exactly why the
	// presence check above is the one that catches that drift. absence_reason is
	// declared with explicit presence and set on this path, so it survives under
	// either codec and is what the walk actually reads. The narrower drift it
	// catches is a codec that still emits unpopulated fields but no longer pins
	// UseProtoNames.
	//
	// Both report rather than stop, so a body that is wrong in both ways says so
	// in both ways instead of hiding the second finding behind the first.
	body := capture.Body(t)
	testutil.AssertWireCarries(t, body, resp.Msg, "offer_groups")
	testutil.AssertCanonicalWireNames(t, body, resp.Msg)
	assertNothingSent(t, fx.wire)
}

// TestResolve_NoVerifiedCaller_Unauthenticated pins the identity derivation's
// guard: an UNSIGNED Resolve (no httpsig context) is rejected with Unauthenticated and
// no upstream side effects fire. brokerConnectSigPredicate lets the unsigned
// /fora. request through (no Signature-Input), the handler finds no verified
// context, and returns Unauthenticated.
func TestResolve_NoVerifiedCaller_Unauthenticated(t *testing.T) {
	ctx := context.Background()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	const callerID = "agent-1"
	pub, _ := newBrokerKeyPair(t)
	srv := startBrokerConnectServer(t, fx, pub)

	// PLAIN unsigned client — NOT the signing transport.
	client := foraconnect.NewBrokerServiceClient(srv.server.Client(), srv.server.URL, connect.WithGRPC())

	_, err := client.Resolve(ctx, connect.NewRequest(buildDiscoveryRequest(callerID, reqOpts{query: "FORA intro"})))
	assertConnectCodeLocal(t, err, connect.CodeUnauthenticated)
	assertNothingSent(t, fx.wire)
}

// TestResolve_MaxHopsIsNotEnforced pins that the Broker refuses no Resolve for
// its RequestConstraints.max_hops. At the pinned protocol the field rides only on
// the DiscoveryRequest, which ends at the Broker: the Broker originates its own
// queries rather than forwarding the agent's, so no party receives or enforces
// it. A Resolve stating max_hops 0, which the Broker once refused as leaving no
// room for its own relay hop, is answered like any other: the fan-out reaches the
// Exchange and its offers come back. Bringing any max_hops refusal back into
// validateResolveRequest fails this test.
func TestResolve_MaxHopsIsNotEnforced(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	zero := int32(0)
	_, out := resolveOverConnect(t, fx, reqOpts{
		query:       "FORA intro",
		budgetMinor: 10000,
		maxHops:     &zero,
	})
	if len(out.GetOfferGroups()) == 0 {
		t.Fatalf("a Resolve stating max_hops 0 was answered with no offers (absence_reason %v); "+
			"no party enforces max_hops", out.GetAbsenceReason())
	}
}

// brokerExtPrefix is the namespace under which the Broker used to smuggle
// non-canonical signals on DiscoveryResponse.ext. ADR-019: the Broker's
// agent-facing response is PURELY the canonical typed DiscoveryResponse — the
// licensed signal is retrieval_endpoint presence, the refusal cause is the typed
// absence_reason, faults are Connect errors carrying a typed ErrorDetail, and
// everything else (offer_id, budget, evaluated candidates) is broker
// selection/billing/audit detail that has no canonical field and is NOT echoed
// to the agent (it lives in the broker's SelectionLog / budget service). So NO
// fora.broker.* key may appear under ext.
const brokerExtPrefix = "fora.broker."

// assertNoBrokerExt fails if out carries ANY fora.broker.* ext key. This is the
// class-level guard: the Broker emits zero ext smuggle keys, current or future.
func assertNoBrokerExt(t *testing.T, out *forav1.DiscoveryResponse) {
	t.Helper()
	for k := range out.GetExt().GetFields() {
		if strings.HasPrefix(k, brokerExtPrefix) {
			t.Errorf("broker ext key %q must NOT be emitted — the agent-facing response is pure canonical DiscoveryResponse (use the typed surface / SelectionLog): ext=%v",
				k, out.GetExt().GetFields())
		}
	}
}

// TestResolve_NoContractualExtKeys_LicensedFlow pins the ADR-019
// cure on the LICENSED-DISCOVERY path: a successful DiscoveryResponse carries NONE of
// fora.broker.{licensed,error,absence_reason} — the licensed signal is derived
// from NON-EMPTY offer_groups presence, not from an ext flag or a
// retrieval_endpoint. Every leg is a genuine protocol round-trip (signed Connect
// client → httpsig → handler → resolve core → toDiscoveryResponse → typed
// DiscoveryResponse).
//
// Needs an e2e test: no fora.broker.* ext key on a licensed Resolve answer built from a real Exchange's offers.
func TestResolve_NoContractualExtKeys_LicensedFlow(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	_, out := resolveOverConnect(t, fx, reqOpts{
		query:       "FORA intro",
		budgetMinor: 10000,
	})
	if len(out.GetOfferGroups()) == 0 {
		t.Fatal("expected non-empty offer_groups (licensed-discovery flow) — wrong fixture state")
	}
	assertNoBrokerExt(t, out)
}

// TestResolve_NoContractualExtKeys_RefusalFlow pins the same cure on a REFUSAL
// path: a no-fora.json refusal carries the typed absence_reason ONLY — none of
// the contractual ext keys (fora.broker.absence_reason in particular must be
// gone, replaced by the typed DiscoveryResponse.absence_reason field). FAILS on HEAD
// (brokerExt sets fora.broker.absence_reason), PASSES after the keys are
// dropped.
func TestResolve_NoContractualExtKeys_RefusalFlow(t *testing.T) {
	ctx := context.Background()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "bare.example", unlicensed: true})

	_, out := resolveOverConnect(t, fx, reqOpts{uri: "https://bare.example/item"})
	// The typed refusal-cause surface stays — it is the sole refusal channel now.
	if out.GetAbsenceReason() != forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG {
		t.Errorf("typed absence_reason = %v, want NOT_IN_CATALOG", out.GetAbsenceReason())
	}
	assertNoBrokerExt(t, out)
	assertNothingSent(t, fx.wire)
}

// TestResolve_FaultCarriesErrorDetail pins the ADR-019 fault
// contract: a true fault (here an UNSIGNED Resolve → Unauthenticated, the same
// fault path TestResolve_NoVerifiedCaller drives) returns a Connect transport
// error that BOTH has the expected code AND carries a typed *forav1.ErrorDetail
// with Domain "fora.v1.BrokerService". On HEAD the broker fault path is a bare
// broker.ToConnect(err) with NO detail attached (broker_connect_handler.go:41-49),
// so the wire error has zero details → assertBrokerErrorDetail fails. PASSES
// after the fault path attaches connect.NewErrorDetail(&forav1.ErrorDetail{
// Message, Domain:"fora.v1.BrokerService"}). The detail-decoding helper mirrors
// the exchange suite's assertDenialReason.
func TestResolve_FaultCarriesErrorDetail(t *testing.T) {
	ctx := context.Background()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	const callerID = "agent-1"
	pub, _ := newBrokerKeyPair(t)
	srv := startBrokerConnectServer(t, fx, pub)

	// PLAIN unsigned client — no httpsig context → Unauthenticated fault.
	client := foraconnect.NewBrokerServiceClient(srv.server.Client(), srv.server.URL, connect.WithGRPC())

	_, err := client.Resolve(ctx, connect.NewRequest(buildDiscoveryRequest(callerID, reqOpts{query: "FORA intro"})))
	assertConnectCodeLocal(t, err, connect.CodeUnauthenticated)
	assertBrokerErrorDetail(t, err, "fora.v1.BrokerService")
	assertNothingSent(t, fx.wire)
}
