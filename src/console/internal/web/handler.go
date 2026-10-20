package web

import (
	"context"
	"errors"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/auth"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

// Route paths. The publisher-facing word is "domain": it is what they type, it
// is what is registered, and it is what one party can take from another by
// registering it first.
const (
	HomePath        = "/"
	DomainsPath     = "/domains"
	NewDomainPath   = "/domains/new"
	DomainPrefix    = "/domains/"
	VerifyEmailPath = "/verify-email"
)

// Handler serves the console's own pages: the domains list and one domain, the
// add-a-domain page, the page that finishes a saved domain's setup, and the
// address-not-confirmed page. The dashboard's pages are DashboardHandler's, and
// the wizard's JSON routes are WizardAPI's.
type Handler struct {
	render  *Renderer
	sites   *wizard.Sites
	wizard  *wizard.Service
	resumer *wizard.Resumer
	dash    *dashboard.Service
}

// Config wires the pages.
type Config struct {
	Renderer *Renderer
	// Sites reads back what a person has registered.
	Sites *wizard.Sites
	// Wizard is what the add-a-domain page opens on.
	Wizard *wizard.Service
	// Resumer is what the page that finishes a saved domain's setup opens on.
	Resumer *wizard.Resumer
	// Dashboard reads what a publisher's content earns.
	Dashboard *dashboard.Service
}

// New builds the page handler.
func New(cfg Config) (*Handler, error) {
	switch {
	case cfg.Renderer == nil:
		return nil, errors.New("web: a renderer is required")
	case cfg.Sites == nil:
		return nil, errors.New("web: the registered-site reads are required")
	case cfg.Wizard == nil:
		return nil, errors.New("web: the wizard is required")
	case cfg.Resumer == nil:
		return nil, errors.New("web: the reopening of saved domains is required")
	case cfg.Dashboard == nil:
		return nil, errors.New("web: the dashboard is required")
	}
	return &Handler{
		render: cfg.Renderer, sites: cfg.Sites, wizard: cfg.Wizard, resumer: cfg.Resumer, dash: cfg.Dashboard,
	}, nil
}

// actorOf builds the actor a call is made for, from the session the middleware
// read. The console asserts who; it never asserts what they own.
func actorOf(ctx context.Context) exchangeclient.Actor {
	s := auth.FromContext(ctx)
	if s == nil {
		return exchangeclient.Actor{}
	}
	return exchangeclient.Actor{
		OIDCIssuer:    s.Issuer,
		OIDCSubject:   s.Subject,
		Email:         s.Email,
		EmailVerified: s.EmailVerified,
	}
}

// Home is where the console opens. A publisher with a verified domain lands on
// their overview, where its sales are. Anybody else lands on their domains: a
// publisher whose domains are all not verified sees what each one still needs,
// and one with no domain is asked for their first, which is what the domains
// list says when it has none.
func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	regs, err := h.sites.Registrations(r.Context(), actorOf(r.Context()))
	if err != nil {
		renderFailure(h.render, w, r, err, "")
		return
	}
	if exchangeclient.HasVerifiedDomain(regs) {
		http.Redirect(w, r, dashboard.PathOverview, http.StatusSeeOther)
		return
	}
	h.renderDomains(w, r, regs)
}

// Domains lists what this person has registered, read from the Exchange on
// every visit, with the fee and the reporting policy the Exchange applies to
// their sales. A person with no domain yet is asked for their first one, with
// no terms and no sidebar: there is nothing in any section until a domain
// sells.
func (h *Handler) Domains(w http.ResponseWriter, r *http.Request) {
	regs, err := h.sites.Registrations(r.Context(), actorOf(r.Context()))
	if err != nil {
		renderFailure(h.render, w, r, err, "")
		return
	}
	h.renderDomains(w, r, regs)
}

func (h *Handler) renderDomains(w http.ResponseWriter, r *http.Request, regs exchangeclient.Registrations) {
	actor := actorOf(r.Context())
	page := Page{Title: "My domains", Registrations: regs}
	if exchangeclient.HasDomains(regs) {
		nav := dashboard.Nav{PublisherID: regs.PublisherID, Section: dashboard.SectionDomains}
		terms, err := h.dash.ExchangeTerms(r.Context(), actor, nav)
		if err != nil {
			renderFailure(h.render, w, r, err, "")
			return
		}
		page.Nav, page.ExchangeTerms = &nav, &terms
	}
	h.render.Render(w, r, "home", page)
}
