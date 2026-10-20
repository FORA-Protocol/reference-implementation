// The licensing answers as the console holds them, and the conversions between
// them and what the Exchange stores.

package wizard

import (
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/publicerr"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// Choice is one option the licensing step offers. Note is the sentence under it
// saying what the option means to a publisher. It travels to the browser, so
// its field names are named here rather than left to the marshaller.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

// normalizeControls settles what the wizard sent into what the publisher is
// asking for.
//
// A free answer drops the rate. The wizard keeps the last price in the browser
// so switching back to charging restores it, and that leftover is not a price
// the publisher is asking for.
//
// A use the publisher ticked is dropped from the prohibited list. The tick is
// the latest thing they said about it, and a use on both lists is one the
// builder refuses.
func normalizeControls(c termspreset.Controls) termspreset.Controls {
	model := strings.TrimSpace(c.PricingModel)
	rate := strings.TrimSpace(c.Rate)
	if model == termspreset.ModelFree {
		rate = ""
	}
	prohibited := make([]string, 0, len(c.ProhibitedFunctions))
	for _, token := range c.ProhibitedFunctions {
		if !containsFold(c.PermittedFunctions, token) {
			prohibited = append(prohibited, token)
		}
	}
	return termspreset.Controls{
		PricingModel:        model,
		Rate:                rate,
		PermittedFunctions:  c.PermittedFunctions,
		ProhibitedFunctions: prohibited,
		AttributionRequired: c.AttributionRequired,
	}
}

// termsRefusal is what a publisher reads when the answers they set cannot be
// turned into terms. It carries the shared package's public sentence, which
// names the control to change, and never the error's own text: that text is for
// the log and names the package's plumbing.
func termsRefusal(err error) string {
	const lead = "These terms cannot be offered as they stand"
	if msg, ok := publicerr.Message(err); ok {
		return lead + ": " + msg
	}
	return lead + "."
}

// termsSummary says one site's licensing in a line, for the page that shows it
// back rather than asking for it: the terms the controls build, said the way
// the console says a term everywhere. Controls that build no terms say so.
func termsSummary(c termspreset.Controls, d termspreset.Defaults) string {
	terms, err := termspreset.FromControls(c, d)
	if err != nil || len(terms) == 0 {
		return "(these terms could not be read)"
	}
	lines := make([]string, 0, len(terms))
	for _, term := range terms {
		lines = append(lines, termspreset.DescribeTerm(term))
	}
	return strings.Join(lines, "; ")
}

// controlsOf renders the licensing the Exchange reported as the shared control
// set the artifact builders take.
func controlsOf(t exchangeclient.Terms) termspreset.Controls {
	return termspreset.Controls{
		PricingModel:        t.PricingModel,
		Rate:                t.Rate,
		PermittedFunctions:  t.PermittedFunctions,
		ProhibitedFunctions: t.ProhibitedFunctions,
		AttributionRequired: t.AttributionRequired,
	}
}

// controlsOrDefault is controlsOf for a site whose terms may not be chosen yet.
// Such a site's files are built with the terms the licensing step opens on,
// the way a domain's files are built mid-wizard, rather than with an empty set
// of controls, which would build a term that permits everything.
func controlsOrDefault(t *exchangeclient.Terms) termspreset.Controls {
	if t == nil {
		return defaultTerms()
	}
	return controlsOf(*t)
}

// termsOfControls is the reverse: the licensing a registration sends.
func termsOfControls(c termspreset.Controls) exchangeclient.Terms {
	return exchangeclient.Terms{
		PricingModel:        c.PricingModel,
		Rate:                c.Rate,
		PermittedFunctions:  c.PermittedFunctions,
		ProhibitedFunctions: c.ProhibitedFunctions,
		AttributionRequired: c.AttributionRequired,
	}
}
