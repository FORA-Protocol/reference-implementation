package cdn_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn/cdntest"
)

func newDetector(t *testing.T, fixture cdntest.Fixture) *cdn.Detector {
	t.Helper()
	d, err := cdn.NewDetector(fixture, time.Second)
	if err != nil {
		t.Fatalf("new detector: %v", err)
	}
	return d
}

// A detector that quietly resolved through the process's own DNS would be a
// network call this package never declared.
func TestNewDetectorRefusesNoResolver(t *testing.T) {
	t.Parallel()
	if _, err := cdn.NewDetector(nil, time.Second); err == nil {
		t.Fatal("a detector was built with nothing to resolve through")
	}
}

func TestDetectFromDelegation(t *testing.T) {
	t.Parallel()
	d := newDetector(t, cdntest.Fixture{
		CNAME: map[string]string{"publisher.example": "d1abc.cloudfront.net."},
	})
	got := d.Detect(t.Context(), "publisher.example", nil)
	if got.Provider != cdn.ProviderCloudFront {
		t.Fatalf("provider = %q, want cloudfront", got.Provider)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].Source != cdn.SourceCNAME {
		t.Fatalf("evidence = %+v", got.Evidence)
	}
}

// An apex record is often flattened, so the delegation only shows on the www
// form. Looking at both is what finds it.
func TestDetectFallsBackToTheWWWForm(t *testing.T) {
	t.Parallel()
	d := newDetector(t, cdntest.Fixture{
		CNAME: map[string]string{"www.publisher.example": "publisher.example.global.fastly.net"},
	})
	got := d.Detect(t.Context(), "publisher.example", nil)
	if got.Provider != cdn.ProviderFastly {
		t.Fatalf("provider = %q, want fastly", got.Provider)
	}
	if got.Evidence[0].Name != "www.publisher.example" {
		t.Fatalf("evidence named %q, want the www form", got.Evidence[0].Name)
	}
}

// A host already in www form is not asked about www.www.
func TestDetectDoesNotDoubleThePrefix(t *testing.T) {
	t.Parallel()
	d := newDetector(t, cdntest.Fixture{
		CNAME: map[string]string{"www.www.publisher.example": "d1abc.cloudfront.net"},
	})
	if got := d.Detect(t.Context(), "www.publisher.example", nil); got.Provider != cdn.ProviderNone {
		t.Fatalf("provider = %q, want none — www.www was resolved", got.Provider)
	}
}

func TestDetectFromHeaders(t *testing.T) {
	t.Parallel()
	d := newDetector(t, cdntest.Fixture{})
	h := http.Header{}
	h.Set("cf-ray", "8f2a1b3c4d5e6f70-FRA")
	got := d.Detect(t.Context(), "publisher.example", h)
	if got.Provider != cdn.ProviderCloudflare {
		t.Fatalf("provider = %q, want cloudflare", got.Provider)
	}
}

func TestDetectFromNameServers(t *testing.T) {
	t.Parallel()
	d := newDetector(t, cdntest.Fixture{
		NS: map[string][]string{"publisher.example": {"kate.ns.cloudflare.com."}},
	})
	got := d.Detect(t.Context(), "news.publisher.example", nil)
	if got.Provider != cdn.ProviderCloudflare {
		t.Fatalf("provider = %q, want cloudflare from the zone", got.Provider)
	}
	if got.Evidence[0].Name != "publisher.example" {
		t.Fatalf("evidence named %q, want the registrable zone", got.Evidence[0].Name)
	}
}

// The conflict this order exists for, end to end: Cloudflare answers DNS, and
// CloudFront serves the content. The publisher needs the CloudFront package.
func TestDetectPrefersDelegationOverTheZone(t *testing.T) {
	t.Parallel()
	d := newDetector(t, cdntest.Fixture{
		CNAME: map[string]string{"publisher.example": "d1abc.cloudfront.net."},
		NS:    map[string][]string{"publisher.example": {"kate.ns.cloudflare.com."}},
	})
	got := d.Detect(t.Context(), "publisher.example", nil)
	if got.Provider != cdn.ProviderCloudFront {
		t.Fatalf("provider = %q, want cloudfront", got.Provider)
	}
	if len(got.Evidence) < 2 {
		t.Fatalf("evidence = %+v, want both observations reported", got.Evidence)
	}
}

// Nothing recognised is an answer, not a failure: the publisher is routed to a
// conversation with us.
func TestDetectNoSupportedNetwork(t *testing.T) {
	t.Parallel()
	d := newDetector(t, cdntest.Fixture{
		CNAME: map[string]string{"publisher.example": "origin.hosting.example"},
		NS:    map[string][]string{"publisher.example": {"ns1.name.com."}},
	})
	h := http.Header{}
	h.Set("server", "nginx")
	got := d.Detect(t.Context(), "publisher.example", h)
	if got.Provider != cdn.ProviderNone {
		t.Fatalf("provider = %q, want none", got.Provider)
	}
	if len(got.Evidence) != 0 {
		t.Fatalf("evidence = %+v, want none", got.Evidence)
	}
}

// A lookup that fails contributes nothing rather than failing the detection:
// answering from the other two beats refusing to answer at all.
func TestDetectSurvivesAFailedLookup(t *testing.T) {
	t.Parallel()
	d := newDetector(t, cdntest.Fixture{
		Err:   map[string]error{"publisher.example": errors.New("server misbehaving")},
		CNAME: map[string]string{"www.publisher.example": "d1abc.cloudfront.net."},
	})
	got := d.Detect(t.Context(), "publisher.example", nil)
	if got.Provider != cdn.ProviderCloudFront {
		t.Fatalf("provider = %q, want the answer from the lookups that worked", got.Provider)
	}
}

// The same site always yields the same answer: the three lookups run together,
// but their evidence is ordered by which question it answers, not by which
// finished first.
func TestDetectEvidenceOrderIsStable(t *testing.T) {
	t.Parallel()
	d := newDetector(t, cdntest.Fixture{
		CNAME: map[string]string{
			"publisher.example":     "d1abc.cloudfront.net.",
			"www.publisher.example": "publisher.example.global.fastly.net",
		},
		NS: map[string][]string{"publisher.example": {"kate.ns.cloudflare.com."}},
	})
	for range 10 {
		got := d.Detect(t.Context(), "publisher.example", nil)
		if len(got.Evidence) != 3 {
			t.Fatalf("evidence = %+v, want all three observations", got.Evidence)
		}
		if got.Evidence[0].Name != "publisher.example" || got.Evidence[0].Source != cdn.SourceCNAME {
			t.Fatalf("evidence[0] = %+v, want the site's own delegation first", got.Evidence[0])
		}
		if got.Evidence[1].Name != "www.publisher.example" {
			t.Fatalf("evidence[1] = %+v, want the www delegation second", got.Evidence[1])
		}
		if got.Evidence[2].Source != cdn.SourceNS {
			t.Fatalf("evidence[2] = %+v, want the zone last", got.Evidence[2])
		}
	}
}

// A local deployment addresses its origin with a port. DNS has no port, so the
// detector asks about the host.
func TestDetectStripsAPort(t *testing.T) {
	t.Parallel()
	d := newDetector(t, cdntest.Fixture{
		CNAME: map[string]string{"publisher.example": "d1abc.cloudfront.net."},
	})
	if got := d.Detect(t.Context(), "publisher.example:8443", nil); got.Provider != cdn.ProviderCloudFront {
		t.Fatalf("provider = %q, want cloudfront despite the port", got.Provider)
	}
}
