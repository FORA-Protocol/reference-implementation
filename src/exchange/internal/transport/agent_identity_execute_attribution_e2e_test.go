//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/google/uuid"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// The execute path attributes a transaction to the agent the TRANSPORT proved:
// the canonical directory host of the covered Signature-Agent header. requester.id
// is a free label the protocol lets an agent choose — "Unique requester
// identifier (e.g. agent-research-bot-001)" — so it selects nothing: no directory
// to fetch, no agents row, no billing account, no idempotency owner. It is kept
// verbatim in transaction_evidence.requester_id because it is part of what the
// agent signed.
//
// Reading it as a host was the defect these tests pin closed. An agent whose
// label was not a reachable host got 503 (the Exchange tried to fetch a key
// directory from it), and one whose label was not a host at all got 400 —
// although its transport signature had already authenticated it one step earlier.
//
// Round-trip honesty. The execute and report legs are full protocol round-trips:
// signed Connect client → RFC 9421 gate → ExecuteTransaction / ReportUsage
// handler → service → repo → Postgres, asserted back through the same RPCs. The
// persistence assertions stop at the production REPOSITORY interface (Testing
// Doctrine §9 tier 2) because the Exchange exposes no RPC that reads a
// transaction or its evidence row back — see the note on each.

// seedAttributionCatalog pushes one priced catalog entry through the signed
// publisher surface and returns its canonical URI. It is the self-signup flow's
// step 1 with nothing added — the variable under test in this file is the
// requester.id spelling, not the catalog — and it runs ONCE per test, because each
// push re-publishes the publisher caller's key and a second push is a key
// rotation the catalog gate has no reason to expect here.
func (h *agentHarness) seedAttributionCatalog(t *testing.T) string {
	t.Helper()
	entry := &forav1.ResourceEntry{
		Domain: h.publisherDom, Path: "/articles/attribution",
		Terms: []*forav1.LicenseTerm{seedPricedTermEst(1)},
	}
	h.seedPublisherEntries(t, "attribution-pub-caller.example", entry)
	return "https://" + entry.GetDomain() + entry.GetPath()
}

// discoverOfferFor discovers uri back as a freshly signed offer for the named
// agent, which is the precondition an execute needs. Each call mints a new offer,
// so repeated executes never reuse one.
func (h *agentHarness) discoverOfferFor(t *testing.T, agentID, uri string) *forav1.Offer {
	t.Helper()
	discResp, err := h.exchange.DiscoverResources(h.ctx,
		connect.NewRequest(newResourceQuery(newRequester(agentID, agentID), []string{uri})))
	if err != nil {
		t.Fatalf("seed discover: %v", err)
	}
	offers := discResp.Msg.GetOffers()
	if len(offers) != 1 {
		t.Fatalf("seed discover returned %d offers, want 1", len(offers))
	}
	return offers[0]
}

// seedSignedOfferFor pushes the catalog entry and discovers one signed offer for
// agentID, for the tests that need exactly one.
func (h *agentHarness) seedSignedOfferFor(t *testing.T, agentID string) *forav1.Offer {
	t.Helper()
	return h.discoverOfferFor(t, agentID, h.seedAttributionCatalog(t))
}

// forbidOrigin serves host and fails the test on ANY request that reaches it. It
// is how "nothing was fetched from the host the label names" becomes an
// observation rather than an assumption: the harness routes every outbound
// well-known fetch through its rewrite table, so a directory fetch aimed at this
// host lands here.
func forbidOrigin(t *testing.T, h *agentHarness, host, why string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("the Exchange fetched %q: %s", host+r.URL.Path, why)
	}))
	t.Cleanup(server.Close)
	h.registerHost(host, server.URL)
}

