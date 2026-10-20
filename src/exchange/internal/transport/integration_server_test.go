//go:build integration

package transport_test

import (
	"cmp"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/agentreg"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/regschema"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sor"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// ledgerCurrenciesOrDefault is the configured currency set, or the
// single-currency default the whole suite runs on.
func (d exchangeServerDeps) ledgerCurrenciesOrDefault() []string {
	if len(d.ledgerCurrencies) > 0 {
		return d.ledgerCurrencies
	}
	return []string{harnessCurrency}
}

// journalQueries is what the denial journal writes through: the harness's own
// queries, or journalPool's when a test names one.
func (d exchangeServerDeps) journalQueries() sqlc.Querier {
	if d.journalPool == nil {
		return d.queries
	}
	return sqlc.New(d.journalPool)
}

// harnessCurrency is the one currency MOST harnesses in this suite bill in. The
// in-memory tier keeps billing.DemoCurrency, and the TigerBeetle e2e harness
// runs on ledger 840, which is that same currency — so one constant covers the
// adapter wiring, the offers the harnesses price, and every balance read.
const harnessCurrency = billing.DemoCurrency

// This file holds the Exchange HTTP server fixture: the httptest server wired
// with the CatalogService and ExchangeService mounts, and the Connect-JSON
// twins built over the same signed transport. The test harness that stands a
// database and a tenant up around it is in integration_helper_test.go, which
// this file was split out of when the two together crossed the test-file cap.

// harnessBrokerHost is the Broker directory host every harness configures as
// ExchangeConfig.BrokerHost unless it names its own. A single-signature request
// whose Signature-Agent is this host is a relayed ExecuteTransaction: the agent is
// then read from Requester.domain, not from the header.
//
// It is a constant rather than a per-test literal for the same reason
// harnessExchangeDomain is: the service compares the caller's directory against
// this exact value, so a test that spells it differently is exercising the direct
// path while believing it exercises the relay.
const harnessBrokerHost = "broker.fora.test"

// exchangeServerFixture bundles an httptest server wired with the Exchange
// Connect-Go handlers for CatalogService + ExchangeService, along with the
// baseTransport callers use to build signing HTTP clients on top.
type exchangeServerFixture struct {
	server        *httptest.Server
	baseTransport http.RoundTripper
	exchange      *service.ExchangeService
	catalogSvc    *service.CatalogService
	// resolver exposed so multi-agent tests (cross-tenant, broker-relay)
	// can register additional keyIDs dynamically via resolver.Put after
	// server start.
	resolver *helpers.StaticKeyResolver
	// rsaPub / rsaKid carry the CloudFront RSA verify key. FORA v1 no longer
	// publishes it at a well-known route, so onboarding tests read it from the
	// fixture to verify CloudFront-signed URLs.
	rsaPub *rsa.PublicKey
	rsaKid string
}

// exchangeServerDeps names the pieces startExchangeServer needs; grouping them
// here keeps the call site readable and stops the argument list from growing
// past the per-file function-arg lint budget.
type exchangeServerDeps struct {
	pool      *pgxpool.Pool
	queries   sqlc.Querier
	registry  agentreg.Registry
	manifests service.ManifestCache
	bill      billing.Adapter
	signer    *signing.Ed25519Signer
	keystore  *signing.InMemoryKeyStore
	logger    *slog.Logger
	// httpsigKeys registers caller pubkeys with the connectserver verify
	// middleware that wraps the mux (mirror of production wiring in
	// cmd/server/main.go::buildWrapped). Tests that issue any /fora.* RPC
	// MUST register the signer's keyID + pubkey here; the global gate
	// will reject the request with helpers.ErrUnknownKey otherwise.
	httpsigKeys map[string]ed25519.PublicKey
	// clk is optional. nil → clock.System{}. Pass a DeterministicClock to
	// control time in tests that exercise window-expiry logic.
	clk clock.Clock
	// txRunner overrides the service's transaction runner. nil →
	// PoolRunner{Pool}. A failing runner drives the persist-failure hot path
	txRunner sharedb.TxRunner
	// keystoreOverride replaces the KeyStore the ExchangeService consults
	// when minting signed URLs. nil → deps.keystore. A failing keystore drives
	// the URL-sign-failure hot path. The well-known RSA wiring
	// still uses deps.keystore.
	keystoreOverride signing.KeyStore
	// maxSignatures bounds the inbound signature (hop) chain depth, mirroring
	// the production ceiling (cmd/server/keyresolver.go: max_intermediary_hops).
	// 0 → unbounded — the default for tests that do not exercise the hop bound.
	maxSignatures int
	// brokerHost is the Broker directory host the service recognises relays from
	// (ExchangeConfig.BrokerHost, derived in production from
	// EXCHANGE_BROKER_WELLKNOWN_URL). Empty → harnessBrokerHost, so every harness
	// has one configured Broker and a relay test needs no extra wiring.
	brokerHost string
	// sor is the account System of Record the Register flow depends on. nil → a
	// fresh in-memory SoR so every harness constructs a valid service even when it
	// never calls Register (production wiring is ST-5's job; the harness needs the
	// dep now). Register tests inject their own to assert account state.
	sor sor.Adapter
	// defaultTenantDomain names the single tenant Register reads its
	// activate_new_agents_by_default policy from (ADR-021 §5 decision 1). Empty for
	// harnesses that do not exercise Register.
	defaultTenantDomain string
	// ledgerCurrencies are the currencies this Exchange bills in, the value
	// cmd/server threads from the selected billing backend. nil → the
	// single-currency default every harness in this package runs on. A
	// multi-currency test sets it together with a billing adapter configured for
	// the same set: the two disagreeing would make every grant fail the billing
	// gate, which is a wiring mistake, not a case worth covering.
	ledgerCurrencies []string
	// defaultAgentCredits is the welcome credit Register grants, by currency —
	// the map cmd/server parses from EXCHANGE_DEFAULT_AGENT_CREDIT and hands the
	// service as configuration. nil → no grant.
	defaultAgentCredits map[string]*big.Rat
	// billingRefGen overrides the billing_ref generator; nil → uuid.NewString
	// inside NewExchangeService. Register tests inject a deterministic counter.
	billingRefGen service.BillingRefGen
	// regSchema is the operator-configured registration schema the Register gate
	// enforces. nil → no schema published, so registration_data passes through
	// uninspected — the shape every harness that does not exercise the gate uses.
	regSchema *regschema.Schema
	// termsDigest is the terms digest this Exchange publishes. Empty → no terms
	// versioning, so a submitted digest is ignored and none is recorded.
	termsDigest string
	// trustProxyHeaders mirrors FORA_TRUST_PROXY_HEADERS: wire the
	// forwarded-header rewrite into WrapPublicSurface, the proxied deployment
	// shape (client signs https, service socket sees plain HTTP).
	trustProxyHeaders bool
	// auditWrap decorates the pool-bound audit repo instead of replacing it, so
	// a test repo can delegate to real appends and start failing only when it is
	// armed. Same seam shape as txRunner above; nil leaves the real repo in
	// place. An audit append that fails INSIDE the registration transaction is
	// the only injection point that tells one transaction apart from two
	// sequential ones.
	auditWrap func(repo.AuditRepo) repo.AuditRepo
	// journalPool, when set, is the pool the denial journal writes through
	// instead of the harness pool. Only the connection changes: the journal
	// still writes through the real repository, so a closed pool makes every
	// journal write fail in the driver.
	journalPool *pgxpool.Pool
	// lifetimes shortens a tenant's offer and delivery-URL lifetimes; nil is
	// production's none.
	lifetimes service.Lifetimes
}

// startExchangeServer serves the Exchange's public trading surface, as
// exchangetest.MountTrading composes it, behind the production public-surface
// wrapper, and returns the fixture. It mirrors cmd/server/main.go::buildWrapped
// so the integration tests exercise the same middleware chain as production
// (per ADR-008 D1). Admin-plane routes are deliberately absent; the
// admin_removed_e2e_test asserts that.
//
// Callers still own DB bring-up, tenant seeding, and any post-wiring
// bootstrap (e.g. catalog.Bootstrap). Callers MUST also register every
// signer's keyID + pubkey via deps.httpsigKeys; the global gate rejects
// unknown keyids with helpers.ErrUnknownKey.
func startExchangeServer(t *testing.T, deps exchangeServerDeps) *exchangeServerFixture {
	t.Helper()
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	// The keystore entry under this ref lets tests whose site uses
	// AWS_CLOUDFRONT_RSA resolve the RSA key when minting CloudFront URLs. It is
	// the ref the seat row carries, which is why both name the same constant.
	// FORA v1 no longer publishes it at a well-known route (CloudFront verifies
	// natively against a trusted key group); tests read fixture.rsaPub instead.
	deps.keystore.PutRSA(cloudFrontTestKeyRef, rsaPriv)

	mux := http.NewServeMux()
	trading := exchangetest.MountTrading(t, mux, deps.trading())

	// trustProxyHeaders wires the same forwarded-header rewrite production
	// wires under FORA_TRUST_PROXY_HEADERS.
	server := httptest.NewServer(transport.WrapPublicSurface(deps.logger, mux,
		runhttp.PublicSurfaceOptions{TrustProxyHeaders: deps.trustProxyHeaders}))
	t.Cleanup(server.Close)

	base := server.Client().Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &exchangeServerFixture{
		server:        server,
		baseTransport: base,
		exchange:      trading.Exchange,
		catalogSvc:    trading.Catalog,
		resolver:      trading.Resolver,
		rsaPub:        &rsaPriv.PublicKey,
		rsaKid:        cloudFrontTestKeyRef,
	}
}

// trading is these deps as exchangetest composes the surface from them: the
// harness's own Exchange domain and default Broker, and the service keystore
// the override replaces when a test sets one.
func (d exchangeServerDeps) trading() exchangetest.TradingDeps {
	var keys signing.KeyStore = d.keystore
	if d.keystoreOverride != nil {
		keys = d.keystoreOverride
	}
	return exchangetest.TradingDeps{
		Pool:                d.pool,
		Queries:             d.queries,
		Registry:            d.registry,
		Manifests:           d.manifests,
		Billing:             d.bill,
		Signer:              d.signer,
		KeyStore:            keys,
		JournalQueries:      d.journalQueries(),
		ExchangeDomain:      harnessExchangeDomain,
		CallerKeys:          d.httpsigKeys,
		Clock:               d.clk,
		TxRunner:            d.txRunner,
		Audit:               d.auditWrap,
		MaxSignatures:       d.maxSignatures,
		BrokerHost:          cmp.Or(d.brokerHost, harnessBrokerHost),
		SoR:                 d.sor,
		DefaultTenantDomain: d.defaultTenantDomain,
		LedgerCurrencies:    d.ledgerCurrenciesOrDefault(),
		DefaultAgentCredits: d.defaultAgentCredits,
		BillingRefGen:       d.billingRefGen,
		Lifetimes:           d.lifetimes,
		RegSchema:           d.regSchema,
		TermsDigest:         d.termsDigest,
	}
}

// jsonWire is the pair of Connect-JSON clients and the capture belonging to
// EACH. One capture shared between them would hold only the last response, so a
// test asserting on the wire would be reading whichever call happened to run
// last — correct or not depending on the order its own statements appear in.
type jsonWire struct {
	exchange        foraconnect.ExchangeServiceClient
	exchangeCapture *testutil.CaptureBody
	catalog         foraconnect.CatalogServiceClient
	catalogCapture  *testutil.CaptureBody
}

// jsonWireClients builds Connect-JSON twins of the harness's clients over the
// SAME signed transport, each with its own capture of the raw response bytes.
//
// The harness's own clients speak gRPC, where proto framing carries every field
// and the response codec is unobservable. JSON is the wire the codec governs, so
// a test that asserts what the codec actually produced has to drive the call
// this way — which is why the codec's field-naming half went unchecked on these
// two mounts until now.
func (h *testHarness) jsonWireClients() jsonWire {
	newCapture := func() *testutil.CaptureBody {
		return &testutil.CaptureBody{
			Base: newSigningTransport(h.baseTransport, h.callerID, h.callerPriv),
		}
	}
	exCapture, catCapture := newCapture(), newCapture()
	return jsonWire{
		exchange: foraconnect.NewExchangeServiceClient(
			&http.Client{Transport: exCapture}, h.server.URL, connect.WithProtoJSON(),
		),
		exchangeCapture: exCapture,
		catalog: foraconnect.NewCatalogServiceClient(
			&http.Client{Transport: catCapture}, h.server.URL, connect.WithProtoJSON(),
		),
		catalogCapture: catCapture,
	}
}
