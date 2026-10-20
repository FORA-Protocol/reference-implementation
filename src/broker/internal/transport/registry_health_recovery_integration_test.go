//go:build integration

package transport_test

// Registry health recovery, driven through fora.v1.BrokerService/Resolve.
//
// The health refresher writes broker.exchanges.healthy, and the resolve fan-out
// reads it: an exchange marked unhealthy is not routed to, so every URL whose
// publisher names it answers with a typed-absence group. The two halves must
// therefore agree about which exchanges are still worth probing. They did not:
// the refresher listed only the healthy rows, so the first failed probe removed
// the exchange from the list the next pass reads and the flag could never go
// back to true. One failed probe during a restart stranded the exchange for
// good, and nothing at any layer said why discovery had stopped answering.
//
// These tests drive the whole loop over the wire -- a real HTTP /healthz at the
// Exchange address, the real refresher, and the real signed Resolve RPC. Health
// state is never written by hand: the tests take the Exchange down by making
// its /healthz answer 503 and bring it back by making it answer 200, which is
// the only lever production has.
//
// No Exchange is emulated. The address is a wireExchange
// (exchange_wire_fixture_test.go) that answers every discovery request 503 with
// no body, so "routed to the Exchange" is read as one more request arriving
// there, and "declined" as none arriving plus the routing-skip record. What an
// Exchange then answers is not what these tests are about.

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/registry"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// catalogedURI is a URL on the publisher domain the fixture's manifest covers,
// so a resolve of it is routed to the Exchange that manifest names.
const catalogedURI = "https://acme.example/article-42"

// newRoutingFixture is the resolve fixture with an endpoint at mp.acme.example
// that answers every discovery request 503 with no body.
func newRoutingFixture(t *testing.T, captureLogs bool) *fixture {
	t.Helper()
	return newFixture(t, t.Context(), fixtureOpts{
		providerDomain: "acme.example", captureLogs: captureLogs, upstream: startUnavailableExchange(t),
	})
}

// newRefresher builds the health refresher over the fixture's database exactly
// as the broker's composition root does: the same constructor, the same
// repository, and the SAME endpoint resolver the resolve handler routes
// through. Only the polling loop is left out — the tests call RefreshOnce so a
// pass happens at a chosen moment instead of after a 30-second interval.
//
// The probe client is the one deliberate difference, and loopbackProbe says
// why. The fixture's exchange domains do not resolve, so the client also maps
// each one onto the loopback address the fixture's resolver names for it: the
// pass fetches the exchange's manifest from its DOMAIN, exactly as production
// does, and that request has to arrive somewhere.
//
// Sharing one resolver is what makes the test honest about the fix: the probe
// has to measure the endpoint routing will actually use, and handing the two
// halves separate resolvers would hide a disagreement between them.
func newRefresher(fx *fixture) *registry.Refresher {
	probe := loopbackProbe()
	probe.HTTP.Transport = &rewritingHTTPClient{targets: maps.Clone(fx.endpoints.byDomain)}
	return registry.NewRefresher(repo.NewExchangeRepo(fx.pool), fx.endpoints, probe, fx.logger, 0)
}

// loopbackProbe is the probe transport for tests whose Exchange listens on
// loopback over plain http. Production wires the SSRF-guarded client, which
// refuses a loopback address by design — that is the whole point of the guard —
// so a test serving 127.0.0.1 has to opt out of it explicitly. Naming the
// opt-out here keeps it visible: a test that reached for a bare &http.Client
// would look like production wiring, and this one cannot be mistaken for it.
func loopbackProbe() registry.ProbeTransport {
	return registry.ProbeTransport{HTTP: &http.Client{Timeout: 3 * time.Second}, Scheme: "http"}
}

// refreshOnce drives a single probe pass and fails the test if the pass could
// not run at all. Asserting the error is the point of RefreshOnce returning
// one: a pass that never listed anything would otherwise look identical to a
// pass that listed everything and found nothing to change.
func refreshOnce(t *testing.T, r *registry.Refresher, ctx context.Context) {
	t.Helper()
	if err := r.RefreshOnce(ctx); err != nil {
		t.Fatalf("refresh pass did not run: %v", err)
	}
}

