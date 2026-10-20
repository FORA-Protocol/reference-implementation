package oauthserver

import (
	"net/http"
)

// handleAuthorize begins a sign-in. It first pins the client and redirect_uri (until
// those are trusted no error may be redirected, so a bad one gets a plain 400), then
// requires a code + S256 challenge, and hands the authorization-request context to the
// shared upstream flow, which seals it beside a fresh leg and 302s the browser to Zitadel.
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	client, err := s.cfg.Clients.ClientByID(r.Context(), q.Get("client_id"))
	if err != nil {
		if storeUnavailable(err) {
			s.serviceUnavailable(w, r, "oauthserver.authorize.store", err)
			return
		}
		userError(w, http.StatusBadRequest, "unknown or unregistered client_id")
		return
	}
	redirectURI := q.Get("redirect_uri")
	if !allowedRedirect(client, redirectURI) {
		userError(w, http.StatusBadRequest, "redirect_uri is not registered for this client")
		return
	}

	state := q.Get("state")
	if q.Get("response_type") != "code" {
		s.redirectError(w, r, redirectURI, state,
			oauthErr{code: "unsupported_response_type", desc: "only response_type=code is supported"})
		return
	}
	challenge := q.Get("code_challenge")
	if challenge == "" || q.Get("code_challenge_method") != "S256" {
		s.redirectError(w, r, redirectURI, state,
			oauthErr{code: "invalid_request", desc: "a S256 code_challenge is required"})
		return
	}

	dest, err := s.flow.Start(w, authFlow{
		ClientID:        client.ID,
		ClientName:      client.Name,
		RedirectURI:     redirectURI,
		ClientState:     state,
		ClientChallenge: challenge,
	})
	if err != nil {
		s.serverError(w, r, "oauthserver.authorize.start", err)
		return
	}
	http.Redirect(w, r, dest, http.StatusFound)
}
