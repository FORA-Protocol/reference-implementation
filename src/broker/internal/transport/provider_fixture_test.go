//go:build integration

package transport_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
)

// The publisher manifests the resolve fixtures serve, kept together because they
// are one document written five ways: the working one, the same at an arbitrary
// version or role, one for an arbitrary publisher and exchange, and an origin
// that hosts none at all. Split out of the shared fixture file, which reached the
// test-file line cap.

// startProviderFixture serves a /.well-known/fora.json pointing at the given
// Exchange endpoint. Returns the provider's domain.
func startProviderFixture(tb testing.TB, exchangeEndpoint string) *httptest.Server {
	tb.Helper()
	return startProviderFixtureAtVersion(tb, exchangeEndpoint, "1.0")
}

// startProviderFixtureAtVersion is startProviderFixture with the document
// version the caller names in place of the one a publisher serves.
//
// It exists so a test can drive the consumer-side version gate through the real
// resolve path with every other member exactly as the working fixture serves it
// — the two share one body, so a document staged for that gate differs from the
// accepted one in the version and in nothing else. Pass "" to stage a manifest
// with no version member at all.
func startProviderFixtureAtVersion(tb testing.TB, exchangeEndpoint, ver string) *httptest.Server {
	tb.Helper()
	return startProviderFixtureWith(tb, exchangeEndpoint, ver, "ROLE_PUBLISHER")
}

// startProviderFixtureWith serves acme.example's manifest at an arbitrary
// document version and an arbitrary role, so a test can stage either of the two
// refusals a manifest can earn on its own content: a ver this build does not
// accept, and a document describing a role that is not a publisher's. An empty
// ver omits the member entirely, which is a distinct refusal from an
// unrecognised one.
func startProviderFixtureWith(tb testing.TB, exchangeEndpoint, ver, role string) *httptest.Server {
	tb.Helper()
	verMember := ""
	if ver != "" {
		verMember = `"ver":"` + ver + `",`
	}
	body := `{` + verMember + `"role":"` + role + `","domain":"acme.example","exchanges":[{"domain":"mp.acme.example","endpoint":"` + exchangeEndpoint + `","relationship":"PROVIDER_RELATIONSHIP_DIRECT"}],"supported_profiles":["fora-news-v1"]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != forawellknown.Path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	tb.Cleanup(srv.Close)
	return srv
}

// startProviderFixtureFor serves a /.well-known/fora.json for an ARBITRARY
// publisher domain that names an ARBITRARY exchange domain at the given
// endpoint. It generalises startProviderFixture (which hardcodes
// acme.example / mp.acme.example) for the multi-publisher resolve fixture: the
// second provider must advertise its OWN domain + its OWN exchange so the
// broker's buildRoutePlan routes the second URI to the second exchange,
// producing a SECOND OfferGroup distinct from the first.
func startProviderFixtureFor(tb testing.TB, providerDomain, exchangeDomain, exchangeEndpoint string) *httptest.Server {
	tb.Helper()
	body := `{"ver":"1.0","role":"ROLE_PUBLISHER","domain":"` + providerDomain +
		`","exchanges":[{"domain":"` + exchangeDomain + `","endpoint":"` + exchangeEndpoint +
		`","relationship":"PROVIDER_RELATIONSHIP_DIRECT"}],"supported_profiles":["fora-news-v1"]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != forawellknown.Path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	tb.Cleanup(srv.Close)
	return srv
}

// noForaProvider serves 404 for /.well-known/fora.json — used for bare-URL test.
func startUnlicensedProvider(tb testing.TB) *httptest.Server {
	tb.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	tb.Cleanup(srv.Close)
	return srv
}
