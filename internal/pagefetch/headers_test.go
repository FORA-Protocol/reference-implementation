package pagefetch

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/guardedfetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/publicerr"
)

// These tests reach a real origin over HTTP. The guarded client refuses a
// loopback address, which is what an httptest origin is, so the fetcher here is
// built over a plain client carrying this package's own redirect rule: every
// redirect decision under test is made by the code under test, and only the
// address check is left out.

// origin is a site on loopback that records which paths were asked for and the
// User-Agent each request came with.
type origin struct {
	*httptest.Server
	mu     sync.Mutex
	hits   map[string]int
	agents []string
}

func newOrigin(t *testing.T, routes map[string]http.HandlerFunc) *origin {
	t.Helper()
	o := &origin{hits: map[string]int{}}
	o.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.hits[r.URL.Path]++
		o.agents = append(o.agents, r.UserAgent())
		o.mu.Unlock()
		if h, ok := routes[r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(o.Close)
	return o
}

// domain is the site's name as a publisher would type it: host and port.
func (o *origin) domain() string { return o.Listener.Addr().String() }

func (o *origin) hitsOn(path string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.hits[path]
}

// fetcherFor builds a fetcher that reaches the loopback origin over plain http,
// following redirects only where this package decides to.
func fetcherFor(t *testing.T) *Fetcher {
	t.Helper()
	f, err := New(&http.Client{CheckRedirect: guardedfetch.RefuseRedirect}, Limits{Scheme: "http"})
	if err != nil {
		t.Fatalf("new fetcher: %v", err)
	}
	return f
}

func redirectTo(location string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", location)
		w.Header().Set("X-Served-By", "the redirect")
		w.WriteHeader(http.StatusFound)
	}
}

func servedBy(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Served-By", name)
		_, _ = w.Write([]byte("<html><title>home</title></html>"))
	}
}

// The home page answering with a redirect to another page of the same site is
// the ordinary http-to-https case, and its headers are on the other side.
func TestHomeHeadersFollowsOneRedirectOnTheSameSite(t *testing.T) {
	var o *origin
	o = newOrigin(t, map[string]http.HandlerFunc{
		"/": func(w http.ResponseWriter, r *http.Request) {
			redirectTo("http://"+o.domain()+"/home")(w, r)
		},
		"/home": servedBy("the page behind the redirect"),
	})
	header, err := fetcherFor(t).HomeHeaders(t.Context(), o.domain())
	if err != nil {
		t.Fatalf("HomeHeaders: %v", err)
	}
	if got := header.Get("X-Served-By"); got != "the page behind the redirect" {
		t.Fatalf("headers came from %q, want the page behind the redirect", got)
	}
	if o.hitsOn("/home") != 1 {
		t.Fatalf("the redirect target was asked %d times, want once", o.hitsOn("/home"))
	}
}

// A relative Location is the same site by construction, and a site that
// redirects its apex to a locale path writes it that way. It is resolved
// against the home page and followed like an absolute one.
func TestHomeHeadersFollowsARelativeRedirect(t *testing.T) {
	o := newOrigin(t, map[string]http.HandlerFunc{
		"/":    redirectTo("/en/"),
		"/en/": servedBy("the page behind the relative redirect"),
	})
	header, err := fetcherFor(t).HomeHeaders(t.Context(), o.domain())
	if err != nil {
		t.Fatalf("HomeHeaders: %v", err)
	}
	if got := header.Get("X-Served-By"); got != "the page behind the relative redirect" {
		t.Fatalf("headers came from %q, want the page behind the relative redirect", got)
	}
	if o.hitsOn("/en/") != 1 {
		t.Fatalf("the relative redirect target was asked %d times, want once", o.hitsOn("/en/"))
	}
}

// A redirect whose target does not answer still answered itself, from this
// site's network. Its headers are the answer rather than the failure.
func TestHomeHeadersKeepsTheRedirectsHeadersWhenTheHopFails(t *testing.T) {
	o := newOrigin(t, map[string]http.HandlerFunc{
		"/": redirectTo("/gone"),
		"/gone": func(w http.ResponseWriter, _ *http.Request) {
			// Drop the connection: the hop has no answer at all.
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
		},
	})
	header, err := fetcherFor(t).HomeHeaders(t.Context(), o.domain())
	if err != nil {
		t.Fatalf("HomeHeaders over a redirect whose target fails: %v", err)
	}
	if got := header.Get("X-Served-By"); got != "the redirect" {
		t.Fatalf("headers came from %q, want the redirect's own", got)
	}
}

// One hop, not a chain: the page behind the first redirect answering with
// another redirect is where the answer stops, and that redirect's own headers
// are what comes back.
func TestHomeHeadersFollowsNoSecondRedirect(t *testing.T) {
	var o *origin
	o = newOrigin(t, map[string]http.HandlerFunc{
		"/": func(w http.ResponseWriter, r *http.Request) {
			redirectTo("http://"+o.domain()+"/first")(w, r)
		},
		"/first": func(w http.ResponseWriter, r *http.Request) {
			redirectTo("http://"+o.domain()+"/second")(w, r)
		},
		"/second": servedBy("a second hop"),
	})
	header, err := fetcherFor(t).HomeHeaders(t.Context(), o.domain())
	if err != nil {
		t.Fatalf("HomeHeaders over two redirects: %v", err)
	}
	if got := header.Get("X-Served-By"); got != "the redirect" {
		t.Fatalf("headers came from %q, want the second hop's redirect", got)
	}
	if o.hitsOn("/second") != 0 {
		t.Fatal("a second redirect was followed")
	}
}

