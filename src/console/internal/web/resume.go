// The page that reopens the wizard on a domain already saved, to finish its
// setup at the first step that is not done.

package web

import (
	"net/http"
	"net/url"
	"strings"
)

// DomainSetupPattern is the page that finishes one saved domain's setup. It
// sits under the domain's own page, because it is about the same domain and
// answers from the same lookup: the registration, read for the person signed
// in.
const DomainSetupPattern = DomainPrefix + "{domain}/setup"

// domainSetupURL is where one saved domain's setup is finished.
func domainSetupURL(domain string) string {
	return DomainPrefix + url.PathEscape(domain) + "/setup"
}

// domainPageURL is one registered domain's page.
func domainPageURL(domain string) string {
	return DomainPrefix + url.PathEscape(domain)
}

// resumeBoot is a saved domain's answers, which the wizard reopens with
// instead of asking for them again.
type resumeBoot struct {
	Domain       string `json:"domain"`
	CDN          string `json:"cdn"`
	NetworkLabel string `json:"network_label"`
	// Paths is the licensed paths as the domain step's field holds them.
	Paths string `json:"content_paths"`
	// TermsSet is false for a domain saved before its terms were chosen; the
	// licensing step then opens on the terms the wizard always opens on.
	TermsSet                  bool `json:"terms_set"`
	ImportReplacesManualTerms bool `json:"import_replaces_manual_terms"`
}

// FinishSetup reopens the wizard on one of the person's saved domains. Where
// it opens is decided after the page loads, by asking the site what it answers
// now (see WizardAPI.Progress); this page only carries the saved answers.
//
// A domain that is not this person's answers not-found, the answer a domain
// nobody holds gets. A verified domain has nothing to finish, so the page sends
// the publisher to it.
func (h *Handler) FinishSetup(w http.ResponseWriter, r *http.Request) {
	resume, err := h.resumer.Reopen(r.Context(), actorOf(r.Context()), r.PathValue("domain"))
	if err != nil {
		renderFailure(h.render, w, r, err, "")
		return
	}
	site := resume.Site
	if site.Active() {
		http.Redirect(w, r, domainPageURL(site.Domain), http.StatusSeeOther)
		return
	}
	boot := bootOf(resume.Start, wizardSteps(true))
	boot.Resume = &resumeBoot{
		Domain:                    site.Domain,
		CDN:                       site.CDN,
		NetworkLabel:              resume.NetworkLabel,
		Paths:                     strings.Join(site.ContentPathScope, ", "),
		TermsSet:                  site.Terms != nil,
		ImportReplacesManualTerms: site.ImportReplacesManualTerms,
	}
	h.renderWizard(w, r, "Finish setting up "+site.Domain, boot)
}
