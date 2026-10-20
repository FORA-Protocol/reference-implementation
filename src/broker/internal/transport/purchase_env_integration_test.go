//go:build integration

package transport_test

// The harness the BrokerService.ExecuteTransaction suite drives: the Broker's
// public surface built the way production builds it — the generated Connect
// handler behind BrokerMountOptions (verify seam, replay store, protovalidate,
// recipient check, body cap) inside WrapPublicSurface — with a real
// (testcontainers) Exchange registry.
//
// What sits at the Exchange's address depends on what the test is about. A test
// of what the Broker does before or instead of contacting an Exchange runs in
// front of a wireExchange (exchange_wire_fixture_test.go): a sink that fails the
// test if anything is sent, or a network condition. No Exchange is emulated.
// Everything that depends on an Exchange's answer is proven against real
// Exchanges by tests/e2e/harness/test_broker_money_path.py,
// test_multi_exchange.py and test_request_id_forwarding.py.
//
// Round-trip honesty: every leg is a protocol round-trip. The agent's call goes
// over real HTTP through the Connect client and a real RFC 9421 signature;
// assertions read the typed BrokerTransactionResponse or Connect error the
// client decodes. The registry is arranged through the production repository
// interface, the only write path it has.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	audiencetest "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience/testutil"
	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/replay"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/purchase"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/resolve"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/transport"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/xclient"
)

// purchaseAgentDirectory is the WBA directory the suite's agent signs from. Its
// host is requesterDomain, the Requester.domain every purchase fixture carries,
// because the Broker refuses a requester.domain that is not the signing
// directory's host.
const purchaseAgentDirectory = "https://" + requesterDomain

// purchaseTestEnv is one Broker in front of one registered, VERIFIED Exchange
// address.
type purchaseTestEnv struct {
	brokerURL string
	// handler is the Broker's whole HTTP surface, the one brokerURL serves. A
	// test serves a request through it directly when it needs a request context
	// that has already ended, which no client can arrange deterministically.
	handler     http.Handler
	exchangeURL string
	exchangeDom string
	brokerKID   string
	agentPub    ed25519.PublicKey
	agentPriv   ed25519.PrivateKey
	// exchange is the wireExchange at exchangeURL.
	exchange *wireExchange
	logs     *testutil.LogCapture
	// client is a BrokerService client signed as the agent, over Connect-JSON.
	client       foraconnect.BrokerServiceClient
	exchangeRepo *repo.PgxExchangeRepo
	// registryPool is the pool exchangeRepo reads through; closing it fails the
	// Broker's registry reads for real.
	registryPool *pgxpool.Pool
	// endpoints is the SAME resolver the purchase service routes through, so a
	// health refresher a test builds probes the endpoint a purchase would use.
	endpoints *resolvers.WellKnownEndpointResolver
}

// newPurchaseTestEnv builds the directly exposed Broker, with no proxy trust,
// in front of a sink: the test fails if the Broker sends the Exchange anything.
func newPurchaseTestEnv(t *testing.T) purchaseTestEnv {
	t.Helper()
	return newPurchaseTestEnvAt(t, startExchangeSink(t), false)
}

// newPurchaseTestEnvShaped is newPurchaseTestEnv with an explicit deployment
// shape: trustProxyHeaders wires the forwarded-header rewrite into
// WrapPublicSurface, the topology where a TLS-terminating proxy fronts the
// Broker.
func newPurchaseTestEnvShaped(t *testing.T, trustProxyHeaders bool) purchaseTestEnv {
	t.Helper()
	return newPurchaseTestEnvAt(t, startExchangeSink(t), trustProxyHeaders)
}

// newPurchaseTestEnvAt builds the Broker in front of the given wireExchange.
func newPurchaseTestEnvAt(t *testing.T, ex *wireExchange, trustProxyHeaders bool) purchaseTestEnv {
	t.Helper()
	env := buildPurchaseTestEnv(t, ex.url, trustProxyHeaders)
	env.exchange = ex
	return env
}

