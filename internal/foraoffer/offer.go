// Package foraoffer projects a resource onto the offer an agent receives.
//
// Two places build an offer for one resource: the Exchange, at discovery, and
// the onboarding preview, which shows a publisher what an agent would be sent
// before they register. The preview's whole claim is that the two agree, so the
// fields that describe the resource are assembled once, here. A field the
// Exchange starts setting — resource mutability read from the catalog entry is
// the one already in sight — then reaches the preview in the same commit
// instead of leaving it quietly answering with last year's shape.
//
// What is NOT here is everything only an Exchange can produce: the offer id it
// assigns, the expiry window it commits to, the signature that makes the price
// its own, and the extension metadata and CoMP projection it reads from its own
// catalog. Those are set by the Exchange on top of what Build returns, and
// their absence is what keeps a preview a preview.
package foraoffer

import (
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// Resource is everything about one resource that decides the offer's shape.
type Resource struct {
	// Exchange is the canonical domain of the Exchange that would issue the
	// offer. It names the issuer in a preview and is signature-covered in a
	// real one.
	Exchange string
	// CanonicalURL is the resource's identity: the catalog entry's URI on the
	// Exchange's path, the address the page declares as its own on the
	// preview's.
	CanonicalURL string
	// Title is the resource's headline, or nil when it has none. nil stays nil:
	// a resource pushed without a title yields an offer without one.
	Title *string
	// Pricing is the price of the term this offer sells, already derived from
	// that term. Callers own the value they pass and Build does not copy it.
	Pricing *forav1.Pricing
	// Terms are the license terms the offer carries. An offer sells exactly one
	// term — the protocol bounds the field to a single element — so both callers
	// pass a one-element slice, and Pricing above is that term's price.
	Terms []*forav1.LicenseTerm
}

// Build assembles the offer fields that describe a resource.
//
// Delivery is by instructions on both paths: FORA hands an agent a signed URL
// to fetch rather than the bytes themselves.
//
// Mutability is STATIC for every offer today. The Exchange has no per-resource
// mutability column, and a preview reads one page once and has no basis for
// saying a resource changes. The protocol refuses an unspecified value, so this
// is a stated default rather than an omission — and when the catalog entry
// starts carrying it, this is the one line that changes.
func Build(r Resource) *forav1.Offer {
	return &forav1.Offer{
		Exchange:       r.Exchange,
		Title:          r.Title,
		Pricing:        r.Pricing,
		DeliveryMethod: forav1.DeliveryMethod_DELIVERY_METHOD_INSTRUCTIONS,
		Identity: &forav1.ResourceIdentity{
			CanonicalUrl:       &r.CanonicalURL,
			ResourceMutability: forav1.ResourceMutability_RESOURCE_MUTABILITY_STATIC,
		},
		Terms: r.Terms,
	}
}
