//go:build integration

package transport_test

// The Broker's handling of a transport-level outcome after it has sent a
// sub-request: an Exchange address that answers 503, one that drops the
// connection, and one that answers OK with no items. The call still succeeds:
// the refusal rides in the body on each affected item as an UpstreamRefusal.
// The Broker never turns an upstream refusal into an error of its own, and
// never stamps a denial_reason on an item an Exchange did not deny.
//
// Each address here is a wireExchange, a network condition rather than an
// emulated Exchange: it answers the same thing whatever the Broker sent. An
// Exchange's own refusal of a relayed sub-request, carried on its items beside
// another Exchange's delivered result, is proven against real Exchanges by
// tests/e2e/harness/test_broker_money_path.py
// (test_an_exchange_that_refuses_the_relay_refuses_only_its_own_items).

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// assertPurchaseAudit fails unless exactly one purchase audit record carries
// outcome, and that one is at level and names exchange. The record is the
// operator's only view of which Exchange refused or was refused.
func assertPurchaseAudit(t *testing.T, env purchaseTestEnv, outcome, level, exchange string) {
	t.Helper()
	var found []map[string]any
	for _, rec := range env.logs.Records(t, "broker.execute_transaction") {
		if rec["outcome"] == outcome {
			found = append(found, rec)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d purchase audit records with outcome %s, want 1: %v", len(found), outcome, found)
	}
	named, _ := found[0]["exchange"].(string)
	if found[0]["level"] != level || named != exchange {
		t.Errorf("audit %s = %v, want level %s naming %s", outcome, found[0], level, exchange)
	}
}

// assertRefusalOnly checks an item carries exactly an UpstreamRefusal from
// exchange with code, and no result field an Exchange would set.
func assertRefusalOnly(t *testing.T, item *forav1.TransactionResultItem, exchange, code string) {
	t.Helper()
	refusal := item.GetRefusal()
	if refusal == nil {
		t.Fatalf("item %s carries no refusal: %v", item.GetOfferId(), item)
	}
	if refusal.GetParty() != exchange || refusal.GetCode() != code {
		t.Errorf("item %s refusal = %s / %q, want %s / %q",
			item.GetOfferId(), refusal.GetParty(), refusal.GetCode(), exchange, code)
	}
	if item.DenialReason != nil {
		t.Errorf("item %s carries denial_reason %v; the Broker must never stamp one on an item "+
			"its Exchange did not deny", item.GetOfferId(), item.GetDenialReason())
	}
	if item.GetTransactionId() != "" || item.GetRetrievalEndpoint() != "" || item.GetCost() != nil {
		t.Errorf("item %s carries result fields beside its refusal: %v", item.GetOfferId(), item)
	}
}

// TestPurchase_AnExchangeAnsweringUnavailableIsAReportedRefusal pins that a
// transport-class answer is reported, not replaced: the code the address
// answered with rides on the item, and no detail is invented when none came.
// The address answers HTTP 503 with no body, as an overloaded proxy in front of
// an Exchange does, so there is no Exchange answer here to emulate.
func TestPurchase_AnExchangeAnsweringUnavailableIsAReportedRefusal(t *testing.T) {
	env := newPurchaseTestEnvAt(t, startUnavailableExchange(t), false)

	resp := env.buy(t, env.purchaseRequest(t, "tx-unavailable", line{"offer-1", env.exchangeDom}))

	item := resp.GetItems()[0]
	assertRefusalOnly(t, item, env.exchangeDom, "unavailable")
	if item.GetRefusal().GetDetail() != nil {
		t.Errorf("refusal detail = %v, want none: the answer carried none", item.GetRefusal().GetDetail())
	}
	if env.exchange.arrivals() != 1 {
		t.Errorf("sub-requests = %d, want 1", env.exchange.arrivals())
	}
	assertPurchaseAudit(t, env, "UPSTREAM_REFUSED", "WARN", env.exchangeDom)
}

// TestPurchase_AnExchangeThatDoesNotAnswerIsAnUnavailableRefusal drives an
// Exchange address that is registered, healthy and advertising its endpoint,
// and that drops the connection when the sub-request arrives. There is no
// answer to report, so the refusal carries the code the Broker's own call
// failed with. A dropped connection is a network condition, not an Exchange
// answer.
func TestPurchase_AnExchangeThatDoesNotAnswerIsAnUnavailableRefusal(t *testing.T) {
	env := newPurchaseTestEnv(t)
	silent := env.addWireExchange(t, startSilentExchange(t))

	resp := env.buy(t, env.purchaseRequest(t, "tx-no-answer", line{"offer-1", silent}))

	assertRefusalOnly(t, resp.GetItems()[0], silent, "unavailable")
}

// TestPurchase_AnUnattributableAnswerIsAnInternalRefusal pins the one case the
// Broker cannot pass on: an answer that is OK without exactly one result per
// item sent. None of that answer reaches the agent; every item of the
// sub-request is reported refused with code internal and no detail.
//
// The address answers 200 with an empty body, which the Broker's Connect client
// decodes as a TransactionResponse with no items. It gives that answer to any
// request, so it is a malformed answer on the wire, not an emulated Exchange.
func TestPurchase_AnUnattributableAnswerIsAnInternalRefusal(t *testing.T) {
	env := newPurchaseTestEnvAt(t, startShortAnswerExchange(t), false)

	resp := env.buy(t, env.purchaseRequest(t, "tx-short-answer",
		line{"offer-1", env.exchangeDom}, line{"offer-2", env.exchangeDom}))

	for _, item := range resp.GetItems() {
		assertRefusalOnly(t, item, env.exchangeDom, "internal")
		if item.GetRefusal().GetDetail() != nil {
			t.Errorf("item %s refusal carries a detail, want none", item.GetOfferId())
		}
	}
	// The record names the branch: an answer that failed to decode would also be
	// refused "internal", but as UPSTREAM_REFUSED.
	assertPurchaseAudit(t, env, "UPSTREAM_UNATTRIBUTABLE", "WARN", env.exchangeDom)
}