// resolveCataloged issues one signed Resolve for the cataloged URL.
func resolveCataloged(t *testing.T, fx *fixture) *forav1.DiscoveryResponse {
	t.Helper()
	_, out := resolveOverConnect(t, fx, reqOpts{uri: catalogedURI, budgetMinor: 10000})
	return out
}

// assertRoutedToExchange issues the cataloged resolve and fails unless it was
// routed to the Exchange: exactly one more request arrived at its address.
func assertRoutedToExchange(t *testing.T, fx *fixture, leg string) {
	t.Helper()
	before := fx.wire.arrivals()
	resolveCataloged(t, fx)
	if got := fx.wire.arrivals(); got != before+1 {
		t.Fatalf("%s: requests at the Exchange went from %d to %d, want one more — the resolve "+
			"was not routed to it", leg, before, got)
	}
}

// assertNotRoutedToExchange issues the cataloged resolve, fails unless it is the
// request-level refusal with reason want, and fails if any request reached the
// Exchange.
func assertNotRoutedToExchange(t *testing.T, fx *fixture, want forav1.OfferAbsenceReason, leg string) {
	t.Helper()
	before := fx.wire.arrivals()
	assertUnroutable(t, resolveCataloged(t, fx), want, leg)
	if got := fx.wire.arrivals(); got != before {
		t.Errorf("%s: requests at the Exchange went from %d to %d — a declined exchange must not "+
			"be dialled", leg, before, got)
	}
}

// assertUnroutable fails unless the response is the group-less request-level
// refusal a URL gets when no exchange its publisher names can serve it, with
// the reason want.
//
// The unroutable URL does start out as a typed-absence group, but that group
// carries no offer, so the batch has no winner and Resolve replaces the whole
// set with the request-level refusal. That shape says nothing at all about
// which exchange dropped out, which is why the log record is asserted too.
//
// The reason is a parameter because the two ways a URL becomes unroutable owe
// an agent different answers. An exchange that is down clears itself within one
// refresher interval, so the batch answers TEMPORARILY_UNAVAILABLE, the same
// answer noHealthyExchangeResponse gives the free-text query path for the same
// condition. An exchange the operator BLOCKED is a settled decision, and a URL
// that routes nowhere else really is not in this broker's catalog.
func assertUnroutable(
	t *testing.T, out *forav1.DiscoveryResponse, want forav1.OfferAbsenceReason, leg string,
) {
	t.Helper()
	if n := len(out.GetOfferGroups()); n != 0 {
		t.Errorf("%s: offer_groups = %d, want 0 (the request-level refusal carries none)", leg, n)
	}
	if got := out.GetAbsenceReason(); got != want {
		t.Errorf("%s: absence_reason = %v, want %v", leg, got, want)
	}
}

