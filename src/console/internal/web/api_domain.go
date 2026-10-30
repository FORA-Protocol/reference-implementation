// The wizard's business step; its domain step -- the domain, what serves it,
// and the save that holds it -- and the files a saved domain is set up with.
// The licensing step is in api_terms.go and the last step's check in
// api_checks.go.

package web

import (
	"net/http"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/setupsteps"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

// Publisher is the business step's Next: it registers the business under the
// name typed. A repeat -- a second tab opened before the first registered, a
// second press of Enter -- goes on to the Exchange like the first call, and
// registration there is idempotent by actor: it answers with the publisher the
// account already holds, under its first name. The e2e obligation that
// registering the business again returns the same publisher relies on the call
// reaching it, so it is not answered here from the registrations instead.
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
	Yours        bool   `json:"yours"`
	Verified     bool   `json:"verified"`
	Link         string `json:"link,omitempty"`
	LimitReached bool   `json:"limit_reached"`
	Message      string `json:"message,omitempty"`
}

// Lookup answers which network serves a domain and whether it could be saved.
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
	})
}

// SaveDomain is the first step's Next: it saves the domain, with no terms yet,
// and answers with the domain as it is saved and the page that finishes its
// setup, which the wizard's address becomes so that a reload reopens it.
func (a *WizardAPI) SaveDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
		CDN    string `json:"cdn"`
		Paths  string `json:"content_paths"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	saved, err := a.wizard.SaveDomain(r.Context(), actorOf(r.Context()), wizard.Draft{
		Domain: req.Domain, CDN: req.CDN, Paths: req.Paths,
	})
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	writeAPI(w, r, struct {
		Domain string `json:"domain"`
		CDN    string `json:"cdn"`
		Paths  string `json:"content_paths"`
		Setup  string `json:"setup"`
	}{
		Domain: saved.Domain, CDN: saved.CDN,
		Paths: pathsField(saved.ContentPathScope), Setup: domainSetupURL(saved.Domain),
	})
}

// pathsField is a saved domain's licensed paths as the domain step's field
// holds them.
func pathsField(scope []string) string {
	return strings.Join(scope, ", ")
}

// fileBody is one generated file as the wizard shows it: the text to serve, and
// where to download it from.
type fileBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	URL         string `json:"url"`
}

// Files answers a saved domain's files, built from what the Exchange holds for
// it. Each one downloads from the domain's own page, the same link that page
// offers.
func (a *WizardAPI) Files(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	out, err := a.wizard.Files(r.Context(), actorOf(r.Context()), req.Domain)
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	files := make([]fileBody, 0, len(out.Bundle.Files))
	for _, f := range out.Bundle.Files {
		files = append(files, fileBody{
			Name: f.Name, Description: f.Description, Body: string(f.Body),
			URL: domainFileURL(out.Site.Domain, f.Name),
		})
	}
	writeAPI(w, r, struct {
		Files     []fileBody        `json:"files"`
		EdgeSteps []setupsteps.Step `json:"edge_steps"`
		Deployed  string            `json:"deployed"`
		Warnings  []string          `json:"warnings"`
	}{Files: files, EdgeSteps: out.EdgeSteps, Deployed: out.Deployed, Warnings: out.Bundle.Warnings})
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
