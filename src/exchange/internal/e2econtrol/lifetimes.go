//go:build e2e

// Package e2econtrol is the Exchange's e2e-only control service (ADR-028 D6).
//
// It sets state the e2e suite needs and no admin RPC reaches, through the two
// controls ADR-028 D6 names for the Exchange:
//
//   - per-tenant offer and delivery-URL lifetimes, so a test can make one seat
//     issue short-lived offers and URLs and then poll until they expire. There
//     is no test clock: shifting one service's clock would break the signature
//     windows between the services of the stack, while a short lifetime does
//     not;
//   - the tenant relay policy, whether a seat accepts purchases a Broker relays,
//     set through the tenant repository rather than by a test writing the
//     database (relay.go).
//
// Every file in this package carries the e2e build constraint, and only the
// e2e-tagged wiring in cmd/server imports it. A build without the tag that
// imports it cannot compile, because the constraint excludes every file. The
// routes are mounted on the admin listener, behind its address allowlist.
//
// A lifetime can only be shortened. The Exchange applies an override only when it
// is shorter than the configured lifetime (service.Lifetimes), and this package
// refuses one that is not, so no control here can make an offer or a delivery URL
// valid for longer than production would.
package e2econtrol

import (
	"sync"
	"time"
)

// Lifetimes holds the per-tenant overrides. It satisfies service.Lifetimes and
// is safe for concurrent use: the control routes write it while the Exchange
// reads it on every discovery and purchase.
type Lifetimes struct {
	mu sync.RWMutex
	// configured are the lifetimes the deployment issues with; an override must
	// be shorter.
	configured Pair
	overrides  map[string]Pair
}

// Pair is a tenant's offer and delivery-URL lifetimes. A zero member states no
// override for that lifetime.
type Pair struct {
	Offer time.Duration
	URL   time.Duration
}

// NewLifetimes holds no override yet. configured is what the Exchange issues
// with when a tenant has none, and the ceiling every override must stay below.
func NewLifetimes(configured Pair) *Lifetimes {
	return &Lifetimes{configured: configured, overrides: make(map[string]Pair)}
}

// OfferTTL reports tenantID's offer-lifetime override.
func (l *Lifetimes) OfferTTL(tenantID string) (time.Duration, bool) {
	pair := l.override(tenantID)
	return pair.Offer, pair.Offer > 0
}

// URLTTL reports tenantID's delivery-URL-lifetime override.
func (l *Lifetimes) URLTTL(tenantID string) (time.Duration, bool) {
	pair := l.override(tenantID)
	return pair.URL, pair.URL > 0
}

// Effective is what tenantID's offers and URLs are issued with now.
func (l *Lifetimes) Effective(tenantID string) Pair {
	pair := l.override(tenantID)
	if pair.Offer == 0 {
		pair.Offer = l.configured.Offer
	}
	if pair.URL == 0 {
		pair.URL = l.configured.URL
	}
	return pair
}

func (l *Lifetimes) override(tenantID string) Pair {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.overrides[tenantID]
}

// set replaces tenantID's overrides. The caller has checked them (validate).
func (l *Lifetimes) set(tenantID string, pair Pair) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if pair == (Pair{}) {
		delete(l.overrides, tenantID)
		return
	}
	l.overrides[tenantID] = pair
}
