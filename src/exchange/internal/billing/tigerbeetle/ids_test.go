package tigerbeetle

import (
	"errors"
	"testing"
)

func TestAccountID_DeterministicAndPrefixNamespaced(t *testing.T) {
	t.Parallel()
	a1, err := AccountID(PrefixAgent, "EUR", "agent-1")
	if err != nil {
		t.Fatalf("AccountID: %v", err)
	}
	a2, err := AccountID(PrefixAgent, "EUR", "agent-1")
	if err != nil {
		t.Fatalf("AccountID (repeat): %v", err)
	}
	if a1.Bytes() != a2.Bytes() {
		t.Fatal("AccountID is not deterministic for equal inputs")
	}
	// Equal business id under different prefixes must never collide.
	owner, _ := AccountID(PrefixOwner, "EUR", "agent-1")
	platform, _ := AccountID(PrefixPlatform, "EUR", "agent-1")
	switch {
	case a1.Bytes() == owner.Bytes(),
		a1.Bytes() == platform.Bytes(),
		owner.Bytes() == platform.Bytes():
		t.Fatal("prefixes collided for an equal business id")
	}
}

// A TigerBeetle id is unique per cluster, not per ledger, so the same agent on
// two ledgers must derive two different account ids. If it did not, the second
// ledger's account could never be created.
func TestAccountID_CurrencyNamespaced(t *testing.T) {
	t.Parallel()
	for _, p := range []Prefix{PrefixAgent, PrefixOwner, PrefixPlatform} {
		eur, err := AccountID(p, "EUR", "business-1")
		if err != nil {
			t.Fatalf("AccountID(%s, EUR): %v", p, err)
		}
		usd, err := AccountID(p, "USD", "business-1")
		if err != nil {
			t.Fatalf("AccountID(%s, USD): %v", p, err)
		}
		if eur.Bytes() == usd.Bytes() {
			t.Fatalf("prefix %s: EUR and USD derived the same account id", p)
		}
	}
}

// The currency and the business id are joined by a colon, so no pair of inputs
// can produce one shared hashed string. Without the separator, ("EU", "Ragent")
// and ("EUR", "agent") would hash identically.
func TestAccountID_CurrencyBoundaryIsUnambiguous(t *testing.T) {
	t.Parallel()
	a, err := AccountID(PrefixAgent, "EUR", "agent")
	if err != nil {
		t.Fatalf("AccountID: %v", err)
	}
	b, err := AccountID(PrefixAgent, "EU", "Ragent")
	if err != nil {
		t.Fatalf("AccountID: %v", err)
	}
	if a.Bytes() == b.Bytes() {
		t.Fatal("currency/business-id boundary is ambiguous: two input pairs collided")
	}
}

func TestAccountID_RejectsEmpty(t *testing.T) {
	t.Parallel()
	if _, err := AccountID(PrefixAgent, "EUR", ""); !errors.Is(err, ErrEmptyID) {
		t.Fatalf("empty business id: want ErrEmptyID, got %v", err)
	}
	// An empty currency would collapse every currency onto one id — the exact
	// collision the argument exists to prevent.
	if _, err := AccountID(PrefixAgent, "", "agent-1"); !errors.Is(err, ErrEmptyID) {
		t.Fatalf("empty currency: want ErrEmptyID, got %v", err)
	}
}

func TestCurrencyLedgerTable_RoundTrips(t *testing.T) {
	t.Parallel()
	for _, ledger := range []uint32{978, 840} {
		currency, ok := CurrencyForLedger(ledger)
		if !ok {
			t.Fatalf("CurrencyForLedger(%d): not supported", ledger)
		}
		back, ok := LedgerForCurrency(currency)
		if !ok || back != ledger {
			t.Fatalf("LedgerForCurrency(%q) = %d, %v; want %d, true", currency, back, ok, ledger)
		}
	}
	if _, ok := CurrencyForLedger(826); ok {
		t.Fatal("CurrencyForLedger(826): want unsupported")
	}
	if _, ok := LedgerForCurrency("GBP"); ok {
		t.Fatal("LedgerForCurrency(GBP): want unsupported")
	}
	// Configuration errors quote this string, so it must name every entry.
	if got := SupportedLedgers(); got != "840=USD, 978=EUR" {
		t.Fatalf("SupportedLedgers() = %q", got)
	}
}

