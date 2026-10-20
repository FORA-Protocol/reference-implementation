//go:build integration

package app_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/session"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/app"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// The deployment every test runs as. The console generates documents naming
// these, and the Exchange refuses documents that do not, so the two are
// configured from the same constants.
const (
	testExchangeDomain = "exchange.example"
	testContributor    = "catalog-contributor.exchange.example"
	// testIssuer is the login provider the console trusts.
	testIssuer = "https://login.example"
)

// fakeUpstream is the login provider. Only the port is faked; everything the
// console does with the claims is the real code.
type fakeUpstream struct {
	claims oidcup.Claims
	// authorizeBase is where the browser is sent. The test's client follows it
	// to the stub's own endpoint, which redirects straight back to the console
	// with a code, the way a provider does once a person has signed in.
	authorizeBase string
	// endSessionBase is the stub's logout address, empty for a provider that
	// advertises none.
	endSessionBase string
	// issuer is the provider's issuer, the only one a session may carry.
	issuer string
}

func (f *fakeUpstream) AuthCodeURL(state, _, _ string) string {
	return f.authorizeBase + "?state=" + url.QueryEscape(state)
}

func (f *fakeUpstream) Issuer() string { return f.issuer }

func (f *fakeUpstream) LogoutURL(returnTo string) string {
	if f.endSessionBase == "" {
		return ""
	}
	return f.endSessionBase + "?post_logout_redirect_uri=" + url.QueryEscape(returnTo)
}

func (f *fakeUpstream) Exchange(_ context.Context, code, _, _ string) (oidcup.Claims, error) {
	if code == "" {
		return oidcup.Claims{}, oidcup.ErrExchange
	}
	return f.claims, nil
}

// consoleFixture is the console under test and the real Exchange it talks to.
//
// Both run over one database, each in its own schema. The console signs every
// call with its own key, the Exchange resolves that key from a directory
// publishing it, and the Exchange's answers come from the production service
// and repositories. The only fakes are the login provider, DNS, and what a
// publisher's site serves -- all hosts a test does not own.
type consoleFixture struct {
	server   *httptest.Server
	client   *http.Client
	exchange *exchangetest.Exchange
	// xclient is the client the console itself signs with. Tests read the
	// Exchange's state back through it: the same client, the same public
	// route, the same signature the console uses on every page.
	xclient  *exchangeclient.Client
	upstream *fakeUpstream
	// claims is the person this fixture's browser signs in as.
	claims oidcup.Claims
	// publisherID is the payee the wizard's first step answered with, once it
	// has run.
	publisherID string
	// config is what the console was built from, for a test that starts a
	// second console beside it.
	config app.Config
}

// option tunes the fixture for one test.
type option func(*app.Config, *exchangetest.Exchange)

// withDetector installs the lookup the wizard's domain step uses.
func withDetector(d wizard.Detector) option {
	return func(c *app.Config, _ *exchangetest.Exchange) { c.Detector = d }
}

// withPages installs the reader the lookup takes response headers from.
func withPages(p app.SiteReader) option {
	return func(c *app.Config, _ *exchangetest.Exchange) { c.Pages = p }
}

// startConsole brings the console up over a real database, in front of a real
// Exchange over the same database.
func startConsole(t *testing.T, claims oidcup.Claims, opts ...option) *consoleFixture {
	t.Helper()
	return startConsoleOver(t, claims, func(*exchangetest.Options) {}, opts...)
}

