package rsl

import (
	"fmt"
	"slices"
	"strings"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

// zeroRate is what a free term carries. The protocol requires a free term to
// state a zero rate rather than leave it out, so that "free" is an assertion
// and not an omission.
const zeroRate = "0"

// mapPricing turns an RSL payment into FORA pricing under the rule for its
// type, or returns nil when the document names a price this preview cannot
// read.
//
// A priced payment whose amount is missing or is not a decimal number used to
// become a term priced at zero, with a warning. That reads as a small mercy and
// is the opposite: it hands the publisher a preview in which their paid content
// is free, and the offer below it says so in the protocol's own words. The
// amount is the one part of a payment nothing else can stand in for, so a
// license that has lost it is a license this preview cannot carry, exactly like
// one whose payment type has no FORA model.
func mapPricing(p Payment, rule paymentRule, d Defaults, idx int) (*forav1.Pricing, []string) {
	pricing := &forav1.Pricing{Model: rule.model, Currency: d.Currency}
	if rule.unit != "" {
		pricing.Unit = &rule.unit
	}
	rate, currency, warnings := declaredAmount(p, idx)
	if currency != "" {
		pricing.Currency = currency
	}
	if rule.model == forav1.PricingModel_PRICING_MODEL_FREE {
		pricing.Rate = zeroRate
		if rate != "" && rate != zeroRate {
			// The type says no charge and the document also names a price. The
			// price is dropped rather than carried, because a term that both
			// says free and charges is not a term anyone can act on.
			warnings = append(warnings, fmt.Sprintf(
				"license %d is a %s payment, which carries no charge, so the declared amount %s %s is not part of the term",
				idx+1, p.Type, rate, pricing.Currency))
		}
		return pricing, warnings
	}
	if rate == "" {
		return nil, append(warnings, fmt.Sprintf(
			"license %d declares a %s payment %s, so the license is not carried; "+
				"a priced term is never rewritten as a free one", idx+1, p.Type, missingAmount(p)))
	}
	pricing.Rate = rate
	unitCost := rate
	pricing.UnitCost = &unitCost
	return pricing, warnings
}

// missingAmount says which way the amount was lost, so the publisher knows
// whether to add one or to correct the one they wrote.
func missingAmount(p Payment) string {
	if len(p.Amounts) == 0 {
		return "with no amount"
	}
	return "whose amount could not be read"
}

// declaredAmount reads the first amount off a payment, canonicalised. Money is
// a decimal string on every FORA surface, never a float, so an amount that is
// not one is dropped with a warning rather than rounded into one.
func declaredAmount(p Payment, idx int) (rate, currency string, warnings []string) {
	if len(p.Amounts) == 0 {
		return "", "", nil
	}
	first := p.Amounts[0]
	if len(p.Amounts) > 1 {
		warnings = append(warnings, fmt.Sprintf(
			"license %d declares %d amounts; the term carries the first", idx+1, len(p.Amounts)))
	}
	if first.Value != "" {
		canonical, err := helpers.CanonicalizeMoney(first.Value)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"license %d declares the amount %q, which is not a decimal number",
				idx+1, first.Value))
		} else {
			rate = canonical
		}
	}
	return rate, strings.ToUpper(first.Currency), warnings
}

// mapRestrictions turns the permits and prohibits statements into restrictions,
// one per axis, with the use named by the payment type folded in.
//
// The axes are built together rather than one at a time because the protocol
// allows one restriction per axis: two statements about usage in a document are
// one restriction here. A token a document both permits and prohibits is
// carried as prohibited only. The protocol refuses a term whose two lists name
// one token, so carrying the contradiction forward would make the whole
// document unusable; the prohibition is the safer half to keep, and the
// publisher is told which statement was dropped.
func mapRestrictions(lic License, impliedFunction string, idx int) ([]*forav1.Restriction, []string) {
	axes := map[forav1.RestrictionKind]*forav1.Restriction{}
	collect := func(usages []Usage, prohibited bool) {
		for _, u := range usages {
			kind := axisKind(u.Type)
			r, ok := axes[kind]
			if !ok {
				r = &forav1.Restriction{Kind: kind}
				axes[kind] = r
			}
			for _, tok := range u.Tokens {
				canonical := canonicalToken(kind, tok)
				if prohibited {
					r.Prohibited = appendToken(r.Prohibited, canonical)
				} else {
					r.Permitted = appendToken(r.Permitted, canonical)
				}
			}
		}
	}
	collect(lic.Permits, false)
	collect(lic.Prohibits, true)
	if impliedFunction != "" {
		addImpliedFunction(axes, impliedFunction)
	}
	ordered := orderedRestrictions(axes)
	warnings := resolveContradictions(ordered, idx)
	return ordered, warnings
}

