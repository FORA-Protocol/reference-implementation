//go:build integration

package transport_test

// The idempotency scope of a purchase a Broker relays.
//
// The verified signer of a re-packaged request is the Broker, and the Broker
// speaks for many agents. The protocol scopes a key to the agent, meaning its key
// directory, whatever the route: on a relayed purchase that is the directory the
// agent's acceptances verify under. So two agents behind one Broker that pick the
// same key must not collide. A scope keyed on the signer alone would give every
// agent behind the Broker one shared key space — the second agent would be
// refused for "reusing" a key it never used, or worse, served the first agent's
// stored result and signed URL.
//
// Round-trip honesty: every leg drives the public ExecuteTransaction RPC through
// the Broker's single-signature client and reads the answer back from it; the
// two agents are told apart only by Requester.domain and the acceptances, the
// way a real re-packaged request carries them.

import (
	"crypto/ed25519"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// sharedKey is the idempotency key both agents pick.
const sharedKey = "tx-shared-key"

// relayAs sends one Broker-relayed purchase of offer for the agent whose
// directory host is domain, signed with that agent's key under sharedKey.
func relayAs(
	t *testing.T, h *testHarness, broker foraconnect.ExchangeServiceClient,
	domain string, priv ed25519.PrivateKey, offer *forav1.Offer,
) (*connect.Response[forav1.TransactionResponse], error) {
	t.Helper()
	key := sharedKey
	requester := newRequester("urn:relay:probe", domain)
	req := &forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: key,
		Requester:      requester,
		Items: []*forav1.TransactionItem{
			{Offer: offer, AgentAcceptance: signAcceptanceFor(t, priv, offer, requester, key)},
		},
	}
	req.AgentRequestAcceptance = signRequestAcceptanceFor(t, priv, req)
	return broker.ExecuteTransaction(h.ctx, connect.NewRequest(req))
}

// TestExecuteRelay_TwoAgentsBehindOneBrokerDoNotShareAKey drives two agents
// through the same Broker with the same idempotency key, each buying its own
// offer, and then has each retry.
//
// The first agent is registered and buys. The second is not registered with
// this Exchange, so its own answer is a per-item ACCOUNT_NOT_REGISTERED denial:
// distinct from anything the first agent's request produced, which is what lets
// the test see whose answer came back. A Broker-scoped key would answer the
// second agent with the AlreadyExists refusal for a key "used with a different
// item set", or with the first agent's stored signed URL.
func TestExecuteRelay_TwoAgentsBehindOneBrokerDoNotShareAKey(t *testing.T) {
	h := newTestHarness(t)
	h.enableBrokerRelay(t, h.tenantID)
	offerA := discoverOffer(t, h, seedResourceWithRate(t, h, "/articles/scope-agent-a", "0.05"))
	offerB := discoverOffer(t, h, seedResourceWithRate(t, h, "/articles/scope-agent-b", "0.05"))
	const agentB = "agent-b.example"
	_, privB := h.addCallerWithKey(t, agentB, string(sqlc.ForaRequesterTypeAGENT))
	broker := h.brokerOnlyClient(t)

	first, err := relayAs(t, h, broker, h.callerID, h.callerPriv, offerA)
	if err != nil {
		t.Fatalf("agent A's relayed purchase: %v", err)
	}
	urlA := first.Msg.GetItems()[0].GetRetrievalEndpoint()
	if urlA == "" {
		t.Fatalf("agent A's purchase delivered nothing: %v", first.Msg.GetItems()[0])
	}

	second, err := relayAs(t, h, broker, agentB, privB, offerB)
	if err != nil {
		t.Fatalf("agent B reusing agent A's key through the same Broker was refused: %v — "+
			"the key is scoped to the Broker, not to the agent", err)
	}
	assertOwnDenial(t, second.Msg, offerB, urlA)

	// Each retry is answered from its own agent's history.
	replayA, err := relayAs(t, h, broker, h.callerID, h.callerPriv, offerA)
	if err != nil {
		t.Fatalf("agent A's retry: %v", err)
	}
	if got := replayA.Msg.GetItems()[0]; got.GetTransactionId() != first.Msg.GetItems()[0].GetTransactionId() {
		t.Errorf("agent A's retry answered transaction %q, want its original %q",
			got.GetTransactionId(), first.Msg.GetItems()[0].GetTransactionId())
	}
	replayB, err := relayAs(t, h, broker, agentB, privB, offerB)
	if err != nil {
		t.Fatalf("agent B's retry: %v", err)
	}
	assertOwnDenial(t, replayB.Msg, offerB, urlA)
}

// assertOwnDenial checks the second agent got its own answer for its own offer:
// the per-item ACCOUNT_NOT_REGISTERED denial, and never the first agent's
// delivery URL.
func assertOwnDenial(t *testing.T, resp *forav1.TransactionResponse, offer *forav1.Offer, otherURL string) {
	t.Helper()
	if len(resp.GetItems()) != 1 {
		t.Fatalf("items = %d, want 1", len(resp.GetItems()))
	}
	item := resp.GetItems()[0]
	if item.GetOfferId() != offer.GetOfferId() {
		t.Errorf("answered offer %q, want the agent's own %q", item.GetOfferId(), offer.GetOfferId())
	}
	if item.GetRetrievalEndpoint() == otherURL {
		t.Fatal("the second agent was served the first agent's signed delivery URL")
	}
	if got := item.GetDenialReason(); got != forav1.DenialReason_DENIAL_REASON_ACCOUNT_NOT_REGISTERED {
		t.Errorf("denial_reason = %v, want the agent's own ACCOUNT_NOT_REGISTERED", got)
	}
}
