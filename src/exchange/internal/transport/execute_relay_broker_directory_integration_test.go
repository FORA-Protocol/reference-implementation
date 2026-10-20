//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// The re-packaged relay: what the production Broker actually sends on execute.
//
// The Broker cannot forward the agent's transport signature. It fans a batch out
// per offer.exchange, which changes the body, and an RFC 9421 signature covers
// the body — so the Broker rebuilds each request and signs it alone. The
// Exchange therefore sees ONE signature, the Broker's, with the Broker's own
// Signature-Agent.
//
// Two things follow, and these tests pin both. The relay must be recognised from
// the Broker's DIRECTORY HOST rather than from a signature count: counting
// signatures classified every real relay as agent-direct, which left the tenant's
// allow_broker_relay opt-out unenforced. And the agent must come from
// Requester.domain, the one field that names it and that the detached body
// acceptance signs.
//
// Round-trip honesty: every leg drives the public Connect-Go ExecuteTransaction
// RPC end to end and asserts back through it. Side-effect absence is read through
// the production repository surface and the billing adapter (Testing Doctrine
// §9 tier 2 — no RPC reads a transaction row back), never raw SQL.

// TestExecuteRelay_ConfiguredBrokerDirectoryIsARelayWithoutASeededRow is the
// classification positive: a SINGLE-signature request whose Signature-Agent is the
// configured Broker directory is treated as a relay, and no fora.agents row with
// requester_type = 'BROKER' is seeded for it.
//
// The tenant's allow_broker_relay is left FALSE, so the relay gate is what answers.
// PermissionDenied is therefore the proof of classification: an unrecognised relay
// would have been executed as a direct request and returned a signed URL. Nothing
// is charged, no claim is taken, and no transaction is persisted.
func TestExecuteRelay_ConfiguredBrokerDirectoryIsARelayWithoutASeededRow(t *testing.T) {
	h := newTestHarness(t)
	// allow_broker_relay defaults to FALSE — deliberately not enabled.
	uri := seedResourceWithRate(t, h, "/articles/broker-directory-relay", "0.05")
	offer := discoverOffer(t, h, uri)

	// No agents row for the Broker: only its signing key is registered with the
	// transport resolver. The directory host is the whole classification.
	if _, err := repo.NewAgentRepo(h.queries).ByID(h.ctx, harnessBrokerHost); err == nil {
		t.Fatalf("an agents row already exists for %q; this test must prove the relay is "+
			"recognised without one", harnessBrokerHost)
	}
	brokerClient := h.brokerOnlyClient(t)

	const txID = "tx-broker-directory-relay"
	_, err := executeBrokerRelay(t, h, brokerClient, txID, offer)
	assertRelayNotAccepted(t, err)
	assertNoTransaction(t, h, derivedTxKey(txID, offer))
	assertBalanceUnchanged(t, h)

	// The Broker was not registered as a side effect of being recognised.
	if _, err := repo.NewAgentRepo(h.queries).ByID(h.ctx, harnessBrokerHost); err == nil {
		t.Errorf("recognising the relay created an agents row for %q; the directory host "+
			"is the classification and needs no registration", harnessBrokerHost)
	}
}

// TestExecuteRelay_AgentResolvedFromRequesterDomain is the relay positive: with
// allow_broker_relay = true the re-packaged request succeeds, and every identity
// it produces is the AGENT's — resolved from Requester.domain, never from the
// Broker's directory and never from the free-label Requester.id.
func TestExecuteRelay_AgentResolvedFromRequesterDomain(t *testing.T) {
	h := newTestHarness(t)
	h.enableBrokerRelay(t, h.tenantID)
	uri := seedResourceWithRate(t, h, "/articles/relay-domain-agent", "0.05")
	offer := discoverOffer(t, h, uri)

	brokerClient := h.brokerOnlyClient(t)
	const txID = "tx-relay-domain-agent"
	resp, err := executeBrokerRelay(t, h, brokerClient, txID, offer)
	if err != nil {
		t.Fatalf("re-packaged relay with allow_broker_relay = true: %v", err)
	}

	// The delivery URL binds the AGENT's key, which is the key the directory named
	// by Requester.domain publishes — never the Broker's.
	wantThumb, err := helpers.Thumbprint(h.callerPub)
	if err != nil {
		t.Fatalf("thumbprint: %v", err)
	}
	if got := resp.Msg.GetAgentIdentityHash(); got != wantThumb {
		t.Errorf("agent_identity_hash = %q, want the AGENT thumbprint %q", got, wantThumb)
	}

	// The ledger is keyed on the agent's directory host, and the agent — not the
	// Broker — is charged the signed price. Tier-2 fallback: no RPC reads a
	// transaction row back.
	rec, err := repo.NewTransactionRepo(h.queries).ByIdempotencyKey(h.ctx, derivedTxKey(txID, offer))
	if err != nil {
		t.Fatalf("ByIdempotencyKey: %v", err)
	}
	if rec.AgentID != h.callerID {
		t.Errorf("transaction_log.agent_id = %q, want the domain host %q", rec.AgentID, h.callerID)
	}
	bal, err := h.billing.GetBalance(h.ctx, h.billingRef, harnessCurrency)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if want := mustBillingAmount(t, "9.95", "USD"); bal.Value.Cmp(want.Value) != 0 {
		t.Errorf("agent balance = %s, want 9.95 — the relayed charge must land on the "+
			"agent's account", bal.Value.FloatString(4))
	}
}

