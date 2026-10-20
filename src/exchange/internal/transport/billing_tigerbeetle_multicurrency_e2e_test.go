//go:build integration

package transport_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tbtest"
)

// End-to-end registration and spending on a deployment that bills in TWO
// currencies, against the real TigerBeetle cluster. The single-currency flows
// are in billing_tigerbeetle_register_e2e_test.go and
// billing_tigerbeetle_e2e_test.go; everything here is about the two currencies
// staying separate through the public RPCs.
//
// Currency roles: e2eTBCurrency is the one the harness prices its offers in and
// the one it lists first, and mcSecondCurrency is the added one. Naming them
// this way rather than writing "USD" and "EUR" keeps these cases correct if the
// harness currency ever changes.

const (
	// mcSecondCurrency is the second currency these deployments bill in, and
	// mcSecondLedger its ISO 4217 numeric. It is deliberately NOT the harness
	// currency: the point of every case below is that the two do not mix.
	mcSecondCurrency        = "EUR"
	mcSecondLedger   uint32 = 978
)

// mcLedger is the test handle for the second currency's ledger, the sibling of
// tbLedger() for the harness currency.
func mcLedger() tbtest.Ledger {
	return tbtest.Ledger{Client: tbClient, ID: mcSecondLedger}
}

// mcCurrencies is the configured set, harness currency first — so the harness
// currency is also what this Exchange advertises as base_currency.
func mcCurrencies() []string { return []string{e2eTBCurrency, mcSecondCurrency} }

// assertBalanceIn asserts an agent's settled balance in one named currency,
// through the same adapter read surface assertBalanceThrough uses (the
// Testing-Doctrine §9 tier-2 fallback: no public RPC reports the settled balance).
func assertBalanceIn(
	t *testing.T, ctx context.Context, bill billing.Adapter, ref, currency, want string,
) {
	t.Helper()
	bal, err := bill.GetBalance(ctx, ref, currency)
	if err != nil {
		t.Fatalf("GetBalance(%q, %s): %v", ref, currency, err)
	}
	if bal.Currency != currency {
		t.Fatalf("GetBalance(%q, %s) returned a %s amount", ref, currency, bal.Currency)
	}
	if got := bal.Value.FloatString(2); got != want {
		t.Fatalf("balance(%q, %s) = %s, want %s", ref, currency, got, want)
	}
}

// TestTigerBeetleRegister_WelcomeCreditPerCurrency: an Exchange configured with
// a welcome credit in each billed currency grants BOTH at Register, each as its
// own posted transfer on its own ledger. A repeat Register takes the fast path
// and changes neither balance.
func TestTigerBeetleRegister_WelcomeCreditPerCurrency(t *testing.T) {
	salt := sharedTB.Salt(t)
	h := newRegisterHarnessWith(t, registerHarnessOptions{
		billing:          newTBAdapterFor(t, salt, mcCurrencies()...),
		ledgerCurrencies: mcCurrencies(),
		// Deliberately different amounts: equal ones would pass even if both
		// grants landed on one ledger.
		defaultAgentCredits: creditsOf(t, map[string]string{
			e2eTBCurrency:    "30.00",
			mcSecondCurrency: "20.00",
		}),
	})

	a := h.newAgent(t, "tb-multi-credit-agent.example")
	ref := registerCaller(t, h.ctx, a.client)
	assertBalanceIn(t, h.ctx, h.billing, ref, e2eTBCurrency, "30.00")
	assertBalanceIn(t, h.ctx, h.billing, ref, mcSecondCurrency, "20.00")

	if again := registerCaller(t, h.ctx, a.client); again != ref {
		t.Fatalf("repeat billing_ref = %q, want %q", again, ref)
	}
	assertBalanceIn(t, h.ctx, h.billing, ref, e2eTBCurrency, "30.00")
	assertBalanceIn(t, h.ctx, h.billing, ref, mcSecondCurrency, "20.00")
}

