package transport

import (
	"context"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/broker"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/resolve"
)

// validateResolveRequest and authenticatedAgentID are the transport adapter's
// request-admission gates: they run in BrokerConnectHandler.Resolve, BEFORE the
// resolve business core is entered. They live in transport (not in the resolve
// service) because both are boundary concerns — validation shapes the wire input,
// and identity derivation reads the httpsig verification context the broker
// middleware populated on the inbound request (helpers.FromContext). The resolve
// core receives a request whose AgentID is the authenticated identity.
//
// The search inputs are not admitted here. Which search_filters keys a request
// may carry, and whether each value is well formed, is the vocabulary of the
// configured catalog search provider, a dependency of the resolve service; so the
// service parses them, together with the one-of rule over query, uri and
// search_filters (resolve.Service.admitSearch). Transport hands them over as
// decoded (foraRequestToInput) and maps the domain error to its Connect code.

// authenticatedAgentID returns the caller's identity: the canonical host of the
// verified Signature-Agent directory. After the WBA split the RFC 9421 keyid is
// only the key's RFC 7638 thumbprint (proof of possession), not the agent
// identity — the identity is the signed Signature-Agent directory origin. The
// broker httpsig middleware has already verified the signature (and that
// signature-agent is a covered component) before this runs. A missing httpsig
// context or an empty Signature-Agent (caller bypassed the middleware) is
// rejected as Unauthenticated rather than treated as any caller, and so is a
// directory that names no host.
//
// Requester.id plays no part. It is a wire label the agent chooses; the Broker
// forwards it unchanged and never authorizes or accounts on it. Everything that
// needs to know WHO is asking — the budget counter and the selection_log row —
// reads the value returned here, so two requester labels from one agent share one
// counter, and one label used by two agents gives each its own.
//
// What makes the returned host mean "this caller IS that agent" is the binding of
// the verified KEY to the Signature-Agent directory. The WBA path
// (internal/agentkeys) provides it: the key is resolved by fetching the directory
// the signature commits to, so a signer cannot name a directory that does not
// publish its key. This is the path EVERY inbound key takes — agentSig1Resolver
// has no other delegate (the Broker's own-key registry plays no part in it; its
// keys' private halves never sign an inbound request). Keep it that way: a
// resolver delegate that mapped thumbprint to key with no identity attached would
// let a signer claim any agent it cared to name.
//
// The Exchange re-derives the agent itself. On ExecuteTransaction it takes the
// covered Signature-Agent host for a direct request, and Requester.domain for one
// this Broker re-packaged (which the Broker's purchase service first binds to
// the verified directory host), and proves the acting identity with the
// body offer-acceptance signature against that agent's registered key
// (verifyAgentAcceptance). Requester.id selects nothing there either.
func authenticatedAgentID(ctx context.Context) (string, *broker.Error) {
	v := helpers.FromContext(ctx)
	if v == nil || v.SignatureAgent == "" {
		return "", broker.Newf(broker.KindUnauthenticated, "no verified caller in request context")
	}
	return broker.CallerHost(v.SignatureAgent)
}

// validateResolveRequest checks the one field the wire shape leaves to the
// handler: requester.id is required, but is otherwise free-form, so any
// protocol-valid label passes, host or not.
func validateResolveRequest(req *resolve.Request) error {
	if req.RequesterID == "" {
		return broker.Newf(broker.KindInvalidArgument, "requester.id is required").WithField("requester.id")
	}
	return nil
}
