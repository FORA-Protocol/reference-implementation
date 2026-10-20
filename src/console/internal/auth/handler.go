package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/session"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/repo"
)

// returnTo is what a sign-in carries across the trip to the provider: the path
// to come back to, so a link into a deep page survives the trip. It is always a
// path on this console, checked by safeReturn before it is sealed.
type returnTo struct {
	Path string `json:"path"`
}

// Route paths.
const (
	LoginPath    = "/login"
	CallbackPath = "/auth/callback"
	LogoutPath   = "/logout"
	// SignedOutPath is where the provider sends the browser once it has
	// ended its session, so it is registered there as a post-logout address.
	// It goes straight on to sign in again.
	SignedOutPath = "/signed-out"
)

// Upstream is the login provider: it signs a person in, and ends their session
// there when they sign out.
type Upstream interface {
	oidcup.Authenticator
	oidcup.Logouter
	// Issuer is the provider's issuer. It is the only one a session may carry:
	// a deployment that moves its publishers to another provider ends every
	// session sealed under the old one.
	Issuer() string
}

// Handler serves the sign-in and sign-out routes and gates every other page.
type Handler struct {
	// flow is the trip to the login provider and back.
	flow     *oidcup.Flow[returnTo]
	upstream oidcup.Logouter
	// issuer is the provider's issuer, the only one a session may carry.
	issuer   string
	accounts repo.AccountRepo
	cookies  cookieWriter
	// signedOut is SignedOutPath as an absolute address on this console, the
	// form the provider needs to send the browser back to it.
	signedOut string
	// renderSignedOut draws the page a sign-out ends on when the provider has
	// no logout address to send the browser to.
	renderSignedOut http.HandlerFunc
	// afterLogin is where a person lands when the sign-in carried no return
	// path of its own.
	afterLogin string
	// renderError shows a failure as a page rather than as a bare status, so a
	// person who cannot sign in reads a sentence instead of a number.
	renderError func(w http.ResponseWriter, r *http.Request, status int, message string)
}

// Config wires the handler.
type Config struct {
	Upstream Upstream
	// PublicOrigin is the console's own address, which the post-logout
	// address the provider sends the browser back to is built on.
	PublicOrigin    string
	RenderSignedOut http.HandlerFunc
	Accounts        repo.AccountRepo
	Codec           *session.Codec
	SecureCookies   bool
	AfterLogin      string
	RenderError     func(w http.ResponseWriter, r *http.Request, status int, message string)
	// Clk sources the cookie expiry a browser is told. The sealed expiry is
	// what actually ends a session; this one only stops a browser from keeping
	// a cookie it can no longer use.
	Clk clock.Clock
}

// New builds the handler, refusing a configuration that cannot work.
func New(cfg Config) (*Handler, error) {
	switch {
	case cfg.Upstream == nil:
		return nil, errors.New("auth: an upstream authenticator is required")
	case cfg.Upstream.Issuer() == "":
		return nil, errors.New("auth: the upstream must name its issuer")
	case cfg.PublicOrigin == "":
		return nil, errors.New("auth: the console's public origin is required")
	case cfg.RenderSignedOut == nil:
		return nil, errors.New("auth: a signed-out page renderer is required")
	case cfg.Accounts == nil:
		return nil, errors.New("auth: an account repository is required")
	case cfg.Codec == nil:
		return nil, errors.New("auth: a session codec is required")
	case cfg.RenderError == nil:
		return nil, errors.New("auth: an error renderer is required")
	case cfg.Clk == nil:
		return nil, errors.New("auth: a clock is required")
	}
	after := cfg.AfterLogin
	if after == "" {
		after = "/"
	}
	cookies := cookieWriter{codec: cfg.Codec, secure: cfg.SecureCookies, clk: cfg.Clk}
	flow, err := oidcup.NewFlow[returnTo](oidcup.FlowConfig{
		Upstream: cfg.Upstream, Jar: cookies, Cookie: flowCookie, TTL: flowTTL,
	})
	if err != nil {
		return nil, err
	}
	return &Handler{
		flow:            flow,
		upstream:        cfg.Upstream,
		issuer:          cfg.Upstream.Issuer(),
		accounts:        cfg.Accounts,
		cookies:         cookies,
		signedOut:       strings.TrimSuffix(cfg.PublicOrigin, "/") + SignedOutPath,
		renderSignedOut: cfg.RenderSignedOut,
		afterLogin:      after,
		renderError:     cfg.RenderError,
	}, nil
}

// RegisterRoutes mounts the sign-in and sign-out routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+LoginPath, h.login)
	mux.HandleFunc("GET "+CallbackPath, h.callback)
	mux.HandleFunc("POST "+LogoutPath, h.logout)
	mux.HandleFunc("GET "+SignedOutPath, h.signedOutReturn)
}

