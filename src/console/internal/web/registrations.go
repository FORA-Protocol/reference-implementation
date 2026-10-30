// The read in front of every publisher page: what the signed-in person's
// account has registered, read from the Exchange once per request and handed to
// the page, which is drawn from it.

package web

import (
	"context"
	"errors"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

// registrationsKey is the request-context key the read hands the registrations
// to the page under. It is unexported, so nothing outside this package can put a
// value there: what a page finds under it is what the Exchange listed for the
// signed-in person on this request.
type registrationsKey struct{}

// RegistrationsReader reads the signed-in person's registrations in front of
// every publisher page and hands them to the page.
type RegistrationsReader struct {
	render *Renderer
	sites  *wizard.Sites
}

// NewRegistrationsReader builds the read in front of every publisher page.
func NewRegistrationsReader(render *Renderer, sites *wizard.Sites) (*RegistrationsReader, error) {
	switch {
	case render == nil:
		return nil, errors.New("web: a renderer is required")
	case sites == nil:
		return nil, errors.New("web: the registered-site reads are required")
	}
	return &RegistrationsReader{render: render, sites: sites}, nil
}

// Read reads what the signed-in person's account has registered -- whether it
// has a business, and its sites -- and hands it to the page, which is drawn from
// it rather than asking the Exchange again (see pageRegistrations).
//
// It runs inside the session and address gates, so the person it reads for is
// signed in with an address the provider confirmed. It decides nothing about
// where the person goes: every page that draws the console sends an account
// without a business to the wizard, through consoleNav, the other pages answer
// it in their own way, and the Exchange checks every call against the grants
// it holds. A read that fails is answered here, with the failure page every
// page answers it with, rather than letting the page find out for itself.
//
// The registrations are read on every request and nothing about them is kept in
// the session, so a business registered on another device, or a grant an
// operator removed, is seen on the next page.
func (g *RegistrationsReader) Read(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		regs, err := g.sites.Registrations(r.Context(), actorOf(r.Context()))
		if err != nil {
			renderFailure(g.render, w, r, err, "")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), registrationsKey{}, regs)))
	})
}

// pageRegistrations is what the read in front of this page found: the
// registrations the Exchange listed for the signed-in person on this request.
// ok is false when the page has been answered already.
//
// The routing table puts the read in front of every publisher page, so a page
// that finds nothing here was mounted without it. That is a fault on our side:
// it is logged and answered as an internal error. It is never drawn as an
// account with nothing registered, and never covered over by reading the
// registrations here instead, which would hide the missing read.
func pageRegistrations(w http.ResponseWriter, r *http.Request) (exchangeclient.Registrations, bool) {
	regs, ok := r.Context().Value(registrationsKey{}).(exchangeclient.Registrations)
	if !ok {
		reqctx.FromContext(r.Context()).ErrorContext(r.Context(), "console.page_without_registrations",
			"path", r.URL.Path)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return exchangeclient.Registrations{}, false
	}
	return regs, true
}
