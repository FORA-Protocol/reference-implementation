//go:build integration

package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// What the relay's verdict tests share: one Exchange fixture, one registry seed
// and one assertion block. Three tests drive three verdicts — a refused
// manifest version, a refused endpoint, an unusable host — and each used to
// carry its own copy of all three pieces, differing only in the strings. A
// change to how the relay reports a verdict then needed three edits in one
// package, and jscpd could not see the copies because the strings differed.

// startExchangeServing stands up an Exchange that serves, at its own
// /.well-known/fora.json, whatever manifest the caller builds for the host that
// served it. Every OTHER route counts a dial: a relay that read past a refusal
// and posted the signed transaction to this origin lands there.
func startExchangeServing(tb testing.TB, manifest func(host string) []byte) (domain string, dialled *atomic.Int32) {
	tb.Helper()
	dialled = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != forawellknown.Path {
			dialled.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(manifest(r.Host))
	}))
	tb.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), dialled
}

// startDialCounter stands up a server that answers every request and counts it.
// It is the origin a manifest may point AWAY from: the counter on it is what
// says whether the relay followed an address the check should have refused.
func startDialCounter(tb testing.TB) (url string, dialled *atomic.Int32) {
	tb.Helper()
	dialled = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dialled.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	tb.Cleanup(srv.Close)
	return srv.URL, dialled
}

// seedVerifiedExchange registers domain as a VERIFIED Exchange at endpoint, so
// the trust gate passes and a refusal can only come from the check under test.
func seedVerifiedExchange(t *testing.T, env relayTestEnv, domain, endpoint string) {
	t.Helper()
	if _, err := env.exchangeRepo.UpsertFromBootstrap(context.Background(), repo.Exchange{
		ID:                "mp-" + domain,
		Domain:            domain,
		Endpoint:          endpoint,
		TrustLevel:        "VERIFIED",
		SupportedProfiles: []string{"fora-news-v1"},
		Priority:          10,
	}); err != nil {
		t.Fatalf("seed exchange %s: %v", domain, err)
	}
}

// assertRelayVerdict asserts the relay answered a VERDICT: nothing was dialled
// on any counter given, and the response is 400 carrying the invalid_argument
// kind, read through the broker's ErrorDetail rather than matched in raw bytes.
//
// The dial counters are checked FIRST, and not because they are the headline.
// They are the assertion that stops holding for a reason the status code cannot
// show: a relay that fell back to the registry's endpoint on a refused
// resolution would post the signed transaction and still answer something.
// Reading the status first would report that as a wrong code and never say
// where the request went. A 503 is named separately because it is the specific
// wrong answer these tests exist to rule out — retryable, so an agent loops.
func assertRelayVerdict(t *testing.T, resp *http.Response, dialled ...*atomic.Int32) {
	t.Helper()
	for _, counter := range dialled {
		if got := counter.Load(); got != 0 {
			t.Fatalf("an origin was dialled %d times behind a refused check, want 0", got)
		}
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Fatalf("a verdict was reported as retryable (503) — an agent would retry forever")
	}
	detail := readBrokerErrorDetail(t, resp, http.StatusBadRequest)
	if !strings.HasPrefix(detail.GetMessage(), "invalid_argument:") {
		t.Errorf("refusal did not carry the invalid_argument kind; message=%q", detail.GetMessage())
	}
}