// login starts a sign-in: the shared flow mints the state, the nonce and the
// PKCE pair, seals them with the return path, and names the provider's address.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	dest, err := h.flow.Start(w, returnTo{Path: safeReturn(r.URL.Query().Get("next"))})
	if err != nil {
		h.fail(w, r, "could not start the sign-in", err)
		return
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// callback finishes a sign-in.
//
// The shared flow compares the state against the sealed copy before the code
// is exchanged. That is what makes the callback the browser's own: a callback a
// third party caused would carry a state this browser never minted, and
// exchanging its code first would sign the person in as whoever that party
// authenticated.
func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	back, claims, err := h.flow.Finish(w, r)
	if err == nil {
		// On success startSession has answered the request. Its refusals go
		// through the same classification as the flow's, below.
		err = h.startSession(w, r, claims, back.Path)
	}
	switch {
	case err == nil:
		return
	case errors.Is(err, oidcup.ErrNoFlow):
		// Expired, cleared, or a callback nobody started here. Sending them to
		// /login rather than showing an error is right for the common case,
		// which is a person who left the tab open too long.
		http.Redirect(w, r, LoginPath, http.StatusFound)
		return
	case errors.Is(err, oidcup.ErrStateMismatch):
		h.renderError(w, r, http.StatusBadRequest, "That sign-in did not start here. Please try again.")
		return
	case errors.Is(err, oidcup.ErrDenied):
		h.renderError(w, r, http.StatusUnauthorized, "The login provider refused the sign-in.")
		return
	default:
		h.fail(w, r, "the sign-in could not be completed", err)
	}
}

// startSession records the account and seals the session cookie.
//
// Claims from another issuer are refused before anything is recorded. The
// provider's client already refuses such a token; this keeps the console from
// sealing a session its own middleware would refuse, which would send the
// person around the sign-in forever. The refusal is classified as the
// client's own refusal of the same token is, oidcup.ErrExchange, and the
// callback answers both through one switch, so they get one answer.
func (h *Handler) startSession(
	w http.ResponseWriter, r *http.Request, claims oidcup.Claims, returnTo string,
) error {
	if claims.Issuer != h.issuer {
		return fmt.Errorf("%w: a sign-in from issuer %q, not %q", oidcup.ErrExchange, claims.Issuer, h.issuer)
	}
	account, err := h.accounts.Upsert(r.Context(), repo.Account{
		Issuer:        claims.Issuer,
		Subject:       claims.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
		DisplayName:   claims.Name,
	})
	if err != nil {
		return err
	}
	sess := Session{
		Issuer:        account.Issuer,
		Subject:       account.Subject,
		Email:         account.Email,
		EmailVerified: account.EmailVerified,
		DisplayName:   account.DisplayName,
	}
	if err := h.cookies.Set(w, SessionCookie, sess, sessionTTL); err != nil {
		return err
	}
	dest := returnTo
	if dest == "" {
		dest = h.afterLogin
	}
	http.Redirect(w, r, dest, http.StatusFound)
	return nil
}

// logout clears the session here and ends the one at the login provider. It
// is a POST like every other state change, and the console's cross-origin
// protection refuses one that a third-party page submits: otherwise any site
// could sign the person out with a hidden form.
//
// Clearing the console's cookie alone does not sign anybody out. The provider
// still holds its own session, so the next protected page sends the browser
// there and it comes straight back signed in, without asking. So the browser
// goes to the provider's logout address, which ends that session and sends it
// on to SignedOutPath, and from there to sign in again.
//
// A provider with no logout address leaves only the console's session to end.
// Sending that browser to sign in would sign it straight back in, so it is
// shown the signed-out page instead.
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	h.cookies.Clear(w, SessionCookie)
	dest := h.upstream.LogoutURL(h.signedOut)
	if dest == "" {
		h.renderSignedOut(w, r)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// signedOutReturn is where the provider sends the browser after ending its
// session. With that session gone, signing in asks for the person's
// credentials again, so the browser goes straight there.
func (h *Handler) signedOutReturn(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, LoginPath, http.StatusSeeOther)
}

// fail logs the cause and shows the person a sentence that names none of it.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	reqctx.FromContext(r.Context()).ErrorContext(r.Context(), "console.auth_failed",
		"what", what, "err", err)
	h.renderError(w, r, http.StatusInternalServerError,
		"Something went wrong on our side. Please try signing in again.")
}

// safeReturn keeps a sign-in from being used as an open redirect.
//
// Only a path on this console is allowed back. A value carrying a scheme, a
// host, or a leading double slash -- which a browser reads as a host -- is
// dropped, and the person lands on the default page instead.
func safeReturn(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return ""
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return ""
	}
	return u.RequestURI()
}
