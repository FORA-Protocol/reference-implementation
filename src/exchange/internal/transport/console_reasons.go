package transport

import (
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// wireReasons renders the service's check reasons as the codes the console
// reads. An empty answer is an empty list on the wire rather than null, so a
// reader that ranges over it needs no nil check.
//
// A reason with no wire code is a server fault: the service reported a rule the
// console has no sentence for, and sending it an unknown code would leave the
// publisher with a failed check and nothing to fix.
func wireReasons(reasons []service.CheckReason) ([]string, error) {
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		code, ok := wireReason(r)
		if !ok {
			return nil, exchange.Newf(exchange.KindInternal, "check reason %q has no wire code", r)
		}
		out = append(out, code)
	}
	return out, nil
}

// wireReason maps one service reason to its consoleapi code.
func wireReason(r service.CheckReason) (string, bool) {
	switch r {
	case service.CheckReasonManifestMissing:
		return consoleapi.ReasonManifestMissing, true
	case service.CheckReasonManifestUnreadable:
		return consoleapi.ReasonManifestUnreadable, true
	case service.CheckReasonVersionRefused:
		return consoleapi.ReasonManifestVersionRefused, true
	case service.CheckReasonExchangeNotListed:
		return consoleapi.ReasonExchangeNotListed, true
	case service.CheckReasonOwnerMismatch:
		return consoleapi.ReasonResourceOwnerMismatch, true
	case service.CheckReasonContributorNotListed:
		return consoleapi.ReasonContributorNotListed, true
	case service.CheckReasonDomainTaken:
		return consoleapi.ReasonDomainTaken, true
	default:
		return "", false
	}
}
