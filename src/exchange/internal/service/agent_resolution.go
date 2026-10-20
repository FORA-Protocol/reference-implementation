// Agent-id resolution for ExecuteTransaction.
//
// The agent identity comes from the TRANSPORT, never from the request body. The
// signed Signature-Agent header names a Web Bot Auth key directory, and the host
// of that directory is the agent's identity (ADR-009 D3/D4, internal/agentid).
// The RFC 9421 keyid beside it is only an RFC 7638 thumbprint — proof that the
// signer holds a key, not a statement about who the signer is.
//
// An unknown directory host is lazily registered (ADR-009 D2, v1.1): the
// Exchange pulls→verifies→persists the agent from that same directory before the
// transaction proceeds. This adds NO new trust surface — the execute path
// already fetches and trusts the agent's well-known key to verify the body
// AgentAcceptance binding, so persisting the agent row here records the same key
// the delivery URL will be bound to. A forged header cannot self-register
// (agentreg pins only a key the directory's own host publishes).

package service

import (
	"context"
	"crypto/ed25519"
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// resolvedAgent is everything ONE ExecuteTransaction resolves about its
// requester, decided once before any verification runs: the id the transaction
// is attributed to, the account handle the paid path charges, the key snapshot
// every signature in the request is verified against, and how the request
// reached the Exchange. The first three come from a single read of the agent
// row, so they cannot describe different registrations of the same agent.
type resolvedAgent struct {
	id         string
	billingRef string
	key        agentKey
	// relay records whether a Broker delivered this request and which key it
	// signed with. It is decided once, here, so the relay gate and the per-item
	// pipeline cannot reach different verdicts about the same request.
	relay transportChain
}

// resolveAgent returns the agent_id to attribute the transaction to, the
// agent's billing_ref (the account handle the paid path charges, ADR-021 D5),
// the registered key every signature in this request is verified against, and
// the transport shape that delivered it.
//
// The identity is derived from the transport in one of two ways, and the
// verified Signature-Agent host decides which:
//
//	DIRECT   The header names the agent's own key directory, so the agent IS the
//	         caller. callerAgentRecord normalizes the host, lazily registers it
//	         from that same directory, and refuses a caller whose directory does
//	         not publish the key that signed the request.
//
//	RELAYED  The header names the configured Broker's key directory
//	         (cfg.BrokerHost). The Broker re-packages ExecuteTransaction — batch
//	         fan-out per exchange changes the body, so the agent's transport
//	         signature cannot survive — and signs the new request itself. The
//	         header therefore names the relay, not the agent, and the agent is
//	         taken from Requester.domain, which the detached body acceptance
//	         signs. A Broker that substitutes a different domain invalidates
//	         that acceptance.
//
// Requester.id is a FREE LABEL. The protocol lets an agent put any string there
// — a campaign name, a probe tag, a URN — and the Exchange keeps it verbatim in
// transaction_evidence.requester_id because it is part of what the agent signed.
// It never selects a directory to fetch, an agents row, a billing_ref, or the
// idempotency owner. Reading it as a host was the defect this derivation
// replaces: a caller whose label was not a reachable host was refused with 503
// (an unreachable directory) or 400 (a label that names no host), although its
// transport signature had already authenticated it one step earlier.
//
// The billing_ref comes ONLY from the resolved row — never from anything the
// caller sends — and is empty when the agent has not registered for paid
// content.
func (s *ExchangeService) resolveAgent(
	ctx context.Context, req *forav1.TransactionRequest,
) (resolvedAgent, error) {
	v := helpers.FromContext(ctx)
	if v == nil || v.KeyID == "" {
		// The httpsig middleware refuses an unsigned request long before the
		// service sees it, so this is the internal-caller case resolveCaller
		// guards the same way: no verified transport means no identity to derive.
		return resolvedAgent{}, exchange.Newf(exchange.KindUnauthenticated,
			"no verified caller in request context")
	}
	if s.agents == nil {
		// Defensive, and matching resolveCaller: a service with no agents repo
		// cannot resolve an identity, so refuse rather than attribute the
		// transaction to an unverified string.
		return resolvedAgent{}, exchange.Newf(exchange.KindInternal, "service has no agents repo wired")
	}
	relay := classifyTransportChain(helpers.AllSignaturesFromContext(ctx))
	if s.callerIsConfiguredBroker(v) {
		// A single-signature Broker re-package carries no second signature for
		// classifyTransportChain to count, so the relay is recorded here instead.
		// The Broker's own verified keyid is what the audit line names, unless a
		// chain was present and already named its outermost hop.
		relay.hasBroker = true
		if relay.brokerKey == "" {
			relay.brokerKey = v.KeyID
		}
		return s.resolveRelayedAgent(ctx, req, relay)
	}
	callerHost, agent, err := s.callerAgentRecord(ctx, v)
	if err != nil {
		return resolvedAgent{}, err
	}
	// callerAgentRecord succeeded, so the row's pinned key and the transport-proven
	// key are the same bytes. Build the ONE snapshot from the row: the acceptance
	// verification, the billing handle and the attribution then all describe a
	// single registration.
	return resolvedAgent{
		id:         callerHost,
		billingRef: agent.BillingRef,
		key:        snapshotKey(agent),
		relay:      relay,
	}, nil
}

// callerIsConfiguredBroker reports whether the verified caller signed as the
// Broker this Exchange is configured to accept relays from. The comparison is
// between canonical directory hosts: cfg.BrokerHost is derived at boot from
// EXCHANGE_BROKER_WELLKNOWN_URL — the same document the transport's revocation
// resolver is bound to — and the caller's Signature-Agent is normalized the same
// way, so one Broker is one host however either value was spelled.
//
// Recognition does NOT depend on a seeded fora.agents row with
// requester_type = 'BROKER'. The transport already proved the signing key is
// published by that directory, which is a stronger statement than a row an
// operator typed. An empty BrokerHost (a deployment with no Broker configured)
// matches nothing.
func (s *ExchangeService) callerIsConfiguredBroker(v *helpers.VerifiedRequest) bool {
	if s.cfg.BrokerHost == "" {
		return false
	}
	callerHost, err := agentid.FromDirectory(v.SignatureAgent)
	if err != nil {
		// Not a host, so not this Broker. The direct path refuses the same value
		// with an authentication failure; deciding it here would duplicate that
		// refusal in a second vocabulary.
		return false
	}
	return callerHost == s.cfg.BrokerHost
}

// resolveRelayedAgent resolves the agent a Broker relayed this request for.
//
// Requester.domain is the only field that can name the agent here: the transport
// header names the Broker, and Requester.id is a free label. The domain is
// inside the AgentAcceptancePayload the agent signed with its own key, so a
// Broker cannot swap in another agent's domain without invalidating every
// acceptance in the request — which is what makes a body field usable as an
// identity on this path and nowhere else.
//
// A domain that is empty or names no host is refused as an invalid request, and
// the refusal happens before any outbound fetch: nothing is learned about an
// agent whose domain cannot be a directory. There is deliberately NO fallback to
// Requester.id.
func (s *ExchangeService) resolveRelayedAgent(
	ctx context.Context, req *forav1.TransactionRequest, relay transportChain,
) (resolvedAgent, error) {
	domain := req.GetRequester().GetDomain()
	agentHost, err := agentid.FromDirectory(domain)
	if err != nil {
		return resolvedAgent{}, exchange.Wrap(exchange.KindInvalidRequest, err,
			fmt.Sprintf("requester.domain %q does not name the relayed agent's key directory host", domain)).
			WithField("requester.domain")
	}
	agent, err := s.resolveAgentLazily(ctx, agentHost)
	if err != nil {
		return resolvedAgent{}, err
	}
	return resolvedAgent{
		id:         agentHost,
		billingRef: agent.BillingRef,
		key:        snapshotKey(agent),
		relay:      relay,
	}, nil
}

// snapshotKey takes the request's one agent-key snapshot off a resolved agent
// row: the pinned public key plus the directory it was pinned from. Both
// resolution paths build it the same way, from the same column pair, so neither
// can attribute a transaction to a key the other would have rejected.
func snapshotKey(agent repo.Agent) agentKey {
	return agentKey{pub: ed25519.PublicKey(agent.PublicKey), discoveryURL: agent.DiscoveryURL}
}
