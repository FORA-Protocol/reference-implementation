// Package web is the console's browser surface: HTML pages rendered from
// html/template and locally served styles and scripts. There is no build step
// and no framework beyond Alpine, which is vendored and served like the
// stylesheet beside it.
//
// The pages are ordinary server-rendered documents. The one exception is adding
// a domain, which is a single page holding the publisher's answers in the
// browser: it cannot be a chain of form posts, because the domain must not be
// stored until the publisher's own site has proved they control it, and proving
// that means handing them a file to publish first.
package web

import (
	"embed"
	"html/template"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/auth"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// StaticPrefix is where the styles are served from.
const StaticPrefix = "/static/"

// pages names each content template. Each is parsed with the layout, so a page
// is one template set rather than a layout that looks up a name at render time
// -- a missing content block is then a boot failure rather than a blank page.
var pages = []string{
	"home", "newdomain", "domain", "message",
	"overview", "transactions", "transaction", "catalog", "resource", "income",
}

// Renderer holds the parsed templates.
type Renderer struct {
	sets map[string]*template.Template
}

// NewRenderer parses every page. A template that does not parse stops the boot:
// the alternative is a service that starts and then answers one route with an
// error nobody sees until a publisher opens it.
func NewRenderer() (*Renderer, error) {
	sets := make(map[string]*template.Template, len(pages))
	for _, name := range pages {
		t, err := template.ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		sets[name] = t
	}
	return &Renderer{sets: sets}, nil
}

// StaticHandler serves the vendored styles.
func StaticHandler() http.Handler {
	return http.StripPrefix("/", http.FileServerFS(staticFS))
}

// Page is what every template is rendered with. One shape for all of them, so
// the layout can always read the session and any message,
// whichever page is being drawn.
type Page struct {
	Title   string
	Session *auth.Session
	Error   string
	Notice  string
	// Nav draws the sidebar. Nil on a page that is not about one publisher:
	// signing in, the add-a-domain wizard, and every error page.
	Nav *dashboard.Nav

	// Home.
	Registrations exchangeclient.Registrations

	// Boot is the add-a-domain page's starting state, already JSON. The page
	// is one script over one set of values; fetching them after it loads would
	// show an empty wizard while the call ran. It is a plain string, written
	// into an attribute and escaped like any other.
	Boot string

	// One registered domain.
	Registration exchangeclient.Registration
	// SiteTerms is the domain's licensing, for a page that shows it rather
	// than asks for it.
	SiteTerms wizard.SiteTerms
	Warnings  []string
	// Downloads are the files the domain was set up with, built again from
	// the stored registration each time the page is drawn.
	Downloads []Download

	// The fee and reporting policy beside the domains list.
	ExchangeTerms *dashboard.ExchangeTerms

	// The dashboard's pages.
	Overview     *dashboard.Overview
	Transactions *dashboard.TransactionList
	Transaction  *dashboard.TransactionView
	Catalog      *dashboard.CatalogList
	Resource     *dashboard.ResourceView
	Income       *dashboard.IncomeReport

	// Message pages.
	Message   string
	Link      string
	LinkLabel string
}

// Download is one generated file a page links to.
type Download struct {
	Name        string
	Description string
	URL         string
}

// Render draws one page.
//
// A template that fails mid-write has already sent a 200 and part of a page, so
// there is nothing useful left to say to the browser; it is logged and the
// truncated page is what the person sees. Every value a template reads is
// escaped by html/template, which is why the publisher-supplied domain and the
// Exchange's own refusal messages can be written straight into a page.
func (r *Renderer) Render(w http.ResponseWriter, req *http.Request, name string, page Page) {
	t, ok := r.sets[name]
	if !ok {
		reqctx.FromContext(req.Context()).ErrorContext(req.Context(), "console.unknown_template",
			"template", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	page.Session = auth.FromContext(req.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", page); err != nil {
		reqctx.FromContext(req.Context()).ErrorContext(req.Context(), "console.render_failed",
			"template", name, "err", err)
	}
}

// RenderMessage draws a one-sentence page, used for every outcome that is not a
// form or a list: an error, a sign-in that cannot proceed, the support path.
func (r *Renderer) RenderMessage(
	w http.ResponseWriter, req *http.Request, status int, title, message, link, linkLabel string,
) {
	w.WriteHeader(status)
	r.Render(w, req, "message", Page{
		Title: title, Message: message, Link: link, LinkLabel: linkLabel,
	})
}
