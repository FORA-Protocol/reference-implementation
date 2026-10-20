//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/core"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraauth"
	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// Test helpers for the R4 relay path: body offer-acceptance signing and
// the agent+broker / broker-only transport signing variants. Split out of
// integration_helper_test.go to keep that file under the 800-line test cap.

// signAcceptanceFor mints a body AgentAcceptance for offer/txID signed with priv
// over the EXACT requester that will ride on the request. The
// signing key MUST be the agent's REGISTERED key, and requester MUST equal the
// request's requester, since the acceptance payload covers requester.id +
// requester.domain.
func signAcceptanceFor(
	t *testing.T, priv ed25519.PrivateKey, offer *forav1.Offer, requester *forav1.Requester, txID string,
) *forav1.AgentAcceptance {
	t.Helper()
	return exchangetest.OfferAcceptance(t, priv, offer, requester, txID)
}

// signAcceptance is signAcceptanceFor for the canonical agent.example requester
// (id, domain "agent.example", type AGENT) the standard execute requests use.
func signAcceptance(
	t *testing.T, priv ed25519.PrivateKey, offer *forav1.Offer, requesterID, txID string,
) *forav1.AgentAcceptance {
	t.Helper()
	return signAcceptanceFor(t, priv, offer,
		newRequester(requesterID, "agent.example"),
		txID)
}

// defaultAcceptance signs the body acceptance with the harness's default
// agent-test caller key (the agents-row registered key), for the canonical
// agent-direct execute path.
func (h *testHarness) defaultAcceptance(t *testing.T, offer *forav1.Offer, txID string) *forav1.AgentAcceptance {
	t.Helper()
	return signAcceptance(t, h.callerPriv, offer, "agent-test", txID)
}

// newMultisigSigningTransport composes two SDK signing transports into a
// two-signature forwarding chain: the OUTER agent transport signs sig1 (and
// stamps the agent's Signature-Agent, which the hop preserves rather than
// stamping its own), then the INNER hop transport (WithAppendSigner) sees the
// existing Signature and appends the chain-linked sig2. It is
// newChainSigningTransport with exactly one forwarding hop.
//
// This is NOT what the production Broker sends. The Broker signs its own
// re-packaged requests alone (brokerOnlyClient below is that shape). The
// protocol defines a chain only for a ResourceQuery that another party forwards
// byte-for-byte toward an Exchange, and never for a purchase. The execute relay
// tests still drive a chain because the Exchange classifies one as a relay and
// applies the tenant's allow_broker_relay opt-out to it. Both signers'
// thumbprint keyids MUST be registered with the httpsig resolver.
func newMultisigSigningTransport(
	base http.RoundTripper, agentKeyID string, agentPriv ed25519.PrivateKey,
	_ string, brokerPriv ed25519.PrivateKey,
) http.RoundTripper {
	return newChainSigningTransport(base, agentKeyID, agentPriv, brokerPriv)
}

// newChainSigningTransport signs every /fora.* request with an N-signature
// forwarding chain built from SDK signing transports only: the agent signs sig1
// and names agentDirectory as its Signature-Agent member, then each key in
// hopPrivs, in order, appends a signature with a member of its own
// (hopOrigin) that covers the one before it. hopPrivs[0] is the
// first forwarding hop. Each signature carries a fresh nonce from the SDK
// transport, so back-to-back identical calls are not refused as replays.
func newChainSigningTransport(
	base http.RoundTripper, agentDirectory string, agentPriv ed25519.PrivateKey,
	hopPrivs ...ed25519.PrivateKey,
) http.RoundTripper {
	win := core.ClockWindow(clock.System{}.Now, exchangetest.SignatureTTL)
	// Built from the inside out: the transport nearest the wire signs last, so
	// the last hop wraps base and the first hop sits just inside the agent.
	rt := base
	for i := len(hopPrivs) - 1; i >= 0; i-- {
		rt = core.NewSigningTransport(
			exchangetest.Signer(hopPrivs[i]), rt,
			core.WithSignPredicate(foraauth.IsFORAProcedure),
			core.WithAppendSigner(),
			core.WithCoverPrevious(),
			core.WithSignatureAgent(hopOrigin(i)),
			core.WithWindow(win),
		)
	}
	return core.NewSigningTransport(
		exchangetest.Signer(agentPriv), rt,
		core.WithSignPredicate(foraauth.IsFORAProcedure),
		core.WithSignatureAgent(exchangetest.SignatureAgentOrigin(agentDirectory)),
		core.WithWindow(win),
	)
}

// hopOrigin is the Signature-Agent member forwarding hop i signs with: each
// signature names its own signer's directory under the Web Bot Auth profile.
func hopOrigin(i int) string {
	return fmt.Sprintf("https://hop%d.example", i+1)
}

