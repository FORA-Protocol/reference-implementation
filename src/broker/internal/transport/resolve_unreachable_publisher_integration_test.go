//go:build integration

package transport_test

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// TestResolve_UnreachablePublisherIsTemporarilyUnavailable drives the fourth
// cause of an empty resolve, the one the settled causes are told apart from: a
// publisher origin that did not answer at all. It is an outage, so the agent is
// told to come back rather than that the publisher is not in the catalog.
//
// The two records it leaves are asserted with their levels. The resolve path
// owns the failure and records it at WARN, with the request_id that ties it to
// the call. The probe's own line repeats the same fact without that id, so it
// sits at DEBUG: at INFO it doubled every outage in an operator's default view.
//
// No publisher manifest named an Exchange, so the Exchange address is a sink
// that fails the test if the Broker sends it anything.
func TestResolve_UnreachablePublisherIsTemporarilyUnavailable(t *testing.T) {
	ctx := t.Context()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "down.example", providerDown: true, captureLogs: true})

	_, out := resolveOverConnect(t, fx, reqOpts{uri: "https://down.example/item"})

	want := forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE
	if got := out.GetAbsenceReason(); got != want {
		t.Errorf("absence_reason = %v, want %v", got, want)
	}
	assertNothingSent(t, fx.wire)
	fx.logs.AssertLoggedOnce(t, "broker.resolve.probe", "WARN")
	fx.logs.AssertLoggedOnce(t, "broker.probe", "DEBUG")
}
