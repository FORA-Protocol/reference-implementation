//go:build integration

package transport_test

// The delivery URL's signing scheme is a property of the SITE, and the private
// key it is signed with is a property of the SEAT (per ADR-026 D5).
//
// One deployment, one seat, two publisher sites: one registered on ED25519 and
// one on AWS_CLOUDFRONT_RSA. Both are sold by the same seat and both settle to
// the same payee, so the only thing that can decide which signer runs is the
// site row. Before the cutover a seat could not do this at all: the scheme sat
// on fora.tenants and pinned a tenant to one CDN.

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/url"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/google/uuid"
)

// TestExecuteTransaction_SigningSchemeIsPerSite drives two sites of one seat
// through the full public surface — push, discover, execute — and verifies each
// delivered URL under the scheme its own site was registered with.
//
// Round-trip: every leg is a real RPC over the Connect-Go router. The catalog
// entries are written through PushResources, read back as offers through
// DiscoverResources, and turned into delivery URLs through ExecuteTransaction.
// Nothing is arranged or observed past the transport layer except the site
// registration itself, which is seeded rather than driven through the
// registration routes. Those are exercised in the console suites; what this
// test needs from a site is that it exists with a given scheme, and seeding
// keeps it about the scheme rather than about how the site got there.
func TestExecuteTransaction_SigningSchemeIsPerSite(t *testing.T) {
	h := newPushHarness(t)

	// Site one: the harness's own, registered on ED25519.
	const ed25519Caller = "ed25519-caller.example"
	h.publisher.setContributors(ed25519Caller)
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 site contributor keypair: %v", err)
	}
	h.publishAgent(t, ed25519Caller, edPriv)

	// Site two: a second site on the SAME seat, registered on
	// AWS_CLOUDFRONT_RSA.
	const (
		cfDomain = "cloudfront-site.example"
		cfCaller = "cloudfront-caller.example"
	)
	registerCloudFrontSite(t, h, cfDomain)
	cfSite := newSiteWithContributor(t, h, cfDomain, cfCaller)

	const path = "/articles/delivery"
	pushSiteEntry(t, h, h.signedCat(ed25519Caller, edPriv), ed25519Caller, h.publisherDom, path)
	pushSiteEntry(t, h, cfSite.client, cfCaller, cfDomain, path)

	// The agent that buys from both sites.
	const agentID = "delivery-agent.example"
	agentPub, agentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("agent keypair: %v", err)
	}
	h.publishAgent(t, agentID, agentPriv)
	h.registerForBilling(t, agentID, agentPub, agentPriv)

	edURL := executeForURL(t, h, agentID, agentPriv, "https://"+h.publisherDom+path)
	cfURL := executeForURL(t, h, agentID, agentPriv, "https://"+cfDomain+path)

	// The ED25519 site's URL verifies against the seat's Ed25519 public key. The
	// key is the seat's, which is the half of the split that does NOT move per
	// site.
	if _, err := helpers.VerifyURLEd25519(edURL, h.deliveryPub, time.Now().UTC()); err != nil {
		t.Fatalf("delivery URL for the ED25519 site does not verify: %v (url %q)", err, edURL)
	}

	// The CloudFront site's URL is a canned-policy URL signed with the seat's RSA
	// key and naming the key-pair id the SITE row carries.
	if err := verifyCloudFrontCannedPolicy(cfURL, exchangeCloudFrontKey(h)); err != nil {
		t.Fatalf("delivery URL for the AWS_CLOUDFRONT_RSA site does not verify: %v (url %q)", err, cfURL)
	}
	parsed, err := url.Parse(cfURL)
	if err != nil {
		t.Fatalf("parse the CloudFront delivery URL: %v", err)
	}
	if got := parsed.Query().Get("Key-Pair-Id"); got != cloudFrontTestKeyRef {
		t.Errorf("CloudFront delivery URL Key-Pair-Id = %q, want %q — the key-pair id "+
			"must come from the site row", got, cloudFrontTestKeyRef)
	}

	// The two URLs are shaped by different schemes, which is the whole claim. An
	// Ed25519 URL carries no CloudFront parameters and a CloudFront URL carries
	// no Ed25519 signature parameter.
	edParsed, err := url.Parse(edURL)
	if err != nil {
		t.Fatalf("parse the Ed25519 delivery URL: %v", err)
	}
	if got := edParsed.Query().Get("Key-Pair-Id"); got != "" {
		t.Errorf("the ED25519 site's URL carries Key-Pair-Id %q; the seat signed it "+
			"under the wrong site's scheme", got)
	}
	if got := parsed.Query().Get("sig"); got != "" {
		t.Errorf("the CloudFront site's URL carries an Ed25519 sig parameter %q; the "+
			"seat signed it under the wrong site's scheme", got)
	}
}

// pushSiteEntry pushes one priced entry for a site through the public
// PushResources RPC and fails the test unless it is accepted.
func pushSiteEntry(
	t *testing.T, h *pushHarness, client foraconnect.CatalogServiceClient, callerID, domain, path string,
) {
	t.Helper()
	resp, err := client.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, callerID,
		[]*forav1.ResourceEntry{{
			Domain: domain, Path: path,
			Terms: []*forav1.LicenseTerm{seedPricedTermEst(1)},
		}})))
	if err != nil {
		t.Fatalf("push %s%s: %v", domain, path, err)
	}
	if got := resp.Msg.GetAccepted(); got != 1 {
		t.Fatalf("push %s%s accepted = %d, want 1 (rejected=%d)",
			domain, path, got, resp.Msg.GetRejected())
	}
}

// executeForURL discovers uri as agentID, executes the offer that comes back,
// and returns the delivered retrieval endpoint.
func executeForURL(
	t *testing.T, h *pushHarness, agentID string, agentPriv ed25519.PrivateKey, uri string,
) string {
	t.Helper()
	requester := newRequester(agentID, agentID)
	discovered, err := h.exchange.DiscoverResources(h.ctx,
		connect.NewRequest(newResourceQuery(requester, []string{uri})))
	if err != nil {
		t.Fatalf("discover %s: %v", uri, err)
	}
	offers := discovered.Msg.GetOffers()
	if len(offers) != 1 {
		t.Fatalf("discover %s returned %d offers, want 1", uri, len(offers))
	}
	offer := offers[0]

	idem := "tx-" + uuid.NewString()
	execResp, err := h.exchange.ExecuteTransaction(h.ctx, connect.NewRequest(&forav1.TransactionRequest{
		Ver: helpers.ProtocolVersion, IdempotencyKey: idem, Requester: requester,
		Items: []*forav1.TransactionItem{
			{Offer: offer, AgentAcceptance: signAcceptanceFor(t, agentPriv, offer, requester, idem)},
		},
	}))
	if err != nil {
		t.Fatalf("execute %s: %v", uri, err)
	}
	endpoint := singleResultItem(t, execResp).GetRetrievalEndpoint()
	if endpoint == "" {
		t.Fatalf("execute %s delivered no retrieval endpoint", uri)
	}
	return endpoint
}
