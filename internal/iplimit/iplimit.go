// Package iplimit rate-limits anonymous HTTP endpoints per client address.
//
// Two public surfaces accept unauthenticated traffic — the Exchange's agent
// self-signup and the onboarding preview — and both need the same control: a
// token bucket per caller, answered 429 when the caller is over budget. One
// implementation rather than two keeps the eviction policy and the
// address-extraction rule from drifting apart between them.
package iplimit

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/time/rate"
)

// Defaults applied to the zero value of each Options field. They are
// deliberately conservative: every caller states its own budget, so these only
// decide what an unconfigured limiter does rather than what any deployment
// actually runs.
const (
	defaultRatePerMinute = 60
	defaultBurst         = 1
	defaultMaxEntries    = 10_000
)

// Options tune a Limiter. A zero field selects the default above.
type Options struct {
	// RatePerMinute is the sustained budget one address gets.
	RatePerMinute int
	// Burst is how many requests an address may make back to back before the
	// sustained rate binds. Burst 1 means the very first over-budget call is
	// refused.
	Burst int
	// MaxEntries bounds how many addresses are tracked at once. The bound is
	// what stops a flood of distinct addresses growing this map until the
	// process runs out of memory. It has a cost: past the bound the least
	// recently seen address is evicted, and an evicted address gets a fresh
	// bucket when it returns. A caller that floods from many addresses can
	// therefore give itself a reset, which is the weaker of the two failures.
	MaxEntries int
	// Factory overrides bucket construction. When nil, buckets are built from
	// RatePerMinute and Burst. Tests inject this to build a deterministic
	// bucket — a limiter that refuses everything, say — without waiting on a
	// real clock.
	Factory func() *rate.Limiter
}

// Limiter hands each client address its own token bucket.
type Limiter struct {
	buckets *lru.Cache[string, *rate.Limiter]
	build   func() *rate.Limiter
}

// New builds a Limiter. It never fails: an out-of-range field takes its
// default, because both callers already treat a zero option as "use the
// default" and one of them constructs its handler without an error return.
func New(opts Options) *Limiter {
	build := opts.Factory
	if build == nil {
		rpm := opts.RatePerMinute
		if rpm <= 0 {
			rpm = defaultRatePerMinute
		}
		burst := opts.Burst
		if burst <= 0 {
			burst = defaultBurst
		}
		perSecond := rate.Limit(float64(rpm) / 60.0)
		build = func() *rate.Limiter { return rate.NewLimiter(perSecond, burst) }
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}
	// lru.New only errors on a non-positive size, which the clamp above rules
	// out; the value is dropped rather than propagated so the constructor can
	// stay total.
	buckets, _ := lru.New[string, *rate.Limiter](maxEntries)
	return &Limiter{buckets: buckets, build: build}
}

// Allow reports whether ip may make one request now, and spends a token when it
// may.
func (l *Limiter) Allow(ip string) bool {
	return l.bucketFor(ip).Allow()
}

// bucketFor returns ip's bucket, creating one on first sight. A hit goes
// through Get rather than Peek so an address that keeps calling keeps its place
// in the cache and is not evicted while it is active. Two callers racing on the
// same new address both reach PeekOrAdd, which admits one bucket and hands the
// loser that same bucket — without it, the second caller would install a second
// bucket over the first and hand itself a full one.
func (l *Limiter) bucketFor(ip string) *rate.Limiter {
	if bucket, ok := l.buckets.Get(ip); ok {
		return bucket
	}
	fresh := l.build()
	if existing, ok, _ := l.buckets.PeekOrAdd(ip, fresh); ok {
		return existing
	}
	return fresh
}

// ClientIP extracts the caller's address.
//
// Which address that is depends on how the service is deployed, so the
// deployment has to say. A directly-bound listener reads RemoteAddr: the
// address the connection came from is the caller, and X-Forwarded-For is
// written by that caller, so honouring it there would let anyone spend another
// address's budget — or claim an address on an allowlist.
//
// Behind a reverse proxy the deployment controls, RemoteAddr is the proxy on
// every request, which collapses a per-address budget into one bucket the whole
// internet shares: one script then makes a public endpoint answer 429 to
// everybody. There the caller's address is the last entry of X-Forwarded-For,
// which is the address that proxy itself saw. Entries to its left are whatever
// the caller sent and are never read.
//
// trustProxyHeaders is the same opt-in that decides whether the forwarded
// scheme is believed, and it comes from the deployment's configuration, never
// from the request.
func ClientIP(r *http.Request, trustProxyHeaders bool) string {
	if trustProxyHeaders {
		if ip := lastForwardedFor(r.Header.Get("X-Forwarded-For")); ip != "" {
			return ip
		}
	}
	return remoteHost(r.RemoteAddr)
}

// lastForwardedFor reads the address the trusted proxy saw: the last entry of
// the header, since a proxy appends the peer it accepted the connection from.
// A value that is not an address is ignored, so a malformed header falls back
// to the socket rather than creating a bucket named after whatever was sent.
func lastForwardedFor(header string) string {
	last := header
	if i := strings.LastIndexByte(header, ','); i >= 0 {
		last = header[i+1:]
	}
	candidate := remoteHost(strings.TrimSpace(last))
	if _, err := netip.ParseAddr(candidate); err != nil {
		return ""
	}
	return candidate
}

// remoteHost drops the port from an address that carries one, and the brackets
// from an IPv6 literal. A value with neither is returned as it stands.
func remoteHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]")
}
