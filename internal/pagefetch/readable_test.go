package pagefetch

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// fetcherWithScheme builds a fetcher that reaches sites over the given scheme.
func fetcherWithScheme(t *testing.T, scheme string) *Fetcher {
	t.Helper()
	f, err := New(noopDoer{}, Limits{Scheme: scheme})
	if err != nil {
		t.Fatalf("new fetcher: %v", err)
	}
	return f
}

// A robots.txt written before the site moved to https still names its sitemap
// as http, and the site answers that address with a redirect. The guarded
// client dials https only, so the pointer is asked for over https rather than
// refused: the Guardian names both of its sitemaps that way.
func TestAnHTTPPointerIsAskedForOverHTTPS(t *testing.T) {
	f := fetcherWithScheme(t, "https")
	address, warning, ok := f.readable("http://www.theguardian.com/sitemaps/news.xml", "theguardian.com", "a sitemap")
	if !ok {
		t.Fatalf("the pointer was refused: %s", warning)
	}
	if address != "https://www.theguardian.com/sitemaps/news.xml" {
		t.Fatalf("address = %q, want the https form of the same address", address)
	}
}

// A deployment configured for plain http means an origin that really speaks it,
// and rewriting a pointer there would send the request where nothing answers.
func TestAPointerIsLeftAloneWhenTheDeploymentSpeaksHTTP(t *testing.T) {
	f := fetcherWithScheme(t, "http")
	address, _, ok := f.readable("http://127.0.0.1:8080/sitemap.xml", "127.0.0.1:8080", "a sitemap")
	if !ok {
		t.Fatal("the pointer was refused")
	}
	if address != "http://127.0.0.1:8080/sitemap.xml" {
		t.Fatalf("address = %q, want it untouched", address)
	}
}

// Only http rises. An https pointer is never lowered, whatever is configured.
func TestAnHTTPSPointerIsNeverLowered(t *testing.T) {
	for _, scheme := range []string{"https", "http"} {
		f := fetcherWithScheme(t, scheme)
		address, _, ok := f.readable("https://x.example/sitemap.xml", "x.example", "a sitemap")
		if !ok {
			t.Fatalf("scheme %q: the pointer was refused", scheme)
		}
		if address != "https://x.example/sitemap.xml" {
			t.Fatalf("scheme %q: address = %q, want it untouched", scheme, address)
		}
	}
}

// The upgrade never reaches a pointer at another host: that one is refused
// before the scheme is considered at all.
func TestAPointerAtAnotherHostIsStillRefused(t *testing.T) {
	f := fetcherWithScheme(t, "https")
	if _, warning, ok := f.readable("http://elsewhere.example/sitemap.xml", "x.example", "a sitemap"); ok {
		t.Fatal("a pointer at another host was accepted")
	} else if warning == "" {
		t.Fatal("no warning explaining the refusal")
	}
}

// Credentials in a pointer are refused, and never carried into the upgraded
// address either.
func TestAPointerCarryingCredentialsIsRefused(t *testing.T) {
	f := fetcherWithScheme(t, "https")
	_, warning, ok := f.readable("http://user:secret@x.example/sitemap.xml", "x.example", "a sitemap")
	if ok {
		t.Fatal("a pointer carrying credentials was accepted")
	}
	if warning == "" {
		t.Fatal("no warning explaining the refusal")
	}
	if strings.Contains(warning, "secret") {
		t.Fatalf("the password was repeated back: %q", warning)
	}
}

// secureForm works on a parsed address and leaves everything but the scheme.
func TestTheUpgradeKeepsTheRestOfTheAddress(t *testing.T) {
	f := fetcherWithScheme(t, "https")
	parsed, err := url.Parse("http://x.example:8443/a/b?c=d#e")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := f.secureForm(parsed); got != "https://x.example:8443/a/b?c=d#e" {
		t.Fatalf("secureForm = %q", got)
	}
}

// noopDoer stands in for the guarded client: these tests reach no network.
type noopDoer struct{}

func (noopDoer) Do(*http.Request) (*http.Response, error) { panic("no request is made") }
