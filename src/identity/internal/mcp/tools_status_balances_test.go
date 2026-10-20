//go:build integration

package mcp_test

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

// fora_status balances: what the account can spend now, passed through from the
// Exchange. Every entry carries the member as a list, never null, whichever
// branch built it.

// statusAt asks the fixture's Exchange for the caller's status and returns the
// one entry.
func statusAt(t *testing.T, f *fixture) statusEntry {
	t.Helper()
	a := f.provision(t, "dev-one")
	out := callTool[statusResult](t, f.connect(t, a.Token), "fora_status", map[string]any{
		"exchange": f.exchange.Domain(t),
	})
	if len(out.Accounts) != 1 {
		t.Fatalf("status returned %d entries, want 1", len(out.Accounts))
	}
	return out.Accounts[0]
}

// assertEmptyBalances fails unless balances arrived as a literal [], which is
// what "not reported" is on the wire. A missing member or null decodes to nil.
func assertEmptyBalances(t *testing.T, what string, e statusEntry) {
	t.Helper()
	if e.Balances == nil || len(e.Balances) != 0 {
		t.Errorf("%s: balances = %#v, want a literal []", what, e.Balances)
	}
}

func TestStatus_PassesTheExchangeBalancesThrough(t *testing.T) {
	f := newFixture(t)
	f.exchange.statusResp = &forav1.GetAccountStatusResponse{
		Ver: helpers.ProtocolVersion, BillingRef: "acct-123", Active: true,
		Balances: []*forav1.Cost{
			{Amount: "12.5", Currency: "USD"},
			{Amount: "0.00000001", Currency: "EUR"},
		},
	}

	e := statusAt(t, f)

	if len(e.Balances) != 2 ||
		e.Balances[0].Amount != "12.5" || e.Balances[0].Currency != "USD" ||
		e.Balances[1].Amount != "0.00000001" || e.Balances[1].Currency != "EUR" {
		t.Fatalf("balances = %+v, want the Exchange's two entries unchanged and in order", e.Balances)
	}
}

func TestStatus_RegisteredAccountWithoutBalancesIsAnEmptyList(t *testing.T) {
	f := newFixture(t)
	f.exchange.statusResp = &forav1.GetAccountStatusResponse{
		Ver: helpers.ProtocolVersion, BillingRef: "acct-123", Active: true,
	}
	e := statusAt(t, f)
	if !e.Registered {
		t.Fatal("registered = false for an account the Exchange reported")
	}
	assertEmptyBalances(t, "an Exchange that reports no balances", e)
}

func TestStatus_NoAccountIsAnEmptyBalanceList(t *testing.T) {
	f := newFixture(t)
	f.exchange.statusResp = &forav1.GetAccountStatusResponse{Ver: helpers.ProtocolVersion}
	assertEmptyBalances(t, "an account that does not exist", statusAt(t, f))
}

func TestStatus_HintsCarryAnEmptyBalanceList(t *testing.T) {
	f := newFixture(t)
	a := f.provision(t, "dev-one")
	session := f.connect(t, a.Token)
	callTool[registerResult](t, session, "fora_register", registerArgsAt(f.exchange.Domain(t)))

	out := callTool[statusResult](t, session, "fora_status", nil)
	if len(out.Accounts) != 1 {
		t.Fatalf("status returned %d hints, want 1", len(out.Accounts))
	}
	assertEmptyBalances(t, "a local hint", out.Accounts[0])
}
