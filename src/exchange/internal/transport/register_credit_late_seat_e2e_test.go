//go:build integration

package transport_test

import (
	"math/big"
	"testing"

	connect "connectrpc.com/connect"
	"github.com/google/uuid"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
)

// The welcome credit is Exchange configuration, not seat state. The Exchange
// parses EXCHANGE_DEFAULT_AGENT_CREDIT once at boot and Register grants from
// that parsed map, so the credit does not depend on the default seat existing
// when the process starts. A deployment that creates its seat after the
// Exchange is already running — the order a fresh stack is brought up in —
// grants the configured credit from the very first Register, with no restart.
//
// Each test below starts a second Exchange over the harness database whose
// default seat names a domain no row carries yet, creates that seat only after
// the service is up, and reads the result back through GetAccountStatus on the
// same public surface the Register went in through.

// lateSeatExchange starts an Exchange whose default seat does not exist yet,
// configured to grant credits at Register, and returns it with the seat's
// domain. Register on it is refused until the caller creates the seat.
func lateSeatExchange(t *testing.T, credits map[string]*big.Rat) (*registerHarness, string) {
	t.Helper()
	domain := "late-seat-" + uuid.NewString() + ".example"
	late := newRegisterHarness(t).restartedWith(t, func(deps *exchangeServerDeps) {
		deps.defaultTenantDomain = domain
		deps.defaultAgentCredits = credits
	})
	return late, domain
}

// registerAfterSeatCreated registers a fresh agent on h twice: once while the
// default seat is missing, which must be refused FailedPrecondition, and once
// after the seat is created. The refusal is what proves the service was built
// and serving before the seat existed. It returns the agent.
func registerAfterSeatCreated(t *testing.T, h *registerHarness, seatDomain, agentID string) *registerAgent {
	t.Helper()
	a := h.newAgent(t, agentID)
	_, err := a.client.Register(h.ctx, connect.NewRequest(newRegisterRequest(nil)))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("Register before the seat exists: code = %v, want FailedPrecondition (err=%v)", got, err)
	}
	repotest.SeedSeat(t, h.ctx, h.pool, repotest.Seat{Domain: seatDomain})
	registerCaller(t, h.ctx, a.client)
	return a
}

// TestRegisterDefaultCredit_SeatCreatedAfterStartIsGranted: an Exchange
// configured with a 7.25 welcome credit, whose default seat is created after
// the service was constructed, grants the full 7.25 at the agent's first
// Register. The balance is read back through GetAccountStatus.
func TestRegisterDefaultCredit_SeatCreatedAfterStartIsGranted(t *testing.T) {
	h, seat := lateSeatExchange(t, harnessCredit(t, "7.25"))
	a := registerAfterSeatCreated(t, h, seat, "late-seat-agent.example")

	assertOnlyBalance(t, statusBalances(t, h.ctx, a.client), "7.25")
}

// TestRegisterDefaultCredit_SeatCreatedAfterStartUnconfiguredGrantsNothing is
// the negative: the same late-created seat on an Exchange with no welcome
// credit configured grants nothing, so GetAccountStatus reports a zero balance.
func TestRegisterDefaultCredit_SeatCreatedAfterStartUnconfiguredGrantsNothing(t *testing.T) {
	h, seat := lateSeatExchange(t, nil)
	a := registerAfterSeatCreated(t, h, seat, "late-seat-unfunded-agent.example")

	assertOnlyBalance(t, statusBalances(t, h.ctx, a.client), "0")
}
