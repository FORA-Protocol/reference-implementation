// The wizard's licensing step: what the publisher's own site already says about
// its terms, and what an agent would be shown under the answers on screen.

package web

import (
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

// controlsJSON is the licensing answers as the wizard sends them. It is a
// separate type from termspreset.Controls so the field names on the wire are
// this console's decision rather than a consequence of the shared struct's tags.
//
// ProhibitedFunctions has no control of its own. It is what the defaults or the
// site's own licensing document prohibit outright, carried through the wizard
// unchanged so the prohibition reaches the registration rather than turning
// into a use that is merely not permitted.
type controlsJSON struct {
	PricingModel        string   `json:"pricing_model"`
	Rate                string   `json:"rate"`
	PermittedFunctions  []string `json:"permitted_functions"`
	ProhibitedFunctions []string `json:"prohibited_functions"`
	AttributionRequired bool     `json:"attribution_required"`
}

// controls is the answers as the wizard service takes them. It is a plain
// mapping; settling what the answers amount to is the service's.
func (c controlsJSON) controls() termspreset.Controls {
	return termspreset.Controls{
		PricingModel:        c.PricingModel,
		Rate:                c.Rate,
		PermittedFunctions:  c.PermittedFunctions,
		ProhibitedFunctions: c.ProhibitedFunctions,
		AttributionRequired: c.AttributionRequired,
	}
}

// controlsOrNil is c's controls, or nil when no terms were sent.
func controlsOrNil(c *controlsJSON) *termspreset.Controls {
	if c == nil {
		return nil
	}
	controls := c.controls()
	return &controls
}

func controlsJSONOf(c termspreset.Controls) controlsJSON {
	return controlsJSON{
		PricingModel:        c.PricingModel,
		Rate:                c.Rate,
		PermittedFunctions:  c.PermittedFunctions,
		ProhibitedFunctions: c.ProhibitedFunctions,
		AttributionRequired: c.AttributionRequired,
	}
}

// Page reads the publisher's site once, when the licensing step opens.
func (a *WizardAPI) Page(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	read, err := a.wizard.ReadPage(r.Context(), req.Domain)
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	var terms *controlsJSON
	if read.Terms != nil {
		t := controlsJSONOf(*read.Terms)
		terms = &t
	}
	writeAPI(w, r, struct {
		PageTitle string        `json:"page_title"`
		PageURL   string        `json:"page_url"`
		Sitemap   string        `json:"sitemap_url"`
		Terms     *controlsJSON `json:"terms"`
		TermsNote string        `json:"terms_note,omitempty"`
	}{
		PageTitle: read.Page.Title,
		PageURL:   read.Page.CanonicalURL,
		Sitemap:   read.Sitemap,
		Terms:     terms,
		TermsNote: read.Note,
	})
}

// TermsPreview answers what an agent would be shown for one page of the domain
// under the answers currently on screen.
func (a *WizardAPI) TermsPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PageTitle string       `json:"page_title"`
		PageURL   string       `json:"page_url"`
		Terms     controlsJSON `json:"terms"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	page := pagefetch.Page{Title: req.PageTitle, CanonicalURL: req.PageURL}
	preview, err := a.wizard.PreviewTerms(r.Context(), page, req.Terms.controls())
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	writeAPI(w, r, struct {
		Lines   []wizard.OfferLine `json:"lines"`
		Warning string             `json:"warning,omitempty"`
		Summary string             `json:"summary"`
	}{Lines: preview.Lines, Warning: preview.Warning, Summary: preview.Summary})
}
