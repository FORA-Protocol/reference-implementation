package termspreset_test

import (
	"encoding/json"
	"strings"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/publicerr"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

var testDefaults = termspreset.Defaults{Currency: "EUR", Unit: "fetches"}

// assertAccepted checks the Exchange would take these terms for a page. A
// control set that produces terms the Exchange refuses would show a publisher a
// preview they could never publish.
func assertAccepted(t *testing.T, built []*forav1.LicenseTerm) {
	t.Helper()
	v := termspreset.Verdict("publisher.example", "/news/a", "Midnight Harbour", built)
	if !v.OK() {
		t.Fatalf("the built terms would be refused: %+v", v.Violations)
	}
}

func TestFromControlsFree(t *testing.T) {
	t.Parallel()
	built, err := termspreset.FromControls(termspreset.Controls{
		PricingModel:       "free",
		PermittedFunctions: []string{"search", "ai-input"},
	}, testDefaults)
	if err != nil {
		t.Fatalf("from controls: %v", err)
	}
	assertAccepted(t, built)
	p := built[0].GetPricing()
	if p.GetModel() != forav1.PricingModel_PRICING_MODEL_FREE {
		t.Fatalf("model = %v, want free", p.GetModel())
	}
	if p.GetRate() != "0" {
		t.Fatalf("rate = %q, want 0", p.GetRate())
	}
	if p.GetCurrency() != "EUR" {
		t.Fatalf("currency = %q", p.GetCurrency())
	}
	if p.GetUnit() != "" {
		t.Fatalf("unit = %q, want none on a free term", p.GetUnit())
	}
}

func TestFromControlsPerUnit(t *testing.T) {
	t.Parallel()
	built, err := termspreset.FromControls(termspreset.Controls{
		PricingModel:       "per_unit",
		Rate:               "0.0500",
		PermittedFunctions: []string{"ai-input"},
	}, testDefaults)
	if err != nil {
		t.Fatalf("from controls: %v", err)
	}
	assertAccepted(t, built)
	p := built[0].GetPricing()
	if p.GetModel() != forav1.PricingModel_PRICING_MODEL_PER_UNIT {
		t.Fatalf("model = %v, want per unit", p.GetModel())
	}
	// The canonical money form, not the spelling the caller sent.
	if p.GetRate() != "0.05" {
		t.Fatalf("rate = %q, want the canonical 0.05", p.GetRate())
	}
	if p.GetUnit() != "fetches" {
		t.Fatalf("unit = %q, want the deployment's metering basis", p.GetUnit())
	}
	if p.GetUnitCost() != "0.05" {
		t.Fatalf("unit cost = %q, want it to match the rate", p.GetUnitCost())
	}
}

func TestFromControlsAttribution(t *testing.T) {
	t.Parallel()
	built, err := termspreset.FromControls(termspreset.Controls{
		PricingModel:        "free",
		AttributionRequired: true,
	}, testDefaults)
	if err != nil {
		t.Fatalf("from controls: %v", err)
	}
	assertAccepted(t, built)
	obligations := built[0].GetObligations()
	if len(obligations) != 1 {
		t.Fatalf("obligations = %+v, want one", obligations)
	}
	if obligations[0].GetKind() != forav1.ObligationKind_OBLIGATION_KIND_ATTRIBUTION {
		t.Fatalf("obligation kind = %v", obligations[0].GetKind())
	}
	if obligations[0].GetTrigger() != forav1.ObligationTrigger_OBLIGATION_TRIGGER_ON_USE {
		t.Fatalf("obligation trigger = %v, want on use", obligations[0].GetTrigger())
	}
}

func TestFromControlsProhibitions(t *testing.T) {
	t.Parallel()
	built, err := termspreset.FromControls(termspreset.Controls{
		PricingModel:        "free",
		PermittedFunctions:  []string{"search"},
		ProhibitedFunctions: []string{"ai-train"},
	}, testDefaults)
	if err != nil {
		t.Fatalf("from controls: %v", err)
	}
	assertAccepted(t, built)
	r := built[0].GetRestrictions()
	if len(r) != 1 {
		t.Fatalf("restrictions = %+v, want one function axis", r)
	}
	if len(r[0].GetPermitted()) != 1 || r[0].GetPermitted()[0] != "search" {
		t.Fatalf("permitted = %v", r[0].GetPermitted())
	}
	if len(r[0].GetProhibited()) != 1 || r[0].GetProhibited()[0] != "ai-train" {
		t.Fatalf("prohibited = %v", r[0].GetProhibited())
	}
}

// The page's controls are a closed set, so a bad value is named as a caller
// fault rather than left for the Exchange to refuse with a finding about a
// field the visitor never saw.
func TestFromControlsRejections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		controls termspreset.Controls
		wantIn   string
	}{
		{
			"unknown pricing model",
			termspreset.Controls{PricingModel: "auction"},
			"auction",
		},
		{
			"per unit with no rate",
			termspreset.Controls{PricingModel: "per_unit"},
			"rate",
		},
		{
			"per unit with a rate that is not a number",
			termspreset.Controls{PricingModel: "per_unit", Rate: "five cents"},
			"decimal",
		},
		{
			"per unit priced at zero",
			termspreset.Controls{PricingModel: "per_unit", Rate: "0.00"},
			"free",
		},
		{
			"free with a charge",
			termspreset.Controls{PricingModel: "free", Rate: "0.05"},
			"free",
		},
		{
			"a use the page does not offer",
			termspreset.Controls{PricingModel: "free", PermittedFunctions: []string{"manufacture"}},
			"manufacture",
		},
		{
			"a use that is both permitted and prohibited",
			termspreset.Controls{
				PricingModel:        "free",
				PermittedFunctions:  []string{"search"},
				ProhibitedFunctions: []string{"search"},
			},
			"search",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := termspreset.FromControls(tc.controls, testDefaults)
			if err == nil {
				t.Fatal("the control set was accepted")
			}
			if got := publicerr.KindOf(err); got != publicerr.KindInvalidRequest {
				t.Fatalf("kind = %v, want an invalid request", got)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("error %q does not name %q, so the page cannot say which control is wrong",
					err.Error(), tc.wantIn)
			}
		})
	}
}