// The question is which network serves THIS site, so a redirect to another
// site is not followed. The redirect itself was this site's answer, so its own
// headers are what comes back.
func TestHomeHeadersDoesNotFollowARedirectOffTheSite(t *testing.T) {
	other := newOrigin(t, map[string]http.HandlerFunc{"/": servedBy("another site")})
	o := newOrigin(t, map[string]http.HandlerFunc{
		"/": redirectTo(other.URL + "/"),
	})
	header, err := fetcherFor(t).HomeHeaders(t.Context(), o.domain())
	if err != nil {
		t.Fatalf("HomeHeaders over an off-site redirect: %v", err)
	}
	if got := header.Get("X-Served-By"); got != "the redirect" {
		t.Fatalf("headers came from %q, want the redirect's own", got)
	}
	if other.hitsOn("/") != 0 {
		t.Fatal("the other site was asked for its headers")
	}
}

// An address carrying credentials is refused before any request is made, and
// the refusal does not repeat the password. ProbeAs and Get take an address
// from their caller, so both are driven here.
func TestAnAddressCarryingCredentialsIsRefusedWithoutEchoingThem(t *testing.T) {
	o := newOrigin(t, map[string]http.HandlerFunc{"/": servedBy("home")})
	address := "http://editor:s3cret-pass@" + o.domain() + "/"
	f := fetcherFor(t)

	_, probeErr := f.ProbeAs(t.Context(), address, "ExampleAgent/1.0")
	_, getErr := f.Get(t.Context(), address, 1<<10, "text/html")
	for name, err := range map[string]error{"ProbeAs": probeErr, "Get": getErr} {
		if err == nil {
			t.Fatalf("%s sent a request to an address carrying credentials", name)
		}
		if publicerr.KindOf(err) != publicerr.KindInvalidRequest {
			t.Fatalf("%s refused it as %v, want an invalid request", name, publicerr.KindOf(err))
		}
		if strings.Contains(err.Error(), "s3cret-pass") {
			t.Fatalf("%s echoed the password: %v", name, err)
		}
	}
	if o.hitsOn("/") != 0 {
		t.Fatal("the origin was asked, so the credentials went on the wire")
	}
}

// A redirect is an answer to the question ProbeAs asks: the edge package refuses
// with a status of its own, so a site that redirects instead did not refuse.
// The probe reports the redirect's status and headers rather than an error.
func TestProbeAsReportsARefusedRedirectAsItsAnswer(t *testing.T) {
	o := newOrigin(t, map[string]http.HandlerFunc{
		"/article": redirectTo("https://login.example/"),
	})
	probe, err := fetcherFor(t).ProbeAs(t.Context(), o.URL+"/article", "ExampleAgent/1.0")
	if err != nil {
		t.Fatalf("ProbeAs over a redirect: %v", err)
	}
	if probe.Status != http.StatusFound {
		t.Fatalf("status = %d, want the redirect's %d", probe.Status, http.StatusFound)
	}
	if got := probe.Header.Get("Location"); got != "https://login.example/" {
		t.Fatalf("Location = %q, want the redirect's own", got)
	}
	o.mu.Lock()
	agents := slices.Clone(o.agents)
	o.mu.Unlock()
	if len(agents) != 1 || agents[0] != "ExampleAgent/1.0" {
		t.Fatalf("the site saw User-Agents %q, want one request as ExampleAgent/1.0", agents)
	}
}

// A caller that asks a site something itself, as the console's package check
// does, builds the address with SiteURL. The address is in the scheme the
// fetcher reads sites with, so on a local stack, which speaks plain http, the
// question reaches the site; with no scheme configured it is https.
func TestSiteURLAddressesTheSiteInTheFetchersScheme(t *testing.T) {
	o := newOrigin(t, map[string]http.HandlerFunc{"/articles/": servedBy("the origin")})
	f := fetcherFor(t)
	target := f.SiteURL(o.domain(), "/articles/")
	if want := "http://" + o.domain() + "/articles/"; target != want {
		t.Fatalf("SiteURL = %q, want %q", target, want)
	}
	probe, err := f.ProbeAs(t.Context(), target, "ExampleAgent/1.0")
	if err != nil {
		t.Fatalf("ProbeAs at %q: %v", target, err)
	}
	if probe.Status != http.StatusOK || o.hitsOn("/articles/") != 1 {
		t.Fatalf("the probe at %q did not reach the site: status %d", target, probe.Status)
	}

	unset, err := New(&http.Client{}, Limits{})
	if err != nil {
		t.Fatalf("new fetcher: %v", err)
	}
	got := unset.SiteURL("publisher.example", "/.well-known/fora.json")
	if want := "https://publisher.example/.well-known/fora.json"; got != want {
		t.Fatalf("with no scheme configured, SiteURL = %q, want %q", got, want)
	}
}
