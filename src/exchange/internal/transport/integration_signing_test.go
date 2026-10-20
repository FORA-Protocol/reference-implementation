//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"net/http"
	"testing"

	sdkconnect "github.com/FORA-Protocol/protocol/sdk/go/connect"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest"
)

// How a test client signs its requests. Split out of integration_helper_test.go
// when that file crossed the 800-line test cap; these three are one topic and
// are used by both harnesses in this package, so they travel together.

// newSigningTransport signs as a real caller does; exchangetest.SigningTransport
// says how, and why a fresh nonce on every signature lets identical calls in one
// second both pass the replay store.
func newSigningTransport(base http.RoundTripper, keyID string, priv ed25519.PrivateKey) http.RoundTripper {
	return exchangetest.SigningTransport(base, keyID, priv)
}

// mustCatalogClient wires the ingest push path's SDK catalog client for tests
// — the constructor the CLI runs, against exchangeURL, signing as kid — so
// every ingest test pushes through exactly the client production pushes
// through.
func mustCatalogClient(t *testing.T, exchangeURL, kid string, priv ed25519.PrivateKey) *sdkconnect.CatalogClient {
	t.Helper()
	client, err := ingest.NewCatalogClient(exchangeURL, exchangetest.SignatureAgentOrigin(kid), priv, nil)
	if err != nil {
		t.Fatalf("build catalog client: %v", err)
	}
	return client
}
