package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// TestManifestNamesTheLiveHealthRoute drives what a Broker does with this
// Exchange's manifest: read health_endpoint out of /.well-known/fora.json and
// GET exactly that URL. A Broker routes only to an Exchange it judges to be
// answering, and the protocol says the manifest is where it learns which URL to
// ask. An Exchange that publishes no health endpoint, or one naming a route it
// does not serve, is judged by a weaker signal or not at all.
//
// The server listens before the mux is built, so EXCHANGE_PUBLIC_ORIGIN can
// name the address the test actually dials. The published value is then used
// verbatim, as a Broker would use it, with no path joined on by the test.
func TestManifestNamesTheLiveHealthRoute(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	origin := "http://" + ln.Addr().String()
	t.Setenv("EXCHANGE_PUBLIC_ORIGIN", origin)
	t.Setenv("EXCHANGE_REGISTRATION_SCHEMA", "")
	t.Setenv("EXCHANGE_TERMS_URI", "")
	t.Setenv("EXCHANGE_TERMS_DIGEST", "")

	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: buildPublicMux(t, muxDeps{})}}
	srv.Start()
	t.Cleanup(srv.Close)

	m, _ := testutil.FetchManifest(t, srv.URL, forawellknown.RoleExchange)
	health := m.GetHealthEndpoint()
	if want := origin + healthzPath; health != want {
		t.Fatalf("health_endpoint = %q, want %q (the liveness route on the public origin)", health, want)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, health, http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", health, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET %s = %d, want 200 — the manifest names a health check this Exchange does not answer",
			health, resp.StatusCode)
	}
}
