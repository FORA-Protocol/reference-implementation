//go:build integration

package transport_test

import (
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// TestExecuteTransaction_ReplayRejectsForeignCaller pins the idempotency-replay
// caller-binding invariant: a stored replay result (including its signed
// retrieval_endpoint) is served ONLY to the same authenticated principal that
// owns the persisted transaction_log row. A DIFFERENT registered agent that
// presents the victim's (idempotency_key, offer_id) — even with its own
// perfectly valid self-signed request — must be refused with PermissionDenied
// and must receive NO retrieval_endpoint.
//
// Why this is the load-bearing check (and why a plain requester.id comparison is
// NOT enough): the exchange treats the BODY offer-acceptance signature — verified
// against the registered key of req.requester.id — as the authoritative agent
// identity, and binds the delivery URL to THAT key's RFC 7638 thumbprint,
// persisted as transaction_log.agent_identity_hash (agent_acceptance.go). The
// replay probe therefore may only return the stored URL to a caller who proves
// possession of the key whose thumbprint == the stored agent_identity_hash. The
// historical bug ran the replay probe before possession was established, handing
// the stored URL to whoever presented the pair — a cross-principal signed-URL leak.
//
// Round-trip honesty:
//   - agent A WRITE leg: A → ExecuteTransaction RPC (real Connect HTTP) →
//     transport → service → repo → DB. Full PROTOCOL+PERSISTENCE round-trip.
//   - agent B REPLAY leg: B → the SAME ExecuteTransaction RPC surface, signed
//     with B's OWN transport + acceptance keys, reusing A's idempotency_key +
//     offer. Full PROTOCOL round-trip; asserts the rejection through the RPC.
//   - side-effect ABSENCE (A's row untouched, A not re-charged) is a PERSISTENCE
//     check via the production repo.TransactionRepo.ByIdempotencyKey (tier-2:
//     No public transaction-read RPC exists) plus
//     the billing adapter GetBalance. No raw sqlc / SQL (Testing Doctrine §9).
func TestExecuteTransaction_ReplayRejectsForeignCaller(t *testing.T) {
	h := newTestHarness(t)
	ctx := h.ctx
	uri := seedResourceWithRate(t, h, "/articles/replay-binding", "0.05")
	offer := discoverOffer(t, h, uri)

	const idem = "tx-cross-caller-replay"

	// Agent A (the harness default caller "agent-test") executes the batch: the
	// original request succeeds and persists the transaction_log row bound to A's
	// registered-key thumbprint, with a signed retrieval endpoint.
	aRequester := newRequester("agent-test", "agent.example")
	aReq := &forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: idem,
		Requester:      aRequester,
		Items: []*forav1.TransactionItem{
			{Offer: offer, AgentAcceptance: signAcceptanceFor(t, h.callerPriv, offer, aRequester, idem)},
		},
	}
	aReq.AgentRequestAcceptance = signRequestAcceptanceFor(t, h.callerPriv, aReq)
	aResp, err := h.exchangeClient.ExecuteTransaction(ctx, connect.NewRequest(aReq))
	if err != nil {
		t.Fatalf("agent A original call must succeed: %v", err)
	}
	aItems := aResp.Msg.GetItems()
	if len(aItems) != 1 || aItems[0].GetRetrievalEndpoint() == "" {
		t.Fatalf("agent A call did not yield a signed retrieval endpoint: %+v", aItems)
	}
	balAfterA, err := h.billing.GetBalance(ctx, h.billingRef, harnessCurrency)
	if err != nil {
		t.Fatalf("GetBalance(billing_ref) after A: %v", err)
	}

	// Agent B: a DIFFERENT registered agent with its OWN keypair. B legitimately
	// discovers the same offer and self-signs a well-formed acceptance under its
	// OWN identity, but reuses A's idempotency_key (a client-chosen dedup token B
	// could learn off-band — it is not a bearer secret). B's request is otherwise
	// valid; only the (idempotency_key, offer_id) pair collides with A's row.
	clientB, bPriv := h.addCallerWithKey(t, "agent-b", "AGENT")
	bRequester := newRequester("agent-b", "agent-b.example")
	bReq := &forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: idem,
		Requester:      bRequester,
		Items: []*forav1.TransactionItem{
			{Offer: offer, AgentAcceptance: signAcceptanceFor(t, bPriv, offer, bRequester, idem)},
		},
	}
	bReq.AgentRequestAcceptance = signRequestAcceptanceFor(t, bPriv, bReq)
	bResp, bErr := clientB.ExecuteTransaction(ctx, connect.NewRequest(bReq))

	// INVARIANT: B must be refused — the stored result belongs to A. A replay is
	// served only to the row's owner; a foreign caller gets PermissionDenied and
	// NO signed URL. (Pre-fix, B receives A's retrieval_endpoint verbatim — the leak.)
	if bErr == nil {
		leaked := ""
		if items := bResp.Msg.GetItems(); len(items) == 1 {
			leaked = items[0].GetRetrievalEndpoint()
		}
		t.Fatalf("SECURITY: foreign caller B replayed A's key and received a response "+
			"(leaked retrieval_endpoint=%q); want PermissionDenied", leaked)
	}
	if got := connect.CodeOf(bErr); got != connect.CodePermissionDenied {
		t.Fatalf("foreign-caller replay error code = %v, want PermissionDenied (err: %v)", got, bErr)
	}

	// Side effects: A's row is untouched and A was charged exactly once. B's
	// rejected replay neither re-served nor re-billed A's transaction.
	derivedKey := idem + ":" + offer.GetOfferId()
	repoTx := repo.NewTransactionRepo(h.queries)
	if _, rerr := repoTx.ByIdempotencyKey(ctx, derivedKey); rerr != nil {
		t.Fatalf("A's persisted row must survive B's rejected replay: %v", rerr)
	}
	balAfterB, err := h.billing.GetBalance(ctx, h.billingRef, harnessCurrency)
	if err != nil {
		t.Fatalf("GetBalance(billing_ref) after B: %v", err)
	}
	if balAfterB.Value.Cmp(balAfterA.Value) != 0 {
		t.Fatalf("agent-test balance moved on B's rejected replay: %s -> %s",
			balAfterA.Value.FloatString(4), balAfterB.Value.FloatString(4))
	}
}

