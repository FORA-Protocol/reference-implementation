//go:build integration

package mcp_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/types/known/timestamppb"

	audiencetest "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraauth"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	wkserver "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
)

// The validity window every fixture peer publishes its offer-signing key over.
// Wide enough that no suite run can fall outside it, which is the only property
// these tests need from it — key-window handling has its own coverage.
var (
	wbaNotBefore = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	wbaNotAfter  = time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)
)

// foraPeer is a stand-in Broker or Exchange that gates every RPC behind the
// protocol SDK's request verifier (helpers.VerifyRequestResolved) — the verify
// face the real Exchange's gate runs. It is served over https, as every FORA
// peer and every key directory is.
//
// That is the point of these tests. A double that merely accepted the call would
// prove the tool assembled a request; running the real verifier proves the request
// was signed with the calling agent's own custodied key, over the covered
// components the protocol requires, inside a live freshness window. An unsigned or
// wrongly-signed call is rejected here exactly as it would be in production.
//
// What this does NOT traverse, named honestly: the key the verifier checks against
// is seeded from custody (see fixture.trust), not fetched from the agent's hosted
// WBA directory over HTTP. So a passing test is a CUSTODY round-trip — the
// signature really came from the agent's stored key — not a DIRECTORY round-trip.
// The directory↔custody link has its own coverage in the publisher suite.
type foraPeer struct {
	// The generated Unimplemented handlers supply the RPCs this adapter never
	// calls, so an accidental call to one fails loudly as unimplemented rather
	// than silently succeeding against a stub.
	forav1connect.UnimplementedExchangeServiceHandler

	srv *httptest.Server

	mu    sync.Mutex
	calls []peerCall

	// Programmable responses. A nil error means the canned response is returned.
	registerResp *forav1.RegisterResponse
	statusResp   *forav1.GetAccountStatusResponse
	resolveResp  *forav1.DiscoveryResponse
	reportResp   *forav1.UsageReportResponse
	err          error

	// BrokerService.ExecuteTransaction. executeResp is the combined answer the
	// Broker returns; executeErr, when set, is the Broker's own refusal instead,
	// ErrorDetail and all.
	executeResp *forav1.BrokerTransactionResponse
	executeErr  *connect.Error

	// Last request per RPC, so a test can assert what the tool actually put on
	// the wire — not merely that something arrived.
	lastRegistration map[string]any
	lastDiscovery    *forav1.DiscoveryRequest
	lastReport       *forav1.UsageReport
	lastExecute      *forav1.TransactionRequest

	// registration, when set, is the account_registration block and terms pair
	// this peer publishes. Zero means an Exchange that asks for nothing in
	// particular, which is the pass-through state every Exchange was in before
	// the block existed and is still the majority case.
	registration rwtestutil.Registration

	// manifestEndpoint, when set, overrides the endpoint this peer advertises in
	// its well-known manifest (default: its own origin). A test points it at a
	// different host to drive the host-anchoring refusal in ReportUsage.
	manifestEndpoint string
	// manifestFailStatus makes the well-known handler answer this status instead
	// of the document. Zero serves it.
	manifestFailStatus int
	// manifestVer, when set, is the document version this peer stamps on its
	// well-known manifest in place of the one the fixture would stamp. It stages
	// the one refusal that is a VERDICT about a document that arrived intact: an
	// Exchange publishing a manifest at a major this build does not implement.
	// Empty serves the fixture's own version.
	manifestVer string

	// lastTermsDigest is the terms_digest the most recent Register echoed, kept
	// as a pointer so an ABSENT digest stays distinguishable from a published
	// empty one — the protocol treats those as different cases.
	lastTermsDigest *string

	// manifestFetches counts reads of this peer's well-known document. The
	// manifest is served OUTSIDE the signature gate, so those reads never appear
	// in calls; a test that needs to prove a refusal landed BEFORE the fetch — the
	// SSRF property, where the fetch itself is the thing being prevented — has to
	// count them separately.
	manifestFetches int

	// httpRequests counts EVERY request that reached this peer's server, on any
	// path and whatever the response. The counters above are per-document and
	// per-path, so neither can answer "was this host dialled at all" — a request
	// to a path the mux does not serve increments no document counter and still
	// arrived. A test proving a refusal landed before anything left the process
	// needs the broad count, because a refusal that failed would send a request
	// to exactly such a path.
	httpRequests int

	// offerPriv signs the offers this peer issues, and its public half is what the
	// peer publishes in its own Web Bot Auth directory.
	//
	// The peer needs a real one because discovery verifies: every offer that
	// reaches an agent is checked against the key the issuing exchange publishes,
	// so an offer signed with a made-up value is REJECTED rather than returned. A
	// fixture that hands out unsigned offers can no longer describe a working
	// discovery at all — which is the point, and is why the key lives on the peer
	// that issues the offers rather than in a helper beside them.
	offerPriv ed25519.PrivateKey
	offerPub  ed25519.PublicKey
}