// brokerRelayClient registers a BROKER caller (keyID carrying the
// helpers.BrokerKeyIDPrefix) and returns a Connect-Go ExchangeService client
// whose transport signs sig1 as the default agent-test agent and sig2 as that
// broker — the forwarding chain the R4 multisig gate verifies. The agent
// identity that binds the URL still comes from the BODY acceptance, not this
// transport chain (binding decision).
func (h *testHarness) brokerRelayClient(t *testing.T, brokerKeyID string) foraconnect.ExchangeServiceClient {
	t.Helper()
	return h.relayClientAs(t, brokerKeyID, sqlc.ForaRequesterTypeBROKER)
}

// relayClientAs is brokerRelayClient with an explicit agents-row requester_type
// for the relay hop (sig2). It lets a test arrange a relay hop whose directory is
// NOT a registered broker (requester_type == AGENT). The single-gate relay posture
// (Option C) admits such a hop purely on tenant.allow_broker_relay: the transport
// chain is classified by POSITION, never re-checked against requester_type, so the
// hop's role has no bearing on the relay decision.
func (h *testHarness) relayClientAs(
	t *testing.T, relayKeyID string, requesterType sqlc.ForaRequesterType,
) foraconnect.ExchangeServiceClient {
	t.Helper()
	_, relayPriv := h.registerBrokerAs(t, relayKeyID, requesterType)
	transport := newMultisigSigningTransport(
		h.baseTransport, "agent-test", h.callerPriv, relayKeyID, relayPriv,
	)
	client := &http.Client{Transport: transport}
	return foraconnect.NewExchangeServiceClient(client, h.server.URL, connect.WithGRPC())
}

// brokerOnlyClient returns a client whose transport carries ONE signature — the
// configured Broker's, with no agent sig1 — which is what the production Broker
// produces on execute: it rebuilds the request per Exchange (batch fan-out
// changes the body), so the agent's own transport signature cannot survive and
// only the Broker's is present. The agent identity comes from Requester.domain
// and the body acceptance signed over it.
//
// The Signature-Agent is harnessBrokerHost, the same host the harness configures
// as ExchangeConfig.BrokerHost, because the directory host is what marks a
// request as relayed. Only the signing key is registered with the httpsig
// resolver; NO fora.agents row is seeded. That absence is deliberate: an
// operator-seeded BROKER row is no longer what makes a relay a relay.
func (h *testHarness) brokerOnlyClient(t *testing.T) foraconnect.ExchangeServiceClient {
	t.Helper()
	_, brokerPriv := h.configuredBrokerKey(t)
	client := &http.Client{Transport: newSigningTransport(h.baseTransport, harnessBrokerHost, brokerPriv)}
	return foraconnect.NewExchangeServiceClient(client, h.server.URL, connect.WithGRPC())
}

// configuredBrokerKey mints the Broker's signing key and registers it with the
// httpsig resolver, which keys by RFC 7638 thumbprint. Nothing else is arranged:
// the relay decision reads the directory host, not a row.
func (h *testHarness) configuredBrokerKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("broker ed25519: %v", err)
	}
	h.resolver.Put(rwtestutil.MustThumbprintPriv(priv), pub)
	return pub, priv
}

// relayRequester is the Requester a relayed ExecuteTransaction carries: a free
// label for id, and the AGENT's OWN directory host for domain. Once the
// transport header names the Broker, domain is the only field that can name the
// agent, and the detached body acceptance covers it. The deliberately
// non-host id also keeps these tests honest — a resolution that fell back to
// requester.id would fail here rather than pass by coincidence.
func (h *testHarness) relayRequester() *forav1.Requester {
	return newRequester("urn:relay:probe", h.callerID)
}

// registerBrokerAs is registerBroker with an explicit agents-row requester_type,
// so a test can arrange the prefix-vs-type disagreement (a 'broker.'-prefixed key
// whose agents row is requester_type=AGENT). The row is seeded below the public
// surface — a Testing-Doctrine-9 arrange-side corner, because no public
// broker-registration RPC exists (lazy self-signup refuses broker prefixes) — but
// through the shared seedAgentAs helper, so the broker carries the same key
// provenance every other seeded caller does. Side-effect ASSERTIONS still go
// through the production repo surface.
func (h *testHarness) registerBrokerAs(
	t *testing.T, brokerKeyID string, requesterType sqlc.ForaRequesterType,
) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	brokerPub, brokerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("broker ed25519: %v", err)
	}
	// The httpsig resolver keys the relay's signature by its RFC 7638 thumbprint
	// (the WBA keyid); the agents row keys by the broker's directory identity.
	h.resolver.Put(rwtestutil.MustThumbprintPriv(brokerPriv), brokerPub)
	seedAgentAs(t, h.ctx, h.queries, brokerKeyID, brokerPub, string(requesterType))
	return brokerPub, brokerPriv
}
