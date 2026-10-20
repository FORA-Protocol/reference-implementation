//go:build integration

package transport_test

// The registration gates on the catalog push path (per ADR-026 D1 and D2): the
// entry's domain must be a registered site, the payee its manifest attests must
// be the one that site is registered under, and this seat must list the site as
// ACTIVE.
//
// Every test here drives the public CatalogService.PushResources RPC over the
// real HTTP surface with a real RFC 9421 signature, against real manifests
// served by httptest origins. Each refusal is asserted three ways: the caller's
// error, the absence of the row through the public DiscoverResources RPC, and
// the operator's audit line.

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// registrationGateCase is one site the gate tests stand up: the domain, the
// manifest origin serving it, and a signed catalog client for a caller that
// manifest lists as a contributor.
type registrationGateCase struct {
	domain string
	client foraconnect.CatalogServiceClient
}

// newSiteWithContributor stands up a manifest origin for domain that lists one
// freshly-keyed caller as a catalog contributor, registers that caller's own
// key directory, and returns a catalog client signing as it.
//
// It registers NOTHING in the database. Each test decides for itself which parts
// of the registration the site has, because the missing part is what the test is
// about.
func newSiteWithContributor(t *testing.T, h *pushHarness, domain, callerID string) registrationGateCase {
	t.Helper()
	h.publishPublisher(t, domain, callerID)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("contributor keypair for %s: %v", domain, err)
	}
	h.publishAgent(t, callerID, priv)
	return registrationGateCase{domain: domain, client: h.signedCat(callerID, priv)}
}

// pushOne pushes a single entry for the case's site and returns the RPC error,
// along with the URI the entry materializes to.
func (c registrationGateCase) pushOne(t *testing.T, h *pushHarness, callerID, path string) (string, error) {
	t.Helper()
	_, err := c.client.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, callerID,
		[]*forav1.ResourceEntry{{
			Domain: c.domain, Path: path,
			Terms: []*forav1.LicenseTerm{seedPricedTerm()},
		}})))
	return "https://" + c.domain + path, err
}

// assertRegistrationRefusal checks the three halves of a refused push: the
// caller's InvalidArgument naming the reason, no discoverable row at the URI,
// and the operator's audit line under the same reason.
func assertRegistrationRefusal(t *testing.T, h *pushHarness, uri string, err error, wantReason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("push for %s was accepted; want a refusal under %q", uri, wantReason)
	}
	assertConnectCode(t, err, connect.CodeInvalidArgument)
	if !strings.Contains(err.Error(), wantReason) {
		t.Fatalf("refusal %q does not name %q", err.Error(), wantReason)
	}
	assertOfferCount(t, h, uri, 0)
	assertCatalogRejectLoggedForURI(t, h, uri, wantReason)
}

