//go:build integration

package transport_test

import (
	"testing"

	"connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// Signed requests pass authentication and proto validation but name another
// recipient. Both production mount shapes must refuse before business effects.
func TestAudienceRPCRejectsOtherExchange(t *testing.T) {
	h := newTestHarness(t)
	const other = "other.example"
	_, err := h.exchangeClient.DiscoverResources(h.ctx, connect.NewRequest(&forav1.ResourceQuery{
		Ver: helpers.ProtocolVersion, Exchange: other,
		Requester: &forav1.Requester{Id: "agent-test", Domain: "agent-test", Type: forav1.RequesterType_REQUESTER_TYPE_AGENT},
		Uris:      []string{"https://" + h.siteDomain + "/wrong-recipient"},
	}))
	assertConnectCode(t, err, connect.CodeInvalidArgument)
	assertRejectionFieldInDomain(t, err, exchangeServiceDomainLiteral, "exchange")
	assertRejectionMeta(t, err, "verdict", "mismatch")
	_, err = h.exchangeClient.Register(h.ctx, connect.NewRequest(&forav1.RegisterRequest{Ver: helpers.ProtocolVersion, Exchange: other}))
	assertConnectCode(t, err, connect.CodeInvalidArgument)
	assertRejectionFieldInDomain(t, err, exchangeServiceDomainLiteral, "exchange")
	assertRejectionMeta(t, err, "verdict", "mismatch")
	_, err = h.exchangeClient.GetAccountStatus(h.ctx, connect.NewRequest(&forav1.GetAccountStatusRequest{Ver: helpers.ProtocolVersion, Exchange: other}))
	assertConnectCode(t, err, connect.CodeInvalidArgument)
	assertRejectionFieldInDomain(t, err, exchangeServiceDomainLiteral, "exchange")
	assertRejectionMeta(t, err, "verdict", "mismatch")
	_, err = h.catalogClient.PushResources(h.ctx, connect.NewRequest(&forav1.PushResourcesRequest{
		Ver: helpers.ProtocolVersion, Exchange: other, TenantId: h.tenantID, CallerId: "agent-test",
		Entries: []*forav1.ResourceEntry{{Domain: h.siteDomain, Path: "/wrong-recipient", Terms: []*forav1.LicenseTerm{seedPricedTermEst(100)}}},
	}))
	assertConnectCode(t, err, connect.CodeInvalidArgument)
	assertRejectionFieldInDomain(t, err, "fora.v1.CatalogService", "exchange")
	assertRejectionMeta(t, err, "verdict", "mismatch")
	discovery, err := h.exchangeClient.DiscoverResources(h.ctx, connect.NewRequest(&forav1.ResourceQuery{
		Ver: helpers.ProtocolVersion, Exchange: harnessExchangeDomain,
		Requester: &forav1.Requester{Id: "agent-test", Domain: "agent-test", Type: forav1.RequesterType_REQUESTER_TYPE_AGENT},
		Uris:      []string{"https://" + h.siteDomain + "/wrong-recipient"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range discovery.Msg.GetOfferGroups() {
		if len(group.GetOffers()) != 0 {
			t.Fatal("misaddressed push populated the catalog")
		}
	}
	// A genuine offer with a wrong recipient is still refused before execution.
	offer := pushDiscoverOffer(t, h, 100)
	offer.Exchange = other
	_, err = executeOfferRawWithID(t, h, offer, "wrong-recipient-execute")
	assertConnectCode(t, err, connect.CodeInvalidArgument)
	assertRejectionFieldInDomain(t, err, exchangeServiceDomainLiteral, "offer.exchange")
	assertRejectionMeta(t, err, "verdict", "mismatch")
	assertRefusedItemIndex(t, err, "0")
	assertNoTransaction(t, h, derivedTxKey("wrong-recipient-execute", offer))
}

// A misaddressed Register with an otherwise valid body is refused before the
// account is created. The general harness cannot show that: its agent is
// pre-registered so execute can run, and an account that exists before the
// call proves nothing about the call. A fresh self-signup agent has no account
// until Register succeeds, so the correctly addressed status query, the only
// public read of account state, is a true "nothing was created" oracle.
func TestAudienceRPCRejectsRegisterBeforeAnyAccount(t *testing.T) {
	h := newRegisterHarness(t)
	a := h.newAgent(t, "wrong-recipient.example")
	req := newRegisterRequest(testutil.RegistrationStruct(t, map[string]any{"legal_entity": "Misaddressed AI Ltd"}))
	req.Exchange = "other.example"
	_, err := a.client.Register(h.ctx, connect.NewRequest(req))
	assertConnectCode(t, err, connect.CodeInvalidArgument)
	assertRejectionFieldInDomain(t, err, exchangeServiceDomainLiteral, "exchange")
	assertRejectionMeta(t, err, "verdict", "mismatch")
	_, err = a.client.GetAccountStatus(h.ctx, connect.NewRequest(newAccountStatusRequest()))
	assertConnectCode(t, err, connect.CodeNotFound)
}
