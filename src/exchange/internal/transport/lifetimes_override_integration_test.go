//go:build integration

package transport_test

import (
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// Per-tenant lifetime overrides (service.Lifetimes), the seam the e2e control
// service drives (ADR-028 D6). An override may shorten what the Exchange issues
// and never extend it, so the seam cannot make an offer or a delivery URL valid
// for longer than production would.
//
// Round trip: the override is set on the service's Lifetimes port at bring-up
// (the e2e control service is that port in an e2e build), and observed on the
// wire. The offer's expires_at is read off DiscoverResources, and the delivery
// URL's off ExecuteTransaction, both through the public Connect-Go routes on a
// deterministic service clock.

// fixedLifetimes is one tenant's overrides: what the e2e control service holds
// after a PUT for that tenant.
type fixedLifetimes struct {
	tenantID   string
	offer, url time.Duration
}

func (f fixedLifetimes) OfferTTL(tenantID string) (time.Duration, bool) {
	return f.offer, tenantID == f.tenantID && f.offer != 0
}

func (f fixedLifetimes) URLTTL(tenantID string) (time.Duration, bool) {
	return f.url, tenantID == f.tenantID && f.url != 0
}

// lifetimesHarness is a harness on a deterministic clock whose seat carries
// overrides set by override, which receives the seat's tenant id.
func lifetimesHarness(t *testing.T, override func(tenantID string) fixedLifetimes) *testHarness {
	t.Helper()
	clk := clock.NewDeterministic(time.Unix(1_800_000_000, 0).UTC())
	lifetimes := &deferredLifetimes{}
	h := newTestHarnessWith(t, harnessOptions{clk: clk, lifetimes: lifetimes})
	lifetimes.inner = override(h.tenantID)
	return h
}

// deferredLifetimes lets the harness be built before the seat's tenant id is
// known, which is what the override is keyed on. Nothing is discovered or
// bought before inner is set.
type deferredLifetimes struct{ inner service.Lifetimes }

func (d *deferredLifetimes) OfferTTL(tenantID string) (time.Duration, bool) {
	if d.inner == nil {
		return 0, false
	}
	return d.inner.OfferTTL(tenantID)
}

func (d *deferredLifetimes) URLTTL(tenantID string) (time.Duration, bool) {
	if d.inner == nil {
		return 0, false
	}
	return d.inner.URLTTL(tenantID)
}

func TestLifetimeOverride_ShortensTheSeatsOffersAndDeliveryURLs(t *testing.T) {
	const offerTTL, urlTTL = 7 * time.Second, 11 * time.Second
	h := lifetimesHarness(t, func(tenantID string) fixedLifetimes {
		return fixedLifetimes{tenantID: tenantID, offer: offerTTL, url: urlTTL}
	})
	seedCatalog(t, h)

	offer := discoverFirst(t, h)[0]
	if got, want := offer.GetExpiresAt().AsTime(), h.now().Add(offerTTL); !got.Equal(want) {
		t.Fatalf("offer expires_at = %v, want now+%v = %v", got, offerTTL, want)
	}

	resp, err := executeSingleItem(t, h, "tx-short-lifetimes", offer)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	item := singleResultItem(t, resp)
	if got, want := item.GetExpiresAt().AsTime(), h.now().Add(urlTTL); !got.Equal(want) {
		t.Fatalf("delivery URL expires_at = %v, want now+%v = %v", got, urlTTL, want)
	}
}

func TestLifetimeOverride_NeverExtendsWhatTheSeatIssues(t *testing.T) {
	h := lifetimesHarness(t, func(tenantID string) fixedLifetimes {
		return fixedLifetimes{tenantID: tenantID, offer: time.Hour, url: time.Hour}
	})
	seedCatalog(t, h)

	offer := discoverFirst(t, h)[0]
	if got, want := offer.GetExpiresAt().AsTime(), h.now().Add(service.DefaultOfferTTL); !got.Equal(want) {
		t.Fatalf("offer expires_at = %v, want the configured now+%v = %v; an override extended it",
			got, service.DefaultOfferTTL, want)
	}
	resp, err := executeSingleItem(t, h, "tx-long-lifetimes", offer)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	item := singleResultItem(t, resp)
	if got, want := item.GetExpiresAt().AsTime(), h.now().Add(service.DefaultURLTTL); !got.Equal(want) {
		t.Fatalf("delivery URL expires_at = %v, want the configured now+%v = %v; an override extended it",
			got, service.DefaultURLTTL, want)
	}
}

func TestLifetimeOverride_LeavesOtherTenantsAlone(t *testing.T) {
	h := lifetimesHarness(t, func(string) fixedLifetimes {
		return fixedLifetimes{tenantID: "another-seat", offer: time.Second, url: time.Second}
	})
	seedCatalog(t, h)

	offer := discoverFirst(t, h)[0]
	if got, want := offer.GetExpiresAt().AsTime(), h.now().Add(service.DefaultOfferTTL); !got.Equal(want) {
		t.Fatalf("offer expires_at = %v, want the configured now+%v = %v; another seat's override "+
			"reached this one", got, service.DefaultOfferTTL, want)
	}
}
