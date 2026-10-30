//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// executeDelegation is the arrangement the execute-side gate scenarios share: a
// FREE subscription-gated term and a FREE public term on the harness site, the
// subscription record trusting one issuer key, and a deterministic service
// clock the chains are minted against. The harness's default caller
// (agent-test, signing with callerPriv) is the agent whose acceptance key every
// chain is bound to, on the direct and the relayed path alike.
type executeDelegation struct {
	h        *testHarness
	clk      *clock.DeterministicClock
	logs     *testutil.LogCapture
	scope    string
	rootPriv ed25519.PrivateKey
	trust    repo.SubscriptionTrustRepo
	gatedURI string
	pubURI   string
}

func newExecuteDelegation(t *testing.T) *executeDelegation {
	t.Helper()
	clk := clock.NewDeterministic(time.Now().UTC().Truncate(time.Second))
	logs, logger := testutil.NewLogCapture()
	h := newTestHarnessWith(t, harnessOptions{clk: clk, logger: logger})
	h.enableBrokerRelay(t, h.tenantID)
	d := &executeDelegation{h: h, clk: clk, logs: logs, scope: "subscription:" + h.siteDomain + ":premium"}
	gated := exchangetest.FreeTerm()
	gated.Scopes = []string{d.scope}
	if _, err := h.catalogClient.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, "agent-test", []*forav1.ResourceEntry{
		{Domain: h.siteDomain, Path: "/sub/gated", Terms: []*forav1.LicenseTerm{gated}},
		{Domain: h.siteDomain, Path: "/sub/public", Terms: []*forav1.LicenseTerm{seedFreeTerm()}},
	}))); err != nil {
		t.Fatalf("push: %v", err)
	}
	d.gatedURI, d.pubURI = "https://"+h.siteDomain+"/sub/gated", "https://"+h.siteDomain+"/sub/public"
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d.rootPriv = rootPriv
	// Arranged through the repository port: no RPC creates a subscription record
	// yet (Testing Doctrine point 9, tier 2); the administration plane that will
	// is owned by the follow-up work on subscription records.
	d.trust = repo.NewSubscriptionTrustRepo(h.queries)
	if _, err := d.trust.Create(h.ctx, repo.SubscriptionRecord{
		TenantID: h.tenantID, Scope: d.scope, IssuerIdentity: issuerID, IssuerKeys: []ed25519.PublicKey{rootPub},
	}); err != nil {
		t.Fatal(err)
	}
	return d
}

// chain is the happy-path chain: one hour of life from the service clock,
// bound to the harness caller's key, covering the gated term's scope.
func (d *executeDelegation) chain() testutil.DelegationChain {
	return testutil.DelegationChain{
		Keys: []ed25519.PrivateKey{d.rootPriv}, Holder: d.h.callerPub, RootIssuer: issuerID,
		Audience: harnessExchangeDomain, Scope: d.scope, Exp: d.clk.Now().Add(time.Hour),
	}
}

// gatedOffer discovers a fresh signed offer for the gated term through the
// direct discovery gate with a valid chain, which is the only way to get one.
func (d *executeDelegation) gatedOffer(t *testing.T) *forav1.Offer {
	t.Helper()
	requester := newRequester("agent-test", "agent.example")
	requester.Delegation = &forav1.Delegation{Token: testutil.MintDelegation(t, d.chain())}
	resp, err := d.h.exchangeClient.DiscoverResources(d.h.ctx, connect.NewRequest(newResourceQuery(requester, []string{d.gatedURI})))
	if err != nil || len(resp.Msg.GetOffers()) != 1 {
		t.Fatalf("discover gated offer: %v (offers=%d)", err, len(resp.Msg.GetOffers()))
	}
	return resp.Msg.GetOffers()[0]
}

// item is one batch item; a nil acceptance leaves the item unaccepted.
type gatedItem struct {
	offer    *forav1.Offer
	accepted bool
}