// TestTigerBeetleRegister_UnconfiguredCurrencyIsNotGranted: a welcome credit
// configured in a currency the deployment does not bill in is skipped, not sent
// to the billing gate. Boot refuses that configuration, but the service is not
// only constructed by boot: the grant loop walks the BILLED currencies, so such
// a map cannot fail every registration — the billed currency is still granted.
func TestTigerBeetleRegister_UnconfiguredCurrencyIsNotGranted(t *testing.T) {
	salt := sharedTB.Salt(t)
	// The deployment bills in the harness currency only.
	h := newRegisterHarnessWith(t, registerHarnessOptions{
		billing: newTBAdapter(t, salt),
		defaultAgentCredits: creditsOf(t, map[string]string{
			e2eTBCurrency:    "15.00",
			mcSecondCurrency: "99.00", // not billed in
		}),
	})

	ref := registerCaller(t, h.ctx, h.newAgent(t, "tb-stale-currency-agent.example").client)
	assertBalanceIn(t, h.ctx, h.billing, ref, e2eTBCurrency, "15.00")
	// The unconfigured currency has no account at all, so the read fails rather
	// than reporting a balance the deployment does not keep.
	if _, err := h.billing.GetBalance(h.ctx, ref, mcSecondCurrency); err == nil {
		t.Errorf("GetBalance in %s succeeded on a deployment that does not bill in it",
			mcSecondCurrency)
	}
}

// failOnceInCurrency wraps a real adapter and fails the FIRST Credit in one
// named currency, then passes everything through. It is not a mock of the
// ledger: every call still reaches the real TigerBeetle adapter, and only the
// one injected failure is synthetic. This is how a registration is made to die
// between two grants — the point the retry has to be safe at.
type failOnceInCurrency struct {
	billing.Adapter
	currency string
	failed   atomic.Bool
}

func (f *failOnceInCurrency) Credit(
	ctx context.Context, billingRef string, amount billing.Amount, key string,
) error {
	if amount.Currency == f.currency && f.failed.CompareAndSwap(false, true) {
		return errInjectedCreditFailure
	}
	return f.Adapter.Credit(ctx, billingRef, amount, key)
}

var errInjectedCreditFailure = errors.New("injected: credit failed after the first currency")

// TestTigerBeetleRegister_PartialGrantRetriesOnlyTheMissingCurrency is the
// reason each currency has its own welcome slot.
//
// The registration below grants the first currency, then fails before the
// second. The whole RPC fails, and because the grant runs BEFORE SetBillingRef
// the agent's row still has no billing_ref — so the retry re-enters the first
// registration path rather than the fast path. On that retry:
//
//   - the first currency's slot is already occupied, so its grant is a no-op
//     and the balance does NOT double;
//   - the second currency's slot is still free, so it is granted, once.
//
// With one shared slot across currencies (the derivation before this work),
// the retry would find the slot taken by the first currency and the second
// currency would silently stay at zero forever.
func TestTigerBeetleRegister_PartialGrantRetriesOnlyTheMissingCurrency(t *testing.T) {
	salt := sharedTB.Salt(t)
	ledger := newTBAdapterFor(t, salt, mcCurrencies()...)
	adapter := &failOnceInCurrency{Adapter: ledger, currency: mcSecondCurrency}
	h := newRegisterHarnessWith(t, registerHarnessOptions{
		billing:          adapter,
		ledgerCurrencies: mcCurrencies(),
		defaultAgentCredits: creditsOf(t, map[string]string{
			e2eTBCurrency:    "30.00",
			mcSecondCurrency: "20.00",
		}),
	})

	a := h.newAgent(t, "tb-partial-grant-agent.example")

	// First attempt: the harness-currency grant lands, the second currency's
	// fails, and the RPC fails with it.
	if _, err := a.client.Register(h.ctx, connect.NewRequest(newRegisterRequest(nil))); err == nil {
		t.Fatal("first Register must fail: the second currency's grant was made to fail")
	}
	if !adapter.failed.Load() {
		t.Fatal("the injected failure never fired — the grant loop did not reach the second currency")
	}

	// Retry: the same subdomain, so the SoR returns the ref it already stored
	// ("the stored id wins"), and the grant loop runs again over both currencies.
	ref := registerCaller(t, h.ctx, a.client)
	assertBalanceIn(t, h.ctx, h.billing, ref, e2eTBCurrency, "30.00")
	assertBalanceIn(t, h.ctx, h.billing, ref, mcSecondCurrency, "20.00")

	// And a third registration changes nothing: by now the row carries the ref,
	// so this one takes the fast path and makes no billing call at all.
	if again := registerCaller(t, h.ctx, a.client); again != ref {
		t.Fatalf("third billing_ref = %q, want %q", again, ref)
	}
	assertBalanceIn(t, h.ctx, h.billing, ref, e2eTBCurrency, "30.00")
	assertBalanceIn(t, h.ctx, h.billing, ref, mcSecondCurrency, "20.00")
}