// TestResolve_UnhealthyExchangeRecoversWhenHealthzReturnsOK is the regression
// test for the one-way health flag. It walks the full outage: the Exchange is
// routed to, goes down, is skipped, comes back, and is routed to again -- each
// leg asserted through the Resolve RPC, with the health flag moved only by the
// refresher probing a real /healthz.
//
// Two legs fail on the old code, and they prove different halves. Leg 3 fails
// because broker.routing.skipped did not exist, so nothing said which exchange
// had dropped out. Leg 5 is the regression proper: with the refresher listing
// only healthy rows, the exchange is absent from the pass that follows its own
// downgrade, so /healthz answering 200 again changes nothing and the final
// resolve is still not routed to it.
func TestResolve_UnhealthyExchangeRecoversWhenHealthzReturnsOK(t *testing.T) {
	ctx := t.Context()
	fx := newRoutingFixture(t, true)
	refresher := newRefresher(fx)

	// Leg 1: the Exchange is up and the URL is routed to it. Establishes that
	// everything after this point is about health alone, not about a broken
	// fixture.
	assertRoutedToExchange(t, fx, "baseline, before the outage")
	if skips := fx.logs.Find("broker.routing.skipped"); len(skips) != 0 {
		t.Fatalf("a healthy exchange was logged as skipped: %v — the record would be "+
			"routine noise rather than a signal", skips)
	}

	// Leg 2: the Exchange goes down. One refresh pass sees the failed probe and
	// writes healthy=false, the same way production learns about an outage.
	fx.wire.setHealthz(http.StatusServiceUnavailable)
	refreshOnce(t, refresher, ctx)
	assertTransitionLogged(t, fx.logs, mockExchangeDomain, repo.ProbeDown, "WARN")

	// Leg 3: while it is down the URL answers with the request-level refusal, the
	// Exchange is not dialled, and the broker says which exchange it declined.
	assertNotRoutedToExchange(t, fx,
		forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE,
		"while the exchange is down")
	assertSkipLogged(t, fx, "unhealthy")

	// Leg 4: the Exchange comes back. The next refresh pass must still reach it.
	fx.wire.setHealthz(http.StatusOK)
	refreshOnce(t, refresher, ctx)
	assertTransitionLogged(t, fx.logs, mockExchangeDomain, repo.ProbeLive, "INFO")

	// Leg 5: routing resumes on its own, with no operator intervention.
	assertRoutedToExchange(t, fx, "after /healthz returned 200 again")
}

// TestRefresher_UnreachableWellKnownIsAnOutageAtWarn pins the other way a pass
// takes an exchange out of service: its fora.json could not be read at all.
// That is an outage and not a verdict, since the next pass may read it, so the
// row goes DOWN.
//
// The failure is real. The pass runs the production well-known resolver against
// a registered exchange whose address refuses connections, which is how a
// crashed exchange looks from the Broker. The record the pass leaves is
// asserted at WARN, because at INFO an operator filtering at WARN saw the
// request path complain about the exchange while the pass that took it out of
// service stayed hidden, and so is the transition it causes.
func TestRefresher_UnreachableWellKnownIsAnOutageAtWarn(t *testing.T) {
	ctx := t.Context()
	env := newPurchaseTestEnv(t)
	down := strings.TrimPrefix(refusingOrigin, "http://")
	// Arranged through the production repository interface: the broker exposes
	// no registry-administration RPC, so the bootstrap upsert is the only write
	// path there is.
	if _, err := env.exchangeRepo.UpsertFromBootstrap(ctx, repo.Exchange{
		ID:                "mp-down",
		Domain:            down,
		Endpoint:          refusingOrigin,
		TrustLevel:        "VERIFIED",
		SupportedProfiles: []string{"fora-news-v1"},
		Priority:          10,
	}); err != nil {
		t.Fatalf("register the unreachable exchange: %v", err)
	}
	logs, logger := testutil.NewLogCapture()
	resolver := resolvers.NewWellKnownEndpointResolver(
		resolvers.WellKnownOptions{Scheme: "http", HTTP: http.DefaultClient},
	)
	refreshOnce(t, registry.NewRefresher(env.exchangeRepo, resolver, loopbackProbe(), logger, 0), ctx)

	if rec := logs.AssertLoggedOnce(t, "broker.registry.resolve", "WARN"); rec["domain"] != down {
		t.Errorf("broker.registry.resolve names %v, want the unreachable exchange %s", rec["domain"], down)
	}
	assertTransitionLogged(t, logs, down, repo.ProbeDown, "WARN")
}

// TestRefresher_APassCutShortByShutdownIsNotAFault pins the refresher's side of
// the rule for a failure its own context caused by ending. A pass that runs
// while the process is shutting down fails at its first registry read, on the
// ended context. That pass was abandoned, not failed, so its record is DEBUG
// and a restart leaves no WARN line behind.
func TestRefresher_APassCutShortByShutdownIsNotAFault(t *testing.T) {
	fx := newSinkFixture(t, t.Context(), fixtureOpts{providerDomain: "acme.example", captureLogs: true})
	ended, cancel := context.WithCancel(t.Context())
	cancel()

	if err := newRefresher(fx).RefreshOnce(ended); err == nil {
		t.Fatal("a pass on an ended context reported success")
	}
	fx.logs.AssertLoggedOnce(t, "broker.registry.list", "DEBUG")
}

