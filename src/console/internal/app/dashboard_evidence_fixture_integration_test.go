//go:build integration

package app_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"maps"
	"os"
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
)

// evidenceFixture is the evidence the console's verification module is tested
// against, kept beside that module's tests.
const evidenceFixture = "../../js/tests/testdata/transaction-evidence.json"

var updateEvidence = flag.Bool("update-evidence", false,
	"rewrite the verification module's fixture from a real transaction")

// TestDashboard_EvidenceFixtureIsARealTransactions keeps the browser
// verification's fixture honest. It makes a real sale, opens its detail page,
// and reads the evidence the page hands the Verify button. Both signatures of
// that evidence must verify, so the page hands over a transaction that does.
// With -update-evidence it writes that evidence as the fixture. Without it,
// the fixture must still have exactly the fields the page hands over, so a
// change to what the page carries cannot leave the module tested against an
// old shape, and its own signatures must verify, so the module's "a real
// transaction verifies" test is about one that does.
func TestDashboard_EvidenceFixtureIsARealTransactions(t *testing.T) {
	f := startSeller(t)
	sale := sell(t, f.exchange.Buyer(t), "/articles/fixture")
	evidence := evidenceOf(t, f.get2(t, dashboard.PathTransactions+"/"+sale.TransactionID))
	assertSignaturesVerify(t, "the page's evidence", evidence)

	if *updateEvidence {
		raw, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatalf("encode fixture: %v", err)
		}
		if err := os.WriteFile(evidenceFixture, append(raw, '\n'), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return
	}
	raw, err := os.ReadFile(evidenceFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture map[string]string
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("the fixture is not the evidence's JSON: %v", err)
	}
	want, got := slices.Sorted(maps.Keys(evidence)), slices.Sorted(maps.Keys(fixture))
	if !slices.Equal(got, want) {
		t.Fatalf("the fixture has fields %v, the page hands over %v; rerun with -update-evidence", got, want)
	}
	for key, value := range fixture {
		if value == "" {
			t.Errorf("fixture field %s is empty", key)
		}
	}
	assertSignaturesVerify(t, "the fixture", fixture)
}

// assertSignaturesVerify checks both signatures of one transaction's evidence
// with Go's own Ed25519 -- the offer's under the exchange key and the
// acceptance's under the agent key -- and that the acceptance names the
// offer's signature. The fields are encoded as the Exchange serves them: bytes
// and keys in standard base64, signatures in hex.
func assertSignaturesVerify(t *testing.T, what string, ev map[string]string) {
	t.Helper()
	decode := func(field string, fromHex bool) []byte {
		t.Helper()
		var raw []byte
		var err error
		if fromHex {
			raw, err = hex.DecodeString(ev[field])
		} else {
			raw, err = base64.StdEncoding.DecodeString(ev[field])
		}
		if err != nil {
			t.Fatalf("%s: %s does not decode: %v", what, field, err)
		}
		return raw
	}
	if !ed25519.Verify(decode("exchange_signing_public_key", false),
		decode("offer_canonical_bytes", false), decode("offer_sig", true)) {
		t.Errorf("%s: the offer's signature does not verify under the exchange key", what)
	}
	acceptance := decode("agent_acceptance_canonical_bytes", false)
	if !ed25519.Verify(decode("agent_public_key", false), acceptance, decode("agent_acceptance_signature", true)) {
		t.Errorf("%s: the acceptance's signature does not verify under the agent key", what)
	}
	var named struct {
		OfferSig string `json:"offer_sig"`
	}
	if err := json.Unmarshal(acceptance, &named); err != nil || named.OfferSig != ev["offer_sig"] {
		t.Errorf("%s: the acceptance names offer signature %q (%v), want this offer's", what, named.OfferSig, err)
	}
}
