//go:build integration

package transport_test

// Which health check decides that an Exchange is answering, driven through
// fora.v1.BrokerService/ExecuteTransaction across real health-refresher passes.
//
// The protocol puts the answer in the Exchange's own manifest:
// WellKnownManifest.health_endpoint names the URL to ask, and the member is
// optional. The refresher used to ask {endpoint}/healthz whatever the manifest
// said. An Exchange that publishes no such path was therefore never LIVE, and
// the Broker routes discovery and purchases only to LIVE Exchanges, so a
// conformant Exchange could never be used. One that served /healthz but named
// a different health check was judged by the path it had not named.
//
// Round-trip honesty: the write leg is real all the way down. The production
// Refresher runs over the production endpoint resolver and fetches the
// Exchange's real /.well-known/fora.json over HTTP, then writes through the
// production registry repository to the shared Postgres. The read leg is a real
// signed purchase through the BrokerService client, whose admission reads the
// row the pass wrote. "Routed" is read at the Exchange's address: one more
// request arriving there. Nothing is emulated; the address is a wireExchange
// that answers every purchase sub-request 503 with no body.

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	connect "connectrpc.com/connect"

	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/registry"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// manifestNamingHealth returns a manifest builder whose health_endpoint is
// health(host), so a test can name an absolute URL on the serving origin or a
// relative value.
func manifestNamingHealth(health func(host string) string) func(host string) []byte {
	return func(host string) []byte {
		return rwtestutil.ExchangeManifestWithHealth(host, "http://"+host, health(host))
	}
}

// healthTestEnv is the purchase surface in front of ex, with the production
// refresher over the same registry and resolver, logging into a capture.
type healthTestEnv struct {
	purchaseTestEnv
	refresher *registry.Refresher
	passLogs  *testutil.LogCapture
	purchases int
}

func newHealthTestEnv(t *testing.T, ex *wireExchange) *healthTestEnv {
	t.Helper()
	env := newPurchaseTestEnvAt(t, ex, false)
	logs, logger := testutil.NewLogCapture()
	return &healthTestEnv{
		purchaseTestEnv: env,
		refresher:       registry.NewRefresher(env.exchangeRepo, env.endpoints, loopbackProbe(), logger, 0),
		passLogs:        logs,
	}
}

// pass runs one refresher pass.
func (e healthTestEnv) pass(t *testing.T) {
	t.Helper()
	refreshOnce(t, e.refresher, t.Context())
}

// purchaseReached sends one signed purchase from the Exchange and reports
// whether it reached the Exchange. When it did not, the call must be the
// Broker's own admission refusal: any other failure would leave the test
// unable to say why nothing arrived. Each call carries its own idempotency key,
// so no two purchases share a signature.
func (e *healthTestEnv) purchaseReached(t *testing.T) bool {
	t.Helper()
	e.purchases++
	before := e.exchange.arrivals()
	req := e.purchaseRequest(t, fmt.Sprintf("tx-health-%d", e.purchases), line{"offer-1", e.exchangeDom})
	_, err := e.client.ExecuteTransaction(t.Context(), connect.NewRequest(req))
	reached := e.exchange.arrivals() == before+1
	if !reached {
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeFailedPrecondition {
			t.Fatalf("purchase neither reached the Exchange nor was refused at admission: %v", err)
		}
	}
	return reached
}

// TestRefresher_ManifestWithoutHealthEndpointIsJudgedByTheFetch is an Exchange
// that serves no /healthz at all and whose manifest names no health endpoint.
// The protocol allows both, so a fetched, valid manifest has to be enough.
//
// /healthz is unmounted, so a probe of it would arrive at the Exchange like any
// purchase sub-request and be answered 503. Leg 2 fails on the old code for exactly
// that reason: the pass asked /healthz, got 503, and kept the Exchange DOWN, so
// the purchase was refused.
func TestRefresher_ManifestWithoutHealthEndpointIsJudgedByTheFetch(t *testing.T) {
	ex := startWireExchange(t, ownManifest, answerUnavailable)
	ex.unmountHealth(healthzPath)
	env := newHealthTestEnv(t, ex)

	// Leg 1: the manifest cannot be read, so the Exchange has not answered. The
	// pass takes it out of rotation, which also proves the leg below starts from
	// a row that is not LIVE.
	ex.setManifestStatus(http.StatusServiceUnavailable)
	env.pass(t)
	assertTransitionLogged(t, env.passLogs, ex.domain, repo.ProbeDown, "WARN")
	if env.purchaseReached(t) {
		t.Fatal("the purchase reached an Exchange whose manifest could not be read")
	}

	// Leg 2: the manifest is served again and names no health endpoint. The
	// fetch is the answer: the Exchange is LIVE and the purchase reaches it.
	ex.setManifestStatus(0)
	env.pass(t)
	assertTransitionLogged(t, env.passLogs, ex.domain, repo.ProbeLive, "INFO")
	if !env.purchaseReached(t) {
		t.Error("the purchase did not reach an Exchange whose valid manifest names no health endpoint")
	}
	// The purchase sub-request is the only one that arrived. A pass that probed
	// /healthz, or any other guessed path, would have added one.
	if got := ex.arrivals(); got != 1 {
		t.Errorf("requests at the Exchange = %d, want 1 (the purchase alone) — the pass probed a "+
			"path the manifest does not name", got)
	}
}