// TestRefresher_DoesNotProbeBlockedExchange pins the other half of the list the
// refresher reads. Widening it to unhealthy rows must not widen it to BLOCKED
// ones: the operator withdrew trust from those, so the broker sends them no
// traffic, a health probe included.
func TestRefresher_DoesNotProbeBlockedExchange(t *testing.T) {
	ctx := t.Context()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	blocked := startExchangeSink(t)
	if _, err := repo.NewExchangeRepo(fx.pool).UpsertFromBootstrap(ctx, repo.Exchange{
		ID:                "mp-blocked",
		Domain:            "mp.blocked.example",
		Endpoint:          blocked.url,
		TrustLevel:        "BLOCKED",
		SupportedProfiles: []string{"fora-news-v1"},
		Priority:          10,
	}); err != nil {
		t.Fatalf("upsert blocked exchange: %v", err)
	}

	refreshOnce(t, newRefresher(fx), ctx)

	if got := blocked.healthzProbeCount(); got != 0 {
		t.Errorf("/healthz probes on the BLOCKED exchange = %d, want 0", got)
	}
	// Without this the assertion above would also pass if the pass had probed
	// nothing at all -- a refresher that read an empty list, or never ran.
	if fx.wire.healthzProbeCount() == 0 {
		t.Fatal("the refresh pass probed no exchange at all, so the BLOCKED assertion proves nothing")
	}
}

// TestResolve_BlockedExchangeIsRefusedAsNotInCatalog pins the other arm of the
// health-and-trust check, and the absence reason that goes with it.
//
// A BLOCKED exchange and a down one are both unroutable, and both leave a
// broker.routing.skipped record — but they must not read the same. The operator
// withdrew trust from a BLOCKED exchange on purpose, so nothing about it clears
// on its own: the reason says "blocked", not "unhealthy", and the batch answers
// NOT_IN_CATALOG rather than the TEMPORARILY_UNAVAILABLE a down exchange gets.
// Without this test, the routing skip could record either reason for either
// state and the suite would not notice.
func TestResolve_BlockedExchangeIsRefusedAsNotInCatalog(t *testing.T) {
	ctx := t.Context()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "acme.example", captureLogs: true})

	// Withdraw trust from the exchange the publisher's manifest names. Arranged
	// through the production repository interface because the broker exposes no
	// registry-administration RPC — an operator edits the bootstrap file and
	// restarts. That missing surface is the reason this is a tier-2 arrange
	// rather than a round trip through a public one.
	if _, err := repo.NewExchangeRepo(fx.pool).UpsertFromBootstrap(ctx, repo.Exchange{
		ID:                "mp-acme",
		Domain:            mockExchangeDomain,
		Endpoint:          fx.endpoints.byDomain[mockExchangeDomain],
		TrustLevel:        "BLOCKED",
		SupportedProfiles: []string{"fora-news-v1"},
		Priority:          10,
	}); err != nil {
		t.Fatalf("block the exchange: %v", err)
	}

	assertNotRoutedToExchange(t, fx,
		forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG,
		"while the operator has the exchange blocked")
	assertSkipLogged(t, fx, "blocked")
}

