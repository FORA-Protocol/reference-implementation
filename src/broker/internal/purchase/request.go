package purchase

import (
	"strconv"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/proto"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/broker"
)

// bindRequester requires requester.domain to be the host of the verified
// signing directory. Every Exchange the purchase reaches resolves the agent's
// acceptance keys from requester.domain, so a domain naming anyone else would be
// refused at each of them; the Broker refuses it first, before anything is sent.
//
// The signature verified, but not as the requester the body claims, so this is
// a signature that does not verify for that requester: KindSignatureInvalid,
// which the transport sends as UNAUTHENTICATED with request_auth_failure
// SIGNATURE_INVALID. A domain that names no host at all is refused the same way,
// because it cannot be the signing directory's host either.
//
// Both sides are reduced to a canonical host with agentid.FromDirectory, the
// derivation Resolve uses for the caller, so spellings it treats as one host
// match. Only these local copies are normalized: the requester travels to each
// Exchange exactly as the agent wrote it, because the acceptances sign it.
func bindRequester(domain, callerDirectory string) *broker.Error {
	caller, cerr := broker.CallerHost(callerDirectory)
	if cerr != nil {
		return cerr
	}
	claimed, err := agentid.FromDirectory(domain)
	if err != nil || claimed != caller {
		return broker.Newf(broker.KindSignatureInvalid,
			"requester.domain %q is not %q, the host of the directory the request is signed from",
			domain, caller).WithField("requester.domain")
	}
	return nil
}

// validateRequest refuses a malformed purchase with INVALID_ARGUMENT before
// anything is sent: no items, an item with no offer, no offer.exchange or no
// AgentAcceptance, two items presenting one offer_id, or an
// AgentRequestAcceptance that does not list exactly the request's items, in
// order, for the request's requester and idempotency key.
//
// The request acceptance is compared, not verified. Each Exchange verifies its
// signature against the agent's key; the Broker checks only that the payload
// describes this request, because a payload that does not would be refused by
// every Exchange the purchase reaches, after some of them had already been sent
// their share.
func validateRequest(req *forav1.TransactionRequest) *broker.Error {
	items := req.GetItems()
	if len(items) == 0 {
		return broker.Newf(broker.KindInvalidArgument, "a purchase needs at least one item").
			WithField("items")
	}
	seen := make(map[string]int, len(items))
	for i, item := range items {
		if err := validateItem(i, item); err != nil {
			return err
		}
		// Two items presenting one offer would be one resource bought twice from
		// one issuance. Each Exchange refuses that itself, but only for its own
		// group, so the other groups would already have been bought.
		id := item.GetOffer().GetOfferId()
		if j, dup := seen[id]; dup {
			return broker.Newf(broker.KindInvalidArgument,
				"items %d and %d present the same offer_id %q; "+
					"buying a resource twice takes two separately issued offers", j, i, id).
				WithField("items.offer.offer_id").WithMeta("item_index", strconv.Itoa(i))
		}
		seen[id] = i
	}
	return validateRequestAcceptance(req)
}

// validateItem checks one item carries what its Exchange needs: the reflected
// offer, the Exchange it routes to, and the agent's acceptance of that offer.
func validateItem(i int, item *forav1.TransactionItem) *broker.Error {
	index := strconv.Itoa(i)
	switch {
	case item.GetOffer() == nil:
		return broker.Newf(broker.KindInvalidArgument, "item %d carries no offer", i).
			WithField("items.offer").WithMeta("item_index", index)
	case item.GetOffer().GetExchange() == "":
		return broker.Newf(broker.KindInvalidArgument,
			"item %d: offer.exchange is required to route the item to its Exchange", i).
			WithField("items.offer.exchange").WithMeta("item_index", index)
	case item.GetAgentAcceptance().GetSignature() == "":
		return broker.Newf(broker.KindInvalidArgument, "item %d carries no agent_acceptance", i).
			WithField("items.agent_acceptance").WithMeta("item_index", index)
	}
	return nil
}

// validateRequestAcceptance requires the AgentRequestAcceptance payload to be
// the payload this request's own items, requester and key produce.
func validateRequestAcceptance(req *forav1.TransactionRequest) *broker.Error {
	acceptance := req.GetAgentRequestAcceptance()
	if acceptance.GetPayload() == nil {
		return broker.Newf(broker.KindInvalidArgument,
			"agent_request_acceptance is required: it fixes the complete ordered set of items").
			WithField("agent_request_acceptance")
	}
	want, err := helpers.RequestAcceptancePayload(req)
	if err != nil {
		return broker.Wrapf(broker.KindInvalidArgument, err,
			"the request's items cannot form an agent_request_acceptance payload").
			WithField("items")
	}
	if !proto.Equal(want, acceptance.GetPayload()) {
		return broker.Newf(broker.KindInvalidArgument,
			"agent_request_acceptance does not list the request's items in order "+
				"(offer signature and exchange) for its requester and idempotency_key").
			WithField("agent_request_acceptance.payload")
	}
	return nil
}
