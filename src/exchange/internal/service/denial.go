package service

import (
	"errors"
	"strings"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forareason"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// denialReasonByKind maps the Exchange's domain denial kinds onto the canonical
// proto DenialReason vocabulary (ADR-019). Only kinds that represent a refused
// transaction appear here; non-denial kinds (invalid request, not found,
// internal) are absent and either carry only a transport code (single-offer
// path) or abort the whole batch (batch path).
//
// KindContentUnavailable is a denial and KindNotFound is not, even though the
// catalog miss that produces the first is a lookup failure. The distinction is
// what the caller can do about it: an agent holding an authentic offer for
// withdrawn content is refused that purchase and keeps its other items, while a
// missing agent, tenant, evidence row or obligation means the request itself
// does not describe anything the Exchange can act on.
//
// This is the SINGLE source of truth for the denial vocabulary: the transport
// layer's executeTxError reuses DenialReasonForKind for the single-offer typed
// ErrorDetail, and the batch ExecuteTransaction loop reuses it to set each
// TransactionResultItem.denial_reason — so the per-item batch denial and the
// single-offer transport denial can never drift (the derived per-item key).
var denialReasonByKind = map[exchange.Kind]forav1.DenialReason{
	exchange.KindContentUnavailable:    forav1.DenialReason_DENIAL_REASON_CONTENT_UNAVAILABLE,
	exchange.KindBillingDenied:         forav1.DenialReason_DENIAL_REASON_INSUFFICIENT_BALANCE,
	exchange.KindAccountNotRegistered:  forav1.DenialReason_DENIAL_REASON_ACCOUNT_NOT_REGISTERED,
	exchange.KindAccountInactive:       forav1.DenialReason_DENIAL_REASON_ACCOUNT_INACTIVE,
	exchange.KindSignatureInvalid:      forav1.DenialReason_DENIAL_REASON_SIGNATURE_INVALID,
	exchange.KindOfferExpired:          forav1.DenialReason_DENIAL_REASON_OFFER_EXPIRED,
	exchange.KindEntitlementMissing:    forav1.DenialReason_DENIAL_REASON_ENTITLEMENT_MISSING,
	exchange.KindEntitlementMalformed:  forav1.DenialReason_DENIAL_REASON_ENTITLEMENT_MALFORMED,
	exchange.KindEntitlementExpired:    forav1.DenialReason_DENIAL_REASON_ENTITLEMENT_EXPIRED,
	exchange.KindEntitlementWrongBuyer: forav1.DenialReason_DENIAL_REASON_ENTITLEMENT_WRONG_BUYER,
	exchange.KindSubscriptionLapsed:    forav1.DenialReason_DENIAL_REASON_SUBSCRIPTION_LAPSED,
	exchange.KindEntitlementNotGranted: forav1.DenialReason_DENIAL_REASON_ENTITLEMENT_NOT_GRANTED,
	exchange.KindReportingOverdue:      forav1.DenialReason_DENIAL_REASON_REPORTING_OVERDUE,
	exchange.KindRelayNotAccepted:      forav1.DenialReason_DENIAL_REASON_RELAY_NOT_ACCEPTED,
}

// DenialReasonForKind returns the canonical DenialReason for a transaction-denial
// error, or ok=false when the failure is not a denial (envelope-invalid /
// internal / not-found). For the batch path, ok=false means the error is NOT a
// per-item denial and MUST abort the whole batch; ok=true means it becomes the
// item's denial_reason and the loop continues (collect-and-continue).
func DenialReasonForKind(err error) (forav1.DenialReason, bool) {
	var de *exchange.Error
	if !errors.As(err, &de) {
		return forav1.DenialReason_DENIAL_REASON_UNSPECIFIED, false
	}
	reason, ok := denialReasonByKind[de.Kind]
	return reason, ok
}

// denialPrefix starts every proto DenialReason name. The fora.denial_reason
// enum spells each value without it, which is the whole of the mapping between
// the two: the database parity test pins that the enum and the proto name the
// same reasons.
const denialPrefix = "DENIAL_REASON_"

// denialFromLabel is the proto DenialReason a journal label names. ok is false
// for a label the proto does not name, and for UNSPECIFIED, which is never a
// denial: a label the proto does not name looks up as zero, which is
// UNSPECIFIED, so one rule refuses both.
//
// That rule, a value the protocol declares and never zero, is
// forareason.EnumName's. denialLabel and the console's wire mapping ask it too,
// so none of them can disagree on what counts as a denial.
func denialFromLabel(l repo.DenialReason) (forav1.DenialReason, bool) {
	r := forav1.DenialReason(forav1.DenialReason_value[denialPrefix+string(l)])
	return r, forareason.EnumName(r) != ""
}

// denialLabel is the journal label of a proto DenialReason: its name without
// denialPrefix. ok is false for UNSPECIFIED, which is never a denial, and for a
// number the proto does not name.
func denialLabel(r forav1.DenialReason) (repo.DenialReason, bool) {
	name := forareason.EnumName(r)
	if name == "" {
		return "", false
	}
	return repo.DenialReason(strings.TrimPrefix(name, denialPrefix)), true
}

// batchDenialResult classifies a per-item execution error: a denial Kind
// (DenialReasonForKind ok) becomes an in-body TransactionResultItem carrying the
// denial_reason and offer_id; any other Kind (envelope-invalid / internal /
// not-found) returns ok=false so the caller aborts the whole batch.
func batchDenialResult(
	item *forav1.TransactionItem, err error,
) (*forav1.TransactionResultItem, bool) {
	reason, ok := DenialReasonForKind(err)
	if !ok {
		return nil, false
	}
	r := reason
	return &forav1.TransactionResultItem{
		OfferId:      item.GetOffer().GetOfferId(),
		DenialReason: &r,
	}, true
}
