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
// address-not-confirmed page. The read of the account's registrations in front
// of every page is RegistrationsReader's, the dashboard's pages are
// DashboardHandler's, and the wizard's JSON routes are WizardAPI's.
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
	// Sites builds a registered site's page and files from what the person has
	// registered, as the read in front of the page found it.
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

// Home is where the console opens, and where a sign-in lands. A publisher with
// a verified domain lands on their overview, where its sales are. Anybody else
// lands on their domains: a publisher whose domains are all not verified sees
// what each one still needs, one with no domain yet sees the console with
// nothing in it and the button that adds the first, and an account that has
// not named its business is sent to the add-a-domain wizard, whose first step
// asks for it.
func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	regs, ok := pageRegistrations(w, r)
	if !ok {
		return
	}
	if dashboard.HasVerifiedDomain(regs) {
		http.Redirect(w, r, dashboard.PathOverview, http.StatusSeeOther)
		return
	}
	h.renderDomains(w, r, regs)
}

// Domains lists what this person has registered, read from the Exchange on
// every visit by the read in front of it, with the fee and the reporting policy
// the Exchange applies to their sales. With no domain yet -- this is where the
// wizard's Finish later leads before the first one is saved -- the list says
// so and offers to add the first. An account that has not named its business
// is sent to the wizard instead.
func (h *Handler) Domains(w http.ResponseWriter, r *http.Request) {
	regs, ok := pageRegistrations(w, r)
	if !ok {
		return
	}
	h.renderDomains(w, r, regs)
}

func (h *Handler) renderDomains(w http.ResponseWriter, r *http.Request, regs exchangeclient.Registrations) {
	nav, ok := consoleNav(w, r, h.dash, regs, dashboard.SectionDomains)
	if !ok {
		return
	}
	terms, err := h.dash.ExchangeTerms(r.Context(), actorOf(r.Context()), nav)
	if err != nil {
		renderFailure(h.render, w, r, err, "")
		return
	}
	h.render.Render(w, r, "home", Page{
		Title: "My domains", Nav: &nav, Registrations: regs, ExchangeTerms: &terms,
	})
}

// consoleNav is the sidebar of a page in section, for the account regs
// describes: every page that draws the console takes it from here. ok is false
// when the page has been answered already. An account the Exchange lists no
// publisher for has nothing any section could show, and is sent to the
// add-a-domain wizard, whose first step names the business: until it is named,
// every page that draws the console leads back to that step.
func consoleNav(
	w http.ResponseWriter, r *http.Request, dash *dashboard.Service,
	regs exchangeclient.Registrations, section string,
) (dashboard.Nav, bool) {
	nav, ok := dash.Nav(regs, section)
	if !ok {
		http.Redirect(w, r, NewDomainPath, http.StatusSeeOther)
	}
	return nav, ok
}
