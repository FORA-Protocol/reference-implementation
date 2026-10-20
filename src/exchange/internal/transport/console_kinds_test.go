package transport_test

import (
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
)

// TestConsoleErrorKinds_AreTheExchangesKindTokens holds the contract's kind
// tokens to the tokens the console surface writes, which are Kind.String. The
// console decides whose fault a refusal is by comparing against these
// constants, so a token that drifted from its kind would turn a caller fault
// into "something went wrong on our side", or the reverse, with nothing else
// failing.
func TestConsoleErrorKinds_AreTheExchangesKindTokens(t *testing.T) {
	for token, kind := range map[string]exchange.Kind{
		consoleapi.ErrorKindInvalidRequest:     exchange.KindInvalidRequest,
		consoleapi.ErrorKindNotFound:           exchange.KindNotFound,
		consoleapi.ErrorKindPermissionDenied:   exchange.KindPermissionDenied,
		consoleapi.ErrorKindFailedPrecondition: exchange.KindFailedPrecondition,
		consoleapi.ErrorKindUnauthenticated:    exchange.KindUnauthenticated,
		consoleapi.ErrorKindUnavailable:        exchange.KindUnavailable,
		consoleapi.ErrorKindInternal:           exchange.KindInternal,
	} {
		if got := kind.String(); got != token {
			t.Errorf("consoleapi token %q, but the Exchange writes %q for that kind", token, got)
		}
	}
}
