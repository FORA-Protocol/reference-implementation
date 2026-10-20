//go:build integration

package exchangetest

import (
	"crypto/ed25519"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/types/known/structpb"
)

// The protocol messages a suite sends, with the envelope every one of them
// repeats: the protocol version and the Exchange addressed. They are written
// once here because a required envelope field once cost the Exchange's own
// suites 179 hand edits across call sites that each restated it.
//
// Every builder returns the message rather than sending it, so a caller that
// needs one more field sets it on the result, and a message with something
// deliberately wrong in it keeps its literal at the call site, because that is
// the test.

// PushRequest is a catalog push to exchange under tenantID, sent as callerID.
func PushRequest(exchange, tenantID, callerID string, entries []*forav1.ResourceEntry) *forav1.PushResourcesRequest {
	return &forav1.PushResourcesRequest{
		Exchange: exchange,
		Ver:      helpers.ProtocolVersion,
		TenantId: tenantID,
		CallerId: callerID,
		Entries:  entries,
	}
}

// UsageReport is a report against the obligation transactionID names.
// billingID is empty for an obligation that carries none.
func UsageReport(exchange, idempotencyKey, transactionID, billingID string, usage *forav1.Usage) *forav1.UsageReport {
	return &forav1.UsageReport{
		Exchange:       exchange,
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: idempotencyKey,
		TransactionId:  transactionID,
		BillingId:      billingID,
		Usage:          usage,
	}
}

// ResourceQuery is a discovery query for uris on behalf of requester.
func ResourceQuery(exchange string, requester *forav1.Requester, uris []string) *forav1.ResourceQuery {
	return &forav1.ResourceQuery{
		Exchange:  exchange,
		Ver:       helpers.ProtocolVersion,
		Uris:      uris,
		Requester: requester,
	}
}

// RegisterRequest is an agent registration. data is the free-form payload the
// agent supplies, nil for none. termsDigest names the terms revision the agent
// accepts; empty leaves the optional field unset, which is a different message
// from an empty string: it is the "accepted nothing" case an Exchange that
// publishes a digest refuses.
func RegisterRequest(exchange string, data *structpb.Struct, termsDigest string) *forav1.RegisterRequest {
	req := &forav1.RegisterRequest{
		Exchange:         exchange,
		Ver:              helpers.ProtocolVersion,
		RegistrationData: data,
	}
	if termsDigest != "" {
		req.TermsDigest = &termsDigest
	}
	return req
}

// AccountStatusRequest is the account-status call. It carries nothing but the
// envelope, because the Exchange resolves the account from the signature.
func AccountStatusRequest(exchange string) *forav1.GetAccountStatusRequest {
	return &forav1.GetAccountStatusRequest{
		Exchange: exchange,
		Ver:      helpers.ProtocolVersion,
	}
}

// Requester is the agent identity a query or an execute carries.
func Requester(id, domain string, scopes ...string) *forav1.Requester {
	return &forav1.Requester{
		Id:     id,
		Domain: domain,
		Type:   forav1.RequesterType_REQUESTER_TYPE_AGENT,
		Scopes: scopes,
	}
}

// OfferAcceptance is the body acceptance of offer for the execute keyed
// idempotencyKey, signed with priv over the exact requester the request will
// carry: the payload covers the requester's id and domain, and the key must be
// the one the agent is registered with.
func OfferAcceptance(
	t testing.TB, priv ed25519.PrivateKey, offer *forav1.Offer, requester *forav1.Requester, idempotencyKey string,
) *forav1.AgentAcceptance {
	t.Helper()
	sig, err := helpers.SignOfferAcceptance(priv, offer, requester, idempotencyKey)
	if err != nil {
		t.Fatalf("SignOfferAcceptance: %v", err)
	}
	return &forav1.AgentAcceptance{
		Signature:          sig,
		SignatureAlgorithm: helpers.AcceptanceSignatureAlgorithm,
	}
}

// RequestAcceptance is the agent's acceptance of the whole request, signed
// with priv.
func RequestAcceptance(
	t testing.TB, priv ed25519.PrivateKey, req *forav1.TransactionRequest,
) *forav1.AgentRequestAcceptance {
	t.Helper()
	acceptance, err := helpers.SignRequestAcceptance(priv, req)
	if err != nil {
		t.Fatalf("SignRequestAcceptance: %v", err)
	}
	return acceptance
}

// Purchase is one agent buying one offer: the agent's key and the identity its
// requests carry, the offer, and the idempotency key the transaction runs
// under.
type Purchase struct {
	Priv      ed25519.PrivateKey
	Requester *forav1.Requester
	Offer     *forav1.Offer
	Key       string
}

// Request is the purchase's transaction request with the item's acceptance
// signed and no acceptance of the request as a whole: the wire-compatible form
// a client that predates the request-level proof sends, which the Exchange
// executes on its per-item path.
func (p Purchase) Request(t testing.TB) *forav1.TransactionRequest {
	t.Helper()
	return &forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: p.Key,
		Requester:      p.Requester,
		Items: []*forav1.TransactionItem{{
			Offer: p.Offer, AgentAcceptance: OfferAcceptance(t, p.Priv, p.Offer, p.Requester, p.Key),
		}},
	}
}

// SignedRequest is Request with the agent's acceptance of the whole request
// signed too, the form a current client sends.
func (p Purchase) SignedRequest(t testing.TB) *forav1.TransactionRequest {
	t.Helper()
	req := p.Request(t)
	req.AgentRequestAcceptance = RequestAcceptance(t, p.Priv, req)
	return req
}

// Delivered is the one item of a single-item transaction's answer, which must
// have been delivered. A denial fails the test: it arrives in the body of a
// 200, so a caller checking only the error would carry on with empty ids.
// what names the purchase in the failure.
func Delivered(t testing.TB, resp *forav1.TransactionResponse, what string) *forav1.TransactionResultItem {
	t.Helper()
	items := resp.GetItems()
	if len(items) != 1 {
		t.Fatalf("execute %s answered %d items, want 1: %v", what, len(items), resp)
	}
	if items[0].GetRetrievalEndpoint() == "" {
		t.Fatalf("execute %s was not delivered: %v", what, items[0].GetDenialReason())
	}
	return items[0]
}