// request builds a request for client with the given requester (the chain goes
// on it as Delegation), signing every accepted item and the complete-set proof
// with the harness caller's key.
func (d *executeDelegation) request(t *testing.T, requester *forav1.Requester, token []byte, idem string, items ...gatedItem) *forav1.TransactionRequest {
	t.Helper()
	if token != nil {
		requester.Delegation = &forav1.Delegation{Token: token}
	}
	req := &forav1.TransactionRequest{Ver: helpers.ProtocolVersion, IdempotencyKey: idem, Requester: requester}
	for _, it := range items {
		item := &forav1.TransactionItem{Offer: it.offer}
		if it.accepted {
			item.AgentAcceptance = signAcceptanceFor(t, d.h.callerPriv, it.offer, requester, idem)
		}
		req.Items = append(req.Items, item)
	}
	req.AgentRequestAcceptance = signRequestAcceptanceFor(t, d.h.callerPriv, req)
	return req
}

func (d *executeDelegation) direct() foraconnect.ExchangeServiceClient { return d.h.exchangeClient }

func (d *executeDelegation) directRequester() *forav1.Requester {
	return newRequester("agent-test", "agent.example")
}

func (d *executeDelegation) assertRefusalJournaled(t *testing.T, failure string) {
	t.Helper()
	for _, rec := range d.logs.Records(t, "exchange.execute_transaction") {
		msg, _ := rec["err"].(string)
		if rec["outcome"] == "DELEGATION_REFUSED" && strings.Contains(msg, failure) {
			return
		}
	}
	t.Fatalf("no DELEGATION_REFUSED audit line naming %q", failure)
}

// TestExecute_GatedItemDirectAndRelayed pins the happy paths: a direct purchase
// of a gated FREE term with a valid chain succeeds, and so does the same purchase
// relayed by the configured Broker, where the chain is bound to the AGENT's
// acceptance key, never the Broker's transport key.
func TestExecute_GatedItemDirectAndRelayed(t *testing.T) {
	d := newExecuteDelegation(t)
	h := d.h
	token := testutil.MintDelegation(t, d.chain())

	offer := d.gatedOffer(t)
	resp, err := d.direct().ExecuteTransaction(h.ctx, connect.NewRequest(
		d.request(t, d.directRequester(), token, "tx-gated-direct", gatedItem{offer, true})))
	if err != nil {
		t.Fatalf("direct execute: %v", err)
	}
	if itemSignedURL(t, resp) == "" {
		t.Fatal("no delivery URL")
	}
	if _, err := repo.NewTransactionRepo(h.queries).ByIdempotencyKey(h.ctx, derivedTxKey("tx-gated-direct", offer)); err != nil {
		t.Fatalf("transaction row: %v", err)
	}

	offer = d.gatedOffer(t)
	resp, err = h.brokerOnlyClient(t).ExecuteTransaction(h.ctx, connect.NewRequest(
		d.request(t, h.relayRequester(), token, "tx-gated-relayed", gatedItem{offer, true})))
	if err != nil {
		t.Fatalf("relayed execute: %v", err)
	}
	if itemSignedURL(t, resp) == "" {
		t.Fatal("relayed: no delivery URL")
	}
	if got, want := resp.Msg.GetAgentIdentityHash(), rwtestutil.MustThumbprint(t, h.callerPub); got != want {
		t.Fatalf("agent_identity_hash = %q, want the agent's acceptance key %q", got, want)
	}
}

// TestExecute_RelayedChainBoundToBrokerKeyIsRefused pins the second half of the
// relayed holder binding: cnf.jkt must name the key the item's AgentAcceptance
// verified under, and NEVER the Broker's transport key. The Broker signs the
// request with its own key and carries a chain bound to that key, with a valid
// acceptance from the agent: the item is denied DELEGATION_INVALID, nothing is
// sold, and the journal names holder_mismatch. Were the gate to accept a chain
// bound to the verified wire signer as well, a Broker holding a delegation of
// its own could buy gated items for any agent it relays for.
func TestExecute_RelayedChainBoundToBrokerKeyIsRefused(t *testing.T) {
	d := newExecuteDelegation(t)
	h := d.h
	brokerPub, brokerPriv := h.configuredBrokerKey(t)
	boundToBroker := d.chain()
	boundToBroker.Holder = brokerPub
	offer := d.gatedOffer(t)
	req := d.request(t, h.relayRequester(), testutil.MintDelegation(t, boundToBroker), "tx-broker-bound", gatedItem{offer, true})
	resp, err := h.brokerOnlyClientAs(brokerPriv).ExecuteTransaction(h.ctx, connect.NewRequest(req))
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_DELEGATION_INVALID)
	if _, err := repo.NewTransactionRepo(h.queries).ByIdempotencyKey(h.ctx, derivedTxKey("tx-broker-bound", offer)); !errors.Is(err, repo.ErrTransactionNotFound) {
		t.Fatalf("purchase under a Broker-bound chain persisted: %v", err)
	}
	d.assertRefusalJournaled(t, "holder_mismatch")
}

