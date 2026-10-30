//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/google/uuid"

	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// issuerID is the identity every test subscription record trusts.
const issuerID = "issuer.example"

// gatedAccess is a DIRECT agent that holds a verified grant for one
// subscription scope on the harness seat: its own key (the holder every chain
// binds to), a client signing as its own directory, and the subscription record
// trusting the issuer whose root key mints the chains. It is the arrangement
// any test needs to be offered, or to buy, a subscription-gated term.
type gatedAccess struct {
	h         *pushHarness
	scope     string
	agentID   string
	agentPub  ed25519.PublicKey
	agentPriv ed25519.PrivateKey
	rootPriv  ed25519.PrivateKey
	// direct signs as the agent's own directory; h.exchange signs as the Broker.
	direct   foraconnect.ExchangeServiceClient
	trust    repo.SubscriptionTrustRepo
	recordID string
}

// newGatedAccess arranges the agent, its client and the record for scope.
func newGatedAccess(t *testing.T, h *pushHarness, scope string) *gatedAccess {
	t.Helper()
	g := &gatedAccess{h: h, scope: scope, agentID: "agent-gated-" + uuid.NewString()}
	var err error
	g.agentPub, g.agentPriv, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h.resolver.Put(rwtestutil.MustThumbprintPriv(g.agentPriv), g.agentPub)
	g.direct = h.selfActingExchangeClient(g.agentID, g.agentPriv)

	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	g.rootPriv = rootPriv
	// The subscription record is arranged through the repository port: no RPC
	// creates one yet (Testing Doctrine point 9, tier 2). The missing surface is
	// the subscription administration plane, owned by the follow-up that adds
	// the admin RPC and console page; until then the boot fixture and this port
	// are the only writers.
	g.trust = repo.NewSubscriptionTrustRepo(sqlc.New(h.pool))
	rec, err := g.trust.Create(h.ctx, repo.SubscriptionRecord{
		TenantID: h.tenantID, Scope: scope, IssuerIdentity: issuerID, IssuerKeys: []ed25519.PublicKey{rootPub},
	})
	if err != nil {
		t.Fatalf("create subscription record: %v", err)
	}
	g.recordID = rec.ID
	return g
}

// chain is the happy-path root-only chain for the agent, one hour of life,
// covering exactly the record's scope. Callers change what they test.
func (g *gatedAccess) chain() testutil.DelegationChain {
	return testutil.DelegationChain{
		Keys: []ed25519.PrivateKey{g.rootPriv}, Holder: g.agentPub, RootIssuer: issuerID,
		Audience: harnessExchangeDomain, Scope: g.scope, Exp: time.Now().Add(time.Hour),
	}
}

// token mints the happy-path chain.
func (g *gatedAccess) token(t *testing.T) []byte {
	t.Helper()
	return testutil.MintDelegation(t, g.chain())
}

// requester is the agent's requester carrying the happy-path chain and the
// stated scopes, for a discovery or a purchase.
func (g *gatedAccess) requester(t *testing.T, stated ...string) *forav1.Requester {
	t.Helper()
	r := newRequester(g.agentID, "agent.example", stated...)
	r.Delegation = &forav1.Delegation{Token: g.token(t)}
	return r
}

// discover queries uri directly as the agent with the happy-path chain and
// returns the offers, asking for the given extension profiles.
func (g *gatedAccess) discover(t *testing.T, uri string, profiles ...string) []*forav1.Offer {
	t.Helper()
	query := newResourceQuery(g.requester(t), []string{uri})
	query.SupportedProfiles = profiles
	resp, err := g.direct.DiscoverResources(g.h.ctx, connect.NewRequest(query))
	if err != nil {
		t.Fatalf("discover %s: %v", uri, err)
	}
	return resp.Msg.GetOffers()
}