// TestExecute_FreeLabelRequesterIDAttributesToSignatureAgentHost drives the three
// label shapes a real client sends and the old derivation broke on: a plain
// campaign label, a URN that names no host at all, and — the dangerous one — a
// label that IS a host but belongs to somebody else.
//
// All three succeed, all three are attributed to the signer's own directory host,
// and the third fetches nothing from the host it names. The label is recorded
// verbatim in the evidence row, and no agents row is ever created for it.
func TestExecute_FreeLabelRequesterIDAttributesToSignatureAgentHost(t *testing.T) {
	h := newAgentHarness(t)

	const host = "free-label-agent.example"
	// A host the label will name but that the Exchange must never touch. It is
	// registered as a forbidden origin BEFORE the execute, so a directory fetch
	// aimed at the label is recorded as a test failure rather than a DNS error.
	const foreignHost = "other.example"
	forbidOrigin(t, h, foreignHost,
		"requester.id is a free label and must never select a key directory")

	agentPub, agentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("agent keypair: %v", err)
	}
	h.publishAgent(t, host, agentPriv)
	h.registerForBilling(t, host, agentPub, agentPriv)
	agentClient := h.selfActingExchangeClient(host, agentPriv)
	uri := h.seedAttributionCatalog(t)

	for _, label := range []string{"parse-probe", "urn:parse:probe", foreignHost} {
		t.Run(label, func(t *testing.T) {
			offer := h.discoverOfferFor(t, host, uri)
			idempotencyKey := "tx-" + uuid.NewString()
			// domain carries the agent's own host, as the identity service sends it.
			// On this DIRECT path nothing reads it; only the label varies.
			requester := newRequester(label, host)
			execResp, err := agentClient.ExecuteTransaction(h.ctx, connect.NewRequest(&forav1.TransactionRequest{
				Ver: helpers.ProtocolVersion, IdempotencyKey: idempotencyKey,
				Requester: requester,
				Items: []*forav1.TransactionItem{
					{Offer: offer, AgentAcceptance: signAcceptanceFor(t, agentPriv, offer, requester, idempotencyKey)},
				},
			}))
			if err != nil {
				t.Fatalf("ExecuteTransaction with requester.id %q: %v — the label was read as "+
					"an identity instead of being carried as evidence", label, err)
			}
			item := singleResultItem(t, execResp)

			// Tier-2 fallback (Testing Doctrine §9): no RPC reads a transaction row
			// back. The ledger must be keyed on the signer's directory host.
			txRec, err := repo.NewTransactionRepo(h.queries).
				ByIdempotencyKey(h.ctx, idempotencyKey+":"+offer.GetOfferId())
			if err != nil {
				t.Fatalf("TransactionRepo.ByIdempotencyKey: %v", err)
			}
			if txRec.AgentID != host {
				t.Errorf("transaction_log.agent_id = %q, want %q — the ledger is keyed on the "+
					"caller's label, not on the identity its signature proved", txRec.AgentID, host)
			}

			// No agent was registered under the label. A row here would mean the
			// label had reached the registry as an identity.
			if _, err := repo.NewAgentRepo(h.queries).ByID(h.ctx, label); err == nil {
				t.Errorf("an agents row exists for requester.id %q; a label must never "+
					"become a registration", label)
			}

			// The evidence row keeps the signed label verbatim, which is the property
			// that makes it evidence.
			ev, err := repo.NewEvidenceRepo(h.queries).ByTransactionCrossTenant(h.ctx, item.GetTransactionId())
			if err != nil {
				t.Fatalf("EvidenceRepo.ByTransactionCrossTenant: %v", err)
			}
			if ev.RequesterID != label {
				t.Errorf("transaction_evidence.requester_id = %q, want the signed bytes %q — "+
					"evidence must attest what was signed, never a rewritten value", ev.RequesterID, label)
			}
		})
	}
}