// TestPushResources_RegisteredPublisherAccepted is the positive case the three
// refusals below are the complement of: a site registered under the payee its
// manifest attests, listed ACTIVE on this seat, accepts a push from a listed
// contributor.
//
// Round-trip: the write goes through the public PushResources RPC and the entry
// is read back as an offer through the public DiscoverResources RPC. The payee
// is then read through the production repository ports, because
// resource_owner_id has no public read surface — it is a server-side settlement
// key and never on the wire — which is the documented Testing Doctrine fallback
// tier. The follow-up that would retire it is the revenue-report read path.
func TestPushResources_RegisteredPublisherAccepted(t *testing.T) {
	h := newPushHarness(t)
	const callerID = "registered-caller.example"
	h.publisher.setContributors(callerID)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	h.publishAgent(t, callerID, priv)

	const path = "/articles/registered"
	if _, err := h.signedCat(callerID, priv).PushResources(h.ctx, connect.NewRequest(
		newPushRequest(h.tenantID, callerID, []*forav1.ResourceEntry{{
			Domain: h.publisherDom, Path: path,
			Terms: []*forav1.LicenseTerm{seedPricedTerm()},
		}}))); err != nil {
		t.Fatalf("push for a registered, actively listed site: %v", err)
	}
	uri := "https://" + h.publisherDom + path
	assertOfferCount(t, h, uri, 1)

	rows, err := repo.NewCatalogRepo(h.queries).ListAll(h.ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	var stored *repo.CatalogEntry
	for i := range rows {
		if rows[i].URI == uri {
			stored = &rows[i]
		}
	}
	if stored == nil {
		t.Fatalf("no catalog row for %s", uri)
	}
	if stored.TenantID != h.tenantID {
		t.Errorf("row tenant_id = %q, want the seat %q", stored.TenantID, h.tenantID)
	}
	// The stored payee is a real publisher record, not a free-form string: it
	// resolves to the row the site was registered under.
	payee, err := repo.NewPublisherReadRepo(h.queries).ByID(h.ctx, stored.ResourceOwnerID)
	if err != nil {
		t.Fatalf("stored resource_owner_id %q does not resolve to a publisher: %v",
			stored.ResourceOwnerID, err)
	}
	if payee.ID != harnessResourceOwner {
		t.Errorf("row settles to publisher %q, want %q", payee.ID, harnessResourceOwner)
	}
}

// TestPushResources_AttestedOwnerMismatchRejected drives the case ADR-026 D2
// exists for: the site is registered, but under a different payee than the one
// its served manifest attests for this Exchange. Attaching a domain to a payee
// needs both halves to agree, so the push is refused and the row is not written.
func TestPushResources_AttestedOwnerMismatchRejected(t *testing.T) {
	h := newPushHarness(t)
	const (
		domain   = "mismatch-publisher.example"
		callerID = "mismatch-caller.example"
		// A second payee, registered and real. The manifest origin attests
		// harnessResourceOwner for every domain it serves, so registering the
		// site under this one is what puts the two halves in disagreement.
		otherPayee = "owner-someone-else"
	)
	repotest.SeedPublisher(t, h.ctx, h.pool, otherPayee, "Harmonia Records")
	repotest.SeedSite(t, h.ctx, h.pool, repotest.Site{
		SeatTenantID: h.tenantID,
		Domain:       domain,
		PublisherID:  otherPayee,
	})

	site := newSiteWithContributor(t, h, domain, callerID)
	uri, err := site.pushOne(t, h, callerID, "/articles/mismatch")
	assertRegistrationRefusal(t, h, uri, err, service.RejectionReasonResourceOwnerMismatch)
}

// TestPushResources_UnregisteredDomainRejected proves a site nobody registered
// cannot enter the catalog, even when it serves a perfectly good manifest and
// the caller it lists is a legitimate signer. Nothing on this Exchange knows who
// owns that domain, so no row can name a payee for it.
func TestPushResources_UnregisteredDomainRejected(t *testing.T) {
	h := newPushHarness(t)
	const (
		domain   = "unregistered-publisher.example"
		callerID = "unregistered-caller.example"
	)
	site := newSiteWithContributor(t, h, domain, callerID)
	uri, err := site.pushOne(t, h, callerID, "/articles/unregistered")
	assertRegistrationRefusal(t, h, uri, err, service.RejectionReasonUnregisteredPublisherDomain)
}

// TestPushResources_ContributorGateHidesListingState is the ordering claim with
// teeth. The site is registered under the attested payee but its listing on this
// seat is still PENDING_MANIFEST, and the caller is not in the site's
// catalog_contributors[]. Two checks would refuse this push, and which reason
// comes back is exactly what the gate order decides.
//
// The contributor check runs first, so the stranger is told they are not a
// contributor and learns nothing about whether this seat sells the site. Move
// checkRegistration above resolveManifestGates and this test fails with
// publisher_domain_listing_not_active — which is the leak the order prevents.
//
// What the order does NOT hide is whether the domain is registered at all:
// resolveSite runs before the manifest fetch so an unregistered domain costs no
// outbound request, and TestPushResources_UnregisteredDomainRejected pins that
// reason. Registration existence is disclosed; the payee and the listing state
// are not.
func TestPushResources_ContributorGateHidesListingState(t *testing.T) {
	h := newPushHarness(t)
	const (
		domain         = "pending-hidden-publisher.example"
		listedCaller   = "listed-caller.example"
		strangerCaller = "stranger-hidden-caller.example"
	)
	// Registered under the right payee, but this seat is not selling it yet.
	repotest.SeedSite(t, h.ctx, h.pool, repotest.Site{
		SeatTenantID: h.tenantID,
		Domain:       domain,
		PublisherID:  harnessResourceOwner,
		Pending:      true,
	})
	// The site's manifest names a contributor the pushing caller is not.
	site := newSiteWithContributor(t, h, domain, listedCaller)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("stranger keypair: %v", err)
	}
	h.publishAgent(t, strangerCaller, priv)

	const path = "/articles/pending-hidden"
	_, err = h.signedCat(strangerCaller, priv).PushResources(h.ctx, connect.NewRequest(
		newPushRequest(h.tenantID, strangerCaller, []*forav1.ResourceEntry{{
			Domain: site.domain, Path: path,
			Terms: []*forav1.LicenseTerm{seedPricedTerm()},
		}})))
	uri := "https://" + site.domain + path
	assertRegistrationRefusal(t, h, uri, err, service.RejectionReasonNotInContributors)
	if err != nil && strings.Contains(err.Error(), service.RejectionReasonListingNotActive) {
		t.Errorf("the refusal names %q, so it discloses this seat's listing state to a non-contributor",
			service.RejectionReasonListingNotActive)
	}
}

