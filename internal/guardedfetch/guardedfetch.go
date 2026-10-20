// Package guardedfetch is the one fetch posture for reading a document from
// an address that did not come from the operator: a page a visitor named, a
// sitemap entry a publisher served. Three services read such documents -- the
// onboarding preview and the publisher console, both through
// internal/pagefetch, and the catalog importer -- and a request-forgery
// surface fixed in one and not the others is not fixed, so the posture is
// composed here once and all of them compose this.
//
// The posture: the SDK's guarded transport resolves and checks the address
// before the dial and pins the connection to the address it checked; the
// client follows no redirect, so the guard's decision is never handed to
// whatever an origin names next; an address carrying userinfo is refused
// before the dial, because Go's client would turn it into an Authorization
// header and send the credentials to the origin; and a body over the cap is
// refused rather than truncated, because a document cut short that still
// parses reads as a complete statement the publisher never made.
//
// What each caller keeps is its own: which addresses to read, which hops of
// its own choosing to follow, its User-Agent, and the error vocabulary it
// answers in. This package returns the sentinels below, and a caller wraps or
// maps them. What the posture does fix for every caller is how an address is
// rendered into an error or a log line: Redacted, which never repeats a
// credential, and which this package's own refusals use too.
package guardedfetch

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
)

// HTTPDoer is the minimal contract a reader needs from a client. Readers take
// it as an argument rather than building one, so the composition root decides
// the security posture once, in one place.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ErrRedirectRefused is the cause behind a refusal to follow a redirect.
var ErrRedirectRefused = errors.New("guardedfetch: redirects are not followed")

// ErrTooLarge is the cause behind a refusal to read an oversized document.
var ErrTooLarge = errors.New("guardedfetch: the document is larger than the cap")

// ErrCredentialsInURL is the cause behind a refusal to dial an address that
// carries userinfo.
var ErrCredentialsInURL = errors.New("guardedfetch: the address carries credentials")

// NewClient builds the guarded client: the SDK's guarded transport over base
// (nil for the defaults) under timeout, following nothing. base carries the
// caller's own transport settings under the guard, never instead of it: the
// SDK clones what it is given and drops the proxy and TLS dialer that would
// route a dial around the address check.
func NewClient(base *http.Transport, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     resolvers.NewGuardedTransport(base),
		CheckRedirect: RefuseRedirect,
	}
}

// RefuseRedirect is the CheckRedirect that stops a client at the first
// redirect, naming where the origin tried to send it (RedactedURL).
func RefuseRedirect(req *http.Request, _ []*http.Request) error {
	return fmt.Errorf("%w: %s", ErrRedirectRefused, RedactedURL(req.URL))
}

// CheckAddress refuses an address that carries userinfo. It runs before the
// dial: nothing else would stop a document entry naming
// "https://user:pass@host/x" and getting those credentials put on the wire.
func CheckAddress(u *url.URL) error {
	if u.User != nil {
		return fmt.Errorf("%w: %s", ErrCredentialsInURL, RedactedURL(u))
	}
	return nil
}

// unreadableAddress is how an address that cannot be shown safely is rendered.
const unreadableAddress = "(an address that is not a well-formed http or https URL)"

// Redacted renders an address for an error or a log line without the
// credentials it may carry (RedactedURL). An address that does not parse is not
// rendered at all: only a parsed URL can be relied on to tell a credential from
// the rest of the text.
func Redacted(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return unreadableAddress
	}
	return RedactedURL(u)
}

// RedactedURL renders u for an error or a log line without its credentials:
// the whole userinfo becomes "xxxxx". url.URL.Redacted masks only a password,
// and a token in the user-name slot is as secret as one. Anything but an http
// or https URL with a host -- an opaque one such as "https:user:pass@host/x",
// another scheme, no host at all -- is not rendered, because the userinfo
// cannot be told from the rest of its text.
func RedactedURL(u *url.URL) string {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return unreadableAddress
	}
	shown := *u
	if shown.User != nil {
		shown.User = url.User("xxxxx")
	}
	return shown.String()
}

// ReadCapped reads at most cap bytes and refuses anything longer. Reading one
// byte past the cap is what tells a document that fits exactly from one that
// was cut short.
func ReadCapped(r io.Reader, cap int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, cap+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > cap {
		return nil, ErrTooLarge
	}
	return body, nil
}
