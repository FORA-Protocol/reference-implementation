package auth

import (
	"context"
	"net/http"
	"net/url"
)

// sessionCtxKey carries the signed-in person down to a handler.
type sessionCtxKey struct{}

// FromContext returns the session the middleware read, or nil.
func FromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionCtxKey{}).(*Session)
	return s
}

// RequireSession gates a page on being signed in. Anyone without a session is
// sent to the provider and comes back to the page they asked for.
func (h *Handler) RequireSession(next http.Handler) http.Handler {
	return h.RequireSessionOr(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, LoginPath+"?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
	}), next)
}

// RequireSessionOr gates on a session but hands a visitor without one to deny
// rather than to the provider.
//
// The JSON routes use it. A fetch call follows a redirect without telling the
// script it did, so sending one to the login provider answers the call with the
// provider's sign-in page: the wizard would read HTML where it expected its own
// answer and report a broken call, when what actually happened is that the
// session ended. deny says that in the shape the caller asked for.
func (h *Handler) RequireSessionOr(deny, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sess Session
		if err := h.cookies.Read(r, SessionCookie, &sess); err != nil || sess.Issuer != h.issuer {
			// A cookie that has expired or that this key can no longer open is
			// indistinguishable from none, and both mean the same thing to the
			// person: sign in again. So is one sealed for another provider's
			// issuer, which a deployment leaves behind when it moves its
			// publishers to a provider of their own: the account it names is
			// not one this provider vouches for.
			h.cookies.Clear(w, SessionCookie)
			deny.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionCtxKey{}, &sess)))
	})
}

// RequireVerifiedEmail gates the publisher pages on the login provider having
// checked the person's address.
//
// It runs inside RequireSession, so it can assume a session. The Exchange
// refuses an unverified actor independently: this gate exists so the person
// reads an explanation instead of watching a form fail, not as the control
// that keeps an unverified address out of a grant.
func (h *Handler) RequireVerifiedEmail(unverifiedPath string, next http.Handler) http.Handler {
	return h.RequireVerifiedEmailOr(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, unverifiedPath, http.StatusFound)
	}), next)
}

// RequireVerifiedEmailOr is the same gate answering through deny instead of a
// redirect, for the same reason RequireSessionOr exists.
func (h *Handler) RequireVerifiedEmailOr(deny, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := FromContext(r.Context())
		if sess == nil || !sess.EmailVerified {
			deny.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
