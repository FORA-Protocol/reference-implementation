//go:build integration

package transport_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	wkserver "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/agentreg"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
)

// publisherOrigin serves /.well-known/fora.json for a single publisher domain.
// Contributors and 404 behavior are mutated mid-test to drive the scenarios.
type publisherOrigin struct {
	server       *httptest.Server
	mu           sync.Mutex
	contributors []string
	notFound     bool
	provider     string
	// ver overrides the document version served, nil meaning the one this build
	// stamps. A non-nil empty string serves a manifest with no ver at all, since
	// protojson omits an empty scalar.
	ver *string
}

func newPublisherOrigin(t *testing.T, provider string) *publisherOrigin {
	t.Helper()
	o := &publisherOrigin{provider: provider}
	o.server = httptest.NewServer(http.HandlerFunc(o.handle))
	t.Cleanup(o.server.Close)
	return o
}

func (o *publisherOrigin) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/.well-known/fora.json" {
		http.NotFound(w, r)
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.notFound {
		http.NotFound(w, r)
		return
	}
	ownerExt, err := structpb.NewStruct(map[string]any{"resource_owner_id": harnessResourceOwner})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	doc := &forav1.WellKnownManifest{
		Ver:    forawellknown.Version,
		Role:   forawellknown.RolePublisher,
		Domain: o.provider,
		// The schema requires exchanges[] for ROLE_PUBLISHER. The entry satisfies
		// that AND attests the resource_owner_id payee the catalog resource-owner
		// gate reads for this Exchange (harnessExchangeDomain).
		Exchanges: []*forav1.AuthorizedExchange{{
			Domain:       harnessExchangeDomain,
			Endpoint:     "https://exchange.fora.test/fora",
			Relationship: forav1.ProviderRelationship_PROVIDER_RELATIONSHIP_DIRECT,
			Ext:          ownerExt,
		}},
	}
	if o.ver != nil {
		// Overridden AFTER a correctly-stamped document rather than in the
		// literal above: the literal is where every manifest builder stamps the
		// one constant, and a fixture serving a version the consumer must refuse
		// is not an exception to that rule but a document built under it and then
		// spoiled. Same shape as the shared ExchangeManifestAtVersion fixture.
		doc.Ver = *o.ver
	}
	for _, c := range o.contributors {
		doc.CatalogContributors = append(doc.CatalogContributors, &forav1.CatalogContributor{
			Domain: c, Relationship: "publisher",
		})
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(doc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (o *publisherOrigin) setContributors(cs ...string) {
	o.mu.Lock()
	o.contributors = cs
	o.notFound = false
	o.mu.Unlock()
}

// serveVersion makes the origin publish ver as the manifest's document version,
// leaving every other member exactly as production serves it — so a refusal the
// Exchange reaches can only be the version. Pass "" to serve no ver at all.
func (o *publisherOrigin) serveVersion(ver string) {
	o.mu.Lock()
	o.ver = &ver
	o.mu.Unlock()
}

// pushAgentOrigin serves the agent's key directory for lazy-signup tests. The
// mutually-exclusive failure toggles drive the registry's read down each answer
// first-contact self-signup can give: missing → 404 (Unauthenticated: the caller
// publishes no directory), unavailable → 503 (Unavailable: retry), malformed →
// a signed body that is JSON but not a key directory (Unavailable: the read
// states it as an unavailable document), expired → a directory whose only key
// is outside its validity window (Unauthenticated: no currently valid key).
type pushAgentOrigin struct {
	server      *httptest.Server
	agentID     string
	priv        ed25519.PrivateKey
	mu          sync.Mutex
	missing     bool
	unavailable bool
	malformed   bool
	expired     bool
	// hits counts the directory requests the origin received.
	hits atomic.Int64
}

// hitCount reports how many directory requests the origin received.
func (o *pushAgentOrigin) hitCount() int64 { return o.hits.Load() }

func newPushAgentOrigin(t *testing.T, agentID string, priv ed25519.PrivateKey) *pushAgentOrigin {
	t.Helper()
	o := &pushAgentOrigin{agentID: agentID, priv: priv}
	o.server = httptest.NewServer(http.HandlerFunc(o.handle))
	t.Cleanup(o.server.Close)
	return o
}

func (o *pushAgentOrigin) handle(w http.ResponseWriter, r *http.Request) {
	// After the WBA split the exchange discovers a caller's signing key from its
	// WBA directory (not its fora.json overlay), so the fixture serves the key
	// set at the WBA path.
	if r.URL.Path != forawellknown.WBAPath {
		http.NotFound(w, r)
		return
	}
	o.hits.Add(1)
	o.mu.Lock()
	missing, unavailable, malformed, expired := o.missing, o.unavailable, o.malformed, o.expired
	o.mu.Unlock()
	switch {
	case missing:
		http.NotFound(w, r)
		return
	case unavailable:
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	case malformed:
		serveSignedDirectory(w, r, []byte(`{"keys":"x"}`), o.priv)
		return
	}
	if o.priv == nil {
		http.NotFound(w, r) // an origin built with no key publishes no directory
		return
	}
	from, until := agentKeyValidFrom(), agentKeyValidUntil()
	if expired {
		from, until = time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	}
	serveSignedDirectory(w, r, agentWBABytes(o.priv.Public().(ed25519.PublicKey), from, until), o.priv)
}

// serveSignedDirectory answers r with body as a key directory signed by privs:
// the directory media type and one response signature per key over the
// authority the request named — the agent's own host, which the harness's host
// rewriting keeps in the Host header. The signatures span 2001 to 2096, so the
// Exchange accepts them whatever clock a harness runs it on.
func serveSignedDirectory(w http.ResponseWriter, r *http.Request, body []byte, privs ...ed25519.PrivateKey) {
	signers := make([]helpers.Signer, 0, len(privs))
	for _, p := range privs {
		signers = append(signers, testutil.DirectorySigner(p))
	}
	authority, err := wkserver.RequestAuthority(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sig, err := helpers.SignDirectoryResponse(r.Context(), authority, body, signers, 1_000_000_000, 4_000_000_000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sig.Apply(w.Header())
	w.Header().Set("Content-Type", forawellknown.WBAMediaType)
	_, _ = w.Write(body)
}

// agentWBABytes renders a WBA directory (fora.v1.WBAFile, protojson/snake_case)
// carrying one Ed25519 key valid over [validFrom, validUntil) — the shape the
// exchange's WBA-directory fetch (agentreg / catalog self-signup) consumes to
// TOFU-pin a caller's signing key.
func agentWBABytes(pub ed25519.PublicKey, validFrom, validUntil time.Time) []byte {
	key := forawellknown.NewKey(pub, validFrom, validUntil)
	return testutil.MarshalWBA(testutil.WBAFile(key))
}

// agentKeyValidFrom / agentKeyValidUntil bound a fixture key around "now" so it
// is active at serve time without a fixed clock.
func agentKeyValidFrom() time.Time  { return time.Now().Add(-time.Hour) }
func agentKeyValidUntil() time.Time { return time.Now().Add(24 * time.Hour) }

// rewritingTransport rewrites outbound fetches whose Host appears in rewrite
// onto a local httptest server. Used by the forawellknown cache + agentreg so
// production wiring never sees test-only URLs.
type rewritingTransport struct {
	base    http.RoundTripper
	mu      *sync.Mutex
	rewrite map[string]string // host → target base URL
}

func (t *rewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	target, ok := t.rewrite[req.URL.Host]
	t.mu.Unlock()
	if !ok {
		return t.base.RoundTrip(req)
	}
	u, err := url.Parse(target + req.URL.Path)
	if err != nil {
		return nil, err
	}
	if req.URL.RawQuery != "" {
		u.RawQuery = req.URL.RawQuery
	}
	// The Host header keeps the host the request named, as DNS would: only the
	// dialled address moves. A key directory signs its response over the
	// authority it was fetched under, so a rewritten Host would sign for the
	// loopback address instead of the agent's own host.
	r2 := req.Clone(req.Context())
	r2.URL = u
	r2.Host = req.URL.Host
	return t.base.RoundTrip(r2)
}

// pushHarness wires the full Exchange plane with real agentreg.Registry +
// real forawellknown.Cache, both routed at httptest origins via a rewriting
// transport. This is the E2E substrate for Gate 1 + Gate 2 coverage.
type pushHarness struct {
	ctx          context.Context
	tenantID     string
	publisher    *publisherOrigin
	publisherDom string
	server       *httptest.Server
	// baseTransport is the underlying Transport that signedCat layers RFC
	// 9421 signing on top of. Sibling harnesses (e.g. agent self-signup)
	// reuse it to avoid duplicating the full bring-up.
	baseTransport http.RoundTripper
	// billing is retained for the flows that observe money: the default-credit
	// e2e reads balances through the billing adapter surface.
	billing billing.Adapter
	pool    *pgxpool.Pool
	queries *sqlc.Queries
	// keystore holds the Ed25519 offer-signing key of every publisher tenant the
	// harness seeds, under the tenant row's key ref; addPublisher seeds into it
	// the way the constructor does for the default publisher.
	keystore    *signing.InMemoryKeyStore
	rewriteMu   *sync.Mutex
	rewrite     map[string]string
	unsignedCat foraconnect.CatalogServiceClient
	signedCat   func(id string, priv ed25519.PrivateKey) foraconnect.CatalogServiceClient
	exchange    foraconnect.ExchangeServiceClient
	// discoverKeyID + discoverPriv are the keypair the harness's `exchange`
	// client signs every outbound /fora.v1.ExchangeService/* call with.
	// Sibling harnesses (agent self-signup) that rewrap the transport must
	// re-apply newSigningTransport(..., discoverKeyID, discoverPriv) so the
	// signature still clears the global httpsig gate. The pubkey is
	// registered in the test mux's static resolver at startExchangeServer
	// time via the httpsigKeys field on exchangeServerDeps.
	discoverKeyID string
	discoverPriv  ed25519.PrivateKey
	// resolver is the global-httpsig static resolver. Self-acting agent
	// tests register a fresh transport-signing pubkey here (resolver.Put) so
	// the agent's own signature clears the gate while its agents-repo row is
	// still absent — the precondition for exercising resolveCaller's ADR-009
	// D2 lazy-registration path. Multisig tests likewise register agent
	// pubkeys here dynamically.
	resolver *helpers.StaticKeyResolver
	// rsaPub is the CloudFront RSA verify key the fixture minted. Onboarding
	// tests read it here (FORA v1 no longer publishes it at a well-known route).
	rsaPub *rsa.PublicKey
	// deliveryPub is the public half of the seat's Ed25519 URL-signing key: the
	// key an edge worker verifies a delivery URL with for any site registered on
	// the ED25519 scheme. It is the seat's, not a site's — the seat holds the
	// private keys and the site only chooses which scheme is used.
	deliveryPub ed25519.PublicKey
	// logs captures the server's structured log output (JSON lines), so tests
	// can assert a rejection produced its correlated audit line.
	logs *safeBuffer
}

// selfActingExchangeClient returns an ExchangeService client that signs every
// /fora.* call with keyID/priv — the self-acting shape where the agent signs
// for itself (keyID == requester.id == agent_id). Pairs with resolver.Put so
// the signature clears the global gate.
func (h *pushHarness) selfActingExchangeClient(
	keyID string, priv ed25519.PrivateKey,
) foraconnect.ExchangeServiceClient {
	c := &http.Client{Transport: newSigningTransport(h.baseTransport, keyID, priv)}
	return foraconnect.NewExchangeServiceClient(c, h.server.URL, connect.WithGRPC())
}

func newPushHarness(t *testing.T) *pushHarness {
	t.Helper()
	return newPushHarnessWith(t, pushHarnessOptions{})
}

// pushHarnessOptions is the deployment shape newPushHarnessWith wires:
//   - trustProxyHeaders: the proxied topology (see proxy_trust_integration_test.go).
//   - billing: the adapter; nil is the FreeAdapter, which moves no money.
//   - publisherDomain: the seeded default publisher; empty is a fresh random
//     .example host, so sibling tests never collide on tenants.domain.
//   - defaultAgentCredits: the welcome credit granted at Register; nil grants none.
type pushHarnessOptions struct {
	trustProxyHeaders   bool
	billing             billing.Adapter
	publisherDomain     string
	defaultAgentCredits map[string]*big.Rat
}

// newPushHarnessWith is newPushHarness with explicit options, following the
// package's newXHarnessWith(t, opts) constructor convention.
func newPushHarnessWith(t *testing.T, opts pushHarnessOptions) *pushHarness {
	t.Helper()
	trustProxyHeaders := opts.trustProxyHeaders
	bill := opts.billing
	if bill == nil {
		bill = billing.FreeAdapter{}
	}
	fx := setupExchangeTestDB(t, "unused-agent")
	ctx, pool, queries, keystore := fx.ctx, fx.pool, fx.queries, fx.keystore
	// Capture the server's log output instead of discarding it, so tests can
	// assert rejections log their correlated audit line.
	logBuf := &safeBuffer{}
	logger := slog.New(slog.NewJSONHandler(logBuf, nil))

	publisherDomain := opts.publisherDomain
	if publisherDomain == "" {
		publisherDomain = "pub-" + uuid.NewString() + ".example"
	}
	// The seat and its offer-signing key come from the fixture; this registers
	// the site the harness's own publisher origin serves and lists it active on
	// that seat, so a push for the domain is accepted.
	tenantID, pubOfferPub, pubOfferPriv := fx.tenantID, fx.seatPub, fx.seatPriv
	repotest.SeedSite(t, ctx, pool, repotest.Site{
		SeatTenantID: tenantID,
		Domain:       publisherDomain,
		PublisherID:  harnessResourceOwner,
	})
	// Broker relay is a seller setting and sits on the seat. Every publisher the
	// harness serves is reached through this one seat, so it is enabled once
	// here rather than per site.
	if _, err := repo.NewTenantRelayPolicyRepo(queries).SetAllowBrokerRelay(ctx, tenantID, true); err != nil {
		t.Fatalf("enable broker relay on the seat: %v", err)
	}

	publisher := newPublisherOrigin(t, publisherDomain)

	rewriteMu := &sync.Mutex{}
	rewrite := map[string]string{publisherDomain: publisher.server.URL}

	rewriteClient := &http.Client{Transport: &rewritingTransport{
		base: http.DefaultTransport, mu: rewriteMu, rewrite: rewrite,
	}}

	manifests := forawellknown.NewCache(forawellknown.CacheOptions{
		Client:     rewriteClient,
		ExpectRole: forawellknown.RolePublisher,
	})
	registry := agentreg.New(agentreg.Config{
		Repo: repo.NewAgentRepo(queries),
		HTTP: rewriteClient,
	})

	offerSigner, err := signing.NewEd25519Signer(pubOfferPub, pubOfferPriv)
	if err != nil {
		t.Fatalf("offer signer: %v", err)
	}

	// Discover-side signer: the global httpsig gate applies to every
	// /fora.v1.ExchangeService/* call. Each pushHarness owns one pre-registered
	// keypair its `exchange` client signs outbound Discover/Execute/Report with.
	// It signs as the harness's configured BROKER directory, because these flows
	// sign for an agent named elsewhere in the request — the relay shape — and the
	// Exchange recognises a relay by that directory host (ExchangeConfig.BrokerHost).
	const discoverKeyID = harnessBrokerHost
	discoverPub, discoverPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("discover ed25519: %v", err)
	}

	srv := startExchangeServer(t, exchangeServerDeps{
		pool: pool, queries: queries, registry: registry, manifests: manifests,
		bill: bill, signer: offerSigner, keystore: keystore, logger: logger,
		// Per-contributor signers registered for individual Catalog tests
		// install themselves dynamically (each test generates its own
		// caller keypair and pushes via publishAgent → fora.json self-signup).
		// Those don't go through the global gate (predicate excludes
		// /fora.v1.CatalogService/), so they do NOT need to live in
		// httpsigKeys. Only the discover-side signer below does.
		httpsigKeys: map[string]ed25519.PublicKey{testutil.MustThumbprintPriv(discoverPriv): discoverPub},
		// The publisher tenant doubles as the default tenant Register reads its
		// activation policy from, so agents can billing-register before a paid
		// transaction. billingRefGen stays nil → uuid: these flows use
		// the FreeAdapter and never assert on the ref value.
		defaultTenantDomain: harnessExchangeDomain,
		defaultAgentCredits: opts.defaultAgentCredits,
		trustProxyHeaders:   trustProxyHeaders,
	})
	if err := srv.catalogSvc.Bootstrap(ctx); err != nil {
		t.Fatalf("catalog bootstrap: %v", err)
	}
	// The agents row for the discover-side signer. ExecuteTransaction no longer
	// needs it — a relay is recognised by the configured Broker directory host —
	// but ReportUsage still classifies its caller on requester_type, so this BROKER
	// row is what lets the client report usage for the agents it relays for. Every
	// publisher tenant the harness seeds carries allow_broker_relay=true.
	seedAgentAs(t, ctx, queries, discoverKeyID, discoverPub, string(sqlc.ForaRequesterTypeBROKER))

	signedFactory := func(id string, priv ed25519.PrivateKey) foraconnect.CatalogServiceClient {
		c := &http.Client{Transport: newSigningTransport(srv.baseTransport, id, priv)}
		return foraconnect.NewCatalogServiceClient(c, srv.server.URL, connect.WithGRPC())
	}

	exchangeClient := &http.Client{Transport: newSigningTransport(srv.baseTransport, discoverKeyID, discoverPriv)}

	return &pushHarness{
		ctx:           ctx,
		tenantID:      tenantID,
		publisher:     publisher,
		publisherDom:  publisherDomain,
		server:        srv.server,
		baseTransport: srv.baseTransport,
		billing:       bill,
		pool:          pool,
		queries:       queries,
		keystore:      keystore,
		rewriteMu:     rewriteMu,
		rewrite:       rewrite,
		resolver:      srv.resolver,
		unsignedCat:   foraconnect.NewCatalogServiceClient(srv.server.Client(), srv.server.URL, connect.WithGRPC()),
		signedCat:     signedFactory,
		exchange:      foraconnect.NewExchangeServiceClient(exchangeClient, srv.server.URL, connect.WithGRPC()),
		discoverKeyID: discoverKeyID,
		discoverPriv:  discoverPriv,
		rsaPub:        srv.rsaPub,
		deliveryPub:   fx.seatPub,
		logs:          logBuf,
	}
}

func (h *pushHarness) registerHost(host, target string) {
	h.rewriteMu.Lock()
	h.rewrite[host] = target
	h.rewriteMu.Unlock()
}

// registerForBilling gives an already-directory-registered agent a billing_ref
// through the public Register RPC — the precondition for a paid transaction
// against whatever billing adapter the harness was built with. The agent signs
// for itself (Register keys on the verified caller identity, and a broker is
// refused), so its key is registered with the global httpsig gate first. Used
// by the self-signup and onboarding e2e flows; callers that need the minted
// ref use registerCaller with an agent client directly.
func (h *pushHarness) registerForBilling(t *testing.T, agentID string, pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	t.Helper()
	h.resolver.Put(testutil.MustThumbprintPriv(priv), pub)
	registerCaller(t, h.ctx, h.selfActingExchangeClient(agentID, priv))
}

func (h *pushHarness) publishAgent(t *testing.T, agentID string, priv ed25519.PrivateKey) *pushAgentOrigin {
	return publishAgentOrigin(t, h.registerHost, agentID, priv)
}

func (h *pushHarness) publishPublisher(t *testing.T, domain string, contributors ...string) *publisherOrigin {
	origin := newPublisherOrigin(t, domain)
	origin.setContributors(contributors...)
	h.registerHost(domain, origin.server.URL)
	return origin
}

// hostedPublisher is a publisher the harness serves beyond its default one: the
// seat its entries are stored under, and the origin serving its manifest.
type hostedPublisher struct {
	tenantID string
	origin   *publisherOrigin
}

// addPublisher hosts a second publisher under domain: it registers the site and
// lists it active on the harness's seat, exactly as the constructor does for the
// default publisher, and serves the domain's manifest, listing contributors,
// from a fresh origin the rewriting transport routes the domain's well-known
// fetch to. The domain is taken verbatim, so a host:port such as "edge:8787" is
// served under that authority: ManifestURL keeps a port the host carries, and
// the rewrite keys on the request URL's host, which keeps it too.
func (h *pushHarness) addPublisher(t *testing.T, domain string, contributors ...string) hostedPublisher {
	t.Helper()
	tenantID := seedSiteForDomain(t, h, domain)
	return hostedPublisher{tenantID: tenantID, origin: h.publishPublisher(t, domain, contributors...)}
}

// TestPushResources_UnsignedRequestRejected proves Gate 1 rejects a push that
// lacks the request signature headers, and answers it as the SDK's verify seam
// answers every FORA RPC: with the Accept-Signature value naming what a
// signature here must cover (helpers.AcceptSignature). A refused form never
// reaches the caller's directory, so nothing is fetched to self-sign it up.
func TestPushResources_UnsignedRequestRejected(t *testing.T) {
	h := newPushHarness(t)
	h.publisher.setContributors("caller.example")
	origin := h.publishAgent(t, "caller.example", nil)

	_, err := h.unsignedCat.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, "caller.example", []*forav1.ResourceEntry{{Domain: h.publisherDom, Path: "/articles/one"}})))
	assertConnectCode(t, err, connect.CodeUnauthenticated)
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("refusal %v is not a Connect error", err)
	}
	if got, want := cerr.Meta().Get(helpers.AcceptSignatureHeader), helpers.AcceptSignature(false); got != want {
		t.Errorf("%s = %q, want %q", helpers.AcceptSignatureHeader, got, want)
	}
	if hits := origin.hitCount(); hits != 0 {
		t.Errorf("an unsigned push fetched the caller's directory %d times", hits)
	}
}