// TestExecute_RelayedMixedBatch pins the per-item vocabulary on a relayed batch:
// a gated item without an acceptance is denied DELEGATION_INVALID while its
// siblings (a valid gated item and a public item) execute; the stripped item's
// denial is what a Broker that drops an acceptance can force, and nothing more
// (no transaction, no URL). The agent's retry under a NEW idempotency key with
// the acceptance present succeeds. A PUBLIC item without an acceptance is still
// a malformed request, refused whole with nothing executed.
func TestExecute_RelayedMixedBatch(t *testing.T) {
	d := newExecuteDelegation(t)
	h := d.h
	token := testutil.MintDelegation(t, d.chain())
	stripped, valid, public := d.gatedOffer(t), d.gatedOffer(t), discoverOffer(t, h, d.pubURI)
	broker := h.brokerOnlyClient(t)

	resp, err := broker.ExecuteTransaction(h.ctx, connect.NewRequest(d.request(t, h.relayRequester(), token, "tx-mixed",
		gatedItem{stripped, false}, gatedItem{valid, true}, gatedItem{public, true})))
	if err != nil {
		t.Fatalf("mixed batch: %v", err)
	}
	items := resp.Msg.GetItems()
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if items[0].GetDenialReason() != forav1.DenialReason_DENIAL_REASON_DELEGATION_INVALID || items[0].GetRetrievalEndpoint() != "" {
		t.Fatalf("stripped item = %v, want DELEGATION_INVALID and no URL", items[0])
	}
	for i := 1; i < 3; i++ {
		if items[i].GetRetrievalEndpoint() == "" || items[i].DenialReason != nil {
			t.Fatalf("sibling %d = %v, want executed", i, items[i])
		}
	}
	assertNoTransaction(t, h, derivedTxKey("tx-mixed", stripped))
	d.assertRefusalJournaled(t, "no agent_acceptance")

	// The agent retries the stripped item under a new key, acceptance present.
	resp, err = broker.ExecuteTransaction(h.ctx, connect.NewRequest(d.request(t, h.relayRequester(), token, "tx-mixed-retry", gatedItem{stripped, true})))
	if err != nil || itemSignedURL(t, resp) == "" {
		t.Fatalf("retry with acceptance: %v", err)
	}

	// A public item without an acceptance is a malformed envelope.
	fresh := d.gatedOffer(t)
	_, err = broker.ExecuteTransaction(h.ctx, connect.NewRequest(d.request(t, h.relayRequester(), token, "tx-mixed-public",
		gatedItem{public, false}, gatedItem{fresh, true})))
	assertConnectError(t, err, connect.CodeInvalidArgument, "agent_acceptance")
	assertNoTransaction(t, h, derivedTxKey("tx-mixed-public", fresh))
}

