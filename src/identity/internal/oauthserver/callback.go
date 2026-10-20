package oauthserver

import (
	"errors"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
)

// handleCallback is where Zitadel returns. The shared upstream flow reopens the
// sealed authorize context, checks the upstream state in constant time and
// exchanges the code; this handler then provisions the identity and sends the
// browser on to the consent screen, which is where the downstream code is issued.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	flow, claims, err := s.flow.Finish(w, r)
	switch {
	case errors.Is(err, oidcup.ErrNoFlow):
		userError(w, http.StatusBadRequest, "sign-in session missing or expired; start again")
		return
	case errors.Is(err, oidcup.ErrStateMismatch):
		userError(w, http.StatusBadRequest, "sign-in state mismatch")
		return
	case errors.Is(err, oidcup.ErrDenied):
		// The redirect target is now trusted; report a fixed denial rather than
		// reflecting the upstream's error string back to the client.
		s.redirectError(w, r, flow.RedirectURI, flow.ClientState,
			oauthErr{code: "access_denied", desc: "upstream sign-in was denied"})
		return
	case err != nil:
		logError(r, "oauthserver.callback.exchange", err)
		userError(w, http.StatusBadGateway, "upstream sign-in failed")
		return
	}
	subdomain, err := s.cfg.SignUp.SignIn(r.Context(), claims)
	if err != nil {
		s.unavailableOrServerError(w, r, "oauthserver.callback.provision", err)
		return
	}
	logInfo(r, "oauthserver.signin.ok", "subdomain", subdomain)

	csrf, err := oidcup.RandomToken(16)
	if err != nil {
		s.serverError(w, r, "oauthserver.callback.entropy", err)
		return
	}
	p := pending{
		ClientID:        flow.ClientID,
		ClientName:      flow.ClientName,
		RedirectURI:     flow.RedirectURI,
		ClientState:     flow.ClientState,
		ClientChallenge: flow.ClientChallenge,
		Subdomain:       subdomain,
		CSRFToken:       csrf,
	}
	if err := s.setSealed(w, pendingCookie, p, pendingTTL); err != nil {
		s.serverError(w, r, "oauthserver.callback.seal", err)
		return
	}
	// The developer is authenticated and provisioned, and must now approve the
	// requesting client before any code is issued — authentication is not
	// authorization, so a client the resource owner never approved (e.g. a
	// phisher's self-registered client) gets no code.
	http.Redirect(w, r, ConsentPath, http.StatusFound)
}

// issueCodeAndRedirect mints a one-time authorization code bound to cg through the
// grant service and 302s the browser back to the client. It is the single exit the
// approved-consent path funnels through.
func (s *Server) issueCodeAndRedirect(w http.ResponseWriter, r *http.Request, cg codeGrant) {
	code, err := s.grant.issue(r.Context(), cg)
	if err != nil {
		s.unavailableOrServerError(w, r, "oauthserver.code.issue", err)
		return
	}
	s.redirectSuccess(w, r, cg.redirectURI, code, cg.clientState)
}
