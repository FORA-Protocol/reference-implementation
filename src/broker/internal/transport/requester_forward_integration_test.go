//go:build integration

package transport_test

// What the Broker puts in the requester it sends an Exchange. The Exchange
// selects terms by requester.scopes, so a Broker that drops them hides every
// scope-gated term from an agent that holds the scope. A Broker that adds a
// scope would offer the agent a term nobody granted it.
//
// Each test drives the agent's call through the Broker's Connect router and
// reads the request the Broker sent from a recording endpoint registered as the
// Exchange. That endpoint stores the bytes and answers 503, so no Exchange
// answer is emulated. Whether the Exchange then offers the scope-gated term is
// proven end to end by the scope suite under tests/e2e/harness/obligations/.

import (
	"context"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// TestResolve_ForwardsRequesterScopesUnchanged drives Resolve with and without
// requester.scopes. The query the Broker sends the Exchange carries exactly the
// scopes the agent stated, in the order it stated them, and none when it stated
// none.
func TestResolve_ForwardsRequesterScopesUnchanged(t *testing.T) {
	for name, scopes := range map[string][]string{
		"two scopes":   {"subscription:premium", "dist:US:*"},
		"no scope set": nil,
	} {
		t.Run(name, func(t *testing.T) {
			ex, rec := startRecordingExchange(t)
			fx := newFixture(t, context.Background(), fixtureOpts{providerDomain: "acme.example", upstream: ex})

			resolveOverConnect(t, fx, reqOpts{uri: "https://acme.example/item", scopes: scopes})

			sent := rec.requests()
			if len(sent) == 0 {
				t.Fatal("the Broker sent the Exchange nothing; the test needs the query it forwards")
			}
			for i, f := range sent {
				if f.path != forav1connect.ExchangeServiceDiscoverResourcesProcedure {
					t.Fatalf("request %d went to %s, want %s", i, f.path,
						forav1connect.ExchangeServiceDiscoverResourcesProcedure)
				}
				var q forav1.ResourceQuery
				if err := forwardedCodec(t, f).Unmarshal(f.body, &q); err != nil {
					t.Fatalf("decode forwarded query %d: %v", i, err)
				}
				if got := q.GetRequester().GetScopes(); !slices.Equal(got, scopes) {
					t.Errorf("query %d: requester.scopes = %q, want %q, exactly what the agent sent",
						i, got, scopes)
				}
			}
		})
	}
}

// TestResolve_ForwardsRequesterDelegationUnchanged drives Resolve with a
// requester carrying a delegation. The query the Broker sends the Exchange
// carries the delegation byte for byte, token and plaintext fields alike: the
// Exchange verifies the chain itself, and a Broker that dropped it would hide
// every subscription-gated term from an agent that holds a grant. (Today's
// Exchange still hides gated terms on a Broker-originated query; forwarding is
// what a later Broker path that preserves the agent's signature needs.)
func TestResolve_ForwardsRequesterDelegationUnchanged(t *testing.T) {
	ex, rec := startRecordingExchange(t)
	fx := newFixture(t, context.Background(), fixtureOpts{providerDomain: "acme.example", upstream: ex})
	delegation := &forav1.Delegation{
		PrincipalDomain: "issuer.example", Scopes: []string{"subscription:acme.example:premium"},
		Token: []byte("eyJhbGciOiJFZERTQSJ9.e30.c2ln"),
	}

	resolveOverConnect(t, fx, reqOpts{uri: "https://acme.example/item", delegation: delegation})

	sent := rec.requests()
	if len(sent) == 0 {
		t.Fatal("the Broker sent the Exchange nothing; the test needs the query it forwards")
	}
	for i, f := range sent {
		var q forav1.ResourceQuery
		if err := forwardedCodec(t, f).Unmarshal(f.body, &q); err != nil {
			t.Fatalf("decode forwarded query %d: %v", i, err)
		}
		if got := q.GetRequester().GetDelegation(); !proto.Equal(got, delegation) {
			t.Errorf("query %d: requester.delegation = %v, want exactly what the agent sent %v", i, got, delegation)
		}
	}
}

// TestPurchase_ForwardsTheRequesterUnchanged drives ExecuteTransaction with a
// requester that states scopes and carries a delegation. The sub-request the
// Broker sends the Exchange carries that requester field for field: the
// acceptances are signatures over it, the Exchange selects terms by its scopes
// and verifies the delegation for a subscription-gated item.
func TestPurchase_ForwardsTheRequesterUnchanged(t *testing.T) {
	ex, rec := startRecordingExchange(t)
	env := newPurchaseTestEnvAt(t, ex, false)
	requester := &forav1.Requester{
		Id:     purchaseAgentDirectory,
		Domain: requesterDomain,
		Type:   forav1.RequesterType_REQUESTER_TYPE_AGENT,
		Scopes: []string{"subscription:premium", "dist:US:*"},
		Delegation: &forav1.Delegation{
			PrincipalDomain: "issuer.example", Token: []byte("eyJhbGciOiJFZERTQSJ9.e30.c2ln"),
		},
	}

	env.buy(t, env.purchaseRequestBy(t, "tx-requester-forwarded", requester, line{"offer-1", env.exchangeDom}))

	sent := rec.requests()
	if len(sent) != 1 {
		t.Fatalf("sub-requests = %d, want 1", len(sent))
	}
	if sent[0].path != forav1connect.ExchangeServiceExecuteTransactionProcedure {
		t.Fatalf("the sub-request went to %s, want %s", sent[0].path,
			forav1connect.ExchangeServiceExecuteTransactionProcedure)
	}
	var sub forav1.TransactionRequest
	if err := forwardedCodec(t, sent[0]).Unmarshal(sent[0].body, &sub); err != nil {
		t.Fatalf("decode the forwarded sub-request: %v", err)
	}
	if got := sub.GetRequester(); !proto.Equal(got, requester) {
		t.Errorf("forwarded requester = %v, want the agent's own %v", got, requester)
	}

	// A purchase the Broker sent leaves one VALIDATED record, counting the
	// Exchanges it went to.
	assertPurchaseAudit(t, env, "VALIDATED", "INFO", "")
	for _, rec := range env.logs.Records(t, "broker.execute_transaction") {
		if rec["outcome"] == "VALIDATED" && rec["exchanges"] != float64(1) {
			t.Errorf("VALIDATED counts %v exchanges, want the one it sent to: %v", rec["exchanges"], rec)
		}
	}
}

// unmarshaler is what proto.UnmarshalOptions and protojson.UnmarshalOptions
// share: the decode step of a Connect codec.
type unmarshaler interface {
	Unmarshal(b []byte, m proto.Message) error
}

// forwardedCodec returns the decoder for the Connect codec a recorded request's
// content type names.
func forwardedCodec(t *testing.T, f forwarded) unmarshaler {
	t.Helper()
	switch {
	case strings.HasPrefix(f.contentType, "application/proto"):
		return proto.UnmarshalOptions{}
	case strings.HasPrefix(f.contentType, "application/json"):
		return protojson.UnmarshalOptions{}
	default:
		t.Fatalf("the Broker sent content type %q, which is neither Connect codec", f.contentType)
		return nil
	}
}

// TestPurchase_GatedItemWithoutAcceptanceIsForwarded pins the Broker's admission
// of a subscription-gated item that carries no acceptance: the protocol answers
// it with a per-item DELEGATION_INVALID denial at the Exchange, so the Broker
// forwards the item as it is, acceptance absent, instead of refusing the whole
// purchase. A non-gated item without an acceptance is still refused by the
// Broker before anything is sent.
func TestPurchase_GatedItemWithoutAcceptanceIsForwarded(t *testing.T) {
	ex, rec := startRecordingExchange(t)
	env := newPurchaseTestEnvAt(t, ex, false)

	req := env.purchaseRequest(t, "tx-gated-no-acceptance", line{"offer-gated", env.exchangeDom}, line{"offer-plain", env.exchangeDom})
	req.Items[0].Offer.Terms[0].Scopes = []string{"subscription:publisher.example:premium"}
	req.Items[0].AgentAcceptance = nil
	env.buy(t, req)

	sent := rec.requests()
	if len(sent) != 1 {
		t.Fatalf("sub-requests = %d, want 1", len(sent))
	}
	var sub forav1.TransactionRequest
	if err := forwardedCodec(t, sent[0]).Unmarshal(sent[0].body, &sub); err != nil {
		t.Fatalf("decode the forwarded sub-request: %v", err)
	}
	if len(sub.GetItems()) != 2 || sub.GetItems()[0].GetAgentAcceptance() != nil || sub.GetItems()[1].GetAgentAcceptance() == nil {
		t.Fatalf("forwarded items = %v, want the gated item without acceptance and the plain item with it", sub.GetItems())
	}

	plain := env.purchaseRequest(t, "tx-plain-no-acceptance", line{"offer-plain-2", env.exchangeDom})
	plain.Items[0].AgentAcceptance = nil
	ce := env.refused(t, plain)
	if ce.Code() != connect.CodeInvalidArgument || !strings.Contains(ce.Message(), "agent_acceptance") {
		t.Fatalf("plain item without acceptance: %v, want InvalidArgument naming agent_acceptance", ce)
	}
	if len(rec.requests()) != 1 {
		t.Fatal("the refused purchase reached the Exchange")
	}
}
