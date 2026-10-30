//go:build integration

package app_test

import (
	"crypto/ed25519"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/app"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/auth"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
)

// WHAT THESE TESTS COVER.
//
// They drive the console's own surface end to end: real Postgres, real HTTP,
// the real handler chain, real cookies, and a real Exchange behind it
// (exchangetest), running the production registration service over the same
// database and verifying every call's signature against the console's key.
//
// The login provider is a fake implementing the oidcup port, because a real
// one cannot complete a sign-in headlessly for every provider a deployment
// might use. What a test asserts the Exchange holds is read back through the
// Exchange's own registrations/list route with the console's own client (see
// registrationsOf), never from a table.

// TestConsole_RefusesAnUnsignedVisitor is the gate on every publisher page and
// wizard route, walked from the console's own route table. A visitor with no
// session is sent to sign in from a page, and refused in JSON by a wizard
// route, rather than shown anything.
func TestConsole_RefusesAnUnsignedVisitor(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	// A client with no cookie jar keeps no session, and one that does not
	// follow redirects shows where the console sent it.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	for _, pattern := range append(app.PublisherPagePatterns(), "GET /verify-email") {
		status, header := sendPattern(t, client, f.server.URL, pattern)
		if status != http.StatusFound {
			t.Fatalf("%s unsigned = %d, want a redirect", pattern, status)
		}
		if loc := header.Get("Location"); !strings.HasPrefix(loc, "/login") {
			t.Fatalf("%s unsigned redirected to %q, want the sign-in", pattern, loc)
		}
	}
	for _, pattern := range app.WizardRoutePatterns() {
		if status, _ := sendPattern(t, client, f.server.URL, pattern); status != http.StatusUnauthorized {
			t.Fatalf("%s unsigned = %d, want %d", pattern, status, http.StatusUnauthorized)
		}
	}
}

// TestConsole_RefusesAnUnverifiedAddress covers the publisher gate, walked from
// the console's own route table. The person can sign in -- they have an
// account -- but every page and wizard route that reads or writes a
// registration, the business step's call among them, is closed until their
// provider confirms the address.
func TestConsole_RefusesAnUnverifiedAddress(t *testing.T) {
	claims := verifiedClaims()
	claims.EmailVerified = false
	f := startConsole(t, claims)
	f.signIn(t)

	for _, pattern := range append(app.PublisherPagePatterns(), app.WizardRoutePatterns()...) {
		if status, _ := sendPattern(t, f.client, f.server.URL, pattern); status != http.StatusForbidden {
			t.Fatalf("%s with an unverified address = %d, want %d", pattern, status, http.StatusForbidden)
		}
	}
	status, body := f.get(t, "/domains/new")
	if status != http.StatusForbidden || !strings.Contains(body, "confirm your email") {
		t.Fatalf("the add-a-domain page with an unverified address = %d and does not say what to do: %s",
			status, head(body))
	}
	if f.mine(t).Registered {
		t.Fatal("an unverified visitor registered a publisher on the Exchange")
	}
}

// sendPattern sends one request to the route a ServeMux pattern names, with
// every wildcard filled in with a sample value, and returns the answer's status
// and headers. A gate answers before any body is read, so a POST goes with an
// empty one.
func sendPattern(t *testing.T, client *http.Client, base, pattern string) (int, http.Header) {
	t.Helper()
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		t.Fatalf("route pattern %q names no method", pattern)
	}
	path = strings.TrimSuffix(path, "{$}")
	path = reWildcard.ReplaceAllString(path, "sample")
	req, err := http.NewRequestWithContext(t.Context(), method, base+path, nil)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header
}

// reWildcard finds a ServeMux wildcard such as {domain}.
var reWildcard = regexp.MustCompile(`\{[a-z]+\}`)