// TestExecuteTransaction_ReplayRejectsForeignCallerWithoutRequestProof runs the
// same ownership refusal as its sibling above, on the wire shape that OMITS
// agent_request_acceptance. That shape is not a legacy corner: the field is
// optional, and until the Python and TypeScript SDKs emit it, every non-Go
// client sends exactly this request.
//
// The sibling above cannot catch a regression on this path, because both of its
// legs carry the proof. Admission takes a different branch when the proof is
// absent — it proves possession per item and takes no request claim — and that
// branch must still run the per-item replay probe, because the probe is where
// the ownership gate lives. A version of this code returned before the probe,
// which dropped the gate for every client that omits the proof: the foreign
// caller executed against a key it did not own instead of being refused.
//
// Round-trip honesty matches the sibling: both legs drive the ExecuteTransaction
// RPC end to end; side-effect absence is read through repo.TransactionRepo
// (tier-2 — no public transaction-read RPC exists) and the billing adapter.
func TestExecuteTransaction_ReplayRejectsForeignCallerWithoutRequestProof(t *testing.T) {
	h := newTestHarness(t)
	ctx := h.ctx
	uri := seedResourceWithRate(t, h, "/articles/replay-binding-noproof", "0.05")
	offer := discoverOffer(t, h, uri)

	const idem = "tx-cross-caller-replay-noproof"

	// Agent A executes first and persists the row bound to its own key. No
	// AgentRequestAcceptance is set anywhere in this test.
	aRequester := newRequester("agent-test", "agent.example")
	aReq := &forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: idem,
		Requester:      aRequester,
		Items: []*forav1.TransactionItem{
			{Offer: offer, AgentAcceptance: signAcceptanceFor(t, h.callerPriv, offer, aRequester, idem)},
		},
	}
	aResp, err := h.exchangeClient.ExecuteTransaction(ctx, connect.NewRequest(aReq))
	if err != nil {
		t.Fatalf("agent A original call must succeed: %v", err)
	}
	if items := aResp.Msg.GetItems(); len(items) != 1 || items[0].GetRetrievalEndpoint() == "" {
		t.Fatalf("agent A call did not yield a signed retrieval endpoint: %+v", items)
	}
	balAfterA, err := h.billing.GetBalance(ctx, h.billingRef, harnessCurrency)
	if err != nil {
		t.Fatalf("GetBalance(billing_ref) after A: %v", err)
	}

	// Agent B is a different registered agent with its own keypair. It signs a
	// valid acceptance under its OWN identity and reuses A's idempotency_key and
	// offer. Only that pair collides with A's row.
	clientB, bPriv := h.addCallerWithKey(t, "agent-b-noproof", "AGENT")
	bRequester := newRequester("agent-b-noproof", "agent-b.example")
	bReq := &forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: idem,
		Requester:      bRequester,
		Items: []*forav1.TransactionItem{
			{Offer: offer, AgentAcceptance: signAcceptanceFor(t, bPriv, offer, bRequester, idem)},
		},
	}
	bResp, bErr := clientB.ExecuteTransaction(ctx, connect.NewRequest(bReq))

	if bErr == nil {
		leaked := ""
		denial := forav1.DenialReason_DENIAL_REASON_UNSPECIFIED
		if items := bResp.Msg.GetItems(); len(items) == 1 {
			leaked = items[0].GetRetrievalEndpoint()
			denial = items[0].GetDenialReason()
		}
		t.Fatalf("SECURITY: foreign caller B replayed A's key without a request proof and "+
			"received a response (retrieval_endpoint=%q, denial_reason=%v); want PermissionDenied",
			leaked, denial)
	}
	if got := connect.CodeOf(bErr); got != connect.CodePermissionDenied {
		t.Fatalf("foreign-caller replay error code = %v, want PermissionDenied (err: %v)", got, bErr)
	}

	// A's row survives and A is charged exactly once.
	if _, rerr := repo.NewTransactionRepo(h.queries).ByIdempotencyKey(ctx, idem+":"+offer.GetOfferId()); rerr != nil {
		t.Fatalf("A's persisted row must survive B's rejected replay: %v", rerr)
	}
	balAfterB, err := h.billing.GetBalance(ctx, h.billingRef, harnessCurrency)
	if err != nil {
		t.Fatalf("GetBalance(billing_ref) after B: %v", err)
	}
	if balAfterB.Value.Cmp(balAfterA.Value) != 0 {
		t.Fatalf("agent-test balance moved on B's rejected replay: %s -> %s",
			balAfterA.Value.FloatString(4), balAfterB.Value.FloatString(4))
	}
}