// peerCall records one RPC that made it past the signature gate.
type peerCall struct {
	Path string
	// KeyID is the RFC 9421 keyid the middleware verified — the RFC 7638
	// thumbprint of the key that signed. Asserting on it is how a test proves the
	// call went out as one agent rather than another.
	KeyID string
	// SignatureAgent is the signed directory origin the signer claims, i.e. the
	// agent's own subdomain.
	SignatureAgent string
	// Authorization is whatever arrived in the outbound Authorization header —
	// which must always be EMPTY. The package's central claim is that the inbound
	// bearer never travels outbound, and the peers are third parties (for
	// fora_report, one an offer named), so a bearer reaching them is credential
	// disclosure. Recorded on every call so assertNoBearerLeaked can check the
	// property everywhere rather than in one test that has to remember to.
	Authorization string
	// RequestID is the X-Request-ID the call arrived with, so a test can check
	// that an outbound leg travels under the id of the tool call that caused it.
	RequestID string
}

// endpointPathPrefix is the path an Exchange can legitimately advertise in front
// of its RPC routes. The peer serves its ExchangeService at both the bare path
// and behind this one, so a test can stage the endpoint shape that took the
// account calls out of the signed set.
const endpointPathPrefix = "/api"

// newFORAPeer starts a peer serving both FORA services behind the signature gate.
// Serving both on one origin keeps the fixture small; a tool only ever calls the
// service it is wired to, and the recorded path says which was used.
func newFORAPeer(t *testing.T, resolver helpers.KeyResolver) *foraPeer {
	t.Helper()
	offerPub, offerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate peer offer key: %v", err)
	}
	p := &foraPeer{offerPriv: offerPriv, offerPub: offerPub}

	// Started later than it is built, because the peer's own published identity
	// is the address it ends up listening on and the recipient check needs that
	// value before the first request arrives.
	srv := httptest.NewUnstartedServer(nil)
	self := srv.Listener.Addr().String()

	mux := http.NewServeMux()
	// The peer refuses a request addressed to somebody else, exactly as a real
	// Exchange does. Without it the double is weaker than the peer it stands for:
	// every account call could leave with a missing or wrong recipient and the
	// suite would pass while production refused all of them. Mounted on the
	// ExchangeService alone: a TransactionRequest sent to the Broker names the
	// Exchanges it routes to, not the Broker, so the Broker does not check it.
	// Both doubles serve through the canonical codec, because both stand in for a
	// peer that does. The MCP adapter decodes these replies; feeding it the
	// camelCase alias would test a wire form no FORA service produces.
	exchangePath, exchangeHandler := forav1connect.NewExchangeServiceHandler(p,
		connect.WithInterceptors(audiencetest.MustInterceptor(t, self)),
		connect.WithCodec(connectserver.EmitUnpopulatedJSONCodec()))
	brokerPath, brokerHandler := forav1connect.NewBrokerServiceHandler(brokerFace{p},
		connect.WithCodec(connectserver.EmitUnpopulatedJSONCodec()))
	mux.Handle(exchangePath, exchangeHandler)
	mux.Handle(brokerPath, brokerHandler)
	// The SAME ExchangeService, also served behind a path prefix. An endpoint is
	// constrained on host, port and userinfo and says nothing about a path, so an
	// Exchange advertising ".../api" is conformant and a peer that could only be
	// reached at a bare origin could not stand in for one. Mounted
	// unconditionally: nothing advertises a prefixed endpoint unless a test calls
	// setManifestEndpoint, so this reaches no existing case.
	mux.Handle(endpointPathPrefix+exchangePath,
		http.StripPrefix(endpointPathPrefix, exchangeHandler))

	// The manifest an Exchange serves about itself. fora_report finds where to
	// send a report by reading this — never from our configuration — so the peer
	// has to publish one for that path to work at all. It is deliberately outside
	// the signature gate: the default predicate only covers /fora.* paths, and a
	// public discovery document is unsigned in production too.
	mux.HandleFunc("GET /.well-known/fora.json", func(w http.ResponseWriter, r *http.Request) {
		p.countManifestFetch()
		if status := p.manifestStatus(); status != 0 {
			http.Error(w, "the manifest is unavailable", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// The version override goes through the shared assembler too, so a
		// document staged for the version gate differs from the normal one in the
		// version and in nothing else.
		if ver := p.publishedManifestVer(); ver != "" {
			_, _ = w.Write(rwtestutil.ExchangeManifestAtVersion(
				r.Host, p.advertisedEndpoint(), ver))
			return
		}
		_, _ = w.Write(rwtestutil.ExchangeManifestWithRegistration(
			r.Host, p.advertisedEndpoint(), p.publishedRegistration()))
	})

	// The Web Bot Auth directory carrying this peer's OFFER-signing key. After the
	// WBA split that key lives only here, and it is what discovery resolves an
	// offer's signature against, so a peer without this document issues offers
	// nobody can verify. Served as every key directory is: over https, under the
	// directory media type, with a response signature by the key it lists.
	directory := rwtestutil.MarshalWBA(rwtestutil.WBAFile(
		forawellknown.NewKey(p.offerPub, wbaNotBefore, wbaNotAfter)))
	signatures := wkserver.NewSignatureCache(nil, 0)
	mux.HandleFunc("GET /.well-known/http-message-signatures-directory",
		func(w http.ResponseWriter, r *http.Request) {
			_ = wkserver.WriteDirectory(w, r, directory,
				[]helpers.Signer{rwtestutil.DirectorySigner(p.offerPriv)}, signatures)
		})

	// Verify the /fora.* set, and the same set behind the path prefix. The
	// well-known documents stay unverified — they match neither clause.
	//
	// The prefixed mount needs its own clause and this is the load-bearing half
	// of it. "/api/fora.v1.ExchangeService/Register" matches neither of the other
	// two, so without this the gate would wave a prefixed call through
	// unverified, p.record would never run, and a test asserting the call was
	// signed would pass whether it was or not. It is widened to the exact prefix
	// this peer serves rather than to a substring match, because a predicate that
	// accepts "/fora." anywhere in a path stops being the check it is standing in
	// for.
	gated := verifyGate(resolver, p.record, func(path string) bool {
		return strings.HasPrefix(path, foraauth.FORAProcedurePrefix) ||
			strings.HasPrefix(path, endpointPathPrefix+foraauth.FORAProcedurePrefix)
	}, mux)

	// Counting sits OUTSIDE the signature gate and outside the mux, so it sees a
	// request whatever happens to it afterwards — refused for its signature,
	// answered 404 by the mux, or served. That is the whole point: the question
	// it answers is whether this host was dialled, not whether it liked what
	// arrived.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.countHTTPRequest()
		gated.ServeHTTP(w, r)
	})
	srv.StartTLS()
	p.srv = srv
	t.Cleanup(p.srv.Close)
	return p
}