// TestConsole_RegistersThePublisherAndAssertsTheSignedInPerson is the console's
// half of a registration: the wizard's business step calls, the call carries
// the person the session names rather than anything the browser sent, and the
// home page then opens the console, with nothing in it yet.
//
// What proves it is where the Exchange filed the grant. The payee is listed for
// the signed-in person's issuer and subject and for nobody else, and the
// Exchange refuses an actor whose address is not verified, so the call
// succeeding also proves it carried the verified-address answer. The Exchange
// recorded the registration once in its audit log.
func TestConsole_RegistersThePublisherAndAssertsTheSignedInPerson(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)

	out := f.ok(t, web.APIPublisherPath, map[string]any{"display_name": "Stoa Press"})
	mustContain(t, "the home page once the business is named", f.get2(t, web.HomePath),
		`aria-label="Sections"`, "No domains yet.")
	regs := f.mine(t)
	if !regs.Registered || !strings.HasPrefix(regs.PublisherID, "pub_") || out["publisher_id"] != regs.PublisherID {
		t.Fatalf("the business step answered %v and the Exchange holds %+v for the signed-in person, want one payee",
			out, regs)
	}
	if regs.DisplayName != "Stoa Press" {
		t.Fatalf("the Exchange holds the business name %q", regs.DisplayName)
	}
	someoneElse := verifiedClaims()
	someoneElse.Subject = "sub-2"
	if f.registrationsOf(t, someoneElse).Registered {
		t.Fatal("the payee was granted to a person other than the one signed in")
	}
	if n := f.exchange.CountAuditActions(t, "RegisterPublisher"); n != 1 {
		t.Fatalf("the business step wrote %d RegisterPublisher audit rows, want 1", n)
	}
}

// TestConsole_RefusesAWizardCallFromAnUnverifiedAddress closes the gap a JSON
// surface opens. The pages redirect such a person to an explanation; a call
// cannot be redirected usefully, because fetch follows the redirect and hands
// the script the explanation page as its answer.
//
// The person named their business from a sign-in whose address was verified,
// so a lookup from this sign-in would otherwise go on to have the Exchange read
// the domain's document. Two checks stand in the way: the console's address
// gate, which answers the script 403, and the Exchange, which refuses an actor
// whose address is not verified. The status shows that the console's gate
// answered; the document would be read only if both checks were gone.
func TestConsole_RefusesAWizardCallFromAnUnverifiedAddress(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.registerBusiness(t)
	claims := verifiedClaims()
	claims.EmailVerified = false
	unverified := f.otherPerson(t, claims)

	status, out := unverified.api(t, web.APILookupPath, map[string]any{"domain": "stoa.example"})
	if status != http.StatusForbidden {
		t.Fatalf("an unverified address = %d, want %d", status, http.StatusForbidden)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "email") {
		t.Fatalf("the refusal does not say why: %v", out)
	}
	if n := f.exchange.Manifests.Calls(); n != 0 {
		t.Fatalf("a refused call made the Exchange read a document %d times", n)
	}
}

// TestConsole_RefusesAWizardCallWithoutASession is the same gap one level up.
// A session that ended must answer the script, not send it to the login
// provider whose sign-in page it would read as the answer.
func TestConsole_RefusesAWizardCallWithoutASession(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	// No sign-in at all, so no session cookie.
	status, out := f.api(t, web.APILookupPath, map[string]any{"domain": "stoa.example"})
	if status != http.StatusUnauthorized {
		t.Fatalf("no session = %d, want %d", status, http.StatusUnauthorized)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "sign in") {
		t.Fatalf("the refusal does not say what to do: %v", out)
	}
}

// TestConsole_PublishesItsOwnKeyDirectory is the other half of how the Exchange
// trusts this console. The console signs with its private key; the Exchange
// reads the public one from here. The document must be reachable without a
// session, because the Exchange has none.
func TestConsole_PublishesItsOwnKeyDirectory(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	// A client with no cookies at all: the Exchange is not signed in and never
	// will be.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.server.URL+app.DirectoryPath, nil)
	if err != nil {
		t.Fatalf("build the key directory request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET the key directory: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the key directory answered %d", resp.StatusCode)
	}
	raw := readAll(t, resp)
	dir, err := forawellknown.ParseWBA([]byte(raw))
	if err != nil {
		t.Fatalf("the published directory does not read back: %v", err)
	}
	if len(dir.GetKeys()) != 1 {
		t.Fatalf("the directory publishes %d keys, want 1", len(dir.GetKeys()))
	}
	// Served as every key directory must be: under the directory media type,
	// with a response signature by the key it lists over the authority the
	// request named. The Exchange hands out only keys that signed the response.
	if ct := resp.Header.Get("Content-Type"); ct != forawellknown.WBAMediaType {
		t.Fatalf("the key directory is served as %q, want %s", ct, forawellknown.WBAMediaType)
	}
	listed, err := forawellknown.PublicKey(dir.GetKeys()[0])
	if err != nil {
		t.Fatal(err)
	}
	authority := strings.TrimPrefix(f.server.URL, "http://")
	verified, err := helpers.VerifyDirectoryResponse(authority, resp.Header, []byte(raw),
		[]ed25519.PublicKey{listed}, time.Now())
	if tp, _ := helpers.Thumbprint(listed); err != nil || !verified[tp] {
		t.Fatalf("the directory response is not signed by the key it lists: %v %v", verified, err)
	}
	if strings.Contains(raw, "\"d\"") {
		t.Fatal("the published directory carries a private key")
	}
}