// TestExecuteTransaction_ReplayWithCopiedRequesterLabelRejected pins the sharper
// half of the ownership gate: copying the victim's requester.id changes nothing.
// requester.id is a free label that carries no identity — the agent is derived
// from the verified Signature-Agent directory — so a foreign caller that writes
// the victim's label into its own request is still itself. It cannot be served
// the victim's stored result, because it cannot prove possession of the key whose
// thumbprint the victim's row records.
//
// The refusal is PermissionDenied from the ownership gate in the per-item replay
// probe: the derived key idempotency_key:offer_id is global, so the foreign
// caller's request does land on the victim's persisted row, and the gate is what
// refuses it there. No signed URL reaches the caller and the victim's row and
// balance are untouched.
//
// This guards against a future "simplification" of the ownership check into a
// bare requester.id or claimed-thumbprint compare, which would pass
// TestExecuteTransaction_ReplayRejectsForeignCaller and reopen the leak.
//
// Round-trip honesty: identical shape to the sibling test — A writes via the RPC,
// B replays via the SAME RPC signing its transport with B's key while copying
// requester.id="agent-test"; asserted through the RPC surface + repo/billing for
// side-effect absence (Testing Doctrine §9).
func TestExecuteTransaction_ReplayWithCopiedRequesterLabelRejected(t *testing.T) {
	h := newTestHarness(t)
	ctx := h.ctx
	uri := seedResourceWithRate(t, h, "/articles/replay-spoof", "0.05")
	offer := discoverOffer(t, h, uri)

	const idem = "tx-spoofed-requester-replay"

	// Agent A executes and persists the row (bound to A's identity).
	aRequester := newRequester("agent-test", "agent.example")
	aReq := &forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: idem,
		Requester:      aRequester,
		Items: []*forav1.TransactionItem{
			{Offer: offer, AgentAcceptance: signAcceptanceFor(t, h.callerPriv, offer, aRequester, idem)},
		},
	}
	if _, err := h.exchangeClient.ExecuteTransaction(ctx, connect.NewRequest(aReq)); err != nil {
		t.Fatalf("agent A original call must succeed: %v", err)
	}
	balAfterA, err := h.billing.GetBalance(ctx, h.billingRef, harnessCurrency)
	if err != nil {
		t.Fatalf("GetBalance(billing_ref) after A: %v", err)
	}

	// Agent B signs transport with B's own key and copies A's requester.id label.
	// The acceptance is signed with B's OWN key, which is the only key B has, and
	// which is the key B's own Signature-Agent directory publishes — so the
	// acceptance itself verifies. What B cannot do is possess the key A's row
	// records.
	clientB, bPriv := h.addCallerWithKey(t, "agent-b-spoof", "AGENT")
	copiedLabelReq := &forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: idem,
		Requester:      aRequester, // copies A's label; B is still B on the transport
		Items: []*forav1.TransactionItem{
			{Offer: offer, AgentAcceptance: signAcceptanceFor(t, bPriv, offer, aRequester, idem)},
		},
	}
	_, copiedLabelErr := clientB.ExecuteTransaction(ctx, connect.NewRequest(copiedLabelReq))

	// INVARIANT: NO signed URL ever reaches the foreign caller. The request lands
	// on A's globally-keyed row in the per-item replay probe, and the ownership
	// gate refuses it there because B's proven key is not the one A's row records.
	if copiedLabelErr == nil {
		t.Fatal("SECURITY: a foreign caller copying the victim's requester.id was served a response")
	}
	if got := connect.CodeOf(copiedLabelErr); got != connect.CodePermissionDenied {
		t.Fatalf("copied-label replay error code = %v, want PermissionDenied (err: %v)", got, copiedLabelErr)
	}

	// Side effects: A's row survives, A not re-charged.
	derivedKey := idem + ":" + offer.GetOfferId()
	repoTx := repo.NewTransactionRepo(h.queries)
	if _, rerr := repoTx.ByIdempotencyKey(ctx, derivedKey); rerr != nil {
		t.Fatalf("A's persisted row must survive the refused replay: %v", rerr)
	}
	balAfterCopiedLabel, err := h.billing.GetBalance(ctx, h.billingRef, harnessCurrency)
	if err != nil {
		t.Fatalf("GetBalance(billing_ref) after the copied-label replay: %v", err)
	}
	if balAfterCopiedLabel.Value.Cmp(balAfterA.Value) != 0 {
		t.Fatalf("agent-test balance moved on the copied-label replay: %s -> %s",
			balAfterA.Value.FloatString(4), balAfterCopiedLabel.Value.FloatString(4))
	}
}

