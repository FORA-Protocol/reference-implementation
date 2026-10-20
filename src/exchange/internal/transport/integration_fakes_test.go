//go:build integration

package transport_test

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// harnessResourceOwner is the resource_owner_id the catch-all manifest stub
// attests for every push, so accepted entries carry a payee and pass the gate.
const harnessResourceOwner = "owner-test"

// allowAllManifestCache returns a publisher manifest that lists the caller as a
// catalog_contributor for the requested domain AND attests resourceOwner on
// exchangeDomain, so both the contributor gate (Gate 2) and the resource-owner
// gate accept in the catch-all test harness. It satisfies service.ManifestCache.
type allowAllManifestCache struct {
	caller         string
	exchangeDomain string
	resourceOwner  string
}

// newAllowAllManifestCache builds the catch-all stub that authorizes caller and
// attests harnessResourceOwner on harnessExchangeDomain, so both the contributor
// gate and the resource-owner gate accept. Use this everywhere rather than the
// bare struct literal, so no construction site forgets the attestation.
func newAllowAllManifestCache(caller string) *allowAllManifestCache {
	return &allowAllManifestCache{
		caller:         caller,
		exchangeDomain: harnessExchangeDomain,
		resourceOwner:  harnessResourceOwner,
	}
}

// Get serves the document the publisher console would generate for domain,
// built by the shared builder rather than written out here, so the harness
// cannot drift from the file a publisher actually deploys.
func (c *allowAllManifestCache) Get(_ context.Context, domain string) (*forawellknown.Manifest, error) {
	return exchangetest.PublisherManifest(c.exchangeDomain, c.caller, domain, c.resourceOwner), nil
}