// TestConsole_RefusesADomainThatIsNotYours closes the last enumeration path: a
// hostname somebody else registered is answered the same way an unregistered
// one is.
func TestConsole_RefusesADomainThatIsNotYours(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.registerBusiness(t)

	status, body := f.get(t, "/domains/someone-elses.example")
	if status != http.StatusNotFound {
		t.Fatalf("somebody else's domain = %d, want %d", status, http.StatusNotFound)
	}
	if !strings.Contains(body, "not one of yours") {
		t.Fatalf("the page says something else: %s", head(body))
	}
}

// answer is one response, read and closed: its status, where it redirects
// (nil when it does not), its body, and the request it answered.
type answer struct {
	status   int
	location *url.URL
	body     string
	request  string
}

// signOut submits the sign-out form and returns the console's answer, without
// following a redirect.
func (f *consoleFixture) signOut(t *testing.T) answer {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.server.URL+auth.LogoutPath, http.NoBody)
	if err != nil {
		t.Fatalf("build sign-out: %v", err)
	}
	return f.step(t, req)
}

// step sends one request with this browser's cookies and returns the answer,
// without following a redirect.
func (f *consoleFixture) step(t *testing.T, req *http.Request) answer {
	t.Helper()
	resp, err := f.noRedirect().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	a := answer{status: resp.StatusCode, body: readAll(t, resp), request: req.Method + " " + req.URL.String()}
	if loc, err := resp.Location(); err == nil {
		a.location = loc
	}
	return a
}

// redirectTo fails the test unless a is a redirect, and returns where to.
func redirectTo(t *testing.T, a answer) *url.URL {
	t.Helper()
	if a.location == nil || a.status < 300 || a.status > 399 {
		t.Fatalf("%s = %d, want a redirect", a.request, a.status)
	}
	return a.location
}

// TestConsole_SignOutEndsTheProviderSessionToo signs in and out. Clearing the
// console's own cookie is not enough: the provider's session would sign the
// person straight back in on the next page. So the console sends the browser
// to the provider's logout address, naming its signed-out address as the way
// back; the provider sends it there, and from there it goes on to sign in,
// which with the provider's session ended asks for credentials again.
//
// The chain is walked one step at a time because the provider stub signs
// anybody in who reaches its authorize address.
func TestConsole_SignOutEndsTheProviderSessionToo(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	if !f.signedIn(t) {
		t.Fatal("the sign-in did not take")
	}

	loc := redirectTo(t, f.signOut(t))
	if loc.Path != "/end_session" ||
		loc.Query().Get("post_logout_redirect_uri") != f.server.URL+auth.SignedOutPath {
		t.Fatalf("sign out sends the browser to %s, want the provider's logout naming the signed-out address", loc)
	}
	for _, want := range []string{f.server.URL + auth.SignedOutPath, f.server.URL + auth.LoginPath} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, loc.String(), http.NoBody)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if loc = redirectTo(t, f.step(t, req)); loc.String() != want {
			t.Fatalf("%s sends the browser to %s, want %s", req.URL, loc, want)
		}
	}
	if f.signedIn(t) {
		t.Fatal("the console still opens a protected page after sign-out")
	}
}

// TestConsole_SignOutWithoutAProviderLogoutEndsHere signs out through a
// provider that advertises no logout address. Only the console's session can
// be ended, and sending the browser on to sign in would sign it straight back
// in, so the answer is the signed-out page itself.
func TestConsole_SignOutWithoutAProviderLogoutEndsHere(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.upstream.endSessionBase = ""
	f.signIn(t)

	out := f.signOut(t)
	if out.status != http.StatusOK {
		t.Fatalf("sign out = %d, want the signed-out page", out.status)
	}
	mustContain(t, "the signed-out page", out.body, "You are signed out.", `href="/login"`)
	if f.signedIn(t) {
		t.Fatal("the console still opens a protected page after sign-out")
	}
}
