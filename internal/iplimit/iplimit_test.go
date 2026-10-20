package iplimit_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/time/rate"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/iplimit"
)

// A burst of N lets exactly N calls through before the sustained rate binds.
// The rate is one per minute so the refill cannot land inside the test.
func TestAllowSpendsTheBurstThenRefuses(t *testing.T) {
	t.Parallel()
	lim := iplimit.New(iplimit.Options{RatePerMinute: 1, Burst: 3})
	for i := range 3 {
		if !lim.Allow("198.51.100.7") {
			t.Fatalf("call %d refused while the burst still had tokens", i+1)
		}
	}
	if lim.Allow("198.51.100.7") {
		t.Fatal("the call past the burst was allowed")
	}
}

// One address spending its budget must not spend another's.
func TestBudgetsAreIndependentPerAddress(t *testing.T) {
	t.Parallel()
	lim := iplimit.New(iplimit.Options{RatePerMinute: 1, Burst: 1})
	if !lim.Allow("198.51.100.7") {
		t.Fatal("the first address was refused its only token")
	}
	if lim.Allow("198.51.100.7") {
		t.Fatal("the first address was allowed a second call")
	}
	if !lim.Allow("198.51.100.8") {
		t.Fatal("a second address was refused because the first had spent its budget")
	}
}

// Past MaxEntries the least recently seen address is dropped and comes back
// with a fresh bucket. This is the documented cost of bounding the map; the
// test pins it so the behaviour is a decision rather than a surprise.
func TestEvictedAddressReturnsWithAFreshBucket(t *testing.T) {
	t.Parallel()
	lim := iplimit.New(iplimit.Options{RatePerMinute: 1, Burst: 1, MaxEntries: 2})
	if !lim.Allow("198.51.100.7") {
		t.Fatal("the tracked address was refused its only token")
	}
	if lim.Allow("198.51.100.7") {
		t.Fatal("the tracked address was allowed a second call")
	}
	// Two further addresses push the first one out of a two-entry cache.
	lim.Allow("198.51.100.8")
	lim.Allow("198.51.100.9")
	if !lim.Allow("198.51.100.7") {
		t.Fatal("the evicted address did not get a fresh bucket on return")
	}
}

// An address that keeps calling keeps its place: it is not evicted by newer
// addresses while it is active, so its spent budget stays spent.
func TestActiveAddressIsNotEvicted(t *testing.T) {
	t.Parallel()
	lim := iplimit.New(iplimit.Options{RatePerMinute: 1, Burst: 1, MaxEntries: 3})
	if !lim.Allow("198.51.100.7") {
		t.Fatal("the active address was refused its only token")
	}
	for i := range 6 {
		other := fmt.Sprintf("203.0.113.%d", i)
		lim.Allow(other)
		// Touching the active address between newcomers keeps it recent.
		if lim.Allow("198.51.100.7") {
			t.Fatal("the active address regained a token it had already spent")
		}
	}
}

// The factory seam replaces bucket construction wholesale, which is how a test
// pins a refusal without waiting on a clock.
func TestFactoryOverridesBucketConstruction(t *testing.T) {
	t.Parallel()
	lim := iplimit.New(iplimit.Options{
		RatePerMinute: 1000,
		Burst:         1000,
		Factory:       func() *rate.Limiter { return rate.NewLimiter(0, 0) },
	})
	if lim.Allow("198.51.100.7") {
		t.Fatal("the injected always-refusing bucket allowed a call")
	}
}

// A zero Options value is usable: every field falls back to its default rather
// than producing a limiter that refuses or admits everything.
func TestZeroOptionsIsUsable(t *testing.T) {
	t.Parallel()
	lim := iplimit.New(iplimit.Options{})
	if !lim.Allow("198.51.100.7") {
		t.Fatal("a default limiter refused the first call")
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{"host and port", "198.51.100.7:54321", "198.51.100.7"},
		{"ipv6 host and port", "[2001:db8::1]:54321", "2001:db8::1"},
		{"no port", "198.51.100.7", "198.51.100.7"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remoteAddr
			if got := iplimit.ClientIP(r, false); got != tc.want {
				t.Fatalf("ClientIP(%q) = %q, want %q", tc.remoteAddr, got, tc.want)
			}
		})
	}
}

// On a directly-bound listener a forwarding header must not decide the budget:
// it is written by the caller, so honouring it would let one caller spend
// another address's tokens.
func TestClientIPIgnoresForwardingHeadersWhenNoProxyIsTrusted(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.7:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.Header.Set("X-Real-IP", "203.0.113.9")
	if got := iplimit.ClientIP(r, false); got != "198.51.100.7" {
		t.Fatalf("ClientIP honoured a forwarding header: got %q", got)
	}
}

// Behind a proxy the deployment controls, RemoteAddr is that proxy on every
// request. Reading it there would give the whole internet one bucket, so the
// caller's address comes from the header the proxy wrote.
func TestClientIPReadsTheForwardedAddressWhenAProxyIsTrusted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		forwarded string
		want      string
	}{
		{"one proxy", "203.0.113.9", "203.0.113.9"},
		// The proxy appends the peer it accepted, so the last entry is the one
		// it saw. Everything to its left is whatever the caller sent.
		{"caller forged earlier hops", "10.0.0.1, 127.0.0.1, 203.0.113.9", "203.0.113.9"},
		{"entry carries a port", "203.0.113.9:44321", "203.0.113.9"},
		{"ipv6 entry", "2001:db8::2", "2001:db8::2"},
		// A header that is not an address falls back to the socket rather than
		// opening a bucket named after whatever arrived.
		{"not an address", "not-an-address", "198.51.100.7"},
		{"empty", "", "198.51.100.7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			r.RemoteAddr = "198.51.100.7:54321"
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if got := iplimit.ClientIP(r, true); got != tc.want {
				t.Fatalf("ClientIP with X-Forwarded-For %q = %q, want %q", tc.forwarded, got, tc.want)
			}
		})
	}
}

// X-Real-IP is never read, whatever the deployment trusts. One header is the
// contract; a second spelling would be a second thing to get right.
func TestClientIPNeverReadsXRealIP(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.7:54321"
	r.Header.Set("X-Real-IP", "203.0.113.9")
	if got := iplimit.ClientIP(r, true); got != "198.51.100.7" {
		t.Fatalf("ClientIP read X-Real-IP: got %q", got)
	}
}
