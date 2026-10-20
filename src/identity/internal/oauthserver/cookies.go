package oauthserver

import (
	"net/http"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
)

const (
	authFlowCookie = "fora_authflow"
	pendingCookie  = "fora_pending"

	authFlowTTL = 10 * time.Minute
	pendingTTL  = 20 * time.Minute
)

// authFlow is the downstream client's request, carried from /authorize to
// /callback so the redirect can be rebuilt and the code bound to it. The
// upstream leg's own secrets (state, nonce, PKCE verifier) are sealed beside it
// by oidcup.Flow, so the browser can read or forge neither.
type authFlow struct {
	ClientID        string `json:"cid"`
	ClientName      string `json:"cnm"`
	RedirectURI     string `json:"ruri"`
	ClientState     string `json:"cst"`
	ClientChallenge string `json:"cch"`
}

// pending is the state sealed at /callback and reopened at /consent. It carries the
// downstream client's request forward so the code can be issued once the developer
// approves, plus the subdomain minted for them.
//
// It holds no part of the developer's upstream identity. The issuer and subject were
// here only so the registration form could write its fields against them; consent
// needs neither, so the browser stops carrying them.
type pending struct {
	ClientID        string `json:"cid"`
	ClientName      string `json:"cnm"`
	RedirectURI     string `json:"ruri"`
	ClientState     string `json:"cst"`
	ClientChallenge string `json:"cch"`
	Subdomain       string `json:"sd"`
	// CSRFToken is the synchronizer token minted at /callback, sealed here (so the
	// browser cannot read or forge it), and echoed as a hidden field on the consent
	// screen. /consent verifies the submitted field against it, making CSRF
	// protection independent of SameSite support.
	CSRFToken string `json:"csrf"`
}

// setCookie writes a cookie with the sign-up flow's fixed security attributes
// (Path=/, HttpOnly, SameSite=Lax — the flow returns via top-level GET redirects —
// and Secure tracking the deployment scheme so a local http compose stack still
// works). Only value and maxAge vary; a negative maxAge expires the cookie.
// Centralized so the set and clear paths cannot drift on the security attributes —
// the surface the gosec G124 exclusion covers.
func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

// setSealed seals payload into a cookie, bound to the cookie name as its
// purpose, so a value sealed for one cookie (authFlowCookie) cannot be opened as
// another (pendingCookie) even though the key is shared and the payloads share
// JSON field names.
func (s *Server) setSealed(w http.ResponseWriter, name string, payload any, ttl time.Duration) error {
	val, err := s.cfg.Codec.SealFor(name, payload, ttl)
	if err != nil {
		return err
	}
	s.setCookie(w, name, val, int(ttl.Seconds()))
	return nil
}

// readSealed opens the named cookie into dest, returning http.ErrNoCookie when it
// is absent, a session error when it is expired or tampered, and
// session.ErrWrongPurpose when the sealed value was minted for a different
// cookie name.
func (s *Server) readSealed(r *http.Request, name string, dest any) error {
	c, err := r.Cookie(name)
	if err != nil {
		return err
	}
	return s.cfg.Codec.OpenFor(name, c.Value, dest)
}

// clearCookie expires the named cookie so a spent flow leaves nothing behind.
func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	s.setCookie(w, name, "", -1)
}

// sealedJar is the Server's cookie handling seen through the oidcup.Jar port, so
// the shared sign-in flow writes its cookie with this service's attributes.
type sealedJar struct{ s *Server }

func (j sealedJar) Set(w http.ResponseWriter, name string, payload any, ttl time.Duration) error {
	return j.s.setSealed(w, name, payload, ttl)
}

func (j sealedJar) Read(r *http.Request, name string, dst any) error {
	return j.s.readSealed(r, name, dst)
}

func (j sealedJar) Clear(w http.ResponseWriter, name string) { j.s.clearCookie(w, name) }

// csrfField is the hidden form field carrying the synchronizer token bound into the
// sealed pending cookie. A cross-site request cannot read the sealed cookie, so it
// cannot present a matching token — this is what stops a page the developer did not
// open from approving a client on their behalf, even on a SameSite-unaware agent
// where the cookie alone would travel.
const csrfField = "csrf_token"

// openPending opens the sealed pending cookie and is the one place /consent
// refuses a request that has no sign-in session behind it. Both legs of the route
// start here — handleConsentGet for the render, readPendingPOST for the
// submission — so one route answers one condition in one sentence, rather than
// two handlers keeping two copies of it in step by hand.
//
// It writes the 400 and returns ok=false on failure, so the caller returns
// immediately on !ok.
func (s *Server) openPending(w http.ResponseWriter, r *http.Request) (pending, bool) {
	var p pending
	if err := s.readSealed(r, pendingCookie, &p); err != nil {
		userError(w, http.StatusBadRequest, "no active sign-in session; start again")
		return pending{}, false
	}
	return p, true
}

// readPendingPOST is the prelude the /consent POST handler runs: open the sealed
// pending cookie, parse a size-bounded form body, and verify the CSRF token. It
// writes the 400 and returns ok=false on any failure, so the caller returns
// immediately on !ok; on success it returns the reopened pending state.
func (s *Server) readPendingPOST(w http.ResponseWriter, r *http.Request) (pending, bool) {
	p, ok := s.openPending(w, r)
	if !ok {
		return pending{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		userError(w, http.StatusBadRequest, "malformed submission")
		return pending{}, false
	}
	if !oidcup.SameToken(p.CSRFToken, r.PostFormValue(csrfField)) {
		userError(w, http.StatusBadRequest, "invalid or expired form token; start again")
		return pending{}, false
	}
	return p, true
}