// TestExecuteTransaction_CopiedRequesterLabelLeavesNoVictimClaim runs the
// label-copying request BEFORE the victim ever executes, the ordering the
// victim-first sibling cannot prove: a caller that writes the victim's
// requester.id into its own request reserves nothing under the victim's identity,
// so the victim's OWN later request under the same idempotency_key proceeds as
// fresh — it executes and charges, rather than being refused AlreadyExists off a
// claim the label-copying caller poisoned.
//
// The copied label buys the attacker nothing because the agent is derived from
// the verified Signature-Agent directory: the request is the attacker's own, and
// so is every check it then faces. Here that check is the attacker's own account
// state — it never registered for billing, so its paid item is denied
// ACCOUNT_NOT_REGISTERED. The denial reason is the point rather than an
// incidental detail: it names the ATTACKER's account, which is what "the request
// never became the victim's" looks like from the outside.
//
// Round-trip honesty: both legs drive the ExecuteTransaction RPC; the victim's
// fresh execution is observed through the response (a signed URL) and the billing
// balance debit, side effects only through the production surfaces.
func TestExecuteTransaction_CopiedRequesterLabelLeavesNoVictimClaim(t *testing.T) {
	h := newTestHarness(t)
	ctx := h.ctx
	uri := seedResourceWithRate(t, h, "/articles/attacker-first-spoof", "0.05")
	offer := discoverOffer(t, h, uri)

	const idem = "tx-attacker-first-spoof"

	// Attacker B copies requester.id="agent-test" and signs the acceptance with its
	// OWN key. The acceptance verifies — it is B's own request — but B never
	// registered for billing, so the paid item is denied in-body under B's own
	// account and nothing is persisted.
	clientB, bPriv := h.addCallerWithKey(t, "agent-b-attacker", "AGENT")
	aRequester := newRequester("agent-test", "agent.example")
	copiedLabelReq := &forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: idem,
		Requester:      aRequester, // copies the victim's label
		Items: []*forav1.TransactionItem{
			{Offer: offer, AgentAcceptance: signAcceptanceFor(t, bPriv, offer, aRequester, idem)},
		},
	}
	resp, respErr := clientB.ExecuteTransaction(ctx, connect.NewRequest(copiedLabelReq))
	assertItemDenied(t, resp, respErr, forav1.DenialReason_DENIAL_REASON_ACCOUNT_NOT_REGISTERED)
	// The attacker charged nothing and reserved nothing.
	if _, rerr := repo.NewTransactionRepo(h.queries).ByIdempotencyKey(ctx, idem+":"+offer.GetOfferId()); rerr == nil {
		t.Fatal("the label-copying request persisted a row under the derived key; want none")
	}

	// The victim (agent-test) now runs its OWN legitimate request under the SAME
	// idempotency_key. It must be fresh — a claim reserved under the victim's
	// identity would refuse this AlreadyExists (KindIdempotent).
	victim, err := executeSingleItem(t, h, idem, offer)
	if err != nil {
		t.Fatalf("victim's own request after the label-copying attempt must be fresh, "+
			"not AlreadyExists: %v", err)
	}
	if victim.Msg.GetItems()[0].GetRetrievalEndpoint() == "" {
		t.Fatal("victim's fresh request returned no retrieval endpoint")
	}
	// The victim was charged exactly once (0.05 → 9.95); the refused
	// label-copying request charged nothing.
	bal, err := h.billing.GetBalance(ctx, h.billingRef, harnessCurrency)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if want := mustBillingAmount(t, "9.95", "USD"); bal.Value.Cmp(want.Value) != 0 {
		t.Fatalf("balance = %s, want 9.95 (only the victim's fresh request charged)", bal.Value.FloatString(4))
	}
}
