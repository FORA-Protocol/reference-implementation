//go:build integration

package transport_test

// How the Broker authenticates the discovery fan-out it sends an Exchange. FORA
// authenticates every caller through Web Bot Auth, so each ResourceQuery the
// Broker sends carries the Broker's own RFC 9421 signature and nothing else: no
// signature header of the Broker's own design.
//
// The test drives Resolve through the Broker's Connect router, with the
// production relay transport signing the fan-out, and reads what the Broker sent
// from a recording endpoint registered as the Exchange. That endpoint stores the
// requests and answers 503, so no Exchange answer is emulated. Each recorded
// request is then verified with the SDK verifier the Exchange's signature gate
// runs (helpers.VerifyMultisigRequestResolved). That the running Exchange accepts
// the fan-out is proven end to end by every discovery test under
// tests/e2e/harness/.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/xclient"
)

// retiredBrokerSignatureHeader is the hand-made Ed25519 header the Broker once
// added to every discovery request. It was not RFC 9421 and no Exchange read it.
const retiredBrokerSignatureHeader = "X-FORA-Broker-Signature"

// TestResolve_FanOutCarriesOnlyTheBrokersRFC9421Signature drives one Resolve.
// Every ResourceQuery the Broker sent the Exchange carries no
// X-FORA-Broker-Signature header, and verifies under RFC 9421 as exactly one
// signature, made with the Broker's relay key.
func TestResolve_FanOutCarriesOnlyTheBrokersRFC9421Signature(t *testing.T) {
	relayPub, relayPriv := newBrokerKeyPair(t)
	relayKID := rwtestutil.MustThumbprint(t, relayPub)
	ex, rec := startRecordingExchange(t)
	fx := newFixture(t, context.Background(), fixtureOpts{
		providerDomain: "acme.example", upstream: ex,
		relayKey: &xclient.RelayKey{KeyID: relayKID, Private: relayPriv},
	})

	resolveOverConnect(t, fx, reqOpts{uri: "https://acme.example/item"})

	sent := rec.requests()
	if len(sent) == 0 {
		t.Fatal("the Broker sent the Exchange nothing; the test needs the query it fans out")
	}
	resolver := helpers.NewStaticKeyResolver(map[string]ed25519.PublicKey{relayKID: relayPub})
	for i, f := range sent {
		if f.path != forav1connect.ExchangeServiceDiscoverResourcesProcedure {
			t.Fatalf("request %d went to %s, want %s", i, f.path,
				forav1connect.ExchangeServiceDiscoverResourcesProcedure)
		}
		if got := f.header.Values(retiredBrokerSignatureHeader); len(got) != 0 {
			t.Errorf("request %d carries %s %q; the Broker's RFC 9421 signature is its only one",
				i, retiredBrokerSignatureHeader, got)
		}
		sigs, err := helpers.VerifyMultisigRequestResolved(
			context.Background(), f.asRequest(), f.body, resolver, helpers.VerifyOptions{})
		if err != nil {
			t.Fatalf("request %d does not verify under RFC 9421 against the Broker's relay key: %v", i, err)
		}
		if len(sigs) != 1 || sigs[0].KeyID != relayKID {
			t.Errorf("request %d verified as %d signatures, want exactly one by the relay key %s: %+v",
				i, len(sigs), relayKID, sigs)
		}
	}
}

// asRequest rebuilds the recorded request in the shape a server receives it, so
// the SDK verifier reconstructs the same signature base the Exchange would.
func (f forwarded) asRequest() *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, f.path, bytes.NewReader(f.body))
	r.Host = f.host
	r.Header = f.header.Clone()
	return r
}
