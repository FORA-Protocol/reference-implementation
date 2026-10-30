// The page that adds a domain. It is one page of steps, and each step's Next
// saves what it asked: the business name, on the step an account that has not
// named its business opens on, then the domain, then its terms. The last step
// checks the publisher's own site for the discovery file and the package, and
// the domain is verified once both are in place. Nothing is kept in the
// browser, so a publisher who leaves after any step loses nothing: the domain
// is listed as not verified, and its setup reopens where it stands.
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
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
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
	Publisher    string `json:"publisher"`
	Lookup       string `json:"lookup"`
	Register     string `json:"register"`
	Terms        string `json:"terms"`
	Files        string `json:"files"`
	Check        string `json:"check"`
	Page         string `json:"page"`
	TermsPreview string `json:"terms_preview"`
	Home         string `json:"home"`
	Domain       string `json:"domain_prefix"`
}

// paths is every address the wizard calls.
func paths() wizardPaths {
	return wizardPaths{
		Publisher:    APIPublisherPath,
		Lookup:       APILookupPath,
		Register:     APIRegisterPath,
		Terms:        APITermsPath,
		Files:        APIFilesPath,
		Check:        APICheckPath,
		Page:         APIPagePath,
		TermsPreview: APITermsPreviewPath,
		Home:         DomainsPath,
		Domain:       DomainPrefix,
	}
}

// wizardBoot is everything the page needs before it makes its first call.
//
// It is rendered into the page rather than fetched, because every value in it
// is already known when the page is built and a first call to learn them would
// show the publisher an empty wizard while it ran.
type wizardBoot struct {
	// Registered is false for an account that has not named its business: the
	// wizard then opens on the business step.
	Registered bool `json:"registered"`
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

// bootOf is what the wizard opens on for the account regs describes: the steps
// it walks, the business step only while it has no business.
func bootOf(start wizard.Start, regs exchangeclient.Registrations) wizardBoot {
	return wizardBoot{
		Registered:   regs.Registered,
		SupportEmail: start.SupportEmail,
		Currency:     start.Currency,
		DefaultRate:  start.DefaultRate,
		Unit:         start.Unit,
		Terms:        controlsJSONOf(start.Terms),
		Functions:    start.Functions,
		Steps:        wizardSteps(regs.Registered),
		Paths:        paths(),
	}
}

// NewDomain draws the wizard. An account that has not named its business opens
// it on the business step, with the field empty; one that has walks the steps
// after it.
func (h *Handler) NewDomain(w http.ResponseWriter, r *http.Request) {
	regs, ok := pageRegistrations(w, r)
	if !ok {
		return
	}
	start, err := h.wizard.Start(r.Context())
	if err != nil {
		renderFailure(h.render, w, r, err, "")
		return
	}
	h.renderWizard(w, r, "Add a domain", bootOf(start, regs))
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
	// Carried as an ordinary attribute value, not as script content. On a
	// domain the publisher saved it holds strings this service does not choose
	// -- what they typed: the domain and its licensed paths -- and html/template
	// escapes an attribute like any other string, where anything written into a
	// <script> element would have to be handed over as already-safe and escape
	// nothing. The browser reads it back through dataset, which undoes the
	// escaping for us.
	h.render.Render(w, r, "newdomain", Page{Title: title, Boot: string(raw)})
}

// wizardSteps is the list the progress header draws and the wizard walks. The
// business step is left out for an account that has named its business: no
// route changes the name, so showing it again would be a field that cannot do
// anything. The list is built here rather than in the browser, so the page
// numbers its steps from the one list the server answers in.
func wizardSteps(registered bool) []wizardStep {
	steps := make([]wizardStep, 0, 4)
	if !registered {
		steps = append(steps, wizardStep{Key: wizard.StepName, Title: "Your business"})
	}
	return append(steps,
		wizardStep{Key: wizard.StepDomain, Title: "Your domain"},
		wizardStep{Key: wizard.StepTerms, Title: "Your terms"},
		wizardStep{Key: wizard.StepConnect, Title: "Connect your site"},
	)
}
