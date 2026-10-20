//go:build integration

package transport_test

// Where a purchase finds an Exchange, asserted through
// fora.v1.BrokerService/ExecuteTransaction.
//
// The registry's endpoint column is an operator-supplied bootstrap value. The
// resolve path dials whatever the Exchange's own /.well-known/fora.json
// advertises, and the health refresher probes that same resolved address. A
// purchase derives its target from the signed offer.exchange domain, so its
// admission is read off the row that domain matched and the column is never
// consulted: a stale column must not refuse a live, trusted Exchange.
//
// The refresher's probe client is the other half. It dials the address an
// Exchange advertises about itself, and its verdict decides whether a purchase
// is sent at all, so it has to be the SSRF-guarded client.
//
// Round-trip honesty: every leg is a protocol round-trip through the signed
// BrokerService client; the registry is arranged through the production
// repository interface, and the refresher writes through it. Nothing reads the
// database directly.

import (
	"context"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/registry"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// staleEndpoint is an address that refuses a connection immediately, standing
// in for a bootstrap value an Exchange has outgrown. A live second server would
// test something else: the point is that this address is never dialled at all.
const staleEndpoint = refusingOrigin

// staleTheColumn points a registered Exchange's endpoint column at a dead
// address while its well-known keeps advertising the live one.
//
// Arranged through the production repository interface because the broker
// exposes no registry-administration RPC — an operator edits the bootstrap file
// and restarts. That missing surface is the reason this is a tier-2 arrange
// rather than a round trip through a public one.
func staleTheColumn(t *testing.T, ctx context.Context, r *repo.PgxExchangeRepo, domain string) {
	t.Helper()
	if _, err := r.UpsertFromBootstrap(ctx, repo.Exchange{
		ID:                "mp-" + domain,
		Domain:            domain,
		Endpoint:          staleEndpoint,
		TrustLevel:        "VERIFIED",
		SupportedProfiles: []string{"fora-news-v1"},
		Priority:          10,
	}); err != nil {
		t.Fatalf("stale the registry endpoint: %v", err)
	}
}

// TestPurchase_StaleRegistryColumnDoesNotRefuseThePurchase pins that a purchase
// never consults the column.
//
// No refresh pass runs here, and that is the assertion. The Exchange is
// trusted, healthy and advertising a live endpoint; the only thing wrong is a
// column the operator typed. If the admission still matched on that column, the
// Broker would refuse a working Exchange as one nobody registered — while the
// resolve path, reading the same registry, kept serving it.
//
// What is asserted is that the Broker admitted the Exchange and dialled the
// address its manifest advertises. That address answers 503 with no body, so
// the call succeeds with the item refused "unavailable" by that Exchange: the
// sub-request was sent, and nothing answered for an Exchange. An admission
// refusal would instead fail the whole call with FAILED_PRECONDITION.
func TestPurchase_StaleRegistryColumnDoesNotRefuseThePurchase(t *testing.T) {
	ctx := t.Context()
	env := newPurchaseTestEnvAt(t, startUnavailableExchange(t), false)
	staleTheColumn(t, ctx, env.exchangeRepo, env.exchangeDom)

	resp := env.buy(t, env.purchaseRequest(t, "tx-stale-column", line{"offer-1", env.exchangeDom}))

	if got := resp.GetItems()[0].GetRefusal().GetParty(); got != env.exchangeDom {
		t.Errorf("item = %v, want it carried to %s — the Exchange is live and trusted, and only "+
			"its registry column is stale", resp.GetItems()[0], env.exchangeDom)
	}
	if env.exchange.arrivals() != 1 {
		t.Errorf("sub-requests = %d, want 1 — the Broker refused the Exchange over a column value "+
			"that names an address nothing routes to", env.exchange.arrivals())
	}
}

// TestPurchase_ProbeRefusedByTheGuardLeavesTheExchangeDownAndUnadmitted is the
// negative leg for the guarded probe client.
//
// The refresher dials whatever an Exchange's own well-known advertises. An
// Exchange that serves its well-known from a reachable host can advertise an
// address inside the deployment's own network, and an unguarded probe would get
// an answer from it, turn the row live, and admit purchases to it every
// interval.
//
// This drives the production client, in the production posture. The Exchange
// address listens on loopback, which that client refuses — exactly what it
// would do to a metadata or private address a real manifest advertised. The two
// legs are what make it mean something: the Exchange is reachable and admitted
// first, and refused only after a pass whose probe the guard turned away.
//
// The address answers every purchase 503 with no body, so "admitted and sent"
// is read as the request arriving there, and nothing answers for an Exchange.
func TestPurchase_ProbeRefusedByTheGuardLeavesTheExchangeDownAndUnadmitted(t *testing.T) {
	// Production posture: neither opt-out set, so the client refuses both a
	// private address and a plaintext scheme.
	t.Setenv("SKIP_SSRF", "")
	t.Setenv("ALLOW_INSECURE", "")

	ctx := t.Context()
	env := newPurchaseTestEnvAt(t, startUnavailableExchange(t), false)

	// Leg 1: the Exchange is registered, live and sent the purchase. Without
	// this the second leg would prove only that the fixture refuses everything.
	env.buy(t, env.purchaseRequest(t, "tx-guarded", line{"offer-1", env.exchangeDom}))
	if env.exchange.arrivals() != 1 {
		t.Fatalf("sub-requests = %d, want 1 — the Exchange has to be admitted first for the "+
			"refusal below to mean anything", env.exchange.arrivals())
	}

	// One pass with the production probe client. Resolution still succeeds, so
	// the pass learns WHERE the Exchange is; the guard refuses the dial to it,
	// so the pass learns the Exchange is not answering.
	refreshOnce(t, registry.NewRefresher(
		env.exchangeRepo, env.endpoints,
		registry.ProbeTransport{HTTP: resolvers.NewGuardedClientFromEnv(), Scheme: "http"},
		testutil.DiscardLogger(), 0,
	), ctx)

	// Leg 2: the identical purchase is now refused for the health state. The
	// registry still knows this Exchange, so answering "not a registered
	// exchange" would be false about a row the operator wrote.
	ce := env.refused(t, env.purchaseRequest(t, "tx-guarded-again", line{"offer-1", env.exchangeDom}))
	assertNotAdmitted(t, ce, env.exchangeDom, "health check")
	if env.exchange.arrivals() != 1 {
		t.Errorf("sub-requests = %d, want still 1 — a refused Exchange must not be sent a purchase",
			env.exchange.arrivals())
	}
}