// TestPushResources_PendingListingRejected proves the listing state gates the
// push. The site is registered under the right payee, but its listing on this
// seat is still PENDING_MANIFEST, so this Exchange is not selling it yet and the
// push is refused.
func TestPushResources_PendingListingRejected(t *testing.T) {
	h := newPushHarness(t)
	const (
		domain   = "pending-publisher.example"
		callerID = "pending-caller.example"
	)
	repotest.SeedSite(t, h.ctx, h.pool, repotest.Site{
		SeatTenantID: h.tenantID,
		Domain:       domain,
		PublisherID:  harnessResourceOwner,
		Pending:      true,
	})

	site := newSiteWithContributor(t, h, domain, callerID)
	uri, err := site.pushOne(t, h, callerID, "/articles/pending")
	assertRegistrationRefusal(t, h, uri, err, service.RejectionReasonListingNotActive)
}

// TestPushResources_ContributorGateSurvivesRegistration is the ordering claim:
// the registration gates ADD to the contributor gate, they do not replace it. The
// site here is registered under the attested payee and listed ACTIVE, so every
// registration check passes — and the push is still refused, because the caller
// is not in the site's catalog_contributors[].
//
// This case alone cannot distinguish the two orderings, because a fully
// registered site passes every registration check either way.
// TestPushResources_ContributorGateHidesListingState below is the one that can.
func TestPushResources_ContributorGateSurvivesRegistration(t *testing.T) {
	h := newPushHarness(t)
	// The harness's own site is registered and actively listed; its manifest
	// lists a DIFFERENT caller as the contributor.
	const (
		listedCaller   = "listed-caller.example"
		strangerCaller = "stranger-caller.example"
	)
	h.publisher.setContributors(listedCaller)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	h.publishAgent(t, strangerCaller, priv)

	const path = "/articles/stranger"
	_, err = h.signedCat(strangerCaller, priv).PushResources(h.ctx, connect.NewRequest(
		newPushRequest(h.tenantID, strangerCaller, []*forav1.ResourceEntry{{
			Domain: h.publisherDom, Path: path,
			Terms: []*forav1.LicenseTerm{seedPricedTerm()},
		}})))
	assertRegistrationRefusal(t, h, "https://"+h.publisherDom+path, err,
		service.RejectionReasonNotInContributors)
}
