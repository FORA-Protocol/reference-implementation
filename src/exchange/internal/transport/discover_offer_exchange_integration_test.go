//go:build integration

package transport_test

import (
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/proto"
)

// TestDiscover_OfferCarriesSignedExchangeDomain pins step 1 of the signed-exchange-domain routing contract: the
// Exchange populates Offer.exchange with its configured canonical domain BEFORE
// signing, so the field falls inside the signed offer payload
// (helpers.canonicalOfferPayload covers the whole Offer minus
// signature/signature_algorithm). The broker derives the execute-routing target
// from this signed field, retiring the X-FORA-Exchange-Endpoint header.
//
// Round-trip: the offer is produced and read through the public
// CatalogService.PushResources → ExchangeService.DiscoverResources surface; the
// signature is then validated through the public ExecuteTransaction surface
// (which runs helpers.VerifyPresentedOffer against the presented bytes),
// proving the signature now covers offer.exchange. No raw DB / sqlc access.
func TestDiscover_OfferCarriesSignedExchangeDomain(t *testing.T) {
	h := newTestHarness(t)
	uri := seedResourceWithRate(t, h, "/articles/exch-domain", "0.05")

	offer := discoverOffer(t, h, uri)

	const wantDomain = harnessExchangeDomain
	if got := offer.GetExchange(); got != wantDomain {
		t.Fatalf("offer.exchange = %q, want %q", got, wantDomain)
	}
	if offer.GetSignature() == "" || offer.GetSignatureAlgorithm() != "EdDSA" {
		t.Fatalf("offer not signed: %+v", offer)
	}

	// VerifyPresentedOffer still passes: the genuine offer (carrying
	// offer.exchange) is accepted by ExecuteTransaction, which verifies the
	// presented signature server-side.
	const txID = "tx-exch-domain-ok"
	if _, err := executeSingleItem(t, h, txID, offer); err != nil {
		t.Fatalf("execute genuine offer with exchange field: %v", err)
	}
	// The items[] path persists under the DERIVED key idempotency_key:offer_id.
	assertTransactionLogged(t, h.ctx, h, derivedTxKey(txID, offer))
}

// TestDiscover_RepointedExchangeDomainRefusedBeforeTheHandler covers the
// attacker's obvious move: take a genuine offer and repoint its routing target
// at an Exchange of their choosing.
//
// It is refused before the handler runs, by the recipient check, because every
// item's offer.exchange has to name the Exchange receiving the request. That is
// a whole-request refusal rather than a per-item denial: the protocol's rule is
// about who the request is FOR, and a request meant for somebody else is not one
// this Exchange answers item by item. Nothing is persisted.
func TestDiscover_RepointedExchangeDomainRefusedBeforeTheHandler(t *testing.T) {
	h := newTestHarness(t)
	uri := seedResourceWithRate(t, h, "/articles/exch-repointed", "0.05")
	offer := discoverOffer(t, h, uri)

	repointed, ok := proto.Clone(offer).(*forav1.Offer)
	if !ok {
		t.Fatal("clone offer")
	}
	repointed.Exchange = "evil.exchange.test"

	const txID = "tx-exch-repointed"
	_, err := executeSingleItem(t, h, txID, repointed)
	assertConnectError(t, err, connect.CodeInvalidArgument, "addressed to a different Exchange")
	assertNoTransaction(t, h, derivedTxKey(txID, repointed))
}

// TestDiscover_TamperedExchangeDomainRejected proves offer.exchange is covered
// by the signature: mutating it after signing makes the presented bytes diverge
// from what the Exchange signed, so ExecuteTransaction's VerifyPresentedOffer
// rejects the offer with SIGNATURE_INVALID and persists no transaction. This is
// the security guarantee that lets the broker route from the signed field — a
// rogue caller cannot rewrite the routing target without breaking the signature.
//
// The tampered value is this Exchange's own domain in uppercase. The recipient
// check accepts this identity-equivalent spelling, so the request reaches offer
// signature verification. The signed bytes still differ, proving that recipient
// normalization cannot hide a change to the signed offer.
func TestDiscover_TamperedExchangeDomainRejected(t *testing.T) {
	h := newTestHarness(t)
	uri := seedResourceWithRate(t, h, "/articles/exch-tamper", "0.05")
	offer := discoverOffer(t, h, uri)

	tampered, ok := proto.Clone(offer).(*forav1.Offer)
	if !ok {
		t.Fatal("clone offer")
	}
	tampered.Exchange = strings.ToUpper(offer.GetExchange())

	const txID = "tx-exch-tampered"
	requester := newRequester("agent-test", "agent.example")
	resp, err := h.exchangeClient.ExecuteTransaction(h.ctx, connect.NewRequest(&forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: txID,
		Requester:      requester,
		Items: []*forav1.TransactionItem{{
			Offer: tampered, AgentAcceptance: signAcceptanceFor(t, h.callerPriv, tampered, requester, txID),
		}},
	}))
	// A tampered signed field → SIGNATURE_INVALID, a denial-map kind → in-body
	// per-item denial after the C4 items-only collapse (Flag #1).
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_SIGNATURE_INVALID)
	assertNoTransaction(t, h, derivedTxKey(txID, tampered))
}
