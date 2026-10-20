//go:build integration

package transport_test

import (
	"context"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
)

// contributorOnlyManifests authorizes the caller as a catalog contributor for any
// domain but attests NO resource_owner_id, so the contributor gate (Gate 2) passes
// and the resource-owner gate rejects. Drives the missing-attestation path.
type contributorOnlyManifests struct{ caller string }

func (c contributorOnlyManifests) Get(_ context.Context, domain string) (*forawellknown.Manifest, error) {
	return &forawellknown.Manifest{
		Ver: forawellknown.Version, Role: forawellknown.RolePublisher, Domain: domain,
		CatalogContributors: []*forav1.CatalogContributor{{Domain: c.caller, Relationship: "publisher"}},
	}, nil
}

// readCatalog reads every catalog row back through the production CatalogRepo.
//
// resource_owner_id has no public read RPC — it is a server-side payee key, never on
// the wire (ADR-010: reporting reads accrued revenue by resource_owner_id via the
// future RevenueReport surface, not a catalog-entry read). Asserting it through the
// CatalogRepo is the sanctioned Testing Doctrine §9 tier-2 fallback until that read
// surface exists; the follow-up is the RevenueReport read path (ADR-010 D2).
func readCatalog(t *testing.T, h *testHarness) []repo.CatalogEntry {
	t.Helper()
	rows, err := repo.NewCatalogRepo(h.queries).ListAll(h.ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	return rows
}

// TestPushResources_PersistsResourceOwnerID drives a push through the public
// CatalogService.PushResources RPC and asserts the attested resource_owner_id lands
// on the catalog entry. The default harness manifest stub attests harnessResourceOwner.
func TestPushResources_PersistsResourceOwnerID(t *testing.T) {
	h := newTestHarness(t)
	resp, err := h.catalogClient.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, "agent-test", []*forav1.ResourceEntry{{Domain: h.siteDomain, Path: "/article-1"}})))
	if err != nil {
		t.Fatalf("PushResources: %v", err)
	}
	if got := resp.Msg.GetAccepted(); got != 1 {
		t.Fatalf("accepted = %d, want 1", got)
	}

	rows := readCatalog(t, h)
	if len(rows) != 1 {
		t.Fatalf("catalog rows = %d, want 1", len(rows))
	}
	if rows[0].ResourceOwnerID != harnessResourceOwner {
		t.Fatalf("resource_owner_id = %q, want %q", rows[0].ResourceOwnerID, harnessResourceOwner)
	}
}

// TestPushResources_RejectsMissingResourceOwner proves an un-attested push is
// rejected with missing_resource_owner_id and persists nothing (all-or-nothing): the
// payee is never inferred. Drives a two-entry batch through the public RPC.
func TestPushResources_RejectsMissingResourceOwner(t *testing.T) {
	h := newTestHarnessWith(t, harnessOptions{manifests: contributorOnlyManifests{caller: "agent-test"}})
	_, err := h.catalogClient.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, "agent-test", []*forav1.ResourceEntry{
		{Domain: h.siteDomain, Path: "/a"},
		{Domain: h.siteDomain, Path: "/b"},
	})))
	if err == nil {
		t.Fatal("expected PushResources to be rejected for missing resource_owner_id")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
	if !strings.Contains(err.Error(), "missing_resource_owner_id") {
		t.Fatalf("error %q does not name missing_resource_owner_id", err.Error())
	}
	if rows := readCatalog(t, h); len(rows) != 0 {
		t.Fatalf("catalog rows = %d, want 0 (nothing persisted)", len(rows))
	}
}

// TestPushResources_ResourceOwnerGroupsDomains proves the grouping property: an
// owner that attests the same resource_owner_id across two SITES (here the
// harness stub attests harnessResourceOwner for every domain) settles both into
// one payee. Both sites are registered under that payee and listed on the same
// seat, which is what the grouping exists for — one payee, many sites.
func TestPushResources_ResourceOwnerGroupsDomains(t *testing.T) {
	h := newTestHarness(t)
	const (
		domain2 = "sibling.example"
		path1   = "/x"
		path2   = "/y"
	)
	// A second site under the SAME payee. NewPublisher stays false because the
	// harness bring-up already registered harnessResourceOwner.
	repotest.SeedSite(t, h.ctx, h.pool, repotest.Site{
		SeatTenantID: h.tenantID,
		Domain:       domain2,
		PublisherID:  harnessResourceOwner,
	})

	push := func(domain, path string) {
		t.Helper()
		if _, err := h.catalogClient.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, "agent-test", []*forav1.ResourceEntry{{Domain: domain, Path: path}}))); err != nil {
			t.Fatalf("push %s: %v", domain, err)
		}
	}
	push(h.siteDomain, path1)
	push(domain2, path2)

	rows := readCatalog(t, h)
	if len(rows) != 2 {
		t.Fatalf("catalog rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.ResourceOwnerID != harnessResourceOwner {
			t.Fatalf("entry %q resource_owner_id = %q, want %q (grouped)", r.URI, r.ResourceOwnerID, harnessResourceOwner)
		}
	}
}
