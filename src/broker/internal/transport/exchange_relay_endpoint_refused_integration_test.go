//go:build integration

package transport_test

import (
	"net/http"
	"testing"

	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

// TestExchangeRelay_RefusedEndpointIsFinalNotRetryable pins how the relay
// classifies an Exchange whose manifest advertises an endpoint on another
// origin.
//
// The distinction is the whole point. The relay's default classification for a
// failed endpoint resolution is upstream-unavailable, which reaches the agent as
// a retryable code. A refused endpoint is a verdict: the manifest was read and
// says something the resolver will not act on, and it will say the same thing on
// every retry. Reporting it as retryable sends an agent round a loop that cannot
// terminate, so the refusal has to arrive as invalid-argument.
//
// The fixture is an Exchange that serves its own manifest but advertises an
// endpoint on a DIFFERENT origin. A manifest may name only the host and port
// that served it, or a subdomain of that host, so the endpoint resolver refuses
// this document with resolvers.ErrEndpointRefused before returning anything.
// The second origin is a live server rather than an invented address on purpose:
// a dead address would fail to DIAL, which is the transient failure this test
// exists to tell apart from a verdict. The counter that matters is that
// server's — the one the endpoint check exists to keep traffic away from.
//
// Round-trip: agent → the broker relay route over real HTTP → the broker fetches
// the offer.exchange manifest over real HTTP → refusal → the assertion reads the
// broker's own HTTP response and both origins' dial counters.
func TestExchangeRelay_RefusedEndpointIsFinalNotRetryable(t *testing.T) {
	env := newRelayTestEnv(t)
	advertised, elsewhereDialled := startDialCounter(t)
	badDomain, selfDialled := startExchangeServing(t, func(host string) []byte {
		return rwtestutil.ExchangeManifest(host, advertised)
	})
	// The registry carries the advertised endpoint as well, so the post-resolve
	// allowlist could not have been the objection either.
	seedVerifiedExchange(t, env, badDomain, advertised)

	resp, err := http.DefaultClient.Do(env.signedRelayRequest(t, env.txBodyForExchange(t, badDomain)))
	if err != nil {
		t.Fatalf("send to broker: %v", err)
	}
	assertRelayVerdict(t, resp, elsewhereDialled, selfDialled)
	// The verdict is also filed as an endpoint refusal in the audit log.
	assertRelayAudit(t, env.logs.String(), "REJECTED_ENDPOINT")
}

// TestExchangeRelay_UnusableExchangeHostIsFinalNotRetryable is the third verdict
// the endpoint contract defines, and the one the relay did not name.
//
// The other two mean the manifest was read and said something unusable. This one
// means it was never read: the domain is not a host, so there is nothing to
// fetch. It is still a verdict — a value that is not a host will not become one
// on a later attempt — and reporting it as a transient failure sends the agent
// round a retry loop that can never end.
//
// Reachable because nothing keeps such a value out of the registry: the domain
// column is plain text with no constraint on its shape, so a seed or an admin
// write puts one there and every offer naming it takes this path.
func TestExchangeRelay_UnusableExchangeHostIsFinalNotRetryable(t *testing.T) {
	env := newRelayTestEnv(t)

	// A registry row whose domain carries a path.
	const badDomain = "exchange.example/not-a-host"
	seedVerifiedExchange(t, env, badDomain, "http://exchange.example")

	resp, err := http.DefaultClient.Do(env.signedRelayRequest(t, env.txBodyForExchange(t, badDomain)))
	if err != nil {
		t.Fatalf("send to broker: %v", err)
	}
	assertRelayVerdict(t, resp)
}