// TestPushResources_UnknownCallerDirectoryFailures drives Gate 1's first-contact
// self-signup through each answer a caller's key directory can give it. A
// directory the caller does not publish, or whose only key's window has ended,
// is the caller's to repair (Unauthenticated). A 503, or a body that is JSON
// but not a key directory, establishes nothing about the caller's keys and may
// clear on the next attempt (Unavailable). Either way no agent row is written.
func TestPushResources_UnknownCallerDirectoryFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(o *pushAgentOrigin)
		want connect.Code
	}{
		{"the caller publishes no directory", func(o *pushAgentOrigin) { o.missing = true }, connect.CodeUnauthenticated},
		{"every key's window has ended", func(o *pushAgentOrigin) { o.expired = true }, connect.CodeUnauthenticated},
		{"the directory host answers 503", func(o *pushAgentOrigin) { o.unavailable = true }, connect.CodeUnavailable},
		{"the directory is JSON but not a key directory", func(o *pushAgentOrigin) { o.malformed = true }, connect.CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPushHarness(t)
			h.publisher.setContributors("caller.example")

			_, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("gen: %v", err)
			}
			origin := h.publishAgent(t, "caller.example", priv)
			origin.mu.Lock()
			tc.set(origin)
			origin.mu.Unlock()

			client := h.signedCat("caller.example", priv)
			_, err = client.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, "caller.example",
				[]*forav1.ResourceEntry{{Domain: h.publisherDom, Path: "/articles/one"}})))
			assertConnectCode(t, err, tc.want)
			// Through the production repository, not a raw query: no public read
			// says whether a caller was registered, so this reads the repository
			// the registry writes through.
			if _, err := repo.NewAgentRepo(h.queries).ByID(h.ctx, "caller.example"); !errors.Is(err, repo.ErrAgentNotFound) {
				t.Fatalf("a row was stored for a caller whose directory could not be used: ByID = %v", err)
			}
		})
	}
}

