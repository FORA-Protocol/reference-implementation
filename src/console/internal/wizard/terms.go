package wizard

import (
	"slices"
	"strings"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

// defaultTerms is what a publisher who has chosen nothing yet is shown: the
// shared default, free, with search and AI answers permitted and training
// prohibited outright. The licensing step opens on it, unless the domain's own
// licensing document says otherwise.
//
// It is the preset the public preview shows, so a publisher who previewed
// their site there and then signs up here starts from the same terms.
func defaultTerms() termspreset.Controls {
	return termspreset.DefaultFreeControls()
}

// The uses a publisher chooses between, in the order the page lists them. The
// vocabulary is the shared one; this is which of it the console offers and how
// each is described to someone who is not going to read the protocol.
const (
	functionSearch  = "search"
	functionAIInput = "ai-input"
	functionAITrain = "ai-train"
)

// functionChoices describes each use in the publisher's own terms.
func functionChoices() []Choice {
	return []Choice{
		{Value: functionSearch, Label: termspreset.FunctionLabel(functionSearch), Note: "listing and linking your pages"},
		{
			Value: functionAIInput, Label: termspreset.FunctionLabel(functionAIInput),
			Note: "answers and retrieval, quoting your pages",
		},
		{Value: functionAITrain, Label: termspreset.FunctionLabel(functionAITrain), Note: "training a model on your pages"},
	}
}

// offerPreview builds what an agent would be shown for one page of the site
// under the given answers, sold by exchange.
//
// It reads a page of the publisher's own site so the preview is their content
// rather than an invented example. A site that cannot be read yet still gets a
// preview, built from the address alone: the point is to show what the terms
// produce, and that does not depend on the page's title.
func offerPreview(
	exchange string, page pagefetch.Page, controls termspreset.Controls, d termspreset.Defaults,
) ([]OfferLine, string) {
	terms, err := termspreset.FromControls(controls, d)
	if err != nil {
		return nil, termsRefusal(err)
	}
	// One offer per licensing term, and the form's controls always make exactly
	// one term, so the preview is the one offer.
	offers, err := termspreset.BuildOffers(termspreset.OfferInput{
		Exchange:     exchange,
		CanonicalURL: page.CanonicalURL,
		Title:        page.Title,
		Terms:        terms,
	})
	if err != nil || len(offers) != 1 {
		return nil, "The offer could not be built from these terms."
	}
	return offerLines(offers[0], exchange), ""
}

// OfferLine is one label-and-value row of the offer preview.
//
// The offer is rendered as rows rather than as the JSON an agent receives,
// because the reader is a publisher deciding what to charge, not an engineer
// debugging a payload. Indent draws the nesting without asking the template to
// know the shape.
type OfferLine struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Indent bool   `json:"indent"`
}

// offerLines renders the offer for a person. Each term is said the way the
// console says a term everywhere, from the offer's own terms.
func offerLines(offer *forav1.Offer, exchange string) []OfferLine {
	title := offer.GetTitle()
	if title == "" {
		// A site whose page carries no headline: say so rather than draw an
		// empty row, which reads as a value that failed to load.
		title = "(your page's headline)"
	}
	terms := offer.GetTerms()
	lines := make([]OfferLine, 0, len(terms)+6)
	lines = append(lines,
		OfferLine{Label: "title", Value: title},
		OfferLine{Label: "sold by", Value: exchange},
		OfferLine{Label: "price", Value: termspreset.PriceText(offer.GetPricing())},
		OfferLine{Label: "terms", Value: ""},
	)
	for _, term := range terms {
		lines = append(lines, OfferLine{Label: "term", Value: termspreset.DescribeTerm(term), Indent: true})
	}
	return append(lines,
		OfferLine{Label: "identity", Value: ""},
		OfferLine{Label: "canonical url", Value: offer.GetIdentity().GetCanonicalUrl(), Indent: true},
	)
}

func containsFold(list []string, want string) bool {
	return slices.ContainsFunc(list, func(v string) bool {
		return strings.EqualFold(strings.TrimSpace(v), want)
	})
}
