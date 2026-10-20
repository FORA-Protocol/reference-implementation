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
// fail that in three ways that are all VERDICTS — it hosts nothing, it hosts a
// document at a version this Broker does not implement, or it hosts one that
// broke a document rule — and all three owe the agent the same settled answer,
// NOT_IN_CATALOG. The fourth way, an origin that did not respond, is an outage
// and owes TEMPORARILY_UNAVAILABLE. Splitting a verdict from an outage is the
// whole job of this group: the wrong answer either strands a publisher that is
// merely down, or loops an agent against a document that will never change.
//
// Every refusal here lands on the publisher's manifest, before any Exchange is
// contacted, so the Exchange address is a sink that fails the test if the
// Broker sends it anything.

// TestResolve_NoForaJSON_RefusesNotInCatalog locks in the v1 contract:
// a publisher that does not host /.well-known/fora.json is refused with
// NOT_IN_CATALOG. The bare-URL fallback that legacy bot-grade fetchers
// relied on has been removed — there is no more "silently accept and
// hand back the bare URL" tolerance.
func TestResolve_NoForaJSON_RefusesNotInCatalog(t *testing.T) {
	ctx := context.Background()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "bare.example", unlicensed: true})

	_, out := resolveOverConnect(t, fx, reqOpts{uri: "https://bare.example/item"})

	wantReason := forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG
	if got := out.GetAbsenceReason(); got != wantReason {
		t.Errorf("typed absence_reason = %v, want %v", got, wantReason)
	}
	assertNothingSent(t, fx.wire)
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
			fx := newSinkFixture(t, ctx, fixtureOpts{
				providerDomain: "acme.example",
				providerVer:    &ver,
			})

			_, out := resolveOverConnect(t, fx, reqOpts{uri: "https://acme.example/item"})

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
			assertNothingSent(t, fx.wire)
		})
	}
}

// TestResolve_BrokenManifestDocument_RefusesNotInCatalog drives the third settled
// cause through the RPC that owns it.
//
// The publisher hosts a manifest, it arrives, it parses, and it describes a role
// that is not a publisher's. Nothing about that changes on a retry: the origin
// serves those bytes, and the next fetch reads them again. Before this arm
// existed the refusal was folded in with the transport failures and reached the
// agent as TEMPORARILY_UNAVAILABLE, so the Broker re-probed a publisher whose
// manifest names the wrong role for as long as it stayed wrong.
//
// It is driven through Resolve for the reason the refused-version sibling above
// states: probe_test.go stops at Probe returning the sentinel and
// responses_test.go starts from a hand-built probeOutcome, so neither crosses
// probeDomains — the line that joins them, and the one where counting the
// refusal as transient again would pass both.
func TestResolve_BrokenManifestDocument_RefusesNotInCatalog(t *testing.T) {
	ctx := context.Background()
	fx := newSinkFixture(t, ctx, fixtureOpts{
		providerDomain: "acme.example",
		providerRole:   "ROLE_EXCHANGE",
	})

	_, out := resolveOverConnect(t, fx, reqOpts{uri: "https://acme.example/item"})

	wantReason := forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG
	if got := out.GetAbsenceReason(); got != wantReason {
		t.Errorf("typed absence_reason = %v, want %v — a manifest that broke a document "+
			"rule is settled, and reporting it as retryable loops the agent", got, wantReason)
	}
	if len(out.GetOfferGroups()) != 0 {
		t.Errorf("a refused publisher produced %d offer groups", len(out.GetOfferGroups()))
	}
	assertNothingSent(t, fx.wire)
}
