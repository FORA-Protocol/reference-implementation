//go:build integration

package transport_test

import (
	"net/http"
	"testing"

	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/registry"
)

// TestExchangeRelay_RefusedManifestVersionIsFinalNotRetryable pins how the relay
// classifies an Exchange whose manifest carries a document version this reader
// does not accept — an unrecognised major, and no version at all.
//
// Both are verdicts. The manifest was fetched and parsed, and it will carry the
// same version on every retry, so reporting the refusal as upstream-unavailable
// sends an agent round a loop that cannot terminate. The refusal has to arrive as
// invalid-argument, and nothing signed may leave the process for an Exchange
// whose document the reader could not classify.
//
// Every case runs twice, before and after one real health-refresher pass, and
// that second run is the point. The relay reads the registry row's admission
// before it resolves anything, so a pass that recorded the refusal as ordinary
// unhealthiness put a retryable 503 in front of the classification below and the
// agent never reached it. The two runs must answer identically: which one a
// request meets depends only on the refresher's ticker, and an agent must not
// see a refusal change shape on that.
//
// The fixture advertises the origin that served it — the one shape the endpoint
// check accepts — so the resolver's refusal can only be the version gate, which
// runs before the endpoint is read.
//
// Round-trip: agent → the broker relay route over real HTTP → the broker fetches
// the offer.exchange manifest over real HTTP → refusal → the assertion reads the
// broker's own HTTP response and the Exchange's own dial counter. The refresher
// leg drives the real Refresher over the real registry repository against the
// same resolver the relay routes through, so the row it writes is the row the
// relay reads.
func TestExchangeRelay_RefusedManifestVersionIsFinalNotRetryable(t *testing.T) {
	for name, ver := range map[string]string{
		"unrecognised major": "2.0",
		"absent version":     "",
	} {
		for stage, probed := range map[string]bool{
			"before any probe pass": false,
			"after a probe pass":    true,
		} {
			t.Run(name+", "+stage, func(t *testing.T) {
				ctx := t.Context()
				env := newRelayTestEnv(t)
				badDomain, dialled := startExchangeServing(t, func(host string) []byte {
					return rwtestutil.ExchangeManifestAtVersion(host, "http://"+host, ver)
				})
				seedVerifiedExchange(t, env, badDomain, "http://"+badDomain)

				if probed {
					// The only lever production has: one pass of the real refresher
					// over the real resolver, which reads the manifest and records
					// what it concluded on the row the relay will read.
					refreshOnce(t, registry.NewRefresher(
						env.exchangeRepo, env.endpoints, loopbackProbeClient(),
						testutil.DiscardLogger(), 0,
					), ctx)
				}

				resp, err := http.DefaultClient.Do(env.signedRelayRequest(t, env.txBodyForExchange(t, badDomain)))
				if err != nil {
					t.Fatalf("send to broker: %v", err)
				}
				assertRelayVerdict(t, resp, dialled)
			})
		}
	}
}
