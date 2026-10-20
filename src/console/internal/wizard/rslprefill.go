// Reading what a publisher's own site already says about its licensing, so the
// wizard's last step opens with their terms rather than with ours.

package wizard

import (
	"context"
	"strings"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/rsl"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

// detectedControls reads the domain's own licensing document and returns the
// answers it amounts to, when the wizard's controls can express them.
//
// A publisher who has already stated their terms in RSL should not have to
// state them again in a form. But the step is still shown and they still press
// the button: the price is theirs to agree, and a form filled in from a file we
// read is not the same as a price they chose.
//
// Nothing here is an error path. A domain that publishes no licensing document
// is the ordinary case and returns no answers and no note.
func (s *Service) detectedControls(
	ctx context.Context, domain string, site *pagefetch.Site, d termspreset.Defaults,
) (*termspreset.Controls, string) {
	if s.pages == nil || site == nil {
		return nil, ""
	}
	body, address, err := s.pages.LicenseDocument(ctx, domain, site)
	if err != nil {
		reqctx.FromContext(ctx).InfoContext(ctx, "console.license_document_unreadable",
			"domain", domain, "err", err)
		return nil, "Your site publishes a licensing document we could not read, " +
			"so these are our defaults rather than your terms."
	}
	if body == nil {
		return nil, ""
	}
	found := rsl.Detect(body, site.Page.CanonicalURL, d.RSL(domain))
	if found.Failure != rsl.FailureNone {
		return nil, failedDetectionText(found, address)
	}
	controls, ok := controlsFromTerms(found.Terms, d)
	if !ok {
		return nil, saysMoreText(address, found.Warnings)
	}
	note := "Filled in from the licensing your site publishes at " + address +
		". Change anything that is not what you want to offer here."
	if len(found.Warnings) > 0 {
		note += " Some of it could not be carried over: " + strings.Join(found.Warnings, "; ") + "."
	}
	return &controls, note
}

// failedDetectionText is the note for a document that produced no terms, worded
// for a publisher about their own file.
func failedDetectionText(found rsl.Detection, address string) string {
	switch found.Failure {
	case rsl.FailureNotReadable, rsl.FailureRuleUnreadable:
		return "The document at " + address + " is not one we can read, " +
			"so these are our defaults rather than your terms."
	case rsl.FailureNoRule:
		return "The licensing at " + address + " states no rule for your pages, " +
			"so these are our defaults rather than your terms."
	default:
		return saysMoreText(address, found.Warnings)
	}
}

// saysMoreText is the note for a document whose terms this page cannot set,
// with what the mapping itself had to leave out, so the publisher reads why
// rather than only that.
func saysMoreText(address string, warnings []string) string {
	note := "We read the licensing at " + address + ", but it says more than " +
		"this page can set. Choose below what you want to offer here."
	if len(warnings) > 0 {
		note += " What could not be carried over: " + strings.Join(warnings, "; ") + "."
	}
	return note
}

// controlsFromTerms is the reverse of termspreset.FromControls, and it refuses
// far more than that builds.
//
// The two vocabularies are not the same size. A licensing document can state
// several terms at once, price a flat fee, settle through a subscription, and
// restrict by who the user is or where they are; this page sets one price, one
// list of permitted uses, and whether attribution is required. Anything outside
// that is refused rather than approximated, because a form that quietly drops
// half of what a publisher wrote would be worse than one that starts empty:
// they would read it as their terms and press the button.
func controlsFromTerms(terms []*forav1.LicenseTerm, d termspreset.Defaults) (termspreset.Controls, bool) {
	if len(terms) != 1 {
		return termspreset.Controls{}, false
	}
	term := terms[0]
	if len(term.GetScopes()) > 0 {
		// A scope is how a subscription is carried, and it is settled outside
		// the protocol. This page has no way to say that.
		return termspreset.Controls{}, false
	}
	out, ok := pricingControls(term.GetPricing(), d)
	if !ok {
		return termspreset.Controls{}, false
	}
	permitted, prohibited, ok := functionTokens(term.GetRestrictions())
	if !ok {
		return termspreset.Controls{}, false
	}
	out.PermittedFunctions = permitted
	out.ProhibitedFunctions = prohibited
	attribution, ok := attributionOnly(term.GetObligations())
	if !ok {
		return termspreset.Controls{}, false
	}
	out.AttributionRequired = attribution
	// A prohibition the builder has no token for would pass here and be
	// refused at the last button, so the prefill is held to what the builder
	// accepts.
	if _, err := termspreset.FromControls(out, d); err != nil {
		return termspreset.Controls{}, false
	}
	return out, true
}

// pricingControls reads the price, or refuses a model this page cannot set.
//
// A per-unit price is only taken when it is metered the way this deployment
// meters. The form has no unit control: it prices in the deployment's own unit,
// so filling it from a term metered in something else would put a number
// against the wrong thing.
func pricingControls(p *forav1.Pricing, d termspreset.Defaults) (termspreset.Controls, bool) {
	switch p.GetModel() {
	case forav1.PricingModel_PRICING_MODEL_FREE:
		return termspreset.Controls{PricingModel: termspreset.ModelFree}, true
	case forav1.PricingModel_PRICING_MODEL_PER_UNIT:
		if !strings.EqualFold(p.GetUnit(), d.Vocab().Unit) {
			return termspreset.Controls{}, false
		}
		if !strings.EqualFold(p.GetCurrency(), d.Vocab().Currency) {
			return termspreset.Controls{}, false
		}
		return termspreset.Controls{
			PricingModel: termspreset.ModelPerUnit, Rate: p.GetRate(),
		}, true
	default:
		return termspreset.Controls{}, false
	}
}

// functionTokens takes the uses off a term, and refuses a term that restricts
// on anything else.
//
// A restriction by user type or by geography is a real statement the publisher
// made. Dropping it silently would show them a form that permits more than
// their document does, so the whole prefill is refused instead and the note
// says so.
func functionTokens(rs []*forav1.Restriction) (permitted, prohibited []string, ok bool) {
	for _, restriction := range rs {
		if restriction.GetKind() != forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION {
			return nil, nil, false
		}
		permitted = append(permitted, restriction.GetPermitted()...)
		prohibited = append(prohibited, restriction.GetProhibited()...)
	}
	for _, token := range permitted {
		if !offeredHere(token) {
			return nil, nil, false
		}
	}
	return permitted, prohibited, true
}

// offeredHere reports whether a use is one of the three this page lists. A
// permitted use it does not list would be shown as refused, which reverses what
// the publisher wrote.
func offeredHere(token string) bool {
	for _, choice := range functionChoices() {
		if strings.EqualFold(strings.TrimSpace(token), choice.Value) {
			return true
		}
	}
	return false
}

// attributionOnly reports whether the term's duties amount to the one checkbox
// this page has.
func attributionOnly(obligations []*forav1.Obligation) (required, ok bool) {
	for _, o := range obligations {
		if o.GetKind() != forav1.ObligationKind_OBLIGATION_KIND_ATTRIBUTION {
			return false, false
		}
		required = true
	}
	return required, true
}
