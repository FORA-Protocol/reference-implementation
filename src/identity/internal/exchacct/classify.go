package exchacct

import (
	"errors"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/account"
)

// classify turns a failure from the outbound leg into this package's kind.
//
// The leg reports in two vocabularies and both are read here, in that order.
// This service's own sentinels come first: they name conditions the outbound leg
// checked on this service's behalf, and only they can distinguish a domain the
// deployment excludes from a manifest that could not be read. The SDK's typed
// failure comes second and covers everything else.
//
// The refused-argument arm is the odd one and is deliberately unreachable from
// here. Both legs run checkExchange before anything is sent, so a value that is
// not a bare domain is refused with that kind directly and never reaches the
// outbound leg to come back as a sentinel. It is kept because this package's own
// doc names its consumers as "the MCP adapter today, and whatever asks next",
// and a caller that does not front the leg with checkExchange would otherwise
// have the same refusal fall through to the outbound arm, which names the wrong
// problem.
//
// Reading our own first is not a preference. The SDK's class is coarser by
// design -- it answers "was it sent, and did a peer reply" -- so a policy refusal
// and an unreachable manifest arrive under classes that say nothing about which
// happened. An operator reading a failed call needs the difference, because the
// remedy for each is a different person's.
func classify(exchange string, err error) *Error {
	kind := KindOutbound
	switch {
	// Not reachable through Register or Status; see the doc above.
	case errors.Is(err, account.ErrNotBareDomain):
		kind = KindExchangeShape
	case errors.Is(err, account.ErrNotPermitted):
		kind = KindNotPermitted
	// Ahead of the unread arm on purpose. A refused version is reported as a
	// failed read too -- nothing was pre-checked and nothing was sent -- so both
	// sentinels are on the chain, and whichever arm runs first decides. This one
	// is the more specific fact and names the remedy.
	case isManifestVersionRefused(err):
		kind = KindManifestVersionRefused
	case errors.Is(err, account.ErrRequirementsUnread):
		kind = KindRequirements
	default:
		if fields := precheckRefusal(err); len(fields) > 0 {
			return &Error{Kind: KindFieldsRefused, Exchange: exchange, Fields: fields, Err: err}
		}
	}
	return &Error{Kind: kind, Exchange: exchange, Err: err}
}

// precheckRefusal returns the members the outbound leg's own schema check
// refused, or nil when the failure is something else.
//
// The condition is read off a port sentinel rather than off the leg's transport
// error class. The leg is what can tell a refusal it computed from one a peer
// sent — an Exchange refusing the same payload answers with the same shape of
// typed detail — so the leg makes that call and says so in this repository's
// vocabulary. Asking the question here would mean the account port could only be
// satisfied by an implementation that surfaces the SDK's own error type, which
// is the one requirement the port would state in a wire vocabulary.
//
// The members themselves still come off the typed detail on the wrapped chain.
// That much is deliberately NOT translated: the Exchange attaches this same
// detail when it refuses the same payload, computed by the same validator, so a
// consumer that renders one refusal renders both.
func precheckRefusal(err error) []*forav1.RegistrationFieldError {
	var refused *account.FieldsRefusedError
	if !errors.As(err, &refused) {
		return nil
	}
	return refused.Fields
}

// isManifestVersionRefused reports whether err is the one condition both legs of
// this service can meet: the target Exchange's manifest was read and carries a
// document version this build does not accept.
//
// It answers for BOTH spellings on purpose, because the two legs reach the
// condition by different routes. The register leg reads requirements first, so
// the refusal arrives from this repository's reader. The status leg reads no
// requirements, so the same document is refused inside the SDK's resolver and
// arrives underneath a CallError. One condition classified in one place is what
// keeps the two tools from telling an agent two different things about one
// Exchange.
func isManifestVersionRefused(err error) bool {
	return errors.Is(err, account.ErrManifestVersionRefused) ||
		errors.Is(err, resolvers.ErrManifestVersionRefused)
}