// buildPurchaseTestEnv wires the Broker and registers exchangeURL's host as a
// VERIFIED Exchange.
func buildPurchaseTestEnv(t *testing.T, exchangeURL string, trustProxyHeaders bool) purchaseTestEnv {
	t.Helper()
	agentPub, agentPriv := newBrokerKeyPair(t)
	brokerPub, brokerPriv := newBrokerKeyPair(t)
	brokerKID := rwtestutil.MustThumbprint(t, brokerPub)

	exchangeDom := strings.TrimPrefix(exchangeURL, "http://")
	exchangeRepo, registryPool := seedExchangeRegistry(t, t.Context(), exchangeDom, exchangeURL)

	// The Broker's outbound transport signs each sub-request with the Broker's
	// key alone.
	signingRT, err := xclient.NewSigningTransport(nil, &xclient.RelayKey{KeyID: brokerKID, Private: brokerPriv},
		"https://broker.test.example", 30*time.Second, clock.System{})
	if err != nil {
		t.Fatalf("broker signing transport: %v", err)
	}
	xpool := xclient.NewPool(&http.Client{Transport: signingRT})
	// The production endpoint resolver over plain HTTP: the Exchanges here are
	// loopback servers the production SSRF guard would refuse, as it should.
	endpoints := resolvers.NewWellKnownEndpointResolver(
		resolvers.WellKnownOptions{Scheme: "http", HTTP: http.DefaultClient})

	logs, logger := testutil.NewLogCapture()
	handler := transport.WrapPublicSurface(logger,
		purchaseMux(t, agentPub, exchangeRepo, endpoints, xpool),
		runhttp.PublicSurfaceOptions{TrustProxyHeaders: trustProxyHeaders})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, _ := signingBrokerJSONClient(http.DefaultTransport, server.URL, purchaseAgentDirectory, agentPriv)
	return purchaseTestEnv{
		brokerURL: server.URL, handler: handler,
		exchangeURL: exchangeURL, exchangeDom: exchangeDom, brokerKID: brokerKID,
		agentPub: agentPub, agentPriv: agentPriv,
		logs: logs, client: client,
		exchangeRepo: exchangeRepo, registryPool: registryPool, endpoints: endpoints,
	}
}

// purchaseMux mounts the BrokerService the way buildBrokerMux does, with the
// agent's key the only one the seam resolves.
func purchaseMux(
	t *testing.T, agentPub ed25519.PublicKey, exchanges *repo.PgxExchangeRepo,
	endpoints *resolvers.WellKnownEndpointResolver, xpool *xclient.Pool,
) *http.ServeMux {
	t.Helper()
	resolver := helpers.NewStaticKeyResolver(map[string]ed25519.PublicKey{
		rwtestutil.MustThumbprint(t, agentPub): agentPub,
	})
	replayStore := replay.NewCoreAdapter(replay.NewMemoryStore(time.Now))
	svrOpts, err := transport.BrokerMountOptions(
		resolver, replayStore, audiencetest.MustInterceptor(t, harnessBrokerDomain))
	if err != nil {
		t.Fatalf("broker mount options: %v", err)
	}
	resolveSvc := resolve.NewService(resolve.Deps{Exchanges: exchanges, Endpoints: endpoints, Exchange: xpool})
	path, h := connectserver.NewBrokerServiceHandler(
		transport.NewBrokerConnectHandler(resolveSvc, purchase.New(exchanges, endpoints, xpool)), svrOpts...)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	return mux
}

// addWireExchange registers ex VERIFIED and returns its domain.
func (e purchaseTestEnv) addWireExchange(t *testing.T, ex *wireExchange) string {
	t.Helper()
	e.register(t, ex.domain, ex.url, repo.TrustLevelVerified)
	return ex.domain
}

// register upserts a registry row through the production repository surface.
func (e purchaseTestEnv) register(t *testing.T, domain, endpoint, trust string) {
	t.Helper()
	if _, err := e.exchangeRepo.UpsertFromBootstrap(context.Background(), repo.Exchange{
		ID: "mp-" + domain, Domain: domain, Endpoint: endpoint, TrustLevel: trust,
		SupportedProfiles: []string{"fora-news-v1"}, Priority: 10,
	}); err != nil {
		t.Fatalf("register exchange %s: %v", domain, err)
	}
}

// line is one item of a purchase: the offer and the Exchange that issued it.
type line struct {
	offerID  string
	exchange string
}

// purchaseOffer is an offer in the shape the protocol's validation admits: one
// unpriced term under a metered price that carries its estimate, a mutability, a
// delivery method and the issuing Exchange. Its signature is a placeholder: the
// Broker never verifies an offer signature — the Exchange that issued the offer
// does.
func purchaseOffer(l line) *forav1.Offer {
	unit := "accesses"
	est := int32(1)
	price := &forav1.Pricing{
		Rate: "0.05", UnitCost: ptr("0.05"), Currency: "USD",
		Model: forav1.PricingModel_PRICING_MODEL_PER_UNIT, Unit: &unit, EstimatedQuantity: &est,
	}
	canonical := "https://publisher.example/" + l.offerID
	return &forav1.Offer{
		OfferId: l.offerID, Exchange: l.exchange, Pricing: price,
		Signature: "sig-" + l.offerID, SignatureAlgorithm: helpers.AcceptanceSignatureAlgorithm,
		DeliveryMethod: forav1.DeliveryMethod_DELIVERY_METHOD_INSTRUCTIONS,
		Identity: &forav1.ResourceIdentity{
			CanonicalUrl: &canonical, ResourceMutability: forav1.ResourceMutability_RESOURCE_MUTABILITY_STATIC,
		},
		Terms: []*forav1.LicenseTerm{{
			Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED, Scopes: []string{"ai-training"},
		}},
	}
}

