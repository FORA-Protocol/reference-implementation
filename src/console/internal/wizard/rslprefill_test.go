package wizard

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

// Reading a publisher's own licensing back into the four controls this page
// has. It is a unit test because the function is pure and the branches are the
// point: every one of them decides whether a publisher is shown their own terms
// or ours, and getting that wrong shows them a form permitting more than their
// document does.

func defaults() termspreset.Defaults {
	return termspreset.Defaults{Currency: "EUR", Unit: "fetches"}
}

func freeTerm() *forav1.LicenseTerm {
	return &forav1.LicenseTerm{
		Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
		Pricing: &forav1.Pricing{
			Model: forav1.PricingModel_PRICING_MODEL_FREE, Rate: "0", Currency: "EUR",
		},
		Restrictions: []*forav1.Restriction{{
			Kind:       forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION,
			Permitted:  []string{"search"},
			Prohibited: []string{"ai-train"},
		}},
	}
}

func TestPrefillReadsAFreeTerm(t *testing.T) {
	got, ok := controlsFromTerms([]*forav1.LicenseTerm{freeTerm()}, defaults())
	if !ok {
		t.Fatal("a free term the page can express was refused")
	}
	if got.PricingModel != termspreset.ModelFree {
		t.Fatalf("pricing model = %q, want %q", got.PricingModel, termspreset.ModelFree)
	}
	if len(got.PermittedFunctions) != 1 || got.PermittedFunctions[0] != "search" {
		t.Fatalf("permitted = %v, want the one use the document names", got.PermittedFunctions)
	}
	if got.AttributionRequired {
		t.Fatal("a term with no obligations asked for attribution")
	}
}

func TestPrefillReadsAChargeInTheDeploymentsOwnUnit(t *testing.T) {
	unit := "fetches"
	term := freeTerm()
	term.Pricing = &forav1.Pricing{
		Model: forav1.PricingModel_PRICING_MODEL_PER_UNIT,
		Rate:  "0.02", Currency: "EUR", Unit: &unit,
	}
	got, ok := controlsFromTerms([]*forav1.LicenseTerm{term}, defaults())
	if !ok {
		t.Fatal("a per-unit term in the deployment's own unit was refused")
	}
	if got.PricingModel != termspreset.ModelPerUnit || got.Rate != "0.02" {
		t.Fatalf("read back %+v, want the charge the document names", got)
	}
}

func TestPrefillReadsAnAttributionObligation(t *testing.T) {
	term := freeTerm()
	term.Obligations = []*forav1.Obligation{{
		Kind:    forav1.ObligationKind_OBLIGATION_KIND_ATTRIBUTION,
		Trigger: forav1.ObligationTrigger_OBLIGATION_TRIGGER_ON_USE,
	}}
	got, ok := controlsFromTerms([]*forav1.LicenseTerm{term}, defaults())
	if !ok || !got.AttributionRequired {
		t.Fatalf("an attribution obligation was not read back: %+v ok=%v", got, ok)
	}
}

// Everything below is a refusal, and each one is a case where filling the form
// in would misstate what the publisher published.
func TestPrefillRefusesWhatThePageCannotSay(t *testing.T) {
	unit := "fetches"
	otherUnit := "tokens"
	cases := []struct {
		name  string
		terms []*forav1.LicenseTerm
		why   string
	}{
		{
			name:  "no terms at all",
			terms: nil,
			why:   "there is nothing to fill in from",
		},
		{
			name:  "several terms",
			terms: []*forav1.LicenseTerm{freeTerm(), freeTerm()},
			why:   "the page sets one arrangement, not a list of them",
		},
		{
			name: "a subscription",
			terms: func() []*forav1.LicenseTerm {
				t := freeTerm()
				t.Scopes = []string{"subscription:stoa.example"}
				return []*forav1.LicenseTerm{t}
			}(),
			why: "access settled outside the protocol has no control here",
		},
		{
			name: "a flat fee",
			terms: func() []*forav1.LicenseTerm {
				t := freeTerm()
				t.Pricing = &forav1.Pricing{
					Model: forav1.PricingModel_PRICING_MODEL_FLAT, Rate: "50", Currency: "EUR",
				}
				return []*forav1.LicenseTerm{t}
			}(),
			why: "the page offers free or per-unit and nothing else",
		},
		{
			name: "a charge metered in something else",
			terms: func() []*forav1.LicenseTerm {
				t := freeTerm()
				t.Pricing = &forav1.Pricing{
					Model: forav1.PricingModel_PRICING_MODEL_PER_UNIT,
					Rate:  "0.02", Currency: "EUR", Unit: &otherUnit,
				}
				return []*forav1.LicenseTerm{t}
			}(),
			why: "the form has no unit control, so the number would price the wrong thing",
		},
		{
			name: "a charge in another currency",
			terms: func() []*forav1.LicenseTerm {
				t := freeTerm()
				t.Pricing = &forav1.Pricing{
					Model: forav1.PricingModel_PRICING_MODEL_PER_UNIT,
					Rate:  "0.02", Currency: "USD", Unit: &unit,
				}
				return []*forav1.LicenseTerm{t}
			}(),
			why: "the form prices in the deployment's currency",
		},
		{
			name: "a restriction on who the reader is",
			terms: func() []*forav1.LicenseTerm {
				t := freeTerm()
				t.Restrictions = append(t.Restrictions, &forav1.Restriction{
					Kind:      forav1.RestrictionKind_RESTRICTION_KIND_USER_TYPE,
					Permitted: []string{"non-profit"},
				})
				return []*forav1.LicenseTerm{t}
			}(),
			why: "dropping it would show a form permitting more than the document does",
		},
		{
			name: "a permitted use this page does not list",
			terms: func() []*forav1.LicenseTerm {
				t := freeTerm()
				t.Restrictions[0].Permitted = []string{"search", "crawl"}
				return []*forav1.LicenseTerm{t}
			}(),
			why: "a use the page cannot show would come out looking refused",
		},
		{
			name: "a duty other than attribution",
			terms: func() []*forav1.LicenseTerm {
				t := freeTerm()
				t.Obligations = []*forav1.Obligation{{
					Kind: forav1.ObligationKind_OBLIGATION_KIND_CONTRIBUTION,
				}}
				return []*forav1.LicenseTerm{t}
			}(),
			why: "the page has one checkbox and it is not this one",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := controlsFromTerms(tc.terms, defaults()); ok {
				t.Fatalf("the form was filled in anyway; %s", tc.why)
			}
		})
	}
}
