// Package termspreset turns the pricing and permission controls a publisher sets
// into FORA license terms, and those terms into the offer an agent would
// receive.
//
// The offer it builds is unsigned and priced from the terms in front of it. It
// is a preview: nothing about it is a commitment, no Exchange has issued it,
// and it cannot be executed. What it is for is showing a publisher exactly what
// an agent sees and is charged -- on the public page before they register, and
// in the console afterwards, where the same preset seeds the sample feed their
// site is set up with.
package termspreset

import (
	"context"
	"strings"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/gen/go/vocab/functiontokens"
	"github.com/FORA-Protocol/protocol/gen/go/vocab/pricingunits"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/publicerr"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/rsl"
)

// Pricing models the page offers. They are a subset of the protocol's: a flat
// one-time fee is a real model, but it is not a control the page carries, and
// accepting a value the page cannot produce would be a surface with no caller.
const (
	// ModelFree is no charge.
	ModelFree = "free"
	// ModelPerUnit charges per access.
	ModelPerUnit = "per_unit"
)

// zeroRate is what a free term states. The protocol requires a free term to
// carry a zero rate rather than omit one, so that free is an assertion.
const zeroRate = "0"

// Controls are the page's settings, as the request carries them.
type Controls struct {
	PricingModel        string   `json:"pricing_model"`
	Rate                string   `json:"rate,omitempty"`
	PermittedFunctions  []string `json:"permitted_functions,omitempty"`
	ProhibitedFunctions []string `json:"prohibited_functions,omitempty"`
	AttributionRequired bool     `json:"attribution_required,omitempty"`
}

// Defaults are the deployment's settings for values the controls do not carry.
type Defaults struct {
	// Currency prices every term this package builds.
	Currency string
	// Unit is the metering basis of a per-unit term.
	Unit string
}

// Vocabulary is the closed set of values the page may send, returned with every
// preview so the page renders its controls from the service rather than from a
// list copied into the site and left to drift.
type Vocabulary struct {
	PricingModels      []string `json:"pricing_models"`
	PermittedFunctions []string `json:"permitted_functions"`
	Currency           string   `json:"currency"`
	Unit               string   `json:"unit"`
}

// offerableFunctions are the uses the page lets a publisher decide about. The
// protocol registers many more; these are the ones an AI-access decision turns
// on, and a publisher choosing among five checkboxes is making a decision they
// understand.
var offerableFunctions = []string{
	functiontokens.Search,
	functiontokens.AiInput,
	functiontokens.AiIndex,
	functiontokens.AiTrain,
	functiontokens.Crawl,
}

// Vocab describes the controls this deployment accepts.
func (d Defaults) Vocab() Vocabulary {
	return Vocabulary{
		PricingModels:      []string{ModelFree, ModelPerUnit},
		PermittedFunctions: offerableFunctions,
		Currency:           d.Currency,
		Unit:               d.unit(),
	}
}

// Source reports the denomination terms are priced in. The console's source is
// the Exchange, which owns the denomination of everything it registers; a
// fixed Defaults value is a Source too, for a caller that is handed one.
type Source interface {
	TermsDefaults(ctx context.Context) (Defaults, error)
}

// TermsDefaults makes a fixed denomination a Source.
func (d Defaults) TermsDefaults(context.Context) (Defaults, error) { return d, nil }

// Resolved returns the denomination every term built from d is priced in, with
// the fallbacks filled in. It is what a deployment reports to a party that
// prices terms on its behalf, so the two cannot disagree about a default.
func (d Defaults) Resolved() Defaults {
	return Defaults{Currency: d.currency(), Unit: d.unit()}
}

func (d Defaults) unit() string {
	if d.Unit == "" {
		return pricingunits.Fetches
	}
	return d.Unit
}

func (d Defaults) currency() string {
	if d.Currency == "" {
		return "EUR"
	}
	return d.Currency
}

// FromControls builds the term the page's settings describe.
//
// Every value is checked here rather than left for the Exchange to refuse,
// because the page's controls are a closed set: a pricing model outside it or a
// use the vocabulary has no token for is a caller fault with a name, and saying
// which control is wrong is more use than a validation finding about a field
// the visitor never saw.
func FromControls(c Controls, d Defaults) ([]*forav1.LicenseTerm, error) {
	pricing, err := pricingFrom(c, d)
	if err != nil {
		return nil, err
	}
	permitted, err := tokensFrom(c.PermittedFunctions, "permitted_functions")
	if err != nil {
		return nil, err
	}
	prohibited, err := tokensFrom(c.ProhibitedFunctions, "prohibited_functions")
	if err != nil {
		return nil, err
	}
	if clash := firstCommon(permitted, prohibited); clash != "" {
		return nil, publicerr.Newf(publicerr.KindInvalidRequest,
			"%q is both permitted and prohibited; a use can be one or the other", clash)
	}
	term := &forav1.LicenseTerm{
		Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
		Pricing:   pricing,
	}
	if len(permitted) > 0 || len(prohibited) > 0 {
		term.Restrictions = []*forav1.Restriction{{
			Kind:       forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION,
			Permitted:  permitted,
			Prohibited: prohibited,
		}}
	}
	if c.AttributionRequired {
		term.Obligations = []*forav1.Obligation{{
			Kind:    forav1.ObligationKind_OBLIGATION_KIND_ATTRIBUTION,
			Trigger: forav1.ObligationTrigger_OBLIGATION_TRIGGER_ON_USE,
		}}
	}
	return []*forav1.LicenseTerm{term}, nil
}

