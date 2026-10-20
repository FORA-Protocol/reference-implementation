package resolve

import (
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// probeOutcome accumulates the per-domain probe results so the
// refusal-shape selector can map "no manifests at all" to the correct
// canonical absence vocabulary. v1 publishers MUST host fora.json;
// missing and refused → NOT_IN_CATALOG, transient → TEMPORARILY_UNAVAILABLE.
type probeOutcome struct {
	missing   int // ErrManifestMissing — publisher has no fora.json
	refused   int // ErrManifestVersionRefused / ErrManifestUnusable — manifest read, refused on its content
	transient int // ErrProbeFailed / unexpected error — upstream unreachable
}

// settled reports whether any probe reached a verdict about a publisher. There
// are TWO such causes and both are counted here: the publisher hosts no manifest
// (missing), and the publisher hosts one this Broker refuses on its content
// (refused) -- a document version this build does not accept, or a document that
// broke a rule. A
// verdict does not change on a retry, so one settled outcome makes the refusal
// NOT_IN_CATALOG even when another publisher in the batch was merely
// unreachable.
func (o probeOutcome) settled() bool { return o.missing > 0 || o.refused > 0 }

// discoverFlags accumulates the per-exchange observations the
// caller needs to map to a Response.AbsenceReason.
type discoverFlags struct {
	// upstreamReason is the most recent non-UNSPECIFIED request-level
	// absence reason an upstream Exchange surfaced. The broker
	// preserves this signal when none of the exchanges returned an
	// offer so the caller doesn't lose the structured cause across the
	// proxy hop.
	upstreamReason forav1.OfferAbsenceReason
	// allUpstreamFailed is true when every upstream Exchange call
	// failed (transient RPC errors). The resolve response is then
	// stamped INTERNAL_ERROR rather than a refusal-shaped reason.
	allUpstreamFailed bool
	// namedExchangeDown is true when at least one manifest-named exchange was
	// registered and trusted but not usable for this request.
	//
	// Separate from allUpstreamFailed, which observes the other half of the same
	// outage: it needs an exchange to have been DIALLED and the call to have
	// failed, so one the router declined before dialling never reaches it.
	// Without this flag a batch whose only exchange is down falls through to
	// NOT_IN_CATALOG — a permanent answer to a condition that clears within one
	// refresher interval.
	namedExchangeDown bool
}

// noHealthyExchangeResponse is the canonical broker-side response
// when a publisher manifest exists but none of its referenced
// exchanges is healthy. Surfaces as TEMPORARILY_UNAVAILABLE on the
// wire — the W1 proto sync removed the bespoke
// NO_HEALTHY_EXCHANGE enum and the transient-upstream semantic now
// rides on TEMPORARILY_UNAVAILABLE. v1 emits no bare_url — every
// publisher MUST host fora.json, so a refusal here is a real refusal,
// not a redirect to unbrokered fetch.
func noHealthyExchangeResponse() *Response {
	return &Response{
		AbsenceReason: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE,
	}
}

// probeRefusalResponse is the broker's response when probeDomains
// yielded zero usable manifests. Per v1, every publisher MUST host
// /.well-known/fora.json; the absence_reason distinguishes a settled
// verdict — a publisher that returned 404, or one whose manifest carries a
// document version this Broker does not accept (NOT_IN_CATALOG — the
// canonical refusal) — from a publisher whose manifest fetch failed
// transiently (TEMPORARILY_UNAVAILABLE). Mixed outcomes prefer
// TEMPORARILY_UNAVAILABLE only when every failure was transient.
func probeRefusalResponse(outcome probeOutcome) *Response {
	reason := forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG
	if outcome.transient > 0 && !outcome.settled() {
		reason = forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE
	}
	return &Response{
		AbsenceReason: reason,
	}
}

// noOffersResponse is the broker's structured-cause response when the
// upstream exchanges all returned (without RPC errors) but produced
// no offers — either because of an upstream reason that propagates
// through, or a generic empty-offers outcome. AbsenceReason carries the
// ADR-008 D2 enum — the sole refusal-cause surface (ADR-019).
func noOffersResponse(flags discoverFlags) *Response {
	return &Response{
		AbsenceReason: pickResolveAbsenceReason(flags),
	}
}

// budgetExhaustedResponse is the broker's response when the caller's
// budget would not cover the winning offer's price. Surfaces as
// NOT_AUTHORIZED on the wire — the W1 proto sync dropped the
// bespoke BILLING_BLOCK enum value; a budget-side authorization failure
// is reasonably modelled as caller-not-authorized to spend.
func budgetExhaustedResponse() *Response {
	return &Response{
		AbsenceReason: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_AUTHORIZED,
	}
}

// pickResolveAbsenceReason maps the per-exchange observations from
// the resolve hot path to the ADR-008 D2 request-level absence-reason
// vocabulary. Order of precedence:
//
//  1. allUpstreamFailed → TEMPORARILY_UNAVAILABLE (transient: every
//     Exchange RPC raised an error). ADR-008 D2 is explicit that an
//     internal failure must never be miscategorised as a clean refusal.
//     The W1 proto sync dropped the bespoke INTERNAL_ERROR
//     enum; TEMPORARILY_UNAVAILABLE carries the transient-upstream
//     semantic on the canonical surface.
//  2. upstreamReason set → propagate the upstream cause unchanged so
//     the caller sees the same vocabulary across the proxy hop.
//  3. namedExchangeDown → TEMPORARILY_UNAVAILABLE (transient: a
//     manifest-named exchange was registered and trusted but not usable,
//     so the router declined it before dialling). Ranked below the
//     propagated upstream cause, because an exchange that DID answer said
//     something specific about the request and that beats a sibling merely
//     being down. Matches what noHealthyExchangeResponse gives the
//     free-text query path for the same condition.
//  4. Otherwise NOT_IN_CATALOG — generic empty-offers fallback,
//     replacing the prior UNKNOWN_RESOURCE value. An Exchange whose
//     requester's scopes cover none of a resource's terms answers an empty
//     group with no reason, so that case lands here too, the same answer as
//     any resource with nothing to offer. The Broker adds no scope reason of
//     its own: a resource outside the requester's scopes stays hidden (the
//     Requester.scopes comment in the pinned protocol module).
func pickResolveAbsenceReason(flags discoverFlags) forav1.OfferAbsenceReason {
	if flags.allUpstreamFailed {
		return forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE
	}
	if flags.upstreamReason != forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_UNSPECIFIED {
		return flags.upstreamReason
	}
	if flags.namedExchangeDown {
		return forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE
	}
	return forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG
}
