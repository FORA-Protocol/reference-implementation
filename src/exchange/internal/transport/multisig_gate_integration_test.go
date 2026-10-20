//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	sdkconnect "github.com/FORA-Protocol/protocol/sdk/go/connect"

	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraauth"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// The multi-signature gate every ExchangeService RPC passes through
// (connectserver.WithMaxSignatures in transport.ExchangeMountOptions), driven
// through the real Connect router with DiscoverResources.
//
// The pinned protocol makes every request signature a Web Bot Auth signature,
// verified on its own against the directory its own Signature-Agent member
// names. A request may carry several; a later one may cover an earlier one,
// which is optional. The Exchange counts every signature, the agent's included,
// against WellKnownManifest.max_intermediary_hops before it verifies any.
// Production sets the gate's budget to that published value itself, from
// EXCHANGE_MAX_INTERMEDIARY_HOPS.
//
// Every leg here is the public surface: the request goes out through a Connect
// client and the answer comes back through the same client. The Connect
// protocol (JSON), not gRPC, is used on purpose: the gate answers a hop-budget
// refusal with HTTP 429 and a resource_exhausted body, and a gRPC client reads a
// bare 429 as unavailable, which would hide the code under test. The audit
// assertion reads the gate's reject line, the record LogHTTPSigReject writes.
//
// DiscoverResources persists nothing, so a refusal has no row to be absent. The
// side effect a refusal must not have is the handler running at all: the client
// receives no response message, only the gate's error.

// gateHopBudget mirrors production with EXCHANGE_MAX_INTERMEDIARY_HOPS = 2: the
// gate admits 2 signatures, the agent's plus one forwarding hop.
const gateHopBudget = 2

// gateRejectMsg is the message of the Exchange's reject line.
const gateRejectMsg = "exchange.httpsig.reject"

// newMultisigGateHarness brings up the standard harness with the gate's signature budget
// set to gateHopBudget and the service logger writing JSON into the returned
// buffer, then seeds the default catalog entry. The buffer is emptied after
// seeding, so it holds only what the test's own requests produce.
func newMultisigGateHarness(t *testing.T) (*testHarness, *safeBuffer) {
	t.Helper()
	logs := &safeBuffer{}
	h := newTestHarnessWith(t, harnessOptions{
		logger:        slog.New(slog.NewJSONHandler(logs, nil)),
		maxSignatures: gateHopBudget,
	})
	seedCatalog(t, h)
	logs.Reset()
	return h, logs
}

// hopKeys mints n forwarding-hop keys and registers each with the Exchange's
// configured key set, the set the protocol says a hop's key is taken from.
// Registering even the keys of a chain the gate must refuse keeps that refusal
// honest: every signature would verify, so the gate's own rule is the only
// reason left to refuse.
func (h *testHarness) hopKeys(t *testing.T, n int) []ed25519.PrivateKey {
	t.Helper()
	keys := make([]ed25519.PrivateKey, 0, n)
	for range n {
		_, priv := h.configuredBrokerKey(t)
		keys = append(keys, priv)
	}
	return keys
}

// gateClient is a Connect-protocol ExchangeService client over rt.
func (h *testHarness) gateClient(rt http.RoundTripper) foraconnect.ExchangeServiceClient {
	return foraconnect.NewExchangeServiceClient(&http.Client{Transport: rt}, h.server.URL, connect.WithProtoJSON())
}

// gateDiscover sends the seeded URI's discovery through client.
func (h *testHarness) gateDiscover(
	client foraconnect.ExchangeServiceClient,
) (*connect.Response[forav1.ResourceResponse], error) {
	return client.DiscoverResources(h.ctx, connect.NewRequest(newResourceQuery(
		newRequester("agent-test", "agent.example"),
		[]string{"https://" + h.siteDomain + "/articles/hello"})))
}

// independentHopTransport appends a second signature that covers only its own
// request and its own Signature-Agent member, not the agent's signature: the
// Web Bot Auth profile makes covering an earlier signature optional, and each
// signature is verified on its own against the directory its own member names.
func independentHopTransport(base http.RoundTripper, hopPriv ed25519.PrivateKey) http.RoundTripper {
	return core.NewSigningTransport(exchangetest.Signer(hopPriv), base,
		core.WithSignPredicate(foraauth.IsFORAProcedure),
		core.WithAppendSigner(),
		core.WithSignatureAgent(hopOrigin(0)),
		core.WithWindow(core.ClockWindow(clock.System{}.Now, exchangetest.SignatureTTL)),
	)
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// gateRejects returns every reject line in logged, decoded.
func gateRejects(t *testing.T, logged string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(logged), "\n") {
		var rec map[string]any
		if raw == "" || json.Unmarshal([]byte(raw), &rec) != nil {
			continue
		}
		if rec["msg"] == gateRejectMsg {
			out = append(out, rec)
		}
	}
	return out
}

// assertGateRefusal checks one refused discovery end to end: no response message
// reached the client, the error carries want, exactly one reject line was
// written, and that line names the DiscoverResources path and wantOutcome.
func assertGateRefusal(
	t *testing.T, resp *connect.Response[forav1.ResourceResponse], err error,
	want connect.Code, logged, wantOutcome string,
) {
	t.Helper()
	if resp != nil {
		t.Fatalf("the gate must refuse before the handler runs, but a response came back: %v", resp.Msg)
	}
	assertConnectCode(t, err, want)
	rejects := gateRejects(t, logged)
	if len(rejects) != 1 {
		t.Fatalf("reject lines = %d, want exactly 1; log:\n%s", len(rejects), logged)
	}
	if got, _ := rejects[0]["path"].(string); got != foraconnect.ExchangeServiceDiscoverResourcesProcedure {
		t.Errorf("reject line path = %q, want %q", got, foraconnect.ExchangeServiceDiscoverResourcesProcedure)
	}
	if got, _ := rejects[0]["outcome"].(string); got != wantOutcome {
		t.Errorf("reject line outcome = %q, want %q", got, wantOutcome)
	}
}