// TestExecute_SchemedSignatureAgentAttributesToCanonicalHost drives the deployed
// shape end to end: the agent registers as a bare host and then signs its
// Signature-Agent as "https://<host>", the origin form the identity service
// emits.
//
// The load-bearing assertion is the ReportUsage leg. ReportUsage authorizes by
// comparing the verified caller's identity against the agent_id stored on the
// transaction's obligation, so a report signed under the BARE spelling is accepted
// only if the execute leg attributed the transaction to the canonical host. Had
// the schemed string been stored, that comparison would fail and the agent could
// never report usage against its own transaction — a break with no self-heal path.
//
// It also pins the forensic join migration 000024 documents: the evidence row's
// agent_discovery_url reduces, through the same normalization, to
// transaction_log.agent_id.
func TestExecute_SchemedSignatureAgentAttributesToCanonicalHost(t *testing.T) {
	h := newAgentHarness(t)

	const host = "schemed-directory.example"
	const schemed = "https://" + host

	agentPub, agentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("agent keypair: %v", err)
	}
	h.publishAgent(t, host, agentPriv)
	// Registers the agent for billing under the BARE host, which is what its own
	// directory names it. This is the row the schemed execute below must find.
	h.registerForBilling(t, host, agentPub, agentPriv)

	offer := h.seedSignedOfferFor(t, host)

	// Execute signing Signature-Agent with the SCHEMED spelling — the exact shape
	// the identity service produces.
	agentClient := h.selfActingExchangeClient(schemed, agentPriv)
	idempotencyKey := "tx-" + uuid.NewString()
	requester := newRequester("schemed-probe", host)
	execResp, err := agentClient.ExecuteTransaction(h.ctx, connect.NewRequest(&forav1.TransactionRequest{
		Ver: helpers.ProtocolVersion, IdempotencyKey: idempotencyKey,
		Requester: requester,
		Items: []*forav1.TransactionItem{
			{Offer: offer, AgentAcceptance: signAcceptanceFor(t, agentPriv, offer, requester, idempotencyKey)},
		},
	}))
	if err != nil {
		t.Fatalf("ExecuteTransaction signed as %q: %v — the schemed directory did not "+
			"resolve to the agent registered as %q", schemed, err, host)
	}
	item := singleResultItem(t, execResp)

	// The load-bearing leg: report usage signing the BARE spelling. Accepted only
	// if the transaction was attributed to the canonical host.
	bareClient := h.selfActingExchangeClient(host, agentPriv)
	repResp, err := bareClient.ReportUsage(h.ctx, connect.NewRequest(newUsageReport("r-"+uuid.NewString(), item.GetTransactionId(), item.GetBillingId(), &forav1.Usage{ConsumedQuantity: 1, Function: []string{"ai_input"}})))
	if err != nil {
		t.Fatalf("ReportUsage signed as %q against a transaction executed as %q: %v — "+
			"the transaction was attributed to the raw spelling, so the agent cannot "+
			"report usage against its own transaction", host, schemed, err)
	}
	if repResp.Msg.GetReportId() == "" {
		t.Fatal("accepted report carries no report_id")
	}

	// Testing Doctrine §9 tier-2 fallback. The Exchange exposes no RPC that reads a
	// transaction row back, so the ledger's attributed id is observed through the
	// production TransactionRepo interface rather than a public surface. The
	// repository is the highest surface available and the one production itself
	// uses, but it is still a corner cut, and the thing actually missing is a
	// transaction-read RPC — without one, no client can verify what it was charged
	// for, which is a product gap before it is a testing one. Tracked with the
	// change that introduced this file rather than named here, because an id in a
	// comment outlives whatever it pointed at.
	txRec, err := repo.NewTransactionRepo(h.queries).
		ByIdempotencyKey(h.ctx, idempotencyKey+":"+offer.GetOfferId())
	if err != nil {
		t.Fatalf("TransactionRepo.ByIdempotencyKey: %v", err)
	}
	if txRec.AgentID != host {
		t.Errorf("transaction_log.agent_id = %q, want %q — the ledger is keyed on the "+
			"caller's spelling, not the agent's identity", txRec.AgentID, host)
	}

	// The documented forensic join. requester_id is a free label and names nothing,
	// so what ties an evidence row to the ledger is the directory the verifying key
	// was pinned from: agentid.FromDirectory(agent_discovery_url) =
	// transaction_log.agent_id. Migration 000024's column comment states it; assert
	// both halves here so code and that comment cannot drift apart — the failure
	// mode is a forensic join silently returning nothing.
	//
	// Tier-2 fallback for the same reason, and with the same caveat, as above: no
	// RPC reads an evidence row back either.
	ev, err := repo.NewEvidenceRepo(h.queries).ByTransactionCrossTenant(h.ctx, item.GetTransactionId())
	if err != nil {
		t.Fatalf("EvidenceRepo.ByTransactionCrossTenant: %v", err)
	}
	if ev.AgentDiscoveryURL == "" {
		t.Fatal("transaction_evidence.agent_discovery_url is empty; the row records no " +
			"directory for the key it attests, so the forensic join has nothing to stand on")
	}
	joined, err := agentid.FromDirectory(ev.AgentDiscoveryURL)
	if err != nil {
		t.Fatalf("the documented forensic join does not apply to the stored evidence: %v", err)
	}
	if joined != txRec.AgentID {
		t.Errorf("agentid.FromDirectory(evidence.agent_discovery_url) = %q, "+
			"transaction_log.agent_id = %q — migration 000024 documents these as equal; "+
			"a forensic join now returns nothing", joined, txRec.AgentID)
	}
}