// An omitted model is free: a visitor who has touched nothing gets the term a
// publisher is least surprised by.
func TestFromControlsEmptyModelIsFree(t *testing.T) {
	t.Parallel()
	built, err := termspreset.FromControls(termspreset.Controls{}, testDefaults)
	if err != nil {
		t.Fatalf("from controls: %v", err)
	}
	assertAccepted(t, built)
	if built[0].GetPricing().GetModel() != forav1.PricingModel_PRICING_MODEL_FREE {
		t.Fatal("an empty control set did not produce a free term")
	}
	if len(built[0].GetRestrictions()) != 0 {
		t.Fatalf("restrictions = %+v, want none when no use was chosen", built[0].GetRestrictions())
	}
}

// A use named twice is meant once.
func TestFromControlsDropsRepeatedUses(t *testing.T) {
	t.Parallel()
	built, err := termspreset.FromControls(termspreset.Controls{
		PricingModel:       "free",
		PermittedFunctions: []string{"search", "SEARCH", " search "},
	}, testDefaults)
	if err != nil {
		t.Fatalf("from controls: %v", err)
	}
	assertAccepted(t, built)
	if got := built[0].GetRestrictions()[0].GetPermitted(); len(got) != 1 {
		t.Fatalf("permitted = %v, want the one use", got)
	}
}

// A token written as one of its accepted aliases lands on the registered form.
func TestFromControlsCanonicalisesAliases(t *testing.T) {
	t.Parallel()
	built, err := termspreset.FromControls(termspreset.Controls{
		PricingModel:       "free",
		PermittedFunctions: []string{"scrape"},
	}, testDefaults)
	if err != nil {
		t.Fatalf("from controls: %v", err)
	}
	if got := built[0].GetRestrictions()[0].GetPermitted()[0]; got != "crawl" {
		t.Fatalf("permitted = %q, want the registered token", got)
	}
}

func TestDefaultFree(t *testing.T) {
	t.Parallel()
	built := termspreset.DefaultFree(testDefaults)
	assertAccepted(t, built)
	p := built[0].GetPricing()
	if p.GetModel() != forav1.PricingModel_PRICING_MODEL_FREE || p.GetRate() != "0" {
		t.Fatalf("pricing = %+v, want free at zero", p)
	}
	r := built[0].GetRestrictions()[0]
	if len(r.GetPermitted()) != 2 {
		t.Fatalf("permitted = %v, want search and AI input", r.GetPermitted())
	}
	if len(r.GetProhibited()) != 1 || r.GetProhibited()[0] != "ai-train" {
		t.Fatalf("prohibited = %v, want training prohibited", r.GetProhibited())
	}
}

// A deployment that states neither currency nor unit still produces terms the
// Exchange accepts.
func TestDefaultsFillIn(t *testing.T) {
	t.Parallel()
	built, err := termspreset.FromControls(termspreset.Controls{PricingModel: "per_unit", Rate: "1"}, termspreset.Defaults{})
	if err != nil {
		t.Fatalf("from controls: %v", err)
	}
	assertAccepted(t, built)
	p := built[0].GetPricing()
	if p.GetCurrency() != "EUR" || p.GetUnit() != "fetches" {
		t.Fatalf("pricing = %+v, want the fallback currency and unit", p)
	}
}

