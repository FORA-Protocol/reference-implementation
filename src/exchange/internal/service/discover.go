package service

import (
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/google/uuid"
	protobuf "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraoffer"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
)

// newOfferGroup starts the per-URI OfferGroup every discovery answer is built
// from. It states the discovery method for THIS service's answer: the Exchange
// resolves a URI against its own catalog and nothing else, so every group it
// emits is EXCHANGE, on all three groupFor branches alike.
//
// That claim is scoped to callers of ExchangeService.DiscoverResources: an
// agent that queries this Exchange directly receives this value unchanged. It
// is NOT the value an agent sees from BrokerService/Resolve: how
// a URI came to be asked about is something only the Broker knows, so the
// Broker decides it there. A second discovery source is a change in the Broker;
// this function keeps saying EXCHANGE, because that is what the Exchange did.
func newOfferGroup(uri string) *forav1.OfferGroup {
	return &forav1.OfferGroup{
		Uri:             uri,
		DiscoveryMethod: forav1.DiscoveryMethod_DISCOVERY_METHOD_EXCHANGE.Enum(),
	}
}

// groupFor builds the per-URI OfferGroup and returns the concrete offers that
// also land in the flat ResourceResponse.Offers slice (for backwards-compat
// with single-URI callers). The second return value is the same slice as
// group.Offers.
func (s *ExchangeService) groupFor(
	uri string,
	entry repo.CatalogEntry,
	verdict LookupVerdict,
	requester *forav1.Requester,
	profiles []string,
) (*forav1.OfferGroup, []*forav1.Offer, error) {
	switch verdict {
	case LookupMiss:
		group := newOfferGroup(uri)
		group.AbsenceReason = forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG.Enum()
		return group, nil, nil
	case LookupHitPublic:
		offers, err := s.offersFor(entry, requester, profiles)
		if err != nil {
			return nil, nil, exchange.Wrap(exchange.KindInternal, err, "build offer")
		}
		group := newOfferGroup(uri)
		// No offer when the requester is entitled to no priced term: an entry
		// whose terms all filter out (or that carries no terms) has no real
		// LicenseTerm to derive a price from, so it yields NO offer rather than a
		// stub-priced one (core invariant).
		//
		// The empty group states NO absence reason, whatever removed the terms.
		// A requester whose scopes cover none of the terms gets exactly the
		// answer an entry with no usable term gets, and never
		// OFFER_ABSENCE_REASON_SCOPE_INSUFFICIENT. The Requester.scopes comment
		// in the pinned protocol module is the rule: a resource outside the
		// requester's scopes is not returned, and the requester never learns it
		// exists. Naming the scope as the cause would tell it exactly that.
		if len(offers) == 0 {
			return group, nil, nil
		}
		group.Offers = offers
		return group, offers, nil
	default:
		return nil, nil, exchange.Newf(exchange.KindInternal, "unknown lookup verdict %d", verdict)
	}
}

// offersFor projects a catalog entry onto ONE SIGNED OFFER PER ELIGIBLE TERM,
// in the publisher's stored order. An offer sells exactly one LicenseTerm — the
// protocol bounds Offer.terms to a single element — so an entry with two terms
// yields two independently signed offers, each priced from its own term, and
// the agent picks the one it wants.
//
// When requester is non-nil the persisted terms are first filtered through
// selectTerms, so only terms this requester is entitled to become offers
// (selectTerms is the single source of term-eligibility; this is its only
// caller on the discovery path). A nil requester projects all stored terms
// (defensive only — DiscoverResources validates the requester upstream).
//
// Terms are read from the snapshot's rebuild-time decode cache — NOT decoded
// per call. The stored JSONB stays untrusted: a row that failed to decode at
// rebuild is cached as zero terms, which yields no offer here. An entry whose
// terms all filter out likewise returns an empty slice, and the caller emits a
// group with no offers rather than a stub-priced one. The two cases are
// deliberately not told apart: the caller answers both with no reason.
func (s *ExchangeService) offersFor(
	entry repo.CatalogEntry, requester *forav1.Requester, profiles []string,
) ([]*forav1.Offer, error) {
	snap := s.catalog.Snapshot()
	stored := snap.DecodedTerms(entry.ResourceID)
	eligible := stored
	if requester != nil {
		eligible = selectTerms(stored, requester)
	}
	offers := make([]*forav1.Offer, 0, len(eligible))
	for _, term := range eligible {
		offer, err := s.buildOffer(snap, entry, term, storedIndex(stored, term), profiles)
		if err != nil {
			return nil, err
		}
		offers = append(offers, offer)
	}
	return offers, nil
}

