package web

import (
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
)

// TestCheckReasonText_EveryContractReasonHasASentence holds the console's
// sentences to the shared contract. A reason the Exchange can send and the
// console has no sentence for reaches the publisher as a bare code inside the
// fallback "does not match yet" line, which tells them nothing about what to
// change.
func TestCheckReasonText_EveryContractReasonHasASentence(t *testing.T) {
	for _, code := range consoleapi.AllReasons() {
		got := checkReasonText([]string{code})
		if len(got) != 1 {
			t.Fatalf("reason %q produced %d sentences, want 1", code, len(got))
		}
		if strings.Contains(got[0], code) {
			t.Errorf("reason %q has no sentence of its own; the publisher reads %q", code, got[0])
		}
	}
}
