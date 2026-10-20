package exchpolicy

import (
	"context"
	"fmt"

	"github.com/FORA-Protocol/protocol/sdk/go/connect"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/account"
)

// PermittedEndpoints applies this deployment's Exchange policy to endpoint
// resolution and then delegates.
//
// # Why the policy is not simply handed to the resolver
//
// The SDK offers an Allow hook on both of its well-known faces and consults it
// before any fetch, so either way an excluded domain is never dialled. What
// differs is what the refusal SAYS. The endpoint face reports a host the hook
// rejected as resolvers.ErrNoEndpoint, and the registration-requirements face
// reports the same rejection as resolvers.ErrExchangeNotPermitted. Neither
// carries a sentinel of ours.
//
// That cost this service a wrong diagnosis. The register leg reads requirements
// through our own reader, which wraps account.ErrNotPermitted around the SDK's
// sentinel, so a policy refusal there arrives named. The status leg routes
// straight through the endpoint resolver, so the same operator decision arrived
// with nothing our classifier could match and was reported as though a peer had
// answered and refused. One decision, two kinds, and an operator alert keyed on
// the policy kind saw one of the two tools.
//
// The hook cannot fix it: it is a func(string) bool and has no error to shape.
// So the overlay moves off the hook and onto this decorator, which refuses in
// the shape our requirements reader already uses -- our sentinel outermost so
// the account service can classify it, the SDK's underneath so the SDK still
// treats the refusal as final.
//
// # Where this sits relative to the rest of the policy
//
// It is not a new place the policy is applied; it is the same place, saying more.
// The tool layer keeps its own copy for readability, the requirements reader
// keeps its own because that leg dials without going through any resolver, and
// this is the overlay every leg that DOES route through one already passed
// through. A check moved up into a call site instead would make the guarantee
// conventional, which is the arrangement each of those three comments rejects.
type PermittedEndpoints struct {
	allow    *Allowlist
	delegate connect.EndpointResolver
}

// NewPermittedEndpoints returns delegate with allow applied in front of it.
//
// A nil or empty Allowlist permits every domain, so a deployment with no policy
// gets the delegate's own behaviour and no call site needs a branch -- the same
// property Permits gives every other consumer.
func NewPermittedEndpoints(allow *Allowlist, delegate connect.EndpointResolver) *PermittedEndpoints {
	return &PermittedEndpoints{allow: allow, delegate: delegate}
}

// ResolveEndpoint refuses a domain the deployment excludes, and otherwise asks
// the delegate.
//
// The refusal wraps resolvers.ErrNoEndpoint rather than
// resolvers.ErrExchangeNotPermitted, which would read better and would be wrong.
// The SDK's routing tier classifies a resolver's failure by cause, and only four
// sentinels mark it as one the caller must not retry; the not-permitted sentinel
// belongs to the requirements face and is not among them. A refusal wrapping it
// would be read as a transport failure and retried forever against a domain the
// operator has excluded. ErrNoEndpoint is the sentinel the SDK's own endpoint
// resolver uses for this same condition, so this says what that one says and adds
// the name our classifier needs.
//
// The policy is consulted before the delegate's own host check, so a value that
// is not a host at all is reported as excluded rather than as malformed. Both
// account legs and the report leg run their shape check before they get here, so
// nothing in this service reaches it that way, and either answer is a refusal.
func (r *PermittedEndpoints) ResolveEndpoint(ctx context.Context, host string) (string, error) {
	if !r.allow.Permits(host) {
		return "", fmt.Errorf("%w: %w: host %q not allowed",
			account.ErrNotPermitted, resolvers.ErrNoEndpoint, host)
	}
	return r.delegate.ResolveEndpoint(ctx, host)
}