// purchaseRequest is the TransactionRequest an agent sends the Broker: every
// item with the agent's acceptance of that offer, and the request acceptance
// over the complete ordered set, all signed with the agent's key over the
// requester and idempotency key.
func (e purchaseTestEnv) purchaseRequest(t *testing.T, idem string, lines ...line) *forav1.TransactionRequest {
	t.Helper()
	return e.purchaseRequestAs(t, idem, requesterDomain, lines...)
}

// purchaseRequestAs is purchaseRequest with an explicit Requester.domain; the
// acceptances are signed over that domain.
func (e purchaseTestEnv) purchaseRequestAs(
	t *testing.T, idem, domain string, lines ...line,
) *forav1.TransactionRequest {
	t.Helper()
	return e.purchaseRequestBy(t, idem, &forav1.Requester{
		Id: purchaseAgentDirectory, Domain: domain, Type: forav1.RequesterType_REQUESTER_TYPE_AGENT,
	}, lines...)
}

// purchaseRequestBy is purchaseRequest with the whole Requester chosen by the
// caller; the acceptances are signed over it.
func (e purchaseTestEnv) purchaseRequestBy(
	t *testing.T, idem string, requester *forav1.Requester, lines ...line,
) *forav1.TransactionRequest {
	t.Helper()
	req := &forav1.TransactionRequest{Ver: helpers.ProtocolVersion, IdempotencyKey: idem, Requester: requester}
	for _, l := range lines {
		offer := purchaseOffer(l)
		sig, err := helpers.SignOfferAcceptance(e.agentPriv, offer, requester, idem)
		if err != nil {
			t.Fatalf("sign acceptance of %s: %v", l.offerID, err)
		}
		req.Items = append(req.Items, &forav1.TransactionItem{
			Offer: offer,
			AgentAcceptance: &forav1.AgentAcceptance{
				Signature: sig, SignatureAlgorithm: helpers.AcceptanceSignatureAlgorithm,
			},
		})
	}
	acceptance, err := helpers.SignRequestAcceptance(e.agentPriv, req)
	if err != nil {
		t.Fatalf("sign request acceptance: %v", err)
	}
	req.AgentRequestAcceptance = acceptance
	return req
}

// buy sends req through the agent's signed client and fails the test on a
// non-OK answer.
func (e purchaseTestEnv) buy(t *testing.T, req *forav1.TransactionRequest) *forav1.BrokerTransactionResponse {
	t.Helper()
	resp, err := e.client.ExecuteTransaction(t.Context(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	return resp.Msg
}

// refused sends req and returns the Broker's own refusal, failing the test on
// an OK answer.
func (e purchaseTestEnv) refused(t *testing.T, req *forav1.TransactionRequest) *connect.Error {
	t.Helper()
	_, err := e.client.ExecuteTransaction(t.Context(), connect.NewRequest(req))
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("ExecuteTransaction answered %v, want a Connect error", err)
	}
	return ce
}

// purchaseErrorDetail returns the first FORA ErrorDetail on a Connect error,
// failing the test when there is none.
func purchaseErrorDetail(t *testing.T, ce *connect.Error) *forav1.ErrorDetail {
	t.Helper()
	for _, d := range ce.Details() {
		msg, err := d.Value()
		if err != nil {
			continue
		}
		if detail, ok := msg.(*forav1.ErrorDetail); ok {
			return detail
		}
	}
	t.Fatalf("refusal %v carries no ErrorDetail", ce)
	return nil
}

// assertNothingSent fails if any request other than the manifest or the
// liveness probe reached ex. A Broker refusal promises nothing was bought, and
// this is that promise read from the Exchange's address. A sink also fails the
// test from its handler; this states the same fact where the test makes it.
func assertNothingSent(t *testing.T, ex *wireExchange) {
	t.Helper()
	if n := ex.arrivals(); n != 0 {
		t.Errorf("%d requests reached the Exchange at %s behind a Broker refusal, want 0", n, ex.domain)
	}
}