// verifyGate is the peer's signature gate. A request whose path signed selects
// is verified with the SDK's request verifier against resolver — required
// components, the Web Bot Auth profile, the freshness window and the Ed25519
// signature — and refused 401, with Accept-Signature where the SDK names one,
// when it fails. A request that passes is recorded through onVerified and
// served. It keeps no replay store: no case here sends one signature twice.
func verifyGate(
	resolver helpers.KeyResolver, onVerified func(*http.Request, *helpers.VerifiedRequest) *http.Request,
	signed func(string) bool, next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !signed(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		v, err := helpers.VerifyRequestResolved(r.Context(), r, body, resolver, helpers.VerifyOptions{})
		if err != nil {
			if accept, ok := helpers.AcceptSignatureFor(err); ok {
				w.Header().Set(helpers.AcceptSignatureHeader, accept)
			}
			connectserver.WriteReject(w, connect.CodeUnauthenticated, err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, onVerified(r, v))
	})
}

// record notes a verified call. It runs as the gate's verified hook, so it only
// ever sees requests whose signature already passed.
func (p *foraPeer) record(r *http.Request, v *helpers.VerifiedRequest) *http.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, peerCall{
		Path:           r.URL.Path,
		KeyID:          v.KeyID,
		SignatureAgent: v.SignatureAgent,
		Authorization:  r.Header.Get("Authorization"),
		RequestID:      r.Header.Get(reqctx.HeaderRequestID),
	})
	return r
}