// TestExecute_DelegationFailuresDenyTheItem drives one chain per failure class
// through a direct single-item purchase: each is the same in-body
// DELEGATION_INVALID denial, persists no transaction, and leaves an audit line
// naming the failure. A chain that verifies but does not cover the term is the
// same denial.
func TestExecute_DelegationFailuresDenyTheItem(t *testing.T) {
	d := newExecuteDelegation(t)
	h := d.h
	otherPub, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	holder := d.chain()
	holder.Holder = otherPub
	expired := d.chain()
	expired.Exp = d.clk.Now().Add(-2 * time.Hour)
	untrusted := d.chain()
	untrusted.Keys = []ed25519.PrivateKey{otherPriv}
	uncovering := d.chain()
	uncovering.Scope = "subscription:" + h.siteDomain + ":other"
	capped := d.chain()
	capped.Tokens = []testutil.DelegationToken{{Claims: map[string]any{"fora_max_spend_cents": 100}}}
	cases := []struct {
		name, failure string
		token         []byte
	}{
		{"no token", "malformed", nil},
		{"holder mismatch", "holder_mismatch", testutil.MintDelegation(t, holder)},
		{"expired", "expired", testutil.MintDelegation(t, expired)},
		{"untrusted root key", "bad_signature", testutil.MintDelegation(t, untrusted)},
		{"scopes do not cover the term", "do not cover", testutil.MintDelegation(t, uncovering)},
		{"unsupported restriction", "unsupported_restriction", testutil.MintDelegation(t, capped)},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			offer := d.gatedOffer(t)
			idem := "tx-fail-" + string(rune('a'+i))
			resp, err := d.direct().ExecuteTransaction(h.ctx, connect.NewRequest(d.request(t, d.directRequester(), tc.token, idem, gatedItem{offer, true})))
			assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_DELEGATION_INVALID)
			assertNoTransaction(t, h, derivedTxKey(idem, offer))
			d.assertRefusalJournaled(t, tc.failure)
		})
	}
	// The denial journal counts the refusals for the publisher (read through the
	// journal's read port, the console's surface, tier 2).
	counts, err := repo.NewDenialCountRepo(h.queries).DenialCounts(h.ctx, repo.DenialCountQuery{
		TenantID: h.tenantID, PublisherID: harnessResourceOwner, From: d.clk.Now().Add(-time.Hour), To: d.clk.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	var denied int64
	for _, c := range counts {
		if c.Reason == repo.DenialReason(forav1.DenialReason_DENIAL_REASON_DELEGATION_INVALID.String()[len("DENIAL_REASON_"):]) {
			denied = c.Count
		}
	}
	if denied != int64(len(cases)) {
		t.Fatalf("journaled DELEGATION_INVALID = %d, want %d: %v", denied, len(cases), counts)
	}
}

// TestExecute_ReplayOutlivesTrust pins that an exact retry of a completed
// request is served the stored response before any per-item authorization:
// after the issuer key is withdrawn from the record, and after the chain has
// expired, the retry returns the original URL and no second transaction, while
// a fresh purchase is denied.
func TestExecute_ReplayOutlivesTrust(t *testing.T) {
	d := newExecuteDelegation(t)
	h := d.h
	short := d.chain()
	short.Exp = d.clk.Now().Add(2 * time.Minute)
	token := testutil.MintDelegation(t, short)
	offer := d.gatedOffer(t)
	req := d.request(t, d.directRequester(), token, "tx-replay", gatedItem{offer, true})
	first, err := d.direct().ExecuteTransaction(h.ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	url := itemSignedURL(t, first)

	// Trust withdrawn, chain expired (2 minutes past exp, beyond the 60s leeway).
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := d.trust.Put(h.ctx, repo.SubscriptionRecord{
		TenantID: h.tenantID, Scope: d.scope, IssuerIdentity: issuerID, IssuerKeys: []ed25519.PublicKey{otherPub},
	}); err != nil {
		t.Fatal(err)
	}
	d.clk.Advance(4 * time.Minute)
	again, err := d.direct().ExecuteTransaction(h.ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if got := itemSignedURL(t, again); got != url {
		t.Fatalf("retry URL %q, want the stored %q", got, url)
	}
	if again.Msg.GetItems()[0].GetTransactionId() != first.Msg.GetItems()[0].GetTransactionId() {
		t.Fatal("retry minted a second transaction")
	}

	// A fresh purchase under the withdrawn key is denied; the chain is also
	// expired, and either alone would deny it.
	fresh := d.request(t, d.directRequester(), token, "tx-after-withdrawal", gatedItem{discoverOffer(t, h, d.pubURI), true})
	fresh.Items[0].Offer = offer // the gated offer, still within its own lifetime
	fresh.Items[0].AgentAcceptance = signAcceptanceFor(t, h.callerPriv, offer, fresh.Requester, "tx-after-withdrawal")
	fresh.AgentRequestAcceptance = signRequestAcceptanceFor(t, h.callerPriv, fresh)
	resp, err := d.direct().ExecuteTransaction(h.ctx, connect.NewRequest(fresh))
	assertItemDenied(t, resp, err, forav1.DenialReason_DENIAL_REASON_DELEGATION_INVALID)
	if _, err := repo.NewTransactionRepo(h.queries).ByIdempotencyKey(h.ctx, derivedTxKey("tx-after-withdrawal", offer)); !errors.Is(err, repo.ErrTransactionNotFound) {
		t.Fatalf("fresh denied purchase persisted: %v", err)
	}
}
