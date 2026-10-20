// Multisig transport-chain classification + relay authorization for the
// Exchange execute path.
//
// Every request signature (the agent's, and any relaying hop's) is verified at
// the boundary by the SDK's connectserver verify gate, each against the
// directory its own Signature-Agent member names, which exposes the ordered
// []helpers.VerifiedRequest under helpers.AllSignaturesFromContext. This file
// gates a broker relay on the target tenant's allow_broker_relay flag.
//
// A relay is recognised in two shapes, and resolveAgent decides both before
// anything here runs (see resolvedAgent.relay). A SINGLE signature whose
// Signature-Agent names the configured Broker is a re-packaged relay, and it is
// the shape the Broker actually sends: the Broker authors every request it sends
// an Exchange, rebuilding the purchase per Exchange and signing the new body
// itself, so the agent's signature could not survive and only the Broker's is
// present. A chain carrying more than one signature is a forwarded relay: the
// agent signed sig1 and each hop appended its own. The protocol defines that
// chain only for a discovery request (ResourceQuery) that another party forwards
// byte-for-byte toward an Exchange, and never for a purchase, so on this path it
// arrives only from a relay that forwards a purchase anyway; it is classified as
// a relay all the same and held to the same opt-out. Counting signatures alone
// missed the re-packaged shape entirely, which left the tenant's
// allow_broker_relay opt-out unenforced for every real Broker relay.
//
// CRITICAL (binding decision): the AGENT identity that binds the
// delivery URL is NOT taken from the transport chain — it is proven by the BODY
// offer-acceptance signature (verifyAgentAcceptance in exchange.go) and bound to
// the agent's REGISTERED key. The transport chain only establishes WHO relayed
// the request so the broker-relay opt-in can be enforced. Broker-only transport
// (no agent transport signature) is therefore legitimate when a valid body
// acceptance is present and the tenant allows relay.

package service

import (
	"context"
	"errors"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// transportChain summarizes the classification of a verified transport
// signature chain: whether it carries a relaying broker hop and the broker's
// keyID (for audit). hasBroker is true when the chain carries a relay hop beyond
// the originating signature.
type transportChain struct {
	hasBroker bool
	brokerKey string
}

// classifyTransportChain classifies a FORWARDED verified transport chain by
// POSITION, per the forwarding-chain contract the verifier already enforced
// (sig1..sigN contiguous): sig1 is the originating agent and any signature
// beyond it is a relay hop. The outermost (last) signer is the party relaying to
// the Exchange; its keyID (an RFC 7638 thumbprint after the WBA split — there is
// no broker keyid prefix) is carried for audit only. The relay hop's role is NOT
// re-checked against the agents table: after the WBA split the relay is gated
// solely by the target tenant's allow_broker_relay flag (single-broker Option C),
// so a signature's POSITION in the chain is all this function reads.
//
// It cannot see a re-packaged relay, whose single signature is the Broker's own:
// resolveAgent recognises that shape by the Signature-Agent host and sets
// hasBroker on the result this returns.
func classifyTransportChain(verified []helpers.VerifiedRequest) transportChain {
	var tc transportChain
	if len(verified) > 1 {
		tc.hasBroker = true
		tc.brokerKey = verified[len(verified)-1].KeyID
	}
	return tc
}

// authorizeExecute is the R4 authorization heart for ExecuteTransaction. It
//  1. gates a relayed request on tenant.allow_broker_relay (KindRelayNotAccepted
//     otherwise), reading the relay verdict resolveAgent already reached;
//  2. verifies the BODY offer-acceptance signature against the agent's REGISTERED
//     key and derives the delivery-URL binding from THAT key (never the broker's).
//
// It returns the Caller used for audit (the agent, identified by agentID and the
// acceptance-proven key) and the agentBinding the rest of the execute path feeds
// to mintSignedURL / persist. Any rejection is audited through logOutcome before
// it returns, matching every other execute authz outcome.
func (s *ExchangeService) authorizeExecute(
	ctx context.Context, req *forav1.TransactionRequest, agent resolvedAgent, tenant repo.Tenant,
) (Caller, agentBinding, error) {
	auditCaller := Caller{KeyID: agent.id, Kind: CallerAgent, AgentID: agent.id}
	if relayErr := s.authorizeTransportRelay(ctx, agent.relay, tenant); relayErr != nil {
		relayCaller := Caller{KeyID: agent.relay.brokerKey, Kind: CallerBroker}
		s.logOutcome(ctx, "execute_transaction", "REJECTED_AUTHZ", relayCaller, &tenant, agent.id, "", relayErr)
		return Caller{}, agentBinding{}, relayErr
	}
	// The authoritative agent identity is the BODY acceptance key, not the
	// transport chain. Verify it against the request's ONE key snapshot and bind
	// the delivery URL to its RFC 7638 thumbprint. Computed before billing so a
	// bad-acceptance failure reserves no funds.
	binding, err := verifyAgentAcceptance(req, agent.key)
	if err != nil {
		var de *exchange.Error
		if errors.As(err, &de) {
			s.logOutcome(ctx, "execute_transaction", "REJECTED_AUTHZ", auditCaller, &tenant, agent.id, "", de)
		}
		return Caller{}, agentBinding{}, err
	}
	return auditCaller, binding, nil
}

// authorizeTransportRelay enforces the broker-relay opt-in for an execute
// request whose transport chain carries a relay hop. A chain with no relay hop is
// a no-op (agent-direct). When a hop relayed the request the target tenant MUST
// have allow_broker_relay = true, else the request is refused with
// KindRelayNotAccepted — PERMISSION_DENIED carrying the protocol's
// DENIAL_REASON_RELAY_NOT_ACCEPTED, so the agent learns its remedy (buy the
// offer directly here) rather than reading a bare permission failure. That
// tenant flag is the SOLE gate — the relaying party's agents.requester_type is
// deliberately not consulted (see the Option-C paragraph below).
//
// After the WBA split the relay is authorized purely by the tenant's
// allow_broker_relay opt-in (single-broker Option C): a single Signature-Agent
// header cannot carry a distinct directory per hop, so a relay hop is not
// re-identified against the agents table here — its key was already
// cryptographically verified by the multisig middleware, and the delivery-URL
// identity binds to the AGENT's body-acceptance key, never the relay's. Multi-hop
// relay and per-relay identity binding are out of scope (separate, threat-modeled
// ticket).
func (s *ExchangeService) authorizeTransportRelay(
	_ context.Context, chain transportChain, tenant repo.Tenant,
) *exchange.Error {
	if !chain.hasBroker {
		return nil
	}
	if !tenant.AllowBrokerRelay {
		return exchange.Newf(exchange.KindRelayNotAccepted,
			"broker %q rejected: tenant does not allow broker relay", chain.brokerKey)
	}
	return nil
}
