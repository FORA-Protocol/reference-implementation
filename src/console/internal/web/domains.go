package web

import (
	"net/http"
	"net/url"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
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
// anything the browser sent. A publisher who signs back in a month later
// downloads the files their site was registered with, because the registration
// is what they are built from.
func (h *Handler) Domain(w http.ResponseWriter, r *http.Request) {
	regs, ok := pageRegistrations(w, r)
	if !ok {
		return
	}
	domain := r.PathValue("domain")
	if domain == "" {
		domain = siteDomainFrom(r.URL.Path)
	}
	site, err := h.sites.Registered(r.Context(), regs, domain)
	if err != nil {
		renderFailure(h.render, w, r, err, "")
		return
	}
	if name := r.PathValue("name"); name != "" {
		serveBundleFile(w, r, site.Bundle, name)
		return
	}
	h.renderDomain(w, r, regs, site)
}

// renderDomain draws one domain's page from the bundle its registration
// produces, in the console of the account regs describes.
func (h *Handler) renderDomain(
	w http.ResponseWriter, r *http.Request, regs exchangeclient.Registrations, registered wizard.Registered,
) {
	nav, ok := consoleNav(w, r, h.dash, regs, dashboard.SectionDomains)
	if !ok {
		return
	}
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
