// The page that adds a domain. It is one page: the publisher's answers stay in
// their browser and nothing about the domain is saved until they press one of
// the last step's buttons -- Finish setup, or Skip for now, which saves the
// domain without terms.
//
// Saving holds the hostname for this publisher before anything proves it is
// theirs, so the domain is kept even when its setup is not finished. It is
// verified -- and only then is anything on it sold -- once the Exchange has read
// the discovery document the site serves and the console has seen the package
// answer; until then it is listed as not verified. Somebody who saves a domain
// that is not theirs can never verify it, and holds the name until an operator
// releases it.

package web

import (
	"encoding/json"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

// wizardStep is one item of the progress header: a number the browser derives
// from the position, and the title under it.
type wizardStep struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// wizardPaths are the addresses the page calls, named once here so the script
// carries no literal that could drift from the routing table.
type wizardPaths struct {
	Publisher     string `json:"publisher"`
	Lookup        string `json:"lookup"`
	Files         string `json:"files"`
	CheckManifest string `json:"check_manifest"`
	CheckEdge     string `json:"check_edge"`
	Page          string `json:"page"`
	TermsPreview  string `json:"terms_preview"`
	Register      string `json:"register"`
	Progress      string `json:"progress"`
	Home          string `json:"home"`
	Domain        string `json:"domain_prefix"`
}

// paths is every address the wizard calls.
func paths() wizardPaths {
	return wizardPaths{
		Publisher:     APIPublisherPath,
		Lookup:        APILookupPath,
		Files:         APIFilesPath,
		CheckManifest: APICheckManifestPath,
		CheckEdge:     APICheckEdgePath,
		Page:          APIPagePath,
		TermsPreview:  APITermsPreviewPath,
		Register:      APIRegisterPath,
		Progress:      APIProgressPath,
		Home:          DomainsPath,
		Domain:        DomainPrefix,
	}
}

// wizardBoot is everything the page needs before it makes its first call.
//
// It is rendered into the page rather than fetched, because every value in it
// is already known when the page is built and a first call to learn them would
// show the publisher an empty wizard while it ran.
type wizardBoot struct {
	Registered bool `json:"registered"`
	// HasDomains is set for a publisher who already has a domain. The page
	// then offers Cancel after the domain step: they are adding one more, not
	// onboarding, and the list they came from is there to go back to. Every
	// publisher can leave from the domain step itself, with Skip for now,
	// which saves nothing.
	HasDomains  bool   `json:"has_domains"`
	DisplayName string `json:"display_name"`
	// SupportEmail is who a publisher writes to when their CDN is not one they
	// can set up here.
	SupportEmail string `json:"support_email,omitempty"`
	Currency     string `json:"currency"`
	// DefaultRate is what the price field is filled in with when a publisher
	// chooses to charge. It is the deployment's, because what an access is
	// worth is a decision about this market rather than about this publisher.
	DefaultRate string          `json:"default_rate"`
	Unit        string          `json:"unit"`
	Terms       controlsJSON    `json:"terms"`
	Functions   []wizard.Choice `json:"functions"`
	Steps       []wizardStep    `json:"steps"`
	Paths       wizardPaths     `json:"paths"`
	// Resume is set when the wizard reopens on a domain already saved, to
	// finish its setup. It carries the domain's saved answers.
	Resume *resumeBoot `json:"resume,omitempty"`
}

// bootOf is what the wizard opens on for this person, walking steps.
func bootOf(start wizard.Start, steps []wizardStep) wizardBoot {
	return wizardBoot{
		Registered:   start.Registered,
		HasDomains:   start.HasDomains,
		DisplayName:  start.DisplayName,
		SupportEmail: start.SupportEmail,
		Currency:     start.Currency,
		DefaultRate:  start.DefaultRate,
		Unit:         start.Unit,
		Terms:        controlsJSONOf(start.Terms),
		Functions:    start.Functions,
		Steps:        steps,
		Paths:        paths(),
	}
}

// NewDomain draws the wizard.
//
// What it reads from the Exchange is what this person has registered. Whether
// they have a business name decides whether the first step exists, and
// therefore how many steps the progress header has -- which is why the list of
// steps is built here rather than counted in the browser, where a publisher who
// already has a name would see the screens numbered from two. Whether they
// have a domain decides whether the page offers to cancel.
func (h *Handler) NewDomain(w http.ResponseWriter, r *http.Request) {
	start, err := h.wizard.Start(r.Context(), actorOf(r.Context()))
	if err != nil {
		renderFailure(h.render, w, r, err, "")
		return
	}
	h.renderWizard(w, r, "Add a domain", bootOf(start, wizardSteps(start.Registered)))
}

// renderWizard draws the wizard's page over boot.
func (h *Handler) renderWizard(w http.ResponseWriter, r *http.Request, title string, boot wizardBoot) {
	raw, err := json.Marshal(boot)
	if err != nil {
		reqctx.FromContext(r.Context()).ErrorContext(r.Context(), "console.wizard_boot_failed",
			"err", err)
		h.render.RenderMessage(w, r, http.StatusInternalServerError,
			"This page could not be built",
			"Something went wrong on our side. Nothing was changed.",
			DomainsPath, "Back to your domains")
		return
	}
	// Carried as an ordinary attribute value, not as script content. It holds a
	// publisher's own business name, so it is a string this service does not
	// choose -- and html/template escapes an attribute like any other string,
	// where anything written into a <script> element would have to be handed
	// over as already-safe and escape nothing. The browser reads it back
	// through dataset, which undoes the escaping for us.
	h.render.Render(w, r, "newdomain", Page{Title: title, Boot: string(raw)})
}

// wizardSteps is the list the progress header draws and the wizard walks. The
// business name is left out for a publisher who has one already: no route
// changes it, so showing it again would be a field that cannot do anything.
func wizardSteps(registered bool) []wizardStep {
	steps := make([]wizardStep, 0, 5)
	if !registered {
		steps = append(steps, wizardStep{Key: wizard.StepName, Title: "Your business"})
	}
	return append(
		steps,
		wizardStep{Key: wizard.StepDomain, Title: "Your domain"},
		wizardStep{Key: wizard.StepDocument, Title: "Publish a file"},
		wizardStep{Key: wizard.StepPackage, Title: "Install the package"},
		wizardStep{Key: wizard.StepTerms, Title: "Set your terms"},
	)
}