// RSL is what reading a site's own licensing document takes from the
// deployment. The currency is the one every term here is priced in, so an
// amount the document leaves undenominated is read in the same currency the
// controls would have priced it in.
func (d Defaults) RSL(domain string) rsl.Defaults {
	return rsl.Defaults{Currency: d.currency(), Domain: domain}
}

// DefaultFreeControls are the settings a publisher who has chosen nothing yet
// starts from: free, open for search and as AI input, with training
// prohibited. It is the shape the demo catalog uses, and it is deliberately
// the least surprising thing to show a publisher who has chosen nothing yet.
func DefaultFreeControls() Controls {
	return Controls{
		PricingModel:        ModelFree,
		PermittedFunctions:  []string{functiontokens.Search, functiontokens.AiInput},
		ProhibitedFunctions: []string{functiontokens.AiTrain},
	}
}

// DefaultFree is the term a preview shows when neither the page nor the site
// said anything about price: DefaultFreeControls, built.
//
// The controls are this package's own constants and every token in them is
// offerable, so the builder cannot refuse them. Should that ever change, the
// package's tests fail on this function before any caller sees it.
func DefaultFree(d Defaults) []*forav1.LicenseTerm {
	terms, err := FromControls(DefaultFreeControls(), d)
	if err != nil {
		panic("termspreset: the default controls do not build: " + err.Error())
	}
	return terms
}

// pricingFrom builds the pricing half of a control set.
func pricingFrom(c Controls, d Defaults) (*forav1.Pricing, error) {
	switch strings.ToLower(strings.TrimSpace(c.PricingModel)) {
	case ModelFree, "":
		if rate := strings.TrimSpace(c.Rate); rate != "" && rate != zeroRate {
			return nil, publicerr.Newf(publicerr.KindInvalidRequest,
				"a free term carries no charge, so rate %q cannot be part of it", c.Rate)
		}
		return &forav1.Pricing{
			Model:    forav1.PricingModel_PRICING_MODEL_FREE,
			Rate:     zeroRate,
			Currency: d.currency(),
		}, nil
	case ModelPerUnit:
		rate, err := rateFrom(c.Rate)
		if err != nil {
			return nil, err
		}
		unit := d.unit()
		return &forav1.Pricing{
			Model:    forav1.PricingModel_PRICING_MODEL_PER_UNIT,
			Rate:     rate,
			UnitCost: &rate,
			Unit:     &unit,
			Currency: d.currency(),
		}, nil
	default:
		return nil, publicerr.Newf(publicerr.KindInvalidRequest,
			"pricing_model %q is not one this page offers; use %q or %q",
			c.PricingModel, ModelFree, ModelPerUnit)
	}
}

// rateFrom reads a price into the protocol's canonical money form. Money is a
// decimal string on every FORA surface and never a float, so a value that is
// not a decimal number is refused rather than rounded into one.
func rateFrom(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", publicerr.Newf(publicerr.KindInvalidRequest,
			"a per-access term needs a price; set rate")
	}
	canonical, err := helpers.CanonicalizeMoney(trimmed)
	if err != nil {
		return "", publicerr.Newf(publicerr.KindInvalidRequest,
			"rate %q is not a decimal amount", raw)
	}
	if canonical == zeroRate {
		return "", publicerr.Newf(publicerr.KindInvalidRequest,
			"a per-access term priced at zero is a free term; choose %q instead", ModelFree)
	}
	return canonical, nil
}

// tokensFrom checks the uses a control names against the closed set the page
// offers, and returns them in the order given, without repeats.
//
// The fold is the SDK's, which is the one the Exchange applies to a pushed
// entry. It folds ASCII case only, on purpose: several non-ASCII code points
// lower-case into ASCII letters, so folding in Unicode would let a homograph of
// "ai-train" arrive here as the registered token.
func tokensFrom(raw []string, field string) ([]string, error) {
	var out []string
	for _, token := range raw {
		folded := helpers.CanonicalRestrictionToken(
			forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION, token)
		if !isOfferable(folded) {
			return nil, publicerr.Newf(publicerr.KindInvalidRequest,
				"%s contains %q, which is not a use this page offers", field, token)
		}
		if !contains(out, folded) {
			out = append(out, folded)
		}
	}
	return out, nil
}

func isOfferable(token string) bool {
	return contains(offerableFunctions, token)
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// firstCommon returns the first token named on both lists, or empty when they
// are disjoint.
func firstCommon(a, b []string) string {
	for _, item := range a {
		if contains(b, item) {
			return item
		}
	}
	return ""
}
