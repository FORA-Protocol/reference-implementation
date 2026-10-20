//go:build integration

package transport_test

import (
	"regexp"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// At the pinned protocol a usage report for a transaction the Exchange recorded
// under a different agent's account is answered exactly as a report for an
// unknown transaction, with the Connect code not_found, so the answer does not
// reveal that the transaction exists. A Broker files no report for an agent, so
// a report a Broker signs falls under the same rule, whatever the tenant's relay
// setting. In every case the obligation stays PENDING with no validation
// outcome, read through the operator evidence route (assertObligationState).
//
// The Exchange still answers these reports by the old rule, so each report leg
// below is a strict expected failure, held only while the failure is its defect's
// own. The arrange legs run outside the marker, so a failure there fails the
// test. So does the PENDING check wherever it holds today: the first and third
// tests' reports are refused today too, so their obligation stays PENDING before
// and after the fix, and that check runs on t. In the second, today's acceptance
// moves the obligation, so its PENDING check stays inside the marker. Once the
// Exchange answers as the protocol says, a test fails as XPASS(strict); the fix
// then removes the marker, and the body runs directly on t.
//
// The typed USAGE_REPORT_REJECTION_REASON_TRANSACTION_NOT_FOUND the protocol
// pairs with not_found is not asserted here. The Exchange sends no typed usage
// report rejection on any refusal yet, a defect the e2e harness holds on its own
// (USAGE_REPORT_REFUSAL_UNTYPED), and asserting it here would make these markers
// hold for two defects at once.

// TestReportUsage_AnotherAgentsTransaction_AnsweredAsUnknown: agent-A
// (registered with tenant A) executes a transaction; agent-B (registered with
// tenant B, signing with its own key) files a usage report against agent-A's
// (transaction_id, billing_id).
func TestReportUsage_AnotherAgentsTransaction_AnsweredAsUnknown(t *testing.T) {
	h := newTestHarness(t) // tenant A + agent-test
	txID, billingID := executeTransactionFor(t, h, 100)
	_, agentBClient := h.addTenant(t, "tenantb", "agent-b")

	testutil.StrictXFail(t, reportOnAnotherAccountRevealed, reportOnAnotherAccountRevealedSignature,
		func(tb testing.TB) {
			_, err := agentBClient.ReportUsage(h.ctx, connect.NewRequest(
				newUsageReport("r-cross", txID, billingID, &forav1.Usage{ConsumedQuantity: 100})))
			assertConnectCode(tb, err, connect.CodeNotFound)
		})
	// Holds today and after the fix: another agent's report records nothing.
	assertObligationState(t, h, txID, "PENDING", "")
}

// TestReportUsage_BrokerSigned_RelayAllowed_AnsweredAsUnknown: a registered
// BROKER caller files a report on the agent's transaction at a tenant that
// accepts relayed purchases. Accepting relays does not let a Broker report.
func TestReportUsage_BrokerSigned_RelayAllowed_AnsweredAsUnknown(t *testing.T) {
	h := newTestHarness(t)
	txID, billingID := executeTransactionFor(t, h, 100)
	h.enableBrokerRelay(t, h.tenantID)
	brokerClient := h.addCaller(t, "broker-trusted", "BROKER")

	testutil.StrictXFail(t, brokerReportAccepted, brokerReportAcceptedSignature, func(tb testing.TB) {
		resp, err := brokerClient.ReportUsage(h.ctx, connect.NewRequest(
			newUsageReport("r-broker-ok", txID, billingID, &forav1.Usage{ConsumedQuantity: 100})))
		if err == nil {
			tb.Fatalf("a usage report a Broker signed was accepted (report_id %q); a Broker files no "+
				"report for an agent, so it is answered as one for an unknown transaction",
				resp.Msg.GetReportId())
		}
		assertAnsweredAsUnknownTransaction(tb, h, err, txID)
	})
}

// TestReportUsage_BrokerSigned_RelayRefused_AnsweredAsUnknown: the same report
// from a registered BROKER caller at a tenant that has NOT opted into relayed
// purchases.
func TestReportUsage_BrokerSigned_RelayRefused_AnsweredAsUnknown(t *testing.T) {
	h := newTestHarness(t)
	txID, billingID := executeTransactionFor(t, h, 100)
	// Deliberately do NOT call h.enableBrokerRelay — tenant's flag stays false.
	brokerClient := h.addCaller(t, "broker-untrusted", "BROKER")

	testutil.StrictXFail(t, reportOnAnotherAccountRevealed, reportOnAnotherAccountRevealedSignature,
		func(tb testing.TB) {
			_, err := brokerClient.ReportUsage(h.ctx, connect.NewRequest(
				newUsageReport("r-broker-no", txID, billingID, &forav1.Usage{ConsumedQuantity: 100})))
			assertConnectCode(tb, err, connect.CodeNotFound)
		})
	// Holds today and after the fix: a Broker's report records nothing.
	assertObligationState(t, h, txID, "PENDING", "")
}

// assertAnsweredAsUnknownTransaction fails unless err answers a usage report
// the way a report for an unknown transaction is answered — the Connect code
// not_found — and the obligation is still PENDING with no validation outcome.
func assertAnsweredAsUnknownTransaction(tb testing.TB, h *testHarness, err error, txID string) {
	tb.Helper()
	assertConnectCode(tb, err, connect.CodeNotFound)
	assertObligationState(tb, h, txID, "PENDING", "")
}

// reportOnAnotherAccountRevealed is the known defect two of the tests above
// document: the Exchange answers a usage report on a transaction recorded under
// another account with permission_denied, which tells the caller that the
// transaction exists. Its signature is the Connect-code assertion of
// assertAnsweredAsUnknownTransaction.
const reportOnAnotherAccountRevealed = "the Exchange answers a usage report on another account's transaction " +
	"permission_denied, revealing that the transaction exists"

var reportOnAnotherAccountRevealedSignature = regexp.MustCompile(`^code = permission_denied \(msg .*\), want not_found$`)

// brokerReportAccepted is the known defect the relay-allowed test documents: the
// Exchange accepts a usage report a Broker signs when the tenant allows relays.
// Its signature is the body's own refusal of the acceptance.
const brokerReportAccepted = "the Exchange accepts a usage report a Broker signs when the tenant allows relays"

var brokerReportAcceptedSignature = regexp.MustCompile(`^a usage report a Broker signed was accepted`)

// TestExecuteTransaction_CrossTenantRejected mirrors the authz boundary
// for ExecuteTransaction: agent-B (signing with its own key) attempts to
// execute a transaction naming requester.id="agent-test" (which belongs
// to tenant A). Authz MUST reject with PermissionDenied.
// TestExecuteTransaction_CrossTenantRejected pins the cross-identity guard under
// the R4 contract: agent identity is proven by the BODY offer-acceptance
// signature against the CLAIMED agent's REGISTERED key. A caller from another
// tenant (agent-c) cannot forge an agent-test acceptance — it lacks agent-test's
// private key — so a request claiming requester.id = agent-test but carrying an
// acceptance signed by a foreign key is rejected SIGNATURE_INVALID, with no side
// effect. (Pre-R4 this was a transport-keyid PermissionDenied "may not act"; R4
// relocates identity from the transport sig to the body acceptance.)
func TestExecuteTransaction_CrossTenantRejected(t *testing.T) {
	h := newTestHarness(t)
	seedCatalog(t, h)
	offers := discoverFirst(t, h)
	offer := offers[0]

	_, agentBClient := h.addTenant(t, "tenantc", "agent-c")

	const txID = "tx-cross"
	requester := newRequester("agent-test", "agent.example")
	// Falsely claim to be agent-test while supplying an acceptance signed by a
	// key that is NOT agent-test's registered key.
	forged := mintWrongKeyAcceptanceFor(t, offer, requester, txID)
	resp, err := agentBClient.ExecuteTransaction(h.ctx, connect.NewRequest(&forav1.TransactionRequest{
		Ver: helpers.ProtocolVersion, IdempotencyKey: txID,
		Requester: requester,
		Items:     []*forav1.TransactionItem{{Offer: offer, AgentAcceptance: forged}},
	}))
	// The forged acceptance → KindSignatureInvalid, a denial-map kind → in-body
	// per-item denial after the C4 collapse (Flag #1). Strength preserved:
	// SIGNATURE_INVALID + no transaction.
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_SIGNATURE_INVALID)
	assertNoTransaction(t, h, txID+":"+offer.GetOfferId())
}