// assertNoBearerLeaked checks the invariant the package doc calls its reason for
// existing: the caller's OAuth bearer authenticates them to US and must never
// travel outbound. Every peer the tools talk to is a third party, so a bearer
// arriving at one is credential disclosure to a party that should never see it.
//
// Called from every happy path. Cheap, and without it a transport change that
// forwarded the header would pass the entire suite.
func assertNoBearerLeaked(t *testing.T, peers ...*foraPeer) {
	t.Helper()
	for _, p := range peers {
		for _, c := range p.Calls() {
			if c.Authorization != "" {
				t.Errorf("outbound call to %s carried an Authorization header (%q); "+
					"the inbound bearer must never travel outbound", c.Path, c.Authorization)
			}
		}
	}
}

// LastReport / LastExecute / LastDiscovery / LastRegistration read the recorded
// requests under the peer's mutex. The handler goroutine writes them, the test
// goroutine reads them, and there is no happens-before edge between the two — so
// a bare field read is a race the -race detector will eventually catch on someone
// else's afternoon.
func (p *foraPeer) LastReport() *forav1.UsageReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastReport
}

func (p *foraPeer) LastExecute() *forav1.TransactionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastExecute
}

func (p *foraPeer) LastDiscovery() *forav1.DiscoveryRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastDiscovery
}

func (p *foraPeer) LastRegistration() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastRegistration
}

// URL is the peer's origin.
func (p *foraPeer) URL() string { return p.srv.URL }

// Domain is the peer's canonical exchange domain — its host:port, the value an
// Offer carries in its exchange field. The port is part of it: a manifest may
// only advertise an endpoint on the host AND port that served it, and the
// directory lookup concatenates this value, so a bare host would look elsewhere.
//
// It takes t and goes through hostOf so a peer's domain is derived exactly once,
// with the check that the origin is the plain http the fixture builds. Trimming
// the prefix inline returns the whole URL when it is absent, and a test then
// carries a scheme into a field that must hold a host — which fails much later,
// somewhere else.
func (p *foraPeer) Domain(t *testing.T) string {
	t.Helper()
	return hostOf(t, p.srv.URL)
}

// Offer builds an offer this peer has genuinely issued: stamped with its own
// exchange domain and signed with the key its directory publishes.
//
// Tests call it instead of writing an Offer literal because discovery verifies
// what it returns. An offer assembled by hand carries a signature over nothing,
// and now arrives at the agent as a REJECTION rather than as an offer — so a
// literal no longer describes a working discovery, whatever it says.
func (p *foraPeer) Offer(t *testing.T, offerID string) *forav1.Offer {
	t.Helper()
	// An offer with no expiry is an EXPIRED offer: the zero timestamp is in the
	// past, and the protocol requires rejecting an offer whose expires_at has
	// passed. So a fixture offer has to carry a real one or it never reaches the
	// agent, whatever else is right about it.
	//
	// The window is read against the wall clock rather than the fixture's
	// deterministic one, because the verifier the SDK builds takes its own time
	// source. Far enough out that it cannot expire mid-suite.
	return p.OfferAt(t, offerID, time.Now().Add(24*time.Hour))
}

// OfferAt is Offer with the expiry chosen, for the tests that drive the freshness
// rule. A past instant produces an offer that is correctly signed and correctly
// issued and still refused, which is the only way to reach that arm — the
// signature covers expires_at, so an expiry cannot be edited onto a signed offer
// afterwards without turning the case into a signature failure instead.
func (p *foraPeer) OfferAt(t *testing.T, offerID string, expiresAt time.Time) *forav1.Offer {
	t.Helper()
	offer := &forav1.Offer{
		OfferId:   offerID,
		Exchange:  p.Domain(t),
		ExpiresAt: timestamppb.New(expiresAt),
	}
	sig, err := helpers.SignOffer(p.offerPriv, offer)
	if err != nil {
		t.Fatalf("sign offer %q: %v", offerID, err)
	}
	offer.Signature = sig
	offer.SignatureAlgorithm = helpers.OfferSignatureAlgorithm
	return offer
}

