// The wizard's first three steps -- the business name, the domain and what
// serves it, and the files the publisher deploys -- and the save both of its
// last step's buttons send. The checks that gate each step are in
// api_checks.go and the licensing step is in api_terms.go.

package web

import (
	"net/http"
	"net/url"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/setupsteps"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

// Publisher registers the business name.
func (a *WizardAPI) Publisher(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	p, err := a.wizard.RegisterPublisher(r.Context(), actorOf(r.Context()), req.DisplayName)
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	writeAPI(w, r, struct {
		PublisherID string `json:"publisher_id"`
		DisplayName string `json:"display_name"`
	}{PublisherID: p.PublisherID, DisplayName: p.DisplayName})
}

// lookupBody is wizard.Lookup on the wire.
type lookupBody struct {
	Domain   string `json:"domain"`
	Provider string `json:"provider"`
	// ProviderLabel is the network's own name. The browser shows what it is
	// given rather than mapping the token again.
	ProviderLabel string `json:"provider_label"`
	Supported     bool   `json:"supported"`
	Taken         bool   `json:"taken"`
	// Yours is set when the domain is already one of this person's. Link is
	// then where to go instead: its page when it is verified, and the page
	// that finishes its setup when it is not.
	Yours        bool      `json:"yours"`
	Verified     bool      `json:"verified"`
	Link         string    `json:"link,omitempty"`
	LimitReached bool      `json:"limit_reached"`
	Message      string    `json:"message,omitempty"`
	Manifest     checkBody `json:"manifest"`
}

// Lookup answers which network serves a domain and whether it could be
// registered.
func (a *WizardAPI) Lookup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	found, err := a.wizard.Lookup(r.Context(), actorOf(r.Context()), req.Domain)
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	writeAPI(w, r, lookupBody{
		Domain:        found.Domain,
		Yours:         found.Yours,
		Verified:      found.Verified,
		Link:          yoursLink(found),
		Provider:      string(found.Provider),
		ProviderLabel: found.ProviderLabel,
		Supported:     found.Supported,
		Taken:         found.Taken,
		LimitReached:  found.LimitReached,
		Message:       found.Message,
		Manifest:      checkBodyOf(found.Manifest),
	})
}

// fileBody is one generated file as the wizard shows it: the text to serve, and
// where to download it from.
type fileBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	URL         string `json:"url"`
}

// draftOf reads a domain's answers from the fields every file route shares.
func draftOf(domain, network, paths string) wizard.Draft {
	return wizard.Draft{Domain: domain, CDN: network, Paths: paths}
}

// Files builds the files for a domain that is not saved yet.
func (a *WizardAPI) Files(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
		CDN    string `json:"cdn"`
		Paths  string `json:"content_paths"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	out, err := a.wizard.Files(r.Context(), actorOf(r.Context()), draftOf(req.Domain, req.CDN, req.Paths))
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	files := make([]fileBody, 0, len(out.Bundle.Files))
	for _, f := range out.Bundle.Files {
		files = append(files, fileBody{
			Name: f.Name, Description: f.Description, Body: string(f.Body),
			URL: wizardFileURL(out.Site, f.Name),
		})
	}
	writeAPI(w, r, struct {
		Files     []fileBody        `json:"files"`
		EdgeSteps []setupsteps.Step `json:"edge_steps"`
		Warnings  []string          `json:"warnings"`
	}{Files: files, EdgeSteps: out.EdgeSteps, Warnings: out.Bundle.Warnings})
}

// wizardFileURL is where one generated file is downloaded from mid-wizard.
//
// The answers travel in the address because there is no row to read them back
// from: a download is a navigation, so it cannot carry the answers in a body
// the way every other call does.
func wizardFileURL(s setup.Site, name string) string {
	q := url.Values{
		"name":          {name},
		"domain":        {s.Domain},
		"cdn":           {s.CDN},
		"content_paths": {strings.Join(s.ContentPathScope, ",")},
	}
	return APIFilePath + "?" + q.Encode()
}

// File serves one generated file to a link the wizard rendered, rebuilt from
// the answers in its address.
func (a *WizardAPI) File(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	draft := draftOf(q.Get("domain"), q.Get("cdn"), q.Get("content_paths"))
	out, err := a.wizard.Files(r.Context(), actorOf(r.Context()), draft)
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	serveBundleFile(w, r, out.Bundle, q.Get("name"))
}

// Register is what both of the last step's buttons send: it saves the domain,
// and verifies it when it has terms and the site's file and package both
// answer. Terms are null when the licensing step was skipped.
func (a *WizardAPI) Register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain  string        `json:"domain"`
		CDN     string        `json:"cdn"`
		Paths   string        `json:"content_paths"`
		Sitemap string        `json:"sitemap_url"`
		Terms   *controlsJSON `json:"terms"`
		// ImportReplacesManualTerms is the last step's checkbox: whether the
		// automatic import may replace the terms of pages already in the catalog.
		ImportReplacesManualTerms bool `json:"import_replaces_manual_terms"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	done, err := a.wizard.Finish(r.Context(), actorOf(r.Context()), wizard.Registration{
		Draft:                     draftOf(req.Domain, req.CDN, req.Paths),
		Sitemap:                   req.Sitemap,
		Controls:                  controlsOrNil(req.Terms),
		ImportReplacesManualTerms: req.ImportReplacesManualTerms,
	})
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	writeAPI(w, r, struct {
		Domain   string `json:"domain"`
		Verified bool   `json:"verified"`
	}{Domain: done.Domain, Verified: done.Verified})
}

// yoursLink is where a domain the person already holds is opened instead of
// being added again.
func yoursLink(found wizard.Lookup) string {
	switch {
	case !found.Yours:
		return ""
	case found.Verified:
		return domainPageURL(found.Domain)
	default:
		return domainSetupURL(found.Domain)
	}
}
