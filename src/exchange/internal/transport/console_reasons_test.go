package transport

import (
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// TestWireReason_MapsEveryServiceReasonToADistinctContractCode holds the
// mapping between the service's reasons and the console's wire codes total in
// both directions. A reason added to the service without a code would reach
// the console as a server fault; a code the service can never produce would be
// a sentence the console keeps for nothing; two reasons sharing a code would
// tell the publisher to make the wrong edit.
func TestWireReason_MapsEveryServiceReasonToADistinctContractCode(t *testing.T) {
	t.Parallel()
	seen := map[string]service.CheckReason{}
	for _, r := range service.CheckReasons() {
		code, ok := wireReason(r)
		if !ok {
			t.Fatalf("service reason %q has no wire code", r)
		}
		if !slices.Contains(consoleapi.AllReasons(), code) {
			t.Fatalf("service reason %q maps to %q, which is not a consoleapi reason", r, code)
		}
		if prev, dup := seen[code]; dup {
			t.Fatalf("service reasons %q and %q both map to %q", prev, r, code)
		}
		seen[code] = r
	}
	for _, code := range consoleapi.AllReasons() {
		if _, ok := seen[code]; !ok {
			t.Fatalf("consoleapi reason %q is produced by no service reason", code)
		}
	}
}

// TestWireReasons_RefusesAReasonWithNoCode pins the failure the mapping exists
// to catch: an unknown reason is an error, not an empty or invented code.
func TestWireReasons_RefusesAReasonWithNoCode(t *testing.T) {
	t.Parallel()
	if _, err := wireReasons([]service.CheckReason{"not_a_reason"}); err == nil {
		t.Fatal("an unknown reason was rendered instead of refused")
	}
	got, err := wireReasons(nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no reasons rendered as %v (err %v), want an empty non-nil list", got, err)
	}
}