// TestPushResources_UnknownCallerAutoRegisteredAndAdmitted proves the lazy
// self-signup path: unknown caller → registry fetches manifest → key stored →
// request re-verified and, because the caller is a listed contributor, the
// entry is accepted.
func TestPushResources_UnknownCallerAutoRegisteredAndAdmitted(t *testing.T) {
	h := newPushHarness(t)
	callerID := "caller.example"
	h.publisher.setContributors(callerID)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	h.publishAgent(t, callerID, priv)

	client := h.signedCat(callerID, priv)
	resp, err := client.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, callerID, []*forav1.ResourceEntry{{Domain: h.publisherDom, Path: "/articles/one"}})))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if resp.Msg.GetAccepted() != 1 || resp.Msg.GetRejected() != 0 {
		t.Fatalf("accepted=%d rejected=%d, want 1/0", resp.Msg.GetAccepted(), resp.Msg.GetRejected())
	}
}

// TestPushResources_ContributorAdmittedSnapshotRebuilt proves Gate 2 admits a
// listed contributor, rebuilds the snapshot automatically, and makes the
// entry visible via DiscoverResources.
func TestPushResources_ContributorAdmittedSnapshotRebuilt(t *testing.T) {
	h := newPushHarness(t)
	callerID := "caller.example"
	h.publisher.setContributors(callerID)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	h.publishAgent(t, callerID, priv)

	client := h.signedCat(callerID, priv)
	if _, err := client.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, callerID, []*forav1.ResourceEntry{{
		Domain: h.publisherDom, Path: "/articles/one",
		Terms: []*forav1.LicenseTerm{seedPricedTerm()},
	}}))); err != nil {
		t.Fatalf("push: %v", err)
	}

	offers := discoverOffersAs(t, h, "https://"+h.publisherDom+"/articles/one",
		requesterWithScopes("agent-discover"))
	if len(offers) != 1 {
		t.Fatalf("offers len = %d, want 1", len(offers))
	}
}