// TestMultisigGate_DiscoverAdmitsForwardedChainAtBudget is the positive control:
// the agent's signature plus one forwarding hop whose key the Exchange is
// configured with is exactly the budget, so the gate admits it and the handler
// answers with the seeded offer. Without this control, both refusals below would
// also pass against a gate that refused every chain.
func TestMultisigGate_DiscoverAdmitsForwardedChainAtBudget(t *testing.T) {
	h, logs := newMultisigGateHarness(t)
	client := h.gateClient(newChainSigningTransport(h.baseTransport, h.callerID, h.callerPriv, h.hopKeys(t, 1)...))

	resp, err := h.gateDiscover(client)
	if err != nil {
		t.Fatalf("a two-signature chain at the budget must be answered: %v", err)
	}
	if len(resp.Msg.GetOffers()) == 0 {
		t.Fatalf("the admitted chain got no offers for the seeded resource: %v", resp.Msg)
	}
	if rejects := gateRejects(t, logs.String()); len(rejects) != 0 {
		t.Fatalf("an admitted chain wrote reject lines: %v", rejects)
	}
}

// TestMultisigGate_DiscoverAdmitsAnIndependentSecondSignature sends the agent's
// signature and a second one that does not cover it, each naming its own
// Signature-Agent member. Under the Web Bot Auth profile each signature is
// verified on its own, and covering an earlier signature is optional, so the
// gate admits the request when both verify.
func TestMultisigGate_DiscoverAdmitsAnIndependentSecondSignature(t *testing.T) {
	h, logs := newMultisigGateHarness(t)
	hop := independentHopTransport(h.baseTransport, h.hopKeys(t, 1)[0])
	client := h.gateClient(exchangetest.SigningTransport(hop, h.callerID, h.callerPriv))

	resp, err := h.gateDiscover(client)
	if err != nil {
		t.Fatalf("two independently verifying signatures must be answered: %v", err)
	}
	if len(resp.Msg.GetOffers()) == 0 {
		t.Fatalf("the admitted request got no offers for the seeded resource: %v", resp.Msg)
	}
	if rejects := gateRejects(t, logs.String()); len(rejects) != 0 {
		t.Fatalf("an admitted request wrote reject lines: %v", rejects)
	}
}

// TestMultisigGate_DiscoverRefusesASecondSignatureThatDoesNotVerify sends the
// agent's valid signature beside a second one by a key the Exchange cannot
// resolve. A request carrying several signatures is refused if any one of them
// fails, so the agent's valid signature does not carry it: the gate refuses it
// as unauthenticated with the typed reason SIGNATURE_INVALID.
func TestMultisigGate_DiscoverRefusesASecondSignatureThatDoesNotVerify(t *testing.T) {
	h, logs := newMultisigGateHarness(t)
	_, unknownPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	hop := independentHopTransport(h.baseTransport, unknownPriv)
	client := h.gateClient(exchangetest.SigningTransport(hop, h.callerID, h.callerPriv))

	resp, err := h.gateDiscover(client)

	assertGateRefusal(t, resp, err, connect.CodeUnauthenticated, logs.String(), "signature")
	detail, ok := sdkconnect.ErrorDetailFrom(err)
	if !ok {
		t.Fatalf("the refusal carries no ErrorDetail: %v", err)
	}
	if got := detail.GetRequestAuthFailure().GetReason(); got !=
		forav1.RequestAuthFailureReason_REQUEST_AUTH_FAILURE_REASON_SIGNATURE_INVALID {
		t.Errorf("request_auth_failure reason = %v, want SIGNATURE_INVALID", got)
	}
}

// TestMultisigGate_DiscoverRefusesChainOverBudget sends the agent's signature and
// two forwarding hops, one signature more than the budget allows. Every
// signature is valid and chain-linked, so the count is the only thing wrong. The
// gate refuses it as resource_exhausted (HTTP 429), not as an authentication
// failure: the caller's credentials are fine and the remedy is a shorter chain.
// An unauthenticated refusal would carry a request_auth_failure detail; this one
// must carry none.
func TestMultisigGate_DiscoverRefusesChainOverBudget(t *testing.T) {
	h, logs := newMultisigGateHarness(t)
	var status int
	recordStatus := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := h.baseTransport.RoundTrip(req)
		if resp != nil {
			status = resp.StatusCode
		}
		return resp, err
	})
	client := h.gateClient(newChainSigningTransport(recordStatus, h.callerID, h.callerPriv, h.hopKeys(t, 2)...))

	resp, err := h.gateDiscover(client)

	assertGateRefusal(t, resp, err, connect.CodeResourceExhausted, logs.String(), "hop_budget")
	if status != http.StatusTooManyRequests {
		t.Errorf("HTTP status = %d, want %d", status, http.StatusTooManyRequests)
	}
	if detail, ok := sdkconnect.ErrorDetailFrom(err); ok && detail.GetRequestAuthFailure() != nil {
		t.Errorf("a hop-budget refusal must not read as an authentication failure: %v", detail)
	}
}