// startConsoleOver is startConsole in front of an Exchange whose deployment the
// test adjusts first: its denomination, or a seat its operator never seeded.
func startConsoleOver(
	t *testing.T, claims oidcup.Claims, deployment func(*exchangetest.Options), opts ...option,
) *consoleFixture {
	t.Helper()
	pool := acquireTestDB(t)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("console ed25519 gen: %v", err)
	}
	// The console serves its own directory too, and a test below covers it. The
	// Exchange needs the address before the console exists, though, so the
	// same public key is published from a directory of its own here.
	directory := exchangetest.StartDirectory(t, priv)
	exOpts := exchangetest.Options{
		DirectoryURL:    directory,
		ExchangeDomain:  testExchangeDomain,
		ContributorHost: testContributor,
	}
	deployment(&exOpts)
	ex := exchangetest.Start(t, pool, exOpts)
	client, err := exchangeclient.New(exchangeclient.Config{
		BaseURL: ex.URL, Directory: directory, SigningKey: priv,
	})
	if err != nil {
		t.Fatalf("exchange client: %v", err)
	}
	// The provider every fixture signs people in through: the issuer of
	// verifiedClaims.
	upstream := &fakeUpstream{claims: claims, issuer: testIssuer}
	cfg := consoleConfig(t, pool, upstream, client, priv)
	// The listener comes first: the console names its own address to the
	// provider when a person signs out, so it is built knowing it, and so is an
	// option that sets up a provider client redirecting back to it.
	server := httptest.NewUnstartedServer(nil)
	cfg.PublicOrigin = "http://" + server.Listener.Addr().String()
	for _, opt := range opts {
		opt(&cfg, ex)
	}
	handler, err := app.Build(cfg)
	if err != nil {
		t.Fatalf("build console: %v", err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)

	// The provider stub redirects back to the console's callback with a code,
	// which is what a real one does once a person has signed in, and back to
	// the post-logout address it is given, which is what one does once it has
	// ended the person's session there.
	stub := startProviderStub(t, server.URL)
	upstream.authorizeBase, upstream.endSessionBase = stub+"/authorize", stub+"/end_session"
	return &consoleFixture{
		server: server, client: newBrowser(t), exchange: ex, xclient: client,
		upstream: upstream, claims: claims, config: cfg,
	}
}

// consoleConfig is the console's composition for one test.
func consoleConfig(
	t *testing.T, pool *pgxpool.Pool, upstream *fakeUpstream,
	client *exchangeclient.Client, priv ed25519.PrivateKey,
) app.Config {
	t.Helper()
	key := make([]byte, session.KeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("session key: %v", err)
	}
	return app.Config{
		Logger:     testutil.DiscardLogger(),
		Accounts:   repo.NewAccountRepo(sqlc.New(pool)),
		Upstream:   upstream,
		SessionKey: key,
		Exchange:   client,
		SigningKey: priv,
		Deployment: setup.Deployment{
			ExchangeDomain:    testExchangeDomain,
			ExchangeEndpoint:  "https://" + testExchangeDomain,
			ContributorHost:   testContributor,
			SupportedProfiles: []string{"fora-news-v1"},
		},
		SupportEmail: "help@exchange.example",
		// Detection decides whether the wizard goes on at all, so a fixture
		// without one would dead-end every test at the domain step. A test
		// about detection installs its own.
		Detector: newStubDetector(cdn.ProviderCloudflare),
	}
}

// newBrowser is a client that keeps cookies, the way a browser does.
func newBrowser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &http.Client{Jar: jar}
}

// startProviderStub stands in for the provider's hosted pages. Its authorize
// address redirects back to the console with a code and the state it was
// given, the way a provider does after a person signs in. Its end-session
// address redirects to the post-logout address it is given, the way a
// provider does after it ends the person's session there.
func startProviderStub(t *testing.T, consoleURL string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/end_session" {
			http.Redirect(w, r, r.URL.Query().Get("post_logout_redirect_uri"), http.StatusFound)
			return
		}
		state := r.URL.Query().Get("state")
		http.Redirect(w, r, consoleURL+"/auth/callback?code=test-code&state="+url.QueryEscape(state),
			http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// signIn drives a full sign-in as the fixture's person and leaves the session
// in the fixture's jar.
func (f *consoleFixture) signIn(t *testing.T) {
	t.Helper()
	f.upstream.claims = f.claims
	resp := f.do(t, http.MethodGet, "/login", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("sign-in ended at %s with status %d", resp.Request.URL.Path, resp.StatusCode)
	}
}

// otherPerson is a second browser on the same console and the same Exchange,
// signed in as somebody else. It is how a test puts a domain in another
// publisher's hands through the same surfaces the first publisher uses.
func (f *consoleFixture) otherPerson(t *testing.T, claims oidcup.Claims) *consoleFixture {
	t.Helper()
	other := &consoleFixture{
		server: f.server, client: newBrowser(t), exchange: f.exchange, xclient: f.xclient,
		upstream: f.upstream, claims: claims,
	}
	other.signIn(t)
	// The provider answers for whoever signs in next; leave it answering for
	// the fixture's own person.
	f.upstream.claims = f.claims
	return other
}

// do sends one request to the console with the test's context.
func (f *consoleFixture) do(t *testing.T, method, path string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// get fetches one page and returns its status and body.
func (f *consoleFixture) get(t *testing.T, path string) (int, string) {
	t.Helper()
	resp := f.do(t, http.MethodGet, path, nil)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, readAll(t, resp)
}

// get2 fetches a page and returns its body, failing the test on anything but a
// 200. Tests that are about a later step use it so their own body states only
// what they are testing.
func (f *consoleFixture) get2(t *testing.T, path string) string {
	t.Helper()
	status, body := f.get(t, path)
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d", path, status)
	}
	return body
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", resp.Request.URL.Path, err)
	}
	return string(raw)
}