// TestExecuteRelay_RequesterDomainNamingNoHostIsInvalidRequest is the relay
// negative for an unusable domain: the transport header names the Broker, so
// Requester.domain is the only field left that can name the agent, and a value
// that is not a host cannot name one. Every such request is refused
// InvalidArgument with no side effect, and there is deliberately NO fallback to
// Requester.id.
//
// Two layers refuse, and the test drives both because only one of them is
// reachable for any given value. The protocol's own pattern on Requester.domain
// admits host-shaped strings only, so an empty value and a URN are refused by the
// protovalidate interceptor before the service is entered. A value that satisfies
// that pattern but still names no host — "999.1.1.1" ends in a numeric label, so
// it can only be an IPv4 address, and it is not a valid one — reaches the service,
// which refuses it and names the offending field in the typed error detail
// (ADR-019 §1).
//
// The service-level refusal also happens BEFORE any agent-directory fetch. That is
// what the error code proves: this harness's registry refuses self-signup, so a
// registration attempt for an unregistered agent surfaces as Unavailable. An
// InvalidArgument therefore means nothing was fetched.
func TestExecuteRelay_RequesterDomainNamingNoHostIsInvalidRequest(t *testing.T) {
	h := newTestHarness(t)
	h.enableBrokerRelay(t, h.tenantID)
	uri := seedResourceWithRate(t, h, "/articles/relay-bad-domain", "0.05")
	offer := discoverOffer(t, h, uri)
	brokerClient := h.brokerOnlyClient(t)

	cases := []struct {
		name string
		// domain is the value the Broker presents.
		domain string
		// serviceRefused is true when the value clears the protocol pattern and so
		// reaches resolveAgent, which names requester.domain in the error detail.
		serviceRefused bool
	}{
		{name: "empty", domain: ""},
		{name: "urn", domain: "urn:not-a-host"},
		{name: "host-shaped-but-no-host", domain: "999.1.1.1", serviceRefused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			txID := "tx-relay-bad-domain-" + tc.name
			requester := &forav1.Requester{
				Id:     "urn:relay:probe",
				Domain: tc.domain,
				Type:   forav1.RequesterType_REQUESTER_TYPE_AGENT,
			}
			// The SDK refuses to sign an acceptance that names no requester domain,
			// so no agent can produce one. The empty case is therefore a Broker
			// blanking the domain on a request whose acceptance the agent signed
			// for its own host; the request's Requester is what is refused.
			signed := requester
			if tc.domain == "" {
				signed = &forav1.Requester{Id: requester.GetId(), Domain: "agent.example", Type: requester.GetType()}
			}
			_, err := brokerClient.ExecuteTransaction(h.ctx, connect.NewRequest(&forav1.TransactionRequest{
				Ver:            helpers.ProtocolVersion,
				IdempotencyKey: txID,
				Requester:      requester,
				Items: []*forav1.TransactionItem{
					{Offer: offer, AgentAcceptance: signAcceptanceFor(t, h.callerPriv, offer, signed, txID)},
				},
			}))
			if err == nil {
				t.Fatal("a relayed request whose requester.domain names no host was accepted; " +
					"it has no agent identity at all")
			}
			assertConnectCode(t, err, connect.CodeInvalidArgument)
			if tc.serviceRefused {
				assertReportRejectionField(t, err, "requester.domain")
			}
			assertNoTransaction(t, h, derivedTxKey(txID, offer))
			assertBalanceUnchanged(t, h)
		})
	}
}

// TestExecuteRelay_AcceptanceNotSignedByTheDomainAgentDenied is the relay negative
// that carries the path's actual security property: the Broker chooses
// Requester.domain, so the agent it names must be the agent that signed the body
// acceptance. A Broker naming a real agent while presenting an acceptance signed
// by some other key gets nothing.
//
// The item-level split is preserved: an unverifiable acceptance is an in-body
// SIGNATURE_INVALID denial, not a transport error, so a batch's other items still
// reach their own verdicts.
func TestExecuteRelay_AcceptanceNotSignedByTheDomainAgentDenied(t *testing.T) {
	h := newTestHarness(t)
	h.enableBrokerRelay(t, h.tenantID)
	uri := seedResourceWithRate(t, h, "/articles/relay-wrong-signer", "0.05")
	offer := discoverOffer(t, h, uri)
	brokerClient := h.brokerOnlyClient(t)

	const txID = "tx-relay-wrong-signer"
	requester := h.relayRequester()
	// A key no directory publishes for this agent. The Broker names the real agent
	// in domain but cannot produce its signature.
	_, foreignPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("foreign keypair: %v", err)
	}
	resp, execErr := brokerClient.ExecuteTransaction(h.ctx, connect.NewRequest(&forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: txID,
		Requester:      requester,
		Items: []*forav1.TransactionItem{
			{Offer: offer, AgentAcceptance: signAcceptanceFor(t, foreignPriv, offer, requester, txID)},
		},
	}))
	assertItemDenied(t, resp, execErr, forav1.DenialReason_DENIAL_REASON_SIGNATURE_INVALID)
	assertNoTransaction(t, h, derivedTxKey(txID, offer))
	assertBalanceUnchanged(t, h)
}
