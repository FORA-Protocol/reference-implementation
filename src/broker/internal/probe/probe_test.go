package probe_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	sharedtest "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/probe"
)

// rewritingClient rewrites an https request targeting domain "X" to hit the
// given testutil.Origin base URL, so probe can be exercised over loopback.
type rewritingClient struct {
	base string
}

func (c *rewritingClient) Do(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(c.base + req.URL.Path)
	if err != nil {
		return nil, err
	}
	req.URL = u
	req.Host = u.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestProbe_PresentManifest(t *testing.T) {
	origin := testutil.NewOrigin([]byte(`{"ver":"1.0","role":"ROLE_PUBLISHER","domain":"acme.example","exchanges":[{"domain":"mp.example","endpoint":"https://mp.example/fora","relationship":"PROVIDER_RELATIONSHIP_DIRECT"}],"supported_profiles":["fora-news-v1"]}`))
	defer origin.Close()

	p := probe.New(&rewritingClient{base: origin.URL}, sharedtest.DiscardLogger(),
		probe.Options{Scheme: "http", TTL: time.Minute})

	res, err := p.Probe(context.Background(), "acme.example")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got := res.Manifest.Provider; got != "acme.example" {
		t.Errorf("provider = %q, want acme.example", got)
	}
	if len(res.Manifest.Exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(res.Manifest.Exchanges))
	}
	if res.Manifest.Exchanges[0].Endpoint != "https://mp.example/fora" {
		t.Errorf("endpoint = %q", res.Manifest.Exchanges[0].Endpoint)
	}
}

func TestProbe_404ReturnsErrManifestMissing(t *testing.T) {
	origin := testutil.NewOrigin(nil)
	origin.SetManifestStatus(http.StatusNotFound)
	defer origin.Close()

	p := probe.New(&rewritingClient{base: origin.URL}, sharedtest.DiscardLogger(),
		probe.Options{Scheme: "http", TTL: time.Minute})

	_, err := p.Probe(context.Background(), "unlicensed.example")
	if !errors.Is(err, probe.ErrManifestMissing) {
		t.Fatalf("Probe err = %v, want ErrManifestMissing", err)
	}
}

func TestProbe_MalformedReturnsErrProbeFailed(t *testing.T) {
	origin := testutil.NewOrigin([]byte(`not-json`))
	defer origin.Close()

	p := probe.New(&rewritingClient{base: origin.URL}, sharedtest.DiscardLogger(),
		probe.Options{Scheme: "http", TTL: time.Minute})

	_, err := p.Probe(context.Background(), "bad.example")
	if !errors.Is(err, probe.ErrProbeFailed) {
		t.Fatalf("Probe err = %v, want ErrProbeFailed", err)
	}
	if errors.Is(err, probe.ErrManifestMissing) {
		t.Fatal("malformed body must not surface as ErrManifestMissing")
	}
}

func TestProbe_UsesMemCache(t *testing.T) {
	origin := testutil.NewOrigin([]byte(`{"ver":"1.0","role":"ROLE_PUBLISHER","domain":"cache.example","exchanges":[]}`))
	defer origin.Close()

	p := probe.New(&rewritingClient{base: origin.URL}, sharedtest.DiscardLogger(),
		probe.Options{Scheme: "http", TTL: time.Minute})

	for range 3 {
		if _, err := p.Probe(context.Background(), "cache.example"); err != nil {
			t.Fatalf("Probe: %v", err)
		}
	}
	if origin.Hits() != 1 {
		t.Errorf("expected 1 upstream hit, got %d", origin.Hits())
	}
}

func TestProbe_EmptyDomain(t *testing.T) {
	p := probe.New(http.DefaultClient, sharedtest.DiscardLogger(), probe.Options{})
	_, err := p.Probe(context.Background(), "   ")
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("want ErrInvalidDomain, got %v", err)
	}
}