// reBoot finds the wizard's starting state, which the page carries as an
// escaped attribute. The browser reads the same attribute through dataset,
// which un-escapes it the way html.UnescapeString does here.
var reBoot = regexp.MustCompile(`data-boot="([^"]*)"`)

// wizardBoot is the part of that block these tests read back.
type wizardBoot struct {
	Registered   bool   `json:"registered"`
	HasDomains   bool   `json:"has_domains"`
	DisplayName  string `json:"display_name"`
	SupportEmail string `json:"support_email"`
	Currency     string `json:"currency"`
	Unit         string `json:"unit"`
	Steps        []struct {
		Key   string `json:"key"`
		Title string `json:"title"`
	} `json:"steps"`
	// Terms is what the licensing step opens on, in the shape the last step
	// sends back.
	Terms map[string]any `json:"terms"`
	// Resume is the saved domain the page reopens, on the setup page only.
	Resume *struct {
		Domain                    string `json:"domain"`
		CDN                       string `json:"cdn"`
		NetworkLabel              string `json:"network_label"`
		Paths                     string `json:"content_paths"`
		TermsSet                  bool   `json:"terms_set"`
		ImportReplacesManualTerms bool   `json:"import_replaces_manual_terms"`
	} `json:"resume"`
}

// openWizard loads the add-a-domain page and reads what it starts with.
func (f *consoleFixture) openWizard(t *testing.T) wizardBoot {
	t.Helper()
	return f.bootOn(t, "/domains/new")
}

// bootOn loads a page that draws the wizard and reads what it starts with.
func (f *consoleFixture) bootOn(t *testing.T, path string) wizardBoot {
	t.Helper()
	page := f.get2(t, path)
	m := reBoot.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no wizard state in the page: %s", head(page))
	}
	var boot wizardBoot
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &boot); err != nil {
		t.Fatalf("wizard state is not JSON: %v", err)
	}
	return boot
}

// api makes one of the wizard's calls and returns the status and the decoded
// answer. It is the surface these tests drive, because it is the surface the
// page drives.
func (f *consoleFixture) api(t *testing.T, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode %s body: %v", path, err)
	}
	resp := f.do(t, http.MethodPost, path, strings.NewReader(string(raw)))
	defer func() { _ = resp.Body.Close() }()
	text := readAll(t, resp)
	out := map[string]any{}
	if text != "" {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("POST %s answered %d with %s", path, resp.StatusCode, head(text))
		}
	}
	return resp.StatusCode, out
}

// ok makes one call and fails the test unless it succeeded. A test about a
// later step uses it so its own body states only what it is testing.
func (f *consoleFixture) ok(t *testing.T, path string, body any) map[string]any {
	t.Helper()
	status, out := f.api(t, path, body)
	if status != http.StatusOK {
		t.Fatalf("POST %s = %d: %v", path, status, out["error"])
	}
	return out
}

// head shortens a page for a failure message.
func head(s string) string {
	if len(s) > 400 {
		return s[:400]
	}
	return s
}

// claimsFor is a person the provider the console trusts signed in, with an
// address it checked. Every person a fixture signs in is built here, because
// the console refuses claims from any other issuer.
func claimsFor(subject, email, name string) oidcup.Claims {
	return oidcup.Claims{Issuer: testIssuer, Subject: subject, Email: email, EmailVerified: true, Name: name}
}

// verifiedClaims is the ordinary case: a provider that checked the address.
func verifiedClaims() oidcup.Claims {
	return claimsFor("sub-1", "editor@publisher.example", "Ada")
}
