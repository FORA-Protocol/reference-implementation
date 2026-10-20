package foracost_test

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foracost"
)

func item(amount, currency string) *forav1.TransactionResultItem {
	if currency == "" && amount == "" {
		return &forav1.TransactionResultItem{}
	}
	return &forav1.TransactionResultItem{Cost: &forav1.Cost{Amount: amount, Currency: currency}}
}

// TestBatchTotal pins the per-currency aggregation contract: a single scalar
// only when the whole batch shares one currency, nil for mixed-currency / no
// cost, exact decimal sums, and a hard error on a malformed amount.
func TestBatchTotal(t *testing.T) {
	t.Run("single currency sums exactly", func(t *testing.T) {
		got, err := foracost.BatchTotal([]*forav1.TransactionResultItem{
			item("2.5", "USD"), item("5", "USD"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.GetCurrency() != "USD" || got.GetAmount() != "7.5" {
			t.Fatalf("got %v, want amount=7.5 currency=USD", got)
		}
	})

	t.Run("mixed currency drops the scalar", func(t *testing.T) {
		got, err := foracost.BatchTotal([]*forav1.TransactionResultItem{
			item("2.5", "USD"), item("3", "EUR"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("got %v, want nil (mixed currency)", got)
		}
	})

	t.Run("no costed items yields nil", func(t *testing.T) {
		got, err := foracost.BatchTotal([]*forav1.TransactionResultItem{
			item("", ""), item("0.10", ""), // nil cost, then empty currency
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("got %v, want nil (no costed items)", got)
		}
	})

	t.Run("skips uncosted items between costed ones", func(t *testing.T) {
		got, err := foracost.BatchTotal([]*forav1.TransactionResultItem{
			item("1.00", "USD"), item("", ""), item("0.99", "USD"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.GetAmount() != "1.99" {
			t.Fatalf("got %v, want amount=1.99 currency=USD", got)
		}
	})

	t.Run("malformed amount errors", func(t *testing.T) {
		_, err := foracost.BatchTotal([]*forav1.TransactionResultItem{
			item("not-a-number", "USD"),
		})
		if err == nil {
			t.Fatal("want error on malformed amount, got nil")
		}
	})
}

// TestPerCurrency pins the per-currency totals the Broker reports: one entry per
// currency in the order each first appears, exact decimal sums never mixed
// across currencies, nothing for uncosted items, and a hard error on a malformed
// amount.
func TestPerCurrency(t *testing.T) {
	t.Run("one entry per currency in first-appearance order", func(t *testing.T) {
		got, err := foracost.PerCurrency([]*forav1.TransactionResultItem{
			item("1.10", "EUR"), item("2.5", "USD"), item("", ""), item("0.90", "EUR"), item("5", "USD"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []struct{ amount, currency string }{{"2", "EUR"}, {"7.5", "USD"}}
		if len(got) != len(want) {
			t.Fatalf("got %d totals (%v), want %d", len(got), got, len(want))
		}
		for i, w := range want {
			if got[i].GetAmount() != w.amount || got[i].GetCurrency() != w.currency {
				t.Errorf("totals[%d] = %s %s, want %s %s",
					i, got[i].GetAmount(), got[i].GetCurrency(), w.amount, w.currency)
			}
			if got[i].UnitCost != nil {
				t.Errorf("totals[%d] carries unit_cost %q, want it unset", i, got[i].GetUnitCost())
			}
		}
	})

	t.Run("no costed items yields no totals", func(t *testing.T) {
		got, err := foracost.PerCurrency([]*forav1.TransactionResultItem{item("", ""), item("0.10", "")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %v, want no totals", got)
		}
	})

	t.Run("malformed amount errors", func(t *testing.T) {
		_, err := foracost.PerCurrency([]*forav1.TransactionResultItem{item("1", "USD"), item("x", "EUR")})
		if err == nil {
			t.Fatal("want error on malformed amount, got nil")
		}
	})
}