// TestRefresher_NamedHealthEndpointAnswering500KeepsTheExchangeOutOfRotation
// is an Exchange whose manifest names a health check that fails while its
// /healthz still answers 200. The manifest is the Exchange's own statement of
// where to ask, so the named check decides.
//
// Fails on the old code: the pass asked /healthz, got 200, and left the
// Exchange LIVE, so no transition was recorded and the purchase reached it.
func TestRefresher_NamedHealthEndpointAnswering500KeepsTheExchangeOutOfRotation(t *testing.T) {
	const statusPath = "/status/health"
	ex := startWireExchange(t, manifestNamingHealth(func(host string) string {
		return "http://" + host + statusPath
	}), answerUnavailable)
	ex.setHealth(statusPath, http.StatusInternalServerError)
	env := newHealthTestEnv(t, ex)

	env.pass(t)

	assertTransitionLogged(t, env.passLogs, ex.domain, repo.ProbeDown, "WARN")
	if env.purchaseReached(t) {
		t.Error("the purchase reached an Exchange whose named health check answers 500")
	}
	assertPurchaseAudit(t, env.purchaseTestEnv, "REJECTED_EXCHANGE", "WARN", ex.domain)
	if ex.healthProbeCount(statusPath) == 0 {
		t.Errorf("%s received no probe — the pass did not ask the health check the manifest names", statusPath)
	}
	if got := ex.healthzProbeCount(); got != 0 {
		t.Errorf("/healthz received %d probes, want 0 — the manifest does not name it", got)
	}
}

// TestRefresher_NamedHealthEndpointAnswering200PutsTheExchangeInRotation is the
// other direction, with the health endpoint published as a RELATIVE value. The
// pass resolves it against the URL the manifest was served from, so
// "/status/ready" in the manifest of 127.0.0.1:port means that origin's
// /status/ready. /healthz answers 503 throughout.
//
// Leg 2 fails on the old code: the pass asked /healthz, got 503, and kept the
// Exchange DOWN after its named check recovered.
func TestRefresher_NamedHealthEndpointAnswering200PutsTheExchangeInRotation(t *testing.T) {
	const readyPath = "/status/ready"
	ex := startWireExchange(t, manifestNamingHealth(func(string) string { return readyPath }), answerUnavailable)
	ex.setHealthz(http.StatusServiceUnavailable)
	ex.setHealth(readyPath, http.StatusInternalServerError)
	env := newHealthTestEnv(t, ex)

	// Leg 1: the named check fails, so the Exchange leaves rotation.
	env.pass(t)
	assertTransitionLogged(t, env.passLogs, ex.domain, repo.ProbeDown, "WARN")
	if env.purchaseReached(t) {
		t.Fatal("the purchase reached an Exchange whose named health check answers 500")
	}

	// Leg 2: the named check recovers. The Exchange returns to rotation on the
	// next pass, with no operator action.
	ex.setHealth(readyPath, http.StatusOK)
	env.pass(t)
	assertTransitionLogged(t, env.passLogs, ex.domain, repo.ProbeLive, "INFO")
	if !env.purchaseReached(t) {
		t.Error("the purchase did not reach an Exchange whose named health check answers 200")
	}
	if got := ex.healthzProbeCount(); got != 0 {
		t.Errorf("/healthz received %d probes, want 0 — the manifest does not name it", got)
	}
}

// TestRefresher_UndialableHealthEndpointIsARefusal is a manifest naming a
// health endpoint no pass may dial: a scheme that is not http or https. That is
// a fact about the document, repeated identically on every pass until the
// Exchange republishes, so the row is REFUSED, not DOWN. Reporting it as an
// outage would tell agents to retry something that will not change.
//
// The pass must not fall back to /healthz, which answers 200 here: the manifest
// said where to ask, and a guess is not that.
func TestRefresher_UndialableHealthEndpointIsARefusal(t *testing.T) {
	ex := startWireExchange(t, manifestNamingHealth(func(host string) string {
		return "ftp://" + host + "/healthz"
	}), answerUnavailable)
	env := newHealthTestEnv(t, ex)

	env.pass(t)

	assertTransitionLogged(t, env.passLogs, ex.domain, repo.ProbeRefused, "WARN")
	if rec := env.passLogs.AssertLoggedOnce(t, "broker.registry.manifest.refused", "WARN"); rec["domain"] != ex.domain {
		t.Errorf("broker.registry.manifest.refused names %v, want %s", rec["domain"], ex.domain)
	}
	if env.purchaseReached(t) {
		t.Error("the purchase reached an Exchange whose manifest was refused")
	}
	if got := ex.healthzProbeCount(); got != 0 {
		t.Errorf("/healthz received %d probes, want 0 — the pass guessed a path the manifest does not name", got)
	}
}
