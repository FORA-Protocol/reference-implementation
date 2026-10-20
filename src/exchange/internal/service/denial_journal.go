package service

import (
	"context"
	"errors"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/google/uuid"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/transactionkey"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// The transaction event journal records the items ExecuteTransaction refuses,
// so a publisher can count them (ADR-027). This file decides which denials are
// recorded and writes them. A denial is recorded only when both hold:
//
//   - it is attributable to a publisher: the Exchange signature on the offer
//     verified and the offer's catalog entry resolved. The error carries that
//     entry from where the denial was raised (attributedDenial);
//   - the request proved possession of the agent key at admission, by the
//     request-level proof or, for a client that sends none, by an item's own
//     acceptance.

// journalWriteLimit bounds the INSERT one recorded denial costs. The write is
// detached from the caller's cancellation, so this limit, not the caller, is
// what ends a write a slow database stalls. It bounds each write, not the
// response: a batch records its denials one at a time, as each item is refused,
// so a batch that records N denials can wait up to N times this limit on the
// journal.
const journalWriteLimit = 5 * time.Second

// attributedDenial is a denial that knows the catalog entry — and so the seat
// and the publisher — it concerns. It changes neither the denial's kind nor
// what reaches the wire: every consumer finds the *exchange.Error inside it
// through errors.As, and its text is that error's text.
type attributedDenial struct {
	err   error
	entry repo.CatalogEntry
}

func (e *attributedDenial) Error() string { return e.err.Error() }

func (e *attributedDenial) Unwrap() error { return e.err }

// attributeResolved attaches entry to err when err is a denial. It is applied
// to everything an item raises after its catalog entry resolved, so every such
// denial is attributed, including any a later gate adds. A non-denial error is
// returned unchanged: it aborts the batch and is never recorded.
func attributeResolved(err error, entry repo.CatalogEntry) error {
	if _, ok := DenialReasonForKind(err); !ok {
		return err
	}
	return &attributedDenial{err: err, entry: entry}
}

// attributeUnresolved attributes the one denial raised before the entry
// resolves that the journal still records: OFFER_EXPIRED. An expired offer is
// authentic, so the entry its signed canonical_url names is the publisher's.
// The signature is checked again here, on its own, rather than inferred from
// the order in which the SDK checks signature and expiry. Every other error is
// returned unchanged, and so is an expired offer whose resource has left the
// catalog: with no entry there is no publisher to attribute it to.
func (s *ExchangeService) attributeUnresolved(offer *forav1.Offer, err error) error {
	var de *exchange.Error
	if !errors.As(err, &de) || de.Kind != exchange.KindOfferExpired {
		return err
	}
	entry, ok := s.entryForAuthenticOffer(offer)
	if !ok {
		return err
	}
	return &attributedDenial{err: err, entry: entry}
}

// denialRecorder records one request's attributed denials. It is built once
// per request from what admission established, so every item is judged by the
// same possession verdict.
type denialRecorder struct {
	s          *ExchangeService
	requestKey string
	agentID    string
	// proven reports whether admission proved possession of the agent key. A
	// request that proved none records nothing.
	proven bool
}

func (s *ExchangeService) newDenialRecorder(
	req *forav1.TransactionRequest, agentID string, binding agentBinding,
) denialRecorder {
	return denialRecorder{s: s, requestKey: req.GetIdempotencyKey(), agentID: agentID, proven: binding.proven()}
}

// record journals one denied item, when the request proved possession and the
// denial is attributed. It never fails the request: the agent receives its
// denial whatever happens here. Failing the request instead would be worse
// than a missing row, because an earlier item of the same batch may already be
// charged, and an aborted batch leaves the agent paying for an item whose URL
// it never receives. A write that fails is logged at ERROR, so the publisher's
// counts are a lower bound.
func (r denialRecorder) record(
	ctx context.Context, item *forav1.TransactionItem, denied *forav1.TransactionResultItem, cause error,
) {
	var ad *attributedDenial
	if !r.proven || !errors.As(cause, &ad) {
		return
	}
	offerID := item.GetOffer().GetOfferId()
	reason := denied.GetDenialReason()
	if err := r.append(ctx, offerID, reason, ad.entry); err != nil {
		reqctx.FromContext(ctx).ErrorContext(ctx, "exchange.execute_transaction",
			"event", "denial_journal_failed",
			"offer_id", offerID,
			"publisher_id", ad.entry.ResourceOwnerID,
			"denial_reason", reason.String(),
			"err", err.Error())
	}
}

// append writes the event. The write is detached from the caller's
// cancellation and bounded on its own, so an agent that hangs up still leaves
// its denial behind. A row the journal already holds for this agent and item
// key is a retry of the same denial, and is not an error.
func (r denialRecorder) append(
	ctx context.Context, offerID string, reason forav1.DenialReason, entry repo.CatalogEntry,
) error {
	label, ok := denialLabel(reason)
	if !ok {
		return exchange.Newf(exchange.KindInternal, "denial reason %v has no journal label", reason)
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return exchange.Wrap(exchange.KindInternal, err, "mint journal event id")
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), journalWriteLimit)
	defer cancel()
	_, err = r.s.events.AppendDenial(wctx, repo.DenialEvent{
		EventID:         eventID.String(),
		CreatedAt:       r.s.clk.Now(),
		TenantID:        entry.TenantID,
		IdempotencyKey:  transactionkey.DerivedItemKey(r.requestKey, offerID),
		OfferID:         offerID,
		AgentID:         r.agentID,
		ResourceID:      entry.ResourceID,
		ResourceOwnerID: entry.ResourceOwnerID,
		Reason:          label,
	})
	return err
}
