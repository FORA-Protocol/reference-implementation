//go:build integration

package transport_test

import (
	"context"
	"errors"
	"math/big"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
)

// GetAccountStatus.balances through the public RPC: the available balance in
// every currency the deployment bills in, holds subtracted. An empty list means
// "not reported", so every failure either leaves out one currency on purpose or
// fails the whole RPC.

// statusBalances reads balances through GetAccountStatus and returns them as
// currency → wire amount. It fails the test on a repeated currency.
func statusBalances(t *testing.T, ctx context.Context, client statusClient) map[string]string {
	t.Helper()
	resp, err := client.GetAccountStatus(ctx, connect.NewRequest(newAccountStatusRequest()))
	if err != nil {
		t.Fatalf("GetAccountStatus: %v", err)
	}
	out := map[string]string{}
	for _, b := range resp.Msg.GetBalances() {
		if _, dup := out[b.GetCurrency()]; dup {
			t.Fatalf("balances repeat currency %s: %v", b.GetCurrency(), resp.Msg.GetBalances())
		}
		if b.UnitCost != nil {
			t.Fatalf("balance in %s carries unit_cost", b.GetCurrency())
		}
		out[b.GetCurrency()] = b.GetAmount()
	}
	return out
}

// statusClient is the one method statusBalances needs, so it takes any
// generated Exchange client.
type statusClient interface {
	GetAccountStatus(
		context.Context, *connect.Request[forav1.GetAccountStatusRequest],
	) (*connect.Response[forav1.GetAccountStatusResponse], error)
}

// assertOnlyBalance asserts balances hold exactly one entry, in the harness
// currency, equal to want.
func assertOnlyBalance(t *testing.T, got map[string]string, want string) {
	t.Helper()
	if len(got) != 1 || got[harnessCurrency] != want {
		t.Fatalf("balances = %v, want {%s: %s}", got, harnessCurrency, want)
	}
}

// moneyDrop is before − after, computed in exact decimals.
func moneyDrop(t *testing.T, before, after string) string {
	t.Helper()
	b, err := helpers.ParseMoney(before)
	if err != nil {
		t.Fatalf("parse %q: %v", before, err)
	}
	a, err := helpers.ParseMoney(after)
	if err != nil {
		t.Fatalf("parse %q: %v", after, err)
	}
	return b.Sub(a).String()
}

// TestAccountStatusBalances_WelcomeCreditThenPurchase: the welcome credit is
// reported right after Register, and a paid execute lowers it by exactly the
// offer price.
func TestAccountStatusBalances_WelcomeCreditThenPurchase(t *testing.T) {
	h := newPushHarnessWith(t, pushHarnessOptions{
		billing:             billing.NewInMemoryAdapter(billing.InMemoryOptions{}),
		defaultAgentCredits: harnessCredit(t, "0.10"),
	})
	uris := seedThreePricedURIs(t, h)
	agent := h.newAgent(t, "balance-agent.example")
	registerCaller(t, h.ctx, agent.client)

	before := statusBalances(t, h.ctx, agent.client)
	assertOnlyBalance(t, before, "0.1")

	offer := discoverOffersForAgent(t, h, agent.id, uris[0])[0]
	resp, err := executeAsAgent(t, h, agent, offer)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	price := singleResultItem(t, resp).GetCost().GetAmount()
	after := statusBalances(t, h.ctx, agent.client)
	if drop := moneyDrop(t, before[harnessCurrency], after[harnessCurrency]); drop != price {
		t.Fatalf("balance dropped by %s, want the offer price %s", drop, price)
	}
}

// recordFailingAdapter fails every Record without settling, so the hold
// Authorize placed stays live. Everything else reaches the real adapter.
type recordFailingAdapter struct{ billing.Adapter }

func (recordFailingAdapter) Record(context.Context, string, int64, string) error {
	return errors.New("injected: record failed")
}

// TestAccountStatusBalances_FailedRecordKeepsHold: Record is best-effort, so a
// failed Record leaves the hold live and the available balance still lower by
// the price right after the execute. The hold expires later on a ledger with a
// hold timeout, so only the state right after the execute is asserted.
func TestAccountStatusBalances_FailedRecordKeepsHold(t *testing.T) {
	bill := recordFailingAdapter{billing.NewInMemoryAdapter(billing.InMemoryOptions{})}
	h := newPushHarnessWith(t, pushHarnessOptions{billing: bill, defaultAgentCredits: harnessCredit(t, "0.10")})
	uris := seedThreePricedURIs(t, h)
	agent := h.newAgent(t, "record-fails-agent.example")
	ref := registerCaller(t, h.ctx, agent.client)

	offer := discoverOffersForAgent(t, h, agent.id, uris[0])[0]
	if _, err := executeAsAgent(t, h, agent, offer); err != nil {
		t.Fatalf("execute: %v", err)
	}
	assertOnlyBalance(t, statusBalances(t, h.ctx, agent.client), "0.05")
	assertBalanceThrough(t, h.ctx, h.billing, ref, "0.10") // nothing settled
}

// TestAccountStatusBalances_FreeTierReportsNothing: the free tier keeps no
// balance, so the list is empty (not reported), never a zero.
func TestAccountStatusBalances_FreeTierReportsNothing(t *testing.T) {
	h := newRegisterHarnessWith(t, registerHarnessOptions{billing: billing.NewFreeAdapter()})
	a := h.newAgent(t, "free-tier-agent.example")
	registerCaller(t, h.ctx, a.client)
	if got := statusBalances(t, h.ctx, a.client); len(got) != 0 {
		t.Fatalf("balances = %v, want empty on the free tier", got)
	}
}

