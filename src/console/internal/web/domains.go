package web

import (
	"net/http"
	"net/url"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

// DomainFilePattern is the route one registered domain's setup file is
// downloaded from. It sits under the domain's own page because it answers from
// the same lookup: the stored registration, read for the person signed in.
const DomainFilePattern = DomainPrefix + "{domain}/files/{name}"

// domainFileURL is where one registered domain's file is downloaded from.
func domainFileURL(domain, name string) string {
	return DomainPrefix + url.PathEscape(domain) + "/files/" + url.PathEscape(name)
}

// Domain shows one registered domain: its network, its licensing, its setup
// files and anything the console found wrong with the site. Under
// DomainFilePattern it serves one of those files instead.
//
// Both answers are built from the registration the Exchange holds, never from
// the request. A publisher who signs back in a month later downloads the
// files their site was registered with, because the registration is what they
// are built from.
func (h *Handler) Domain(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if domain == "" {
		domain = siteDomainFrom(r.URL.Path)
	}
	site, err := h.sites.Registered(r.Context(), actorOf(r.Context()), domain)
	if err != nil {
		renderFailure(h.render, w, r, err, "")
		return
	}
	if name := r.PathValue("name"); name != "" {
		serveBundleFile(w, r, site.Bundle, name)
		return
	}
	h.renderDomain(w, r, site)
}

// renderDomain draws one domain's page from the bundle its registration
// produces.
func (h *Handler) renderDomain(w http.ResponseWriter, r *http.Request, registered wizard.Registered) {
	site, bundle := registered.Registration, registered.Bundle
	terms, err := h.sites.Terms(r.Context(), registered)
	if err != nil {
		renderFailure(h.render, w, r, err, "")
		return
	}
	// The sample feed is not offered here: the terms are shown in the form
	// they were set with, beside the offer they produce for the same page the
	// sample was built from.
	downloads := make([]Download, 0, len(bundle.Files))
	for _, f := range bundle.Files {
		downloads = append(downloads, Download{
			Name: f.Name, Description: f.Description, URL: domainFileURL(site.Domain, f.Name),
		})
	}
	nav := dashboard.Nav{PublisherID: registered.PublisherID, Section: dashboard.SectionDomains}
	h.render.Render(w, r, "domain", Page{
		Title:        site.Domain,
		Nav:          &nav,
		Registration: site,
		Warnings:     bundle.Warnings,
		Downloads:    downloads,
		SiteTerms:    terms,
	})
}

// serveBundleFile writes one file out of a bundle, or answers not-found.
//
// The name is matched against this package's own constants before it reaches a
// header, so nothing a publisher typed is written into Content-Disposition.
func serveBundleFile(w http.ResponseWriter, r *http.Request, bundle setup.Bundle, name string) {
	files := bundle.Files
	if bundle.Sample != nil {
		files = append(files, *bundle.Sample)
	}
	for _, f := range files {
		if f.Name != name {
			continue
		}
		w.Header().Set("Content-Type", f.ContentType)
		w.Header().Set("Content-Disposition", "attachment; filename=\""+f.Name+"\"")
		_, _ = w.Write(f.Body)
		return
	}
	http.NotFound(w, r)
}

// siteDomainFrom pulls the domain name out of a page path.
func siteDomainFrom(path string) string {
	rest := strings.TrimPrefix(path, DomainPrefix)
	if decoded, err := url.PathUnescape(rest); err == nil {
		return strings.Trim(decoded, "/")
	}
	return strings.Trim(rest, "/")
}

// reasonText is what a publisher reads for each of the Exchange's reason codes.
//
// The codes are stable and the sentences are not, which is why the mapping is
// here and the code is what travels. It is keyed on the shared constants, and a
// test holds it to consoleapi.AllReasons, so a code added to the contract
// without a sentence here fails the build's tests rather than reaching a
// publisher as a bare token.
var reasonText = map[string]string{
	consoleapi.ReasonManifestMissing: "We found no file at /.well-known/fora.json on your site. " +
		"Publish the one below and try again.",
	consoleapi.ReasonManifestUnreadable: "The file at /.well-known/fora.json could not be read. " +
		"Replace it with the one below, unchanged.",
	consoleapi.ReasonManifestVersionRefused: "The file at /.well-known/fora.json is in a format we " +
		"no longer accept. Replace it with the one below.",
	consoleapi.ReasonExchangeNotListed: "The file does not list this exchange. Use the one below, " +
		"unchanged.",
	consoleapi.ReasonResourceOwnerMismatch: "The file names a different payee than your account. " +
		"Use the one below, unchanged.",
	consoleapi.ReasonContributorNotListed: "The file does not authorise us to submit your catalogue. " +
		"Use the one below, unchanged.",
	consoleapi.ReasonDomainTaken: "This domain is already registered to another publisher. " +
		"If it is yours, contact support to have it transferred.",
}

// checkReasonText turns the Exchange's reason codes into what a publisher
// reads.
//
// A code this console does not know still shows something: the person is told
// the file does not match and is given the code, which is better than an empty
// list that reads as though everything is fine. That can only happen when the
// Exchange is newer than the console.
func checkReasonText(codes []string) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		if s, ok := reasonText[c]; ok {
			out = append(out, s)
			continue
		}
		out = append(out, "The file does not match yet ("+c+").")
	}
	return out
}