// deliveryExpiry is the expiry every fixture delivery URL is signed with: far
// enough out that no suite run reaches it.
var deliveryExpiry = time.Unix(4102444800, 0)

// SignDelivery signs rawURL the way this peer, as the issuing Exchange, signs a
// delivery URL: Ed25519 under the key its Web Bot Auth directory publishes, named
// by that key's thumbprint in kid. An agent_id already on rawURL is kept, so the
// URL stays bound to whichever agent the test bound it to.
//
// The identity service does not verify the URL's signature: the protocol leaves
// that to the edge serving it. The fixtures still sign every URL, so what the
// service returns and fetches is shaped exactly as an issuing Exchange would
// issue it.
func (p *foraPeer) SignDelivery(t *testing.T, rawURL string) string {
	t.Helper()
	kid, err := helpers.Thumbprint(p.offerPub)
	if err != nil {
		t.Fatalf("thumbprint the peer key: %v", err)
	}
	signed, err := helpers.SignURLEd25519(p.offerPriv, kid, rawURL, "", deliveryExpiry)
	if err != nil {
		t.Fatalf("sign delivery URL %q: %v", rawURL, err)
	}
	return signed.URL
}

// advertisedEndpoint is the endpoint this peer names in its well-known manifest.
// It defaults to the peer's own origin — the host-anchored case — so tests that do
// not touch it exercise a manifest that points at itself.
func (p *foraPeer) advertisedEndpoint() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.manifestEndpoint != "" {
		return p.manifestEndpoint
	}
	return p.srv.URL
}

// publishedRegistration reports what this peer publishes about registering.
func (p *foraPeer) publishedRegistration() rwtestutil.Registration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.registration
}

// setRegistration makes this peer publish a registration schema, a terms pair,
// or both. Called between calls, so a test can stage an Exchange that revises
// what it asks for and drive what the adapter does about it.
func (p *foraPeer) setRegistration(reg rwtestutil.Registration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registration = reg
}

// LastTermsDigest is the terms_digest the most recent Register echoed, and
// whether it carried one at all. Two return values because an ABSENT digest is
// its own case in the protocol — the Exchange publishes none — and "" would
// make it indistinguishable from a published empty one.
func (p *foraPeer) LastTermsDigest() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastTermsDigest == nil {
		return "", false
	}
	return *p.lastTermsDigest, true
}

// failManifest makes this peer answer its well-known document with status
// instead of serving it, so a test can stage the one outbound failure that has
// nothing to do with the RPC: the Exchange is reachable but its manifest is not
// readable. Zero restores normal serving.
func (p *foraPeer) failManifest(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.manifestFailStatus = status
}

// manifestStatus is the status the well-known handler should answer with, or 0
// to serve the document.
func (p *foraPeer) manifestStatus() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.manifestFailStatus
}

// setManifestVer makes this peer publish its manifest at ver instead of the
// version the fixture stamps, so a test can stage an Exchange whose document
// this build refuses to read. Passing "" through serveManifestAtVersion below
// stages the absent-version case; leaving this unset serves the normal document.
func (p *foraPeer) setManifestVer(ver string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.manifestVer = ver
}

// publishedManifestVer reports the override, empty when none is set.
func (p *foraPeer) publishedManifestVer() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.manifestVer
}

// setManifestEndpoint makes this peer advertise a different endpoint than its own
// origin, so a test can stage a manifest that redirects a report to another host.
func (p *foraPeer) setManifestEndpoint(endpoint string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.manifestEndpoint = endpoint
}

// countManifestFetch notes one read of the well-known document.
func (p *foraPeer) countManifestFetch() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.manifestFetches++
}

// ManifestFetches is how many times this peer's well-known document has been
// read. Zero proves a report was refused before the resolver dialed anything.
func (p *foraPeer) ManifestFetches() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.manifestFetches
}

