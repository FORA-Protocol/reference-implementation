//go:build integration

package app_test

import (
	"net/http"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/auth"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
)

// stateChangingRoutes is every POST the console serves: the wizard's calls and
// the sign-out. A route added to the console without being added here is a
// route this test does not prove is protected.
var stateChangingRoutes = []string{
	web.APIPublisherPath,
	web.APILookupPath,
	web.APIFilesPath,
	web.APICheckManifestPath,
	web.APICheckEdgePath,
	web.APIPagePath,
	web.APITermsPreviewPath,
	web.APIRegisterPath,
	web.APIProgressPath,
	auth.LogoutPath,
}

// foreignSite is the page a forgery would be submitted from.
const foreignSite = "https://elsewhere.example"

// crossOriginRefusal is the sentence both refusals carry, the page and the
// JSON one alike.
const crossOriginRefusal = "came from another site"

// TestConsole_RefusesStateChangesFromAnotherSite covers the console's CSRF
// control. Every POST, the sign-out included, is refused when a browser says
// another site caused it, and accepted when the browser says it came from the
// console's own page.
//
// Both ways a browser says where a request came from are covered: the
// Sec-Fetch-Site header every current browser sends, and the Origin header an
// older one sends alone. The absence of a side effect is asserted through the
// same surfaces a person uses: the session still opens the home page, and the
// Exchange still lists no publisher for this person.
func TestConsole_RefusesStateChangesFromAnotherSite(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)

	foreign := map[string]http.Header{
		"sec-fetch-site cross-site": {"Sec-Fetch-Site": {"cross-site"}, "Origin": {foreignSite}},
		"origin only":               {"Origin": {foreignSite}},
	}
	for _, path := range stateChangingRoutes {
		for name, hdr := range foreign {
			t.Run(path+"/"+name, func(t *testing.T) {
				status, body := f.post(t, path, hdr)
				if status != http.StatusForbidden {
					t.Fatalf("a cross-origin POST %s = %d, want %d", path, status, http.StatusForbidden)
				}
				if !strings.Contains(body, crossOriginRefusal) {
					t.Fatalf("the refusal of POST %s does not say why: %s", path, head(body))
				}
			})
		}
	}
	if !f.signedIn(t) {
		t.Fatal("a cross-origin sign-out ended the session")
	}
	if f.mine(t).Registered {
		t.Fatal("a cross-origin call registered a publisher")
	}

	// The same requests from the console's own page reach their routes. The
	// sign-out is last, since it ends the session the others run under.
	same := http.Header{"Sec-Fetch-Site": {"same-origin"}, "Origin": {f.server.URL}}
	for _, path := range stateChangingRoutes {
		status, body := f.post(t, path, same)
		if status == http.StatusForbidden || strings.Contains(body, crossOriginRefusal) {
			t.Fatalf("a same-origin POST %s was refused: %d %s", path, status, head(body))
		}
	}
	if f.signedIn(t) {
		t.Fatal("a same-origin sign-out left the session open")
	}
}

// post sends one POST carrying hdr and the session cookie, without following a
// redirect, so a route's own answer is what the test reads.
func (f *consoleFixture) post(t *testing.T, path string, hdr http.Header) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.server.URL+path,
		strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build POST %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header[http.CanonicalHeaderKey(k)] = v
	}
	resp, err := f.noRedirect().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, readAll(t, resp)
}

// signedIn reports whether this browser's session still opens the home page.
// A visitor without one is redirected to sign in, and that redirect is not
// followed here: the provider stub would sign the person straight back in.
func (f *consoleFixture) signedIn(t *testing.T) bool {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.server.URL+web.HomePath, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", web.HomePath, err)
	}
	resp, err := f.noRedirect().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", web.HomePath, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// noRedirect is this browser's cookies on a client that stops at the first
// answer.
func (f *consoleFixture) noRedirect() *http.Client {
	return &http.Client{
		Jar: f.client.Jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
