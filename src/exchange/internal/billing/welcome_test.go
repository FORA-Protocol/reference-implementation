package billing_test

import (
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tigerbeetle"
)

// TestWelcomeCreditKey_PinnedTransferVector pins the full welcome-slot
// derivation — key string and the transfer id hashed from it — against
// checked-in vectors, one per currency. The same derivation is hand-written in
// shell in deploy/terraform/scripts/fund-staging-agent.sh (the
// "service-welcome" label branch), which no compiler checks: if this test
// fails, the Go side moved and that script MUST be updated in the same change,
// or a script prefund and the Register grant would land in different ledger
// slots and double-credit the agent.
//
// Both currencies are pinned, not just one, because the currency is what keeps
// the two slots apart. A derivation that dropped it would still pass a
// single-currency vector while silently collapsing every currency onto one
// slot.
func TestWelcomeCreditKey_PinnedTransferVector(t *testing.T) {
	const ref = "00000000-0000-4000-8000-000000000000"

	for _, tc := range []struct {
		currency string
		// wantHex is the hex form EncodeID renders (the little-endian low 16
		// bytes of sha256(key)); wantDecimal is what fund-staging-agent.sh's
		// tb_id computes and hands to the tigerbeetle repl.
		wantHex, wantDecimal string
	}{
		{
			currency:    "EUR",
			wantHex:     "f7dc45b0cafa5954c43151fc768e1050",
			wantDecimal: "106424205941705102778718495573608357111",
		},
		{
			currency:    "USD",
			wantHex:     "f39101c13fb3c8d34ecaebf4c0327b12",
			wantDecimal: "24565785845815050169005649922871103987",
		},
	} {
		t.Run(tc.currency, func(t *testing.T) {
			key := billing.WelcomeCreditKey(ref, tc.currency)
			if want := "service-welcome:" + ref + ":" + tc.currency; key != want {
				t.Fatalf("WelcomeCreditKey(%q, %q) = %q, want %q — "+
					"update fund-staging-agent.sh in the same change", ref, tc.currency, key, want)
			}
			id, err := tigerbeetle.TransferID(key)
			if err != nil {
				t.Fatalf("TransferID(%q): %v", key, err)
			}
			if got := tigerbeetle.EncodeID(id); got != tc.wantHex {
				t.Fatalf("welcome transfer id = %s, want %s — the ledger-slot derivation moved; "+
					"update fund-staging-agent.sh in the same change", got, tc.wantHex)
			}
			if got := id.BigInt().String(); got != tc.wantDecimal {
				t.Fatalf("welcome transfer id (decimal) = %s, want %s — "+
					"must match fund-staging-agent.sh's tb_id", got, tc.wantDecimal)
			}
		})
	}

	// The two currencies must not share a slot. This is the property the whole
	// per-currency key exists for, so it is asserted directly rather than left
	// implied by the two vectors above differing.
	eur, err := tigerbeetle.TransferID(billing.WelcomeCreditKey(ref, "EUR"))
	if err != nil {
		t.Fatalf("TransferID(EUR): %v", err)
	}
	usd, err := tigerbeetle.TransferID(billing.WelcomeCreditKey(ref, "USD"))
	if err != nil {
		t.Fatalf("TransferID(USD): %v", err)
	}
	if eur.Bytes() == usd.Bytes() {
		t.Fatal("the EUR and USD welcome slots share one transfer id: " +
			"the second currency's grant would silently do nothing")
	}
}
