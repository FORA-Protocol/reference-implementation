package purchase

import (
	"context"
	"errors"
	"sync"

	"connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foracost"
)

// answer is what one Exchange made of its sub-request: its response, or the
// refusal every item it was sent carries instead. Exactly one is set.
type answer struct {
	resp    *forav1.TransactionResponse
	refusal *forav1.UpstreamRefusal
}

// send sends every group's sub-request, all at once, and returns the answers in
// group order. The sub-requests are independent purchases at independent
// Exchanges, so one slow Exchange does not hold the others back.
func (s *Service) send(ctx context.Context, req *forav1.TransactionRequest, groups []*group) []answer {
	answers := make([]answer, len(groups))
	var wg sync.WaitGroup
	for i, g := range groups {
		wg.Go(func() { answers[i] = s.sendOne(ctx, req, g) })
	}
	wg.Wait()
	return answers
}

// sendOne sends one Exchange its share of the purchase and classifies what came
// back.
//
// The sub-request is a new request the Broker authors, so ver is the Broker's
// own. Everything else is the agent's and travels unchanged: the reflected
// offers with their acceptances, in request order, the requester, the
// idempotency key and the request acceptance. The acceptances are detached
// signatures over those values, so the Exchange verifies the agent's consent no
// matter who signed the request around them, and the unchanged key is what
// makes a retry through the Broker a replay at every Exchange.
func (s *Service) sendOne(ctx context.Context, req *forav1.TransactionRequest, g *group) answer {
	items := make([]*forav1.TransactionItem, 0, len(g.indexes))
	for _, i := range g.indexes {
		items = append(items, req.GetItems()[i])
	}
	sub := &forav1.TransactionRequest{
		Ver:                    helpers.ProtocolVersion,
		IdempotencyKey:         req.GetIdempotencyKey(),
		Requester:              req.GetRequester(),
		Items:                  items,
		AgentRequestAcceptance: req.GetAgentRequestAcceptance(),
	}
	resp, err := s.exchanges.ExecuteTransaction(ctx, g.endpoint, sub)
	if err != nil {
		refusal := refusalFrom(err)
		audit(ctx, outcomeUpstreamRefused, g.exchange, err,
			"code", refusal.GetCode())
		return answer{refusal: refusal}
	}
	if !answersEveryItem(resp, items) {
		// The answer cannot be attributed item by item, so none of it is passed
		// on: every item is reported refused with "internal" and no detail.
		audit(ctx, outcomeUpstreamUnattributable, g.exchange, nil,
			"sent", len(items), "answered", len(resp.GetItems()))
		return answer{refusal: &forav1.UpstreamRefusal{Code: connect.CodeInternal.String()}}
	}
	return answer{resp: resp}
}

// answersEveryItem reports whether an OK answer carries exactly one result per
// item sent, in the order sent. The protocol requires the count; the offer_id
// check is the Broker's own, because a result attributed to the wrong item would
// hand one resource's delivery URL back as another's.
func answersEveryItem(resp *forav1.TransactionResponse, sent []*forav1.TransactionItem) bool {
	if len(resp.GetItems()) != len(sent) {
		return false
	}
	for i, item := range sent {
		if resp.GetItems()[i].GetOfferId() != item.GetOffer().GetOfferId() {
			return false
		}
	}
	return true
}

// refusalFrom turns an Exchange's non-OK answer, or its failure to answer, into
// the refusal its items carry. The code is the Connect code the Exchange
// answered with, in its wire form, and the detail is the first ErrorDetail it
// attached, unchanged. When there was no answer, the code is the one the
// Broker's own call failed with: unavailable for an Exchange that could not be
// reached, deadline_exceeded for one that did not answer in time.
func refusalFrom(err error) *forav1.UpstreamRefusal {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return &forav1.UpstreamRefusal{Code: ce.Code().String(), Detail: firstErrorDetail(ce)}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &forav1.UpstreamRefusal{Code: connect.CodeDeadlineExceeded.String()}
	}
	return &forav1.UpstreamRefusal{Code: connect.CodeUnavailable.String()}
}

// firstErrorDetail returns the first FORA ErrorDetail attached to a Connect
// error, or nil.
func firstErrorDetail(ce *connect.Error) *forav1.ErrorDetail {
	for _, d := range ce.Details() {
		msg, err := d.Value()
		if err != nil {
			continue
		}
		if detail, ok := msg.(*forav1.ErrorDetail); ok {
			return detail
		}
	}
	return nil
}

// combine assembles the BrokerTransactionResponse: one result per request item,
// in request order; one ExchangeOutcome per Exchange contacted, in the order
// each first appears among the items; and the charged totals per currency.
//
// An item whose Exchange answered is that Exchange's result item, unchanged,
// denial included. An item whose Exchange refused or did not answer carries the
// refusal, named by the item's own signed offer.exchange, with offer_id set and
// no other result field. The Broker never stamps a denial_reason on an item an
// Exchange did not deny.
func combine(
	ctx context.Context, items []*forav1.TransactionItem, groups []*group, answers []answer,
) *forav1.BrokerTransactionResponse {
	results := make([]*forav1.TransactionResultItem, len(items))
	outcomes := make([]*forav1.ExchangeOutcome, 0, len(groups))
	for gi, g := range groups {
		a := answers[gi]
		outcome := &forav1.ExchangeOutcome{Exchange: g.exchange}
		for k, i := range g.indexes {
			offerID := items[i].GetOffer().GetOfferId()
			outcome.OfferIds = append(outcome.OfferIds, offerID)
			if a.refusal != nil {
				results[i] = refusedItem(offerID, items[i].GetOffer().GetExchange(), a.refusal)
				continue
			}
			results[i] = a.resp.GetItems()[k]
		}
		if a.resp != nil {
			outcome.AgentIdentityHash = a.resp.GetAgentIdentityHash()
			outcome.SubscriptionQuota = a.resp.GetSubscriptionQuota()
		}
		outcomes = append(outcomes, outcome)
	}
	return &forav1.BrokerTransactionResponse{
		Ver:       helpers.ProtocolVersion,
		Items:     results,
		Exchanges: outcomes,
		Totals:    chargedTotals(ctx, results),
	}
}

// refusedItem is the result an item carries when its Exchange refused or did
// not answer the sub-request.
func refusedItem(offerID, exchange string, refusal *forav1.UpstreamRefusal) *forav1.TransactionResultItem {
	return &forav1.TransactionResultItem{
		OfferId: offerID,
		Refusal: &forav1.UpstreamRefusal{
			Party:  exchange,
			Code:   refusal.GetCode(),
			Detail: refusal.GetDetail(),
		},
	}
}

// chargedTotals sums the charged items per currency. An item is charged when it
// carries neither a denial_reason nor a refusal.
//
// An amount that is not an exact decimal leaves the totals empty rather than
// failing the call. Every item has already been bought by then, and each item's
// own cost stays the authoritative figure; refusing the response would hand the
// agent an error for purchases it has paid for.
func chargedTotals(ctx context.Context, results []*forav1.TransactionResultItem) []*forav1.Cost {
	charged := make([]*forav1.TransactionResultItem, 0, len(results))
	for _, r := range results {
		if r.DenialReason == nil && r.GetRefusal() == nil {
			charged = append(charged, r)
		}
	}
	totals, err := foracost.PerCurrency(charged)
	if err != nil {
		audit(ctx, outcomeTotalsOmitted, "", err)
		return nil
	}
	return totals
}