// TestAccountStatusBalances_ExactAtAssetScale: the smallest amount the ledger
// holds is reported exactly. A value finer than that fails the RPC instead of
// being rounded into a balance the account does not hold.
func TestAccountStatusBalances_ExactAtAssetScale(t *testing.T) {
	h := newRegisterHarness(t)
	a := h.newAgent(t, "tiny-balance-agent.example")
	ref := registerCaller(t, h.ctx, a.client)

	// Arranged through the adapter port: no public RPC grants an arbitrary
	// amount, and the tenant welcome-credit writer is not meant to hold one this
	// small.
	credit := func(amount *big.Rat, key string) {
		t.Helper()
		if err := h.billing.Credit(h.ctx, ref, billing.Amount{Value: amount, Currency: harnessCurrency}, key); err != nil {
			t.Fatalf("Credit: %v", err)
		}
	}
	credit(big.NewRat(1, 100_000_000), "tiny-1")
	assertOnlyBalance(t, statusBalances(t, h.ctx, a.client), "0.00000001")

	credit(big.NewRat(1, 1_000_000_000), "tiny-2")
	_, err := a.client.GetAccountStatus(h.ctx, connect.NewRequest(newAccountStatusRequest()))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Fatalf("code = %v, want Internal for a balance finer than the asset scale (err=%v)", got, err)
	}
}

// balanceFailingAdapter fails AvailableBalance with inject. Everything else
// reaches the real adapter.
type balanceFailingAdapter struct {
	billing.Adapter
	inject error
}

func (a balanceFailingAdapter) AvailableBalance(context.Context, string, string) (billing.Amount, error) {
	return billing.Amount{}, a.inject
}

// TestAccountStatusBalances_BillingErrorFailsRPC: an error other than the two
// sentinels fails the RPC with its mapped code. A ledger outage must never be
// reported as an empty list, because that reads as "not reported".
func TestAccountStatusBalances_BillingErrorFailsRPC(t *testing.T) {
	for name, tc := range map[string]struct {
		inject error
		want   connect.Code
	}{
		"ledger outage": {billing.ErrBackendUnavailable, connect.CodeUnavailable},
		"unclassified":  {errors.New("injected: ledger read failed"), connect.CodeInternal},
	} {
		t.Run(name, func(t *testing.T) {
			inner := billing.NewInMemoryAdapter(billing.InMemoryOptions{})
			h := newRegisterHarnessWith(t, registerHarnessOptions{
				billing: balanceFailingAdapter{Adapter: inner, inject: tc.inject},
			})
			a := h.newAgent(t, "balance-error-agent.example")
			registerCaller(t, h.ctx, a.client)
			_, err := a.client.GetAccountStatus(h.ctx, connect.NewRequest(newAccountStatusRequest()))
			if got := connect.CodeOf(err); got != tc.want {
				t.Fatalf("code = %v, want %v (err=%v)", got, tc.want, err)
			}
		})
	}
}

// TestTigerBeetleAccountStatusBalances_CurrencyAddedAfterRegistration: the agent
// registered while the deployment billed only in the harness currency. The
// operator then adds a second currency, so the agent has no ledger account in
// it. That currency is left out; the harness currency is still reported.
func TestTigerBeetleAccountStatusBalances_CurrencyAddedAfterRegistration(t *testing.T) {
	salt := sharedTB.Salt(t)
	h := newRegisterHarnessWith(t, registerHarnessOptions{
		billing:             newTBAdapter(t, salt),
		defaultAgentCredits: harnessCredit(t, "3.00"),
	})
	a := h.newAgent(t, "tb-currency-added-agent.example")
	registerCaller(t, h.ctx, a.client)

	both := mcCurrencies()
	later := h.restartedWith(t, func(deps *exchangeServerDeps) {
		deps.bill = newTBAdapterFor(t, salt, both...)
		deps.ledgerCurrencies = both
	})
	got := statusBalances(t, later.ctx, later.clientOn(a))
	if len(got) != 1 || got[e2eTBCurrency] != "3" {
		t.Fatalf("balances = %v, want only {%s: 3} (no %s account)", got, e2eTBCurrency, mcSecondCurrency)
	}
}

// TestTigerBeetleAccountStatusBalances_EveryBilledCurrency: a deployment billing
// in two currencies reports one entry per currency, each from its own ledger.
func TestTigerBeetleAccountStatusBalances_EveryBilledCurrency(t *testing.T) {
	salt := sharedTB.Salt(t)
	h := newRegisterHarnessWith(t, registerHarnessOptions{
		billing:          newTBAdapterFor(t, salt, mcCurrencies()...),
		ledgerCurrencies: mcCurrencies(),
		defaultAgentCredits: creditsOf(t, map[string]string{
			e2eTBCurrency: "30.00", mcSecondCurrency: "20.5",
		}),
	})
	a := h.newAgent(t, "tb-two-currencies-agent.example")
	registerCaller(t, h.ctx, a.client)

	got := statusBalances(t, h.ctx, a.client)
	if len(got) != 2 || got[e2eTBCurrency] != "30" || got[mcSecondCurrency] != "20.5" {
		t.Fatalf("balances = %v, want {%s: 30, %s: 20.5}", got, e2eTBCurrency, mcSecondCurrency)
	}
}