// TestTigerBeetleBatch_BothCurrenciesPaidDebitSeparately executes ONE batch
// holding two PAID items in different currencies, against the real ledger, with
// both currencies funded.
//
// Each item is charged to its own currency's balance: the response reports no
// single total_cost (the Exchange refuses to add amounts in different
// currencies), and each balance drops by its own item's price and nothing else.
// A ledger that pooled the two currencies would leave one balance short by the
// other item's price, and a renderer that summed them would produce a
// total_cost that means nothing.
//
// This is a NEW case, not a change to the existing mixed-currency batch test:
// that one runs on the in-memory harness with a paid item and a free item, and
// still covers what it covered.
func TestTigerBeetleBatch_BothCurrenciesPaidDebitSeparately(t *testing.T) {
	salt := sharedTB.Salt(t)
	// The adapter, not h.billing: on this harness h.billing is the in-memory
	// balance holder used by the single-currency fixtures, while the Exchange
	// itself calls the TigerBeetle adapter returned here.
	h, adapter := newTBHarnessFor(t, salt, mcCurrencies())

	// Fund the same billing_ref on both ledgers. Different amounts again, so a
	// charge against the wrong balance cannot pass unnoticed.
	tbLedger().MustFundAgent(t, salt, h.billingRef, "10.00")
	mcLedger().MustFundAgent(t, salt, h.billingRef, "4.00")

	paidHarnessCurrency, paidSecondCurrency := seedTwoPaidCurrencies(t, h)
	resp := execTwoItemBatch(t, h, paidHarnessCurrency, paidSecondCurrency, "mc-batch-1")

	if len(resp.GetItems()) != 2 {
		t.Fatalf("items = %d, want 2", len(resp.GetItems()))
	}
	// Both paid items are approved, and each keeps its own currency's cost.
	costs := map[string]string{}
	for _, item := range resp.GetItems() {
		if item.GetDenialReason() != forav1.DenialReason_DENIAL_REASON_UNSPECIFIED {
			t.Fatalf("item %q denied: %v", item.GetOfferId(), item.GetDenialReason())
		}
		if item.GetRetrievalEndpoint() == "" {
			t.Fatalf("item %q: retrieval_endpoint missing — both paid items must be approved",
				item.GetOfferId())
		}
		if cost := item.GetCost(); cost != nil {
			costs[cost.GetCurrency()] = cost.GetAmount()
		}
	}
	if costs[e2eTBCurrency] != "0.05" || costs[mcSecondCurrency] != "0.05" {
		t.Fatalf("per-item costs = %v, want 0.05 in each currency", costs)
	}
	// No combined total: the two amounts are in different currencies, so there
	// is no single number to report.
	if total := resp.GetTotalCost(); total != nil {
		t.Errorf("total_cost = {amount:%q currency:%q}, want none for a two-currency batch",
			total.GetAmount(), total.GetCurrency())
	}

	// The seeded price is 0.05 per item in each currency (seedPricedTermCurrency),
	// so each balance drops by its own item's price and by nothing else.
	assertBalanceIn(t, h.ctx, adapter, h.billingRef, e2eTBCurrency, "9.95")
	assertBalanceIn(t, h.ctx, adapter, h.billingRef, mcSecondCurrency, "3.95")
}

// seedTwoPaidCurrencies pushes one PAID entry per billed currency and returns
// their two discovered, signed Offers — the two-paid-currencies sibling of
// seedMixedCurrency, which pairs a paid item with a free one. Both are priced
// 0.05 per access by seedPricedTermCurrency.
func seedTwoPaidCurrencies(t *testing.T, h *testHarness) (harnessCur, secondCur *forav1.Offer) {
	t.Helper()
	first := "/articles/paid-" + strings.ToLower(e2eTBCurrency)
	second := "/articles/paid-" + strings.ToLower(mcSecondCurrency)
	_, err := h.catalogClient.PushResources(h.ctx, connect.NewRequest(
		newPushRequest(h.tenantID, "agent-test", []*forav1.ResourceEntry{
			{
				Domain: h.siteDomain, Path: first,
				Terms: []*forav1.LicenseTerm{seedPricedTermCurrency(e2eTBCurrency)},
			},
			{
				Domain: h.siteDomain, Path: second,
				Terms: []*forav1.LicenseTerm{seedPricedTermCurrency(mcSecondCurrency)},
			},
		})))
	if err != nil {
		t.Fatalf("seed push: %v", err)
	}
	return discoverOffer(t, h, "https://"+h.siteDomain+first),
		discoverOffer(t, h, "https://"+h.siteDomain+second)
}