// TestProbe_RefusedVersionReturnsErrManifestVersionRefused pins the third outcome the
// prober tells apart, and the reason it is not the second.
//
// A publisher whose manifest carries a document version this Broker does not
// implement has answered: the document was fetched and read, and it will carry
// the same version on every retry. Folded into ErrProbeFailed it would reach an
// agent as TEMPORARILY_UNAVAILABLE, and the agent would retry a refusal that
// cannot change. Both siblings are named in the assertions because the defect
// is not "no error" but "the wrong one of three".
func TestProbe_RefusedVersionReturnsErrManifestVersionRefused(t *testing.T) {
	for name, body := range map[string]string{
		// Well-formed in every other respect, so the refusal can only be the version.
		"another major": `{"ver":"2.0","role":"ROLE_PUBLISHER","domain":"future.example"}`,
		"no version":    `{"role":"ROLE_PUBLISHER","domain":"future.example"}`,
	} {
		t.Run(name, func(t *testing.T) {
			origin := testutil.NewOrigin([]byte(body))
			defer origin.Close()

			p := probe.New(&rewritingClient{base: origin.URL}, sharedtest.DiscardLogger(),
				probe.Options{Scheme: "http", TTL: time.Minute})

			_, err := p.Probe(context.Background(), "future.example")
			if !errors.Is(err, probe.ErrManifestVersionRefused) {
				t.Fatalf("Probe err = %v, want ErrManifestVersionRefused", err)
			}
			if errors.Is(err, probe.ErrProbeFailed) {
				t.Error("a refused version must not surface as ErrProbeFailed — an agent would retry it")
			}
			if errors.Is(err, probe.ErrManifestMissing) {
				t.Error("a refused version must not surface as ErrManifestMissing — the document exists")
			}
		})
	}
}

// TestProbe_BrokenDocumentReturnsErrManifestUnusable pins the fourth outcome the
// prober tells apart, and the reason it is not the outage one.
//
// A manifest that arrived and broke a document rule has answered as finally as a
// refused version has: the origin serves those bytes, and the next fetch returns
// them again. Folded into ErrProbeFailed it reaches an agent as
// TEMPORARILY_UNAVAILABLE, so the Broker re-probes a publisher whose manifest
// names the wrong role for as long as it stays wrong. The siblings are named in
// the assertions because the defect is not "no error" but "the wrong one of four".
func TestProbe_BrokenDocumentReturnsErrManifestUnusable(t *testing.T) {
	for name, body := range map[string]string{
		// Reads cleanly and is refused only on the role it declares.
		"wrong role": `{"ver":"1.0","role":"ROLE_EXCHANGE","domain":"mp.example"}`,
		// Valid JSON at an accepted version, refused on the wire shape.
		"broken shape": `{"ver":"1.0","role":"ROLE_PUBLISHER","domain":"acme.example",` +
			`"exchanges":[{"domain":"mp.example","endpoint":"not-a-url","relationship":"nonsense"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			origin := testutil.NewOrigin([]byte(body))
			defer origin.Close()

			p := probe.New(&rewritingClient{base: origin.URL}, sharedtest.DiscardLogger(),
				probe.Options{Scheme: "http", TTL: time.Minute})

			_, err := p.Probe(context.Background(), "acme.example")
			if !errors.Is(err, probe.ErrManifestUnusable) {
				t.Fatalf("Probe err = %v, want ErrManifestUnusable", err)
			}
			if errors.Is(err, probe.ErrProbeFailed) {
				t.Error("a broken document must not surface as ErrProbeFailed — an agent would retry it")
			}
			if errors.Is(err, probe.ErrManifestMissing) {
				t.Error("a broken document must not surface as ErrManifestMissing — the document exists")
			}
			if errors.Is(err, probe.ErrManifestVersionRefused) {
				t.Error("a broken document must not surface as ErrManifestVersionRefused — " +
					"the version was accepted and the refusal is about the rest of the document")
			}
		})
	}
}

// TestProbe_UnparsableBodyStaysAnOutage is the other half of the split above, and
// the one that keeps ErrManifestUnusable honest.
//
// A body that never parsed says nothing about what the publisher serves: a proxy
// answering 200 with an error page produces exactly this, and the next fetch may
// return the real document. Classifying it as a verdict would refuse the
// publisher permanently on someone else's hiccup.
func TestProbe_UnparsableBodyStaysAnOutage(t *testing.T) {
	origin := testutil.NewOrigin([]byte("<html><body>502 Bad Gateway</body></html>"))
	defer origin.Close()

	p := probe.New(&rewritingClient{base: origin.URL}, sharedtest.DiscardLogger(),
		probe.Options{Scheme: "http", TTL: time.Minute})

	_, err := p.Probe(context.Background(), "acme.example")
	if !errors.Is(err, probe.ErrProbeFailed) {
		t.Fatalf("Probe err = %v, want ErrProbeFailed", err)
	}
	if errors.Is(err, probe.ErrManifestUnusable) {
		t.Error("an unparsable body must not surface as ErrManifestUnusable — " +
			"the publisher would be refused permanently for a proxy's error page")
	}
}
