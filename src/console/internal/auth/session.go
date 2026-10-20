// Package auth is the console's sign-in: the redirect to the login provider,
// the callback that turns a verified token into a session cookie, and the
// middleware that reads it back.
//
// The cookie shapes, their names, their lifetimes and their attributes are this
// package's; the sealing is internal/session's, which knows nothing about what
// it seals. That split is deliberate, and it is why the identity registry's
// cookies and these can differ without either service reaching into the other.
package auth

import (
	"errors"
	"net/http"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/session"
)

// Cookie names. Both are prefixed so they cannot collide with a cookie set by
// anything else on the same host.
const (
	// flowCookie carries the in-flight sign-in: the state, the nonce, the
	// PKCE verifier and the path to return to. It exists only between the
	// redirect out and the callback back, which is why it is short-lived.
	//
	// The verifier is sealed into it rather than kept in a server-side store
	// because the console has no session before the callback: there is
	// nothing yet to key a store on. Sealed, the browser can neither read it
	// nor forge one.
	flowCookie = "fora_console_flow"
	// SessionCookie carries the signed-in person. Exported, like LoginPath,
	// for the tests that look for it in a browser's cookies.
	SessionCookie = "fora_console_session"
)

// Cookie lifetimes.
const (
	// flowTTL bounds how long a person may take at the login provider. Long
	// enough to type a password and answer a second factor, short enough that
	// an abandoned attempt does not stay replayable.
	flowTTL = 15 * time.Minute
	// sessionTTL bounds a signed-in session. Setting up a site takes a person
	// across a DNS change and a CDN deploy, so the window is a working day
	// rather than an hour; the sealed expiry is what ends it, not the browser.
	sessionTTL = 12 * time.Hour
)

// Session is the signed-in person, sealed into the session cookie.
//
// It carries the verified-email answer so the middleware can gate a page
// without a database read on every request. The sealed session is trusted until
// it expires: nothing re-reads the account behind it, and the Exchange takes the
// verified flag from the actor the console builds out of this same session. So
// a sign-out elsewhere, or a change at the login provider, does not end a
// session that is already open. Taking a person's access away is done on the
// Exchange, by removing their publisher grant: every registration call is
// checked against the grants held for the session's issuer and subject.
//
// The one thing the middleware does check is the issuer: a session counts only
// while it names the issuer the console trusts now. A console moved to another
// provider ends every session sealed under the previous one.
type Session struct {
	Issuer        string `json:"iss"`
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	DisplayName   string `json:"name"`
}

// cookieWriter seals a value into a cookie and reads it back. It is also the
// jar the shared sign-in flow writes its cookie through, so both cookies carry
// the attributes set here.
type cookieWriter struct {
	codec  *session.Codec
	secure bool
	clk    clock.Clock
}

var _ oidcup.Jar = cookieWriter{}

// Set writes one sealed cookie.
//
// Secure follows the console's own origin rather than being hardcoded: a
// deployment on http (a local bring-up) would otherwise set a cookie the
// browser refuses to send back, and the sign-in would loop with nothing to
// show for it. Production is https and the flag is on there.
func (c cookieWriter) Set(w http.ResponseWriter, name string, payload any, ttl time.Duration) error {
	token, err := c.codec.SealFor(name, payload, ttl)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  c.clk.Now().Add(ttl),
	})
	return nil
}

// Read opens one sealed cookie into dst.
//
// Each cookie is sealed bound to its own name, so the flow cookie presented
// under the session cookie's name is refused rather than read as a session:
// both are sealed under one key, and a payload that happened to share field
// names would otherwise decode.
func (c cookieWriter) Read(r *http.Request, name string, dst any) error {
	ck, err := r.Cookie(name)
	if err != nil {
		return errNoCookie
	}
	return c.codec.OpenFor(name, ck.Value, dst)
}

// Clear removes one cookie.
func (c cookieWriter) Clear(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// errNoCookie means the cookie was not presented at all, which is an ordinary
// state -- a first visit, a cleared browser -- rather than a failure.
var errNoCookie = errors.New("auth: no cookie")