// buildOffer constructs and signs the Offer that sells ONE license term. The
// offer carries that single term and the price derived from it, so the price an
// agent is SHOWN and the price it is CHARGED can never diverge: the billing
// path charges the SIGNED offer's pricing — resolveOfferForTx reads it off the
// verified presented offer — and the signature covers exactly this term.
//
// termIdx is the term's ORIGINAL position in the entry's stored terms, which
// the CoMP package_id carries, so the precomputed CoMP projection is looked up
// for the term this offer actually sells even after eligibility filtering
// removed earlier terms.
//
// The signature is computed LAST, over everything set below. The execute path
// verifies the PRESENTED signed offer via helpers.VerifyPresentedOffer;
// buildOffer is the discovery-time producer only.
func (s *ExchangeService) buildOffer(
	snap *CatalogSnapshot,
	entry repo.CatalogEntry,
	term *forav1.LicenseTerm,
	termIdx int,
	profiles []string,
) (*forav1.Offer, error) {
	// Derive pricing from the term this offer sells. This is the public read
	// surface for terms: DiscoverResources -> Offer.terms is how canonicalized
	// restriction tokens become observable to agents (ADR-008 full-surface
	// round-trip).
	pricing, err := foraoffer.PricingFromTerm(term)
	if err != nil {
		return nil, fmt.Errorf("derive pricing: %w", err)
	}
	expires := s.clk.Now().Add(s.offerTTL(entry.TenantID))
	// The fields that describe the resource — the issuing Exchange, the title,
	// the identity and its mutability, the delivery method and the term — come
	// from the shared projection, which the onboarding preview builds its
	// unsigned samples from as well. The preview's claim is that a publisher sees
	// what an agent receives, so a field added here has to reach it in the same
	// commit.
	//
	// Every one of them is set BEFORE SignOffer a few lines below, so all of
	// them are signature-covered (helpers.canonicalOfferPayload marshals the
	// whole Offer minus signature/signature_algorithm). The Exchange domain
	// being covered is what makes the broker's execute-routing target derivable
	// from the signed offer itself, so no caller-supplied header names the
	// Exchange. The title is NOT routed through applyMetadata: that
	// function projects the extension metadata only, which is the same split the
	// catalog storage follows, and nil stays nil so a resource pushed without a
	// title yields an offer without one.
	offer := foraoffer.Build(foraoffer.Resource{
		Exchange:     s.cfg.Exchange,
		CanonicalURL: entry.URI,
		Title:        entry.Title,
		Pricing:      pricing,
		Terms:        []*forav1.LicenseTerm{unpricedTerm(term)},
	})
	// A fresh random UUID v4 per issued offer: offer_id is an opaque unique
	// identifier, decoupled from resource identity. The execute path binds the
	// offer to its catalog entry via the signed Identity.canonical_url, so
	// nothing derives the resource from this value; what it DOES key is per-item
	// batch correlation, the derived replay key (idempotency_key + offer_id),
	// and the audit rows' offer identity. Two offers for two terms of the same
	// resource therefore carry different offer_ids.
	offer.OfferId = uuid.NewString()
	offer.ExpiresAt = timestamppb.New(expires)
	// Project the resource extension metadata onto the offer BEFORE signing so it
	// is covered by the signature. Read from the same rebuild-time snapshot decode
	// cache (no per-call decode). buildOffer runs only at discovery: the execute
	// path verifies the PRESENTED offer bytes, never a reconstruction, so nothing
	// requires two buildOffer calls to reproduce identical signed bytes.
	md := snap.DecodedMetadata(entry.ResourceID)
	if md != nil {
		applyMetadata(offer, md)
	}
	// Merge the CoMP projection (precomputed once at snapshot rebuild)
	// AFTER metadata and BEFORE signing, so the comp ext is signature-covered.
	// Looked up by this term's ORIGINAL stored index, so the blob is
	// deterministic from stored state.
	cached := snap.RenderedProfile(entry.ResourceID, termIdx, profileCoMPV1)
	applyCompProfile(offer, cached, profiles)
	sig, err := s.offerSigner.SignOffer(offer)
	if err != nil {
		return nil, fmt.Errorf("sign offer: %w", err)
	}
	offer.Signature = sig
	offer.SignatureAlgorithm = signing.SignatureAlgorithm
	return offer, nil
}

// unpricedTerm is the copy of a catalog term an offer carries: the term without
// its pricing. An offer states its price once, in Offer.pricing (the protocol's
// offer.terms.pricing_unset rule), and the SDK refuses to sign an offer whose
// term is priced. The stored term is shared with the snapshot cache, so it is
// cloned rather than changed.
func unpricedTerm(term *forav1.LicenseTerm) *forav1.LicenseTerm {
	out, _ := protobuf.Clone(term).(*forav1.LicenseTerm)
	out.Pricing = nil
	return out
}

// storedIndex returns the ORIGINAL stored position of a projected term.
// selectTerms is order-preserving and returns the same pointers it was given,
// so identity match recovers the publisher index that the CoMP package_id
// carries. Falls back to 0 if not found (defensive).
func storedIndex(stored []*forav1.LicenseTerm, term *forav1.LicenseTerm) int {
	for i, t := range stored {
		if t == term {
			return i
		}
	}
	return 0
}

func strPtr(s string) *string { return &s }