func TestTransferID_DeterministicAndRejectsEmpty(t *testing.T) {
	t.Parallel()
	a, err := TransferID("idem-key-1")
	if err != nil {
		t.Fatalf("TransferID: %v", err)
	}
	b, _ := TransferID("idem-key-1")
	if a.Bytes() != b.Bytes() {
		t.Fatal("TransferID is not deterministic for equal keys")
	}
	if _, err := TransferID(""); !errors.Is(err, ErrEmptyID) {
		t.Fatalf("empty key: want ErrEmptyID, got %v", err)
	}
}

func TestReduce_RejectsReservedIDs(t *testing.T) {
	t.Parallel()
	var zero [32]byte
	if _, err := reduce(zero); !errors.Is(err, ErrReservedID) {
		t.Fatalf("all-zero digest: want ErrReservedID, got %v", err)
	}
	var allFF [32]byte
	for i := range allFF {
		allFF[i] = 0xFF
	}
	if _, err := reduce(allFF); !errors.Is(err, ErrReservedID) {
		t.Fatalf("all-0xFF digest: want ErrReservedID, got %v", err)
	}
}

func TestReduce_ValidDigestPassesThrough(t *testing.T) {
	t.Parallel()
	var sum [32]byte
	sum[0], sum[15] = 0x01, 0x02 // low 16 bytes are non-reserved
	id, err := reduce(sum)
	if err != nil {
		t.Fatalf("reduce(valid): %v", err)
	}
	b := id.Bytes()
	if b[0] != 0x01 || b[15] != 0x02 {
		t.Fatalf("reduce dropped the low 16 bytes: got %v", b)
	}
}

// TestAccountID_PinnedVectors pins the account-id derivation against checked-in
// vectors, one per currency and one per prefix that the operator scripts also
// derive. Two shell copies of this derivation exist and no compiler checks them:
// deploy/terraform/scripts/fund-staging-agent.sh derives the agent and platform
// liquidity accounts it funds, and agent-balances.sh derives the agent accounts
// it reads. A python test beside those scripts asserts the SAME vectors, so a
// change here that is not mirrored there breaks both sides visibly instead of
// leaving the scripts funding or reading accounts the Exchange never touches.
func TestAccountID_PinnedVectors(t *testing.T) {
	t.Parallel()
	const ref = "00000000-0000-4000-8000-000000000000"
	for _, tc := range []struct {
		prefix     Prefix
		currency   string
		businessID string
		want       string
	}{
		{PrefixAgent, "EUR", ref, "dbebb6a3df128036cb0992b4f0d2722e"},
		{PrefixAgent, "USD", ref, "b8b601c143fa91adef31ffbf1c515a12"},
		{PrefixPlatform, "EUR", PlatformLiquidityID, "f7f562042a214492e4ea9f06ee4ad7f7"},
		{PrefixPlatform, "USD", PlatformLiquidityID, "cdcf53ba79768b2fe726f115049c3d14"},
	} {
		id, err := AccountID(tc.prefix, tc.currency, tc.businessID)
		if err != nil {
			t.Fatalf("AccountID(%s, %s, %s): %v", tc.prefix, tc.currency, tc.businessID, err)
		}
		if got := EncodeID(id); got != tc.want {
			t.Errorf("AccountID(%s, %s, %s) = %s, want %s — the derivation moved; "+
				"update the operator scripts and their test in the same change",
				tc.prefix, tc.currency, tc.businessID, got, tc.want)
		}
	}
}