// TestPushResources_CallerNotInContributorsRejectedPerEntry proves Gate 2
// rejects per-entry (not per-request) when the caller is authenticated but is
// not a contributor for the entry's provider. Accepted entries coexist with
// rejected ones in the response.
func TestPushResources_CallerNotInContributorsRejectedPerEntry(t *testing.T) {
	h := newPushHarness(t)
	callerID := "caller.example"
	h.publisher.setContributors(callerID)

	deniedDomain := "denied-" + h.publisherDom
	h.publishPublisher(t, deniedDomain, "some-other.example")

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	h.publishAgent(t, callerID, priv)

	client := h.signedCat(callerID, priv)
	resp, err := client.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, callerID, []*forav1.ResourceEntry{
		{Domain: h.publisherDom, Path: "/articles/ok"},
		{Domain: deniedDomain, Path: "/articles/denied"},
	})))
	// All-or-nothing: one entry on a domain the caller is not a
	// contributor for rejects the WHOLE push (InvalidArgument); the error message
	// enumerates the offending URI + reason. No partial acceptance — neither the
	// authorized nor the denied entry persists.
	if err == nil {
		t.Fatalf("want whole-request rejection, got accepted=%d", resp.Msg.GetAccepted())
	}
	assertConnectCode(t, err, connect.CodeInvalidArgument)
	if got := discoverOfferCount(t, h, "https://"+h.publisherDom+"/articles/ok"); got != 0 {
		t.Fatalf("authorized sibling offers = %d, want 0 (denied sibling sinks the batch)", got)
	}
	if got := discoverOfferCount(t, h, "https://"+deniedDomain+"/articles/denied"); got != 0 {
		t.Fatalf("denied entry offers = %d, want 0", got)
	}
	_ = service.RejectionReasonNotInContributors // symbol-stability touchpoint
}

// NOTE: TestPushResources_PublicEntryMaterializesFreeOffer and
// TestPushResources_ScopeGatedEntryDoesNotMaterializeOffer were removed
// when the proto-rename wave W3 deleted src/exchange/internal/repo/offers.go and the
// FREE-offer materialisation feature (catalog → FREE per-request
// offer row bridge) that they exercised. The canonical ResourceEntry /
// discovery flow no longer mints a parallel offer row at PushResources
// time; obligation 04's anonymous public-resource flow is now served
// through DiscoverResources directly. Re-introducing transport-level
// coverage of that surface is tracked separately from this build-fix.