// countHTTPRequest notes one request reaching this peer's server, on any path.
func (p *foraPeer) countHTTPRequest() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.httpRequests++
}

// HTTPRequests is how many requests have reached this peer on any path, served
// or refused. Zero proves nothing was dialled here at all.
func (p *foraPeer) HTTPRequests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.httpRequests
}

// Calls returns the verified calls so far.
func (p *foraPeer) Calls() []peerCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]peerCall(nil), p.calls...)
}

// failWith makes every subsequent RPC return err, for driving the FORA-side
// refusal paths.
func (p *foraPeer) failWith(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *foraPeer) outcome() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// --- ExchangeService ---

func (p *foraPeer) Register(
	_ context.Context, req *connect.Request[forav1.RegisterRequest],
) (*connect.Response[forav1.RegisterResponse], error) {
	if err := p.outcome(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.lastRegistration = req.Msg.GetRegistrationData().AsMap()
	p.lastTermsDigest = req.Msg.TermsDigest
	resp := p.registerResp
	p.mu.Unlock()
	if resp == nil {
		resp = &forav1.RegisterResponse{Ver: helpers.ProtocolVersion}
	}
	return connect.NewResponse(resp), nil
}

func (p *foraPeer) GetAccountStatus(
	_ context.Context, _ *connect.Request[forav1.GetAccountStatusRequest],
) (*connect.Response[forav1.GetAccountStatusResponse], error) {
	if err := p.outcome(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	resp := p.statusResp
	p.mu.Unlock()
	if resp == nil {
		resp = &forav1.GetAccountStatusResponse{Ver: helpers.ProtocolVersion}
	}
	return connect.NewResponse(resp), nil
}

func (p *foraPeer) ReportUsage(
	_ context.Context, req *connect.Request[forav1.UsageReport],
) (*connect.Response[forav1.UsageReportResponse], error) {
	if err := p.outcome(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.lastReport = req.Msg
	resp := p.reportResp
	p.mu.Unlock()
	if resp == nil {
		resp = &forav1.UsageReportResponse{Ver: helpers.ProtocolVersion}
	}
	return connect.NewResponse(resp), nil
}

// --- BrokerService ---

// brokerFace is the peer seen as the Broker. ExchangeService and BrokerService
// both name an RPC ExecuteTransaction, with different responses, so one type
// cannot serve both: the peer itself is the Exchange, and this wrapper adds the
// Broker's purchase beside the Resolve it already has.
type brokerFace struct{ *foraPeer }

// ExecuteTransaction stands in for the Broker's purchase RPC. By the time it runs
// the signature gate has verified the request, so it records it and answers with
// whatever the test programmed: the Broker's refusal, or its combined answer.
func (b brokerFace) ExecuteTransaction(
	_ context.Context, req *connect.Request[forav1.TransactionRequest],
) (*connect.Response[forav1.BrokerTransactionResponse], error) {
	b.mu.Lock()
	b.lastExecute = req.Msg
	refusal, resp := b.executeErr, b.executeResp
	b.mu.Unlock()
	if refusal != nil {
		return nil, refusal
	}
	if resp == nil {
		resp = &forav1.BrokerTransactionResponse{Ver: helpers.ProtocolVersion}
	}
	return connect.NewResponse(resp), nil
}

func (p *foraPeer) Resolve(
	_ context.Context, req *connect.Request[forav1.DiscoveryRequest],
) (*connect.Response[forav1.DiscoveryResponse], error) {
	if err := p.outcome(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.lastDiscovery = req.Msg
	resp := p.resolveResp
	p.mu.Unlock()
	if resp == nil {
		resp = &forav1.DiscoveryResponse{Ver: helpers.ProtocolVersion}
	}
	return connect.NewResponse(resp), nil
}

// newTrustStore is the key directory the peers verify against: thumbprint →
// public key, seeded from custody as agents are provisioned.
//
// It is the SDK's helpers.StaticKeyResolver — the in-memory map double the
// SDK ships "for preloaded key sets and tests", with Put for dynamic
// registration — the same double the broker suites use. It carries no
// validity-window logic, so a key-window negative cannot be expressed through
// this trust store; window enforcement is covered where it lives, in the
// well-known resolver suites.
func newTrustStore() *helpers.StaticKeyResolver {
	return helpers.NewStaticKeyResolver(nil)
}
