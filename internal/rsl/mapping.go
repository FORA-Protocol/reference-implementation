package rsl

import (
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/gen/go/vocab/functiontokens"
	"github.com/FORA-Protocol/protocol/gen/go/vocab/pricingunits"
	"github.com/FORA-Protocol/protocol/gen/go/vocab/usertypes"
)

// Defaults fill in what an RSL document does not say.
type Defaults struct {
	// Currency is used when a declared amount names none.
	Currency string
	// Domain names the publisher in the scope a subscription maps to.
	Domain string
}

// Mapped is the result of reading one content rule.
type Mapped struct {
	// Terms are the license terms, one per RSL license that could be mapped.
	Terms []*forav1.LicenseTerm
	// Warnings say what the document stated that the terms do not carry. They
	// are for the publisher to read, not for the caller to act on.
	Warnings []string
}

// rslToUserType maps the RSL user vocabulary onto FORA's user types. RSL
// describes who the user is in five words; FORA names six entity types. Four
// map cleanly. "government" has no FORA counterpart and is carried through
// unchanged, where it is accepted as an unregistered token and flagged, rather
// than being forced into a type that would misstate who the publisher meant.
var rslToUserType = map[string]string{
	"commercial":     usertypes.CommercialEntity,
	"non-commercial": usertypes.NonProfit,
	"education":      usertypes.Academic,
	"personal":       usertypes.Individual,
}

// paymentRule is how one RSL payment type becomes FORA pricing.
type paymentRule struct {
	model forav1.PricingModel
	// unit is the metering basis for a per-unit model.
	unit string
	// impliedFunction is the use the payment type itself names. RSL says what
	// is being paid for in the payment element; FORA says it in a restriction,
	// so the token moves across with the price.
	impliedFunction string
	// obligation is the duty the payment type states, for the types that are
	// free but ask something of the user.
	obligation forav1.ObligationKind
	// subscription marks the one type that is settled outside the protocol.
	subscription bool
}

// paymentRules is the mapping published with the protocol, in one place. Each
// row is exercised by its own test, so a row cannot be changed silently.
var paymentRules = map[string]paymentRule{
	"free": {model: forav1.PricingModel_PRICING_MODEL_FREE},
	"attribution": {
		model:      forav1.PricingModel_PRICING_MODEL_FREE,
		obligation: forav1.ObligationKind_OBLIGATION_KIND_ATTRIBUTION,
	},
	"contribution": {
		model:      forav1.PricingModel_PRICING_MODEL_FREE,
		obligation: forav1.ObligationKind_OBLIGATION_KIND_CONTRIBUTION,
	},
	"subscription": {model: forav1.PricingModel_PRICING_MODEL_FREE, subscription: true},
	"crawl": {
		model:           forav1.PricingModel_PRICING_MODEL_PER_UNIT,
		unit:            pricingunits.Fetches,
		impliedFunction: functiontokens.Crawl,
	},
	"training": {
		model:           forav1.PricingModel_PRICING_MODEL_PER_UNIT,
		unit:            pricingunits.Tokens,
		impliedFunction: functiontokens.AiTrain,
	},
	"use": {
		model:           forav1.PricingModel_PRICING_MODEL_PER_UNIT,
		unit:            pricingunits.Tokens,
		impliedFunction: functiontokens.AiInput,
	},
	"inference": {
		model:           forav1.PricingModel_PRICING_MODEL_PER_UNIT,
		unit:            pricingunits.Tokens,
		impliedFunction: functiontokens.AiInput,
	},
	"purchase": {model: forav1.PricingModel_PRICING_MODEL_FLAT},
}

// ToLicenseTerms maps one content rule onto FORA license terms.
//
// A license the document states but FORA cannot express is skipped with a
// warning rather than guessed at: a term that misstates a publisher's price is
// worse than no term. Terms are ENUMERATED, never REFERENCE_ONLY, and the
// address of a referenced licensing document is reported as a warning rather
// than carried: the protocol requires a digest of any referenced document, and
// this package does not fetch the document to compute one.
func ToLicenseTerms(c *Content, d Defaults) (Mapped, error) {
	if c == nil {
		return Mapped{}, fmt.Errorf("rsl: no content rule to map")
	}
	var m Mapped
	for i, lic := range c.Licenses {
		term, warnings := mapLicense(lic, d, i)
		m.Warnings = append(m.Warnings, warnings...)
		if term != nil {
			m.Terms = append(m.Terms, term)
		}
	}
	return m, nil
}

// mapLicense turns one RSL license into a term, or into warnings saying why it
// could not become one.
func mapLicense(lic License, d Defaults, idx int) (*forav1.LicenseTerm, []string) {
	if lic.Payment == nil {
		return nil, []string{fmt.Sprintf(
			"license %d states no payment, which RSL reads as \"ask the publisher\"; it has no price to offer", idx+1)}
	}
	rule, ok := paymentRules[lic.Payment.Type]
	if !ok {
		return nil, []string{fmt.Sprintf(
			"license %d uses the payment type %q, which has no FORA pricing model", idx+1, lic.Payment.Type)}
	}
	pricing, warnings := mapPricing(*lic.Payment, rule, d, idx)
	if pricing == nil {
		return nil, warnings
	}
	term := &forav1.LicenseTerm{
		Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
		Pricing:   pricing,
	}
	if rule.subscription && d.Domain != "" {
		// A subscription is not a pricing model: access is free at the point of
		// use and settled elsewhere, gated on the buyer holding the scope.
		term.Scopes = []string{"subscription:" + d.Domain}
	}
	if rule.obligation != forav1.ObligationKind_OBLIGATION_KIND_UNSPECIFIED {
		term.Obligations = []*forav1.Obligation{{
			Kind:    rule.obligation,
			Trigger: forav1.ObligationTrigger_OBLIGATION_TRIGGER_ON_USE,
		}}
	}
	restrictions, contradictions := mapRestrictions(lic, rule.impliedFunction, idx)
	term.Restrictions = restrictions
	warnings = append(warnings, contradictions...)
	if ref := licenseReference(*lic.Payment); ref != "" {
		warnings = append(warnings, fmt.Sprintf(
			"license %d points at the licensing document %s; the term does not carry it, because the protocol "+
				"requires a digest of a referenced document and this preview does not fetch it", idx+1, ref))
	}
	return term, warnings
}

// licenseReference returns the address of the licensing document behind a
// payment, preferring the publisher's own over the shared framework.
func licenseReference(p Payment) string {
	if p.Custom != "" {
		return p.Custom
	}
	return p.Standard
}