// The page renders its controls from what the service returns, so the two
// cannot drift apart.
func TestVocabularyDescribesTheControls(t *testing.T) {
	t.Parallel()
	v := testDefaults.Vocab()
	if len(v.PricingModels) != 2 {
		t.Fatalf("pricing models = %v", v.PricingModels)
	}
	for _, token := range v.PermittedFunctions {
		if _, err := termspreset.FromControls(termspreset.Controls{
			PricingModel:       "free",
			PermittedFunctions: []string{token},
		}, testDefaults); err != nil {
			t.Fatalf("the vocabulary offers %q but a control set naming it is refused: %v", token, err)
		}
	}
	for _, model := range v.PricingModels {
		c := termspreset.Controls{PricingModel: model}
		if model == "per_unit" {
			c.Rate = "0.01"
		}
		if _, err := termspreset.FromControls(c, testDefaults); err != nil {
			t.Fatalf("the vocabulary offers model %q but a control set naming it is refused: %v", model, err)
		}
	}
}

// The page shows a publisher what an agent receives, so the rendering has to be
// the wire's: snake_case names, enums as names, and unset fields left out.
func TestRenderProtoJSONMatchesTheWire(t *testing.T) {
	t.Parallel()
	offers, err := termspreset.BuildOffers(termspreset.OfferInput{
		Exchange:     "exchange.example",
		CanonicalURL: "https://publisher.example/a",
		Terms:        termspreset.DefaultFree(testDefaults),
	})
	if err != nil {
		t.Fatalf("build offers: %v", err)
	}
	raw, err := termspreset.RenderProtoJSON(offers[0])
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("the rendering is not JSON: %v", err)
	}
	if _, ok := decoded["delivery_method"]; !ok {
		t.Fatalf("rendering uses camel case field names: %s", raw)
	}
	if decoded["delivery_method"] != "DELIVERY_METHOD_INSTRUCTIONS" {
		t.Fatalf("enum rendered as %v, want its name", decoded["delivery_method"])
	}
	if _, ok := decoded["offer_id"]; ok {
		t.Fatalf("an unset field was rendered: %s", raw)
	}
}

// The controls are folded by the same rule the Exchange folds a pushed entry
// with, which trims ASCII whitespace and nothing else.
//
// A wider trim would be worse than a narrower one here. Folding in Unicode
// makes this service accept spellings the Exchange refuses, so a publisher
// would be shown a clean preview for a control set their own feed could not
// carry — and, in the other direction, several non-ASCII code points lower-case
// into ASCII letters, which is how a look-alike of a registered token gets
// admitted as the token itself.
func TestFromControlsFoldsTokensTheWayTheExchangeDoes(t *testing.T) {
	t.Parallel()
	for name, token := range map[string]string{
		"no-break space": " search",
		"en quad":        " search",
		"ideographic":    "　search",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := termspreset.FromControls(termspreset.Controls{
				PricingModel:       "free",
				PermittedFunctions: []string{token},
			}, testDefaults)
			if err == nil {
				t.Fatalf("%q was accepted as a use this page offers; "+
					"the Exchange would not read it as one", token)
			}
		})
	}
}

// The entry is canonicalised before it is checked, as the Exchange canonicalises
// a pushed entry before validating it.
//
// The difference is observable: a restriction token carrying stray whitespace
// fails the wire pattern as it stands, and the fold removes the whitespace. The
// Exchange folds first, so it accepts such a term. A preview that checked the
// term as it stands would tell a publisher their terms are refused while the
// same terms pushed to the Exchange would go through — the opposite of the
// answer this preview exists to give.
func TestVerdictCanonicalisesBeforeChecking(t *testing.T) {
	t.Parallel()
	term := &forav1.LicenseTerm{
		Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
		Restrictions: []*forav1.Restriction{{
			Kind:      forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION,
			Permitted: []string{" search "},
		}},
		Pricing: &forav1.Pricing{
			Model:    forav1.PricingModel_PRICING_MODEL_FREE,
			Rate:     "0",
			Currency: "EUR",
		},
	}
	verdict := termspreset.Verdict("publisher.example", "/news/a", "Midnight Harbour",
		[]*forav1.LicenseTerm{term})
	if !verdict.OK() {
		t.Fatalf("the term was refused, but the Exchange folds the token before checking "+
			"it and accepts the same term: %+v", verdict.Violations)
	}
}