// TestRefresher_ProbesTheWellKnownEndpointNotTheRegistryColumn pins WHICH
// address the health probe measures.
//
// The registry's endpoint column is operator-supplied bootstrap. The address
// routing actually uses comes from the exchange's OWN well-known, resolved
// through the endpoint resolver, and routing overwrites the column value with
// it on every resolve. An exchange that moves and republishes its well-known is
// therefore still reachable by discovery while its column points somewhere
// stale.
//
// If the probe read the column instead, that exchange would be marked down and
// stay down: its real /healthz could answer 200 for good and the broker would
// never ask it. That is a second way to strand an exchange, past the one-way
// flag this suite already covers, and it is invisible in any fixture where the
// two addresses agree — which is why this test makes them disagree.
func TestRefresher_ProbesTheWellKnownEndpointNotTheRegistryColumn(t *testing.T) {
	ctx := t.Context()
	fx := newRoutingFixture(t, false)

	// A dead address that refuses immediately, standing in for a column left
	// behind by an exchange that moved. The resolver still returns the live one,
	// so the two sources now disagree exactly as they would in production.
	// Arranged through the production repository interface: the broker exposes
	// no registry-administration RPC, so the bootstrap upsert is the only write
	// path there is.
	if _, err := repo.NewExchangeRepo(fx.pool).UpsertFromBootstrap(ctx, repo.Exchange{
		ID:                "mp-acme",
		Domain:            mockExchangeDomain,
		Endpoint:          refusingOrigin,
		TrustLevel:        "VERIFIED",
		SupportedProfiles: []string{"fora-news-v1"},
		Priority:          10,
	}); err != nil {
		t.Fatalf("stale the registry endpoint: %v", err)
	}

	refreshOnce(t, newRefresher(fx), ctx)

	// The headline. A probe that read the column would have dialled the dead
	// address and this counter would still be zero.
	if fx.wire.healthzProbeCount() == 0 {
		t.Fatal("the live exchange received no /healthz probe — the pass measured " +
			"the registry column, which is not the address routing uses")
	}
	// And the flag the probe wrote is the one routing reads, so the exchange
	// stays usable rather than being stranded by its own stale column.
	assertRoutedToExchange(t, fx, "after a pass over a stale registry endpoint")
}

// assertSkipLogged fails unless the captured records carry a routing-skip line
// naming the mock exchange's domain and the reason, at INFO. The record is what
// makes an unroutable publisher diagnosable from the broker's logs, so it is
// asserted as behavior, not treated as incidental output.
//
// The level is part of the assertion because it is what separates the two kinds
// of decline. A skip on one of the OPERATOR'S OWN rows is the signal they need
// and stays at INFO; a decline on a domain some remote publisher named is
// routine, unbounded in cardinality, and goes to DEBUG. Demoting one of these
// to DEBUG would take it out of an operator's default view, and the capture
// records every level, so without this clause the demoted record would still
// be found and the test would pass. Promoting one to WARN would turn a routine
// outage into an alarm on every resolve, and nothing else here would notice at
// all.
func assertSkipLogged(t *testing.T, fx *fixture, reason string) {
	t.Helper()
	records := fx.logs.Records(t, "broker.routing.skipped")
	if slices.ContainsFunc(records, func(rec map[string]any) bool {
		return rec["domain"] == mockExchangeDomain && rec["reason"] == reason && rec["level"] == "INFO"
	}) {
		return
	}
	t.Errorf("no INFO broker.routing.skipped record for domain %q with reason %q; captured: %v",
		mockExchangeDomain, reason, records)
}

// assertTransitionLogged fails unless the captured records carry a probe state
// transition of the exchange at domain into now, at level. The transition is
// the only record that says when an exchange stopped being routable and when
// it came back, and a failed health ping writes nothing else. So leaving LIVE
// has to be visible at WARN, and the return is ordinary operation at INFO.
func assertTransitionLogged(t *testing.T, logs *testutil.LogCapture, domain string, now repo.ProbeState, level string) {
	t.Helper()
	records := logs.Records(t, "broker.registry.probe_state_changed")
	for _, rec := range records {
		if rec["domain"] == domain && rec["now"] == string(now) {
			if rec["level"] != level {
				t.Errorf("transition to %s logged at %v, want %s: %v", now, rec["level"], level, rec)
			}
			return
		}
	}
	t.Errorf("no broker.registry.probe_state_changed record into %s for %s; captured: %v", now, domain, records)
}