// resolveContradictions drops from each axis's permitted list every token its
// prohibited list also names, and says so. It reads the axes in the order
// orderedRestrictions fixed, so the same document always produces the same
// warnings, and it runs after the implied use is folded in, because that use
// can be the token the document prohibits.
func resolveContradictions(restrictions []*forav1.Restriction, idx int) []string {
	var warnings []string
	for _, r := range restrictions {
		kept := make([]string, 0, len(r.Permitted))
		for _, token := range r.Permitted {
			if slices.Contains(r.Prohibited, token) {
				warnings = append(warnings, fmt.Sprintf(
					"license %d both permits and prohibits %q, which is a term no exchange can accept, "+
						"so the term prohibits it", idx+1, token))
				continue
			}
			kept = append(kept, token)
		}
		if len(kept) == 0 {
			kept = nil
		}
		r.Permitted = kept
	}
	return warnings
}

// addImpliedFunction records the use the payment type names, unless the
// document already prohibits it. A publisher who prices training and also
// forbids it has contradicted themselves; the prohibition is the safer half to
// keep, and a term carrying the token on both lists would be refused outright.
func addImpliedFunction(axes map[forav1.RestrictionKind]*forav1.Restriction, token string) {
	kind := forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION
	r, ok := axes[kind]
	if !ok {
		r = &forav1.Restriction{Kind: kind}
		axes[kind] = r
	}
	for _, p := range r.GetProhibited() {
		if p == token {
			return
		}
	}
	r.Permitted = appendToken(r.Permitted, token)
}

// orderedRestrictions returns the axes in a fixed order, so the same document
// always produces the same term rather than one that depends on map iteration.
func orderedRestrictions(axes map[forav1.RestrictionKind]*forav1.Restriction) []*forav1.Restriction {
	order := []forav1.RestrictionKind{
		forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION,
		forav1.RestrictionKind_RESTRICTION_KIND_USER_TYPE,
		forav1.RestrictionKind_RESTRICTION_KIND_GEOGRAPHY,
		forav1.RestrictionKind_RESTRICTION_KIND_OTHER,
	}
	var out []*forav1.Restriction
	for _, kind := range order {
		r, ok := axes[kind]
		if !ok {
			continue
		}
		if len(r.GetPermitted()) == 0 && len(r.GetProhibited()) == 0 {
			// An axis the document named but left empty says nothing, and an
			// empty restriction would only add noise to the term.
			continue
		}
		out = append(out, r)
	}
	return out
}

// axisKind maps an RSL statement type onto a FORA restriction axis. RSL reads
// an omitted type as a statement about usage.
func axisKind(rslType string) forav1.RestrictionKind {
	switch rslType {
	case "", "usage":
		return forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION
	case "user":
		return forav1.RestrictionKind_RESTRICTION_KIND_USER_TYPE
	case "geo":
		return forav1.RestrictionKind_RESTRICTION_KIND_GEOGRAPHY
	default:
		return forav1.RestrictionKind_RESTRICTION_KIND_OTHER
	}
}

// canonicalToken folds one token onto the registered spelling for its axis.
//
// The fold itself is the SDK's, which is what the Exchange runs over a pushed
// entry: it trims RFC 8259 whitespace and folds case in ASCII only. ASCII only
// matters — several non-ASCII code points lower-case into ASCII letters, so a
// Unicode fold would turn a homograph of "ai-train" into the registered token
// and grant a use the publisher never wrote.
//
// One thing is added on top of the SDK's fold: the RSL user vocabulary, whose
// words for who the user is are not FORA's. That map is applied to the folded
// token, so a spelling the SDK already resolves reaches it in canonical form.
// A token with no counterpart passes through unchanged: the restriction
// vocabulary is open, so an unknown token is flagged by the Exchange rather
// than refused, and dropping it here would lose a statement the publisher made.
func canonicalToken(kind forav1.RestrictionKind, token string) string {
	if kind == forav1.RestrictionKind_RESTRICTION_KIND_USER_TYPE {
		folded := helpers.CanonicalRestrictionToken(kind, token)
		if mapped, ok := rslToUserType[folded]; ok {
			return mapped
		}
		return folded
	}
	return helpers.CanonicalRestrictionToken(kind, token)
}

// appendToken adds a token once. A document that names the same token twice
// means it once, and a repeated token would only pad the term.
func appendToken(list []string, token string) []string {
	if token == "" {
		return list
	}
	for _, existing := range list {
		if existing == token {
			return list
		}
	}
	return append(list, token)
}
