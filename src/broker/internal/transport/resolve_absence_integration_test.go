//go:build integration

package transport_test

import (
	"context"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// The two SETTLED causes of an empty resolve, driven through the RPC that owns
// them and kept together because they are one decision seen twice.
//
// v1 requires every publisher to host /.well-known/fora.json. A publisher can
// fail that in two ways that are both VERDICTS — it hosts nothing, or it hosts a
// document this Broker cannot read — and both owe the agent the same settled
// answer, NOT_IN_CATALOG. The third way, an origin that did not respond, is an
// outage and owes TEMPORARILY_UNAVAILABLE. Splitting a verdict from an outage is
// the whole job of this group: the wrong answer either strands a publisher that
// is merely down, or loops an agent against a document that will never change.

// TestResolve_NoForaJSON_RefusesNotInCatalog locks in the v1 contract:
// a publisher that does not host /.well-known/fora.json is refused with
// NOT_IN_CATALOG. The bare-URL fallback that legacy bot-grade fetchers
// relied on has been removed — there is no more "silently accept and
// hand back the bare URL" tolerance.
func TestResolve_NoForaJSON_RefusesNotInCatalog(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t, ctx, fixtureOpts{providerDomain: "bare.example", unlicensed: true})

	srv, out := resolveOverConnect(t, fx, "agent-1", reqOpts{uri: "https://bare.example/item"})

	wantReason := forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG
	if got := out.GetAbsenceReason(); got != wantReason {
		t.Errorf("typed absence_reason = %v, want %v", got, wantReason)
	}
	if srv.exchange.executeCalls != 0 {
		t.Errorf("no transaction should happen: executeCalls = %d", srv.exchange.executeCalls)
	}
}

// TestResolve_RefusedManifestVersion_RefusesNotInCatalog drives the second
// settled cause through the RPC that owns it.
//
// The publisher hosts a manifest and it arrives intact; it states a document
// version this Broker does not implement. That is a VERDICT and not an outage,
// so it has to reach the agent as NOT_IN_CATALOG — the same settled answer the
// 404 above gets. TEMPORARILY_UNAVAILABLE would tell the agent to try again, and
// the next attempt reads the same document and reaches the same refusal, which
// is a loop no retry budget ends.
//
// It is driven through Resolve rather than through the classifier because the
// two are proven separately either way: probe_test.go stops at Probe returning
// the sentinel, and responses_test.go starts from a hand-built probeOutcome.
// Neither crosses probeDomains, which is the line that joins them — so with only
// those two, counting the refusal as transient again passes both.
func TestResolve_RefusedManifestVersion_RefusesNotInCatalog(t *testing.T) {
	for name, ver := range map[string]string{
		"an unrecognised major": "2.0",
		"no version at all":     "",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fx := newFixture(t, ctx, fixtureOpts{
				providerDomain: "acme.example",
				providerVer:    &ver,
			})

			srv, out := resolveOverConnect(t, fx, "agent-1", reqOpts{uri: "https://acme.example/item"})

			wantReason := forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG
			if got := out.GetAbsenceReason(); got != wantReason {
				t.Errorf("typed absence_reason = %v, want %v — a manifest this Broker "+
					"cannot read is settled, and reporting it as retryable loops the agent",
					got, wantReason)
			}
			// Nothing was offered and no transaction happened: the refusal lands on
			// the manifest read, before any exchange is queried.
			if len(out.GetOfferGroups()) != 0 {
				t.Errorf("a refused publisher produced %d offer groups", len(out.GetOfferGroups()))
			}
			if srv.exchange.executeCalls != 0 {
				t.Errorf("no transaction should happen: executeCalls = %d", srv.exchange.executeCalls)
			}
		})
	}
}
