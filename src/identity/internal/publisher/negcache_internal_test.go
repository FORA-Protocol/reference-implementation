package publisher

import (
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
)

var negAnchor = time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

func TestNegCache_EvictsToLimit(t *testing.T) {
	t.Parallel()
	c := newNegCache(time.Minute, 2, clock.NewDeterministic(negAnchor))
	c.put("a.foramcp.org")
	c.put("b.foramcp.org")
	c.put("c.foramcp.org") // over the limit → one eviction

	if got := len(c.m); got != 2 {
		t.Fatalf("negCache size = %d, want 2 (must evict, not grow with attacker-chosen names)", got)
	}
	if !c.has("c.foramcp.org") {
		t.Error("the just-put host was evicted; the newest entry should survive")
	}
}

func TestNegCache_Expires(t *testing.T) {
	t.Parallel()
	clk := clock.NewDeterministic(negAnchor)
	c := newNegCache(30*time.Second, 10, clk)
	c.put("agent.foramcp.org")
	if !c.has("agent.foramcp.org") {
		t.Fatal("entry missing immediately after put")
	}
	clk.Advance(31 * time.Second)
	if c.has("agent.foramcp.org") {
		t.Fatal("entry still present past its TTL")
	}
}

func TestNegCache_PrefersEvictingExpired(t *testing.T) {
	t.Parallel()
	clk := clock.NewDeterministic(negAnchor)
	c := newNegCache(30*time.Second, 2, clk)
	c.put("stale.foramcp.org") // will be expired by the time we overflow
	clk.Advance(31 * time.Second)
	c.put("fresh.foramcp.org")
	c.put("newer.foramcp.org") // overflow: the expired "stale" entry is the one to drop

	if c.has("stale.foramcp.org") {
		t.Error("expired entry should have been the eviction victim")
	}
	if !c.has("fresh.foramcp.org") || !c.has("newer.foramcp.org") {
		t.Error("live entries should survive eviction of an expired one")
	}
}
