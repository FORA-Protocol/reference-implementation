//go:build integration

package transport_test

// The audit rows the boot fixture writes (ADR-029 D5). They are read through the
// production audit repository, because no public surface reads the audit log.
// Each expected detail is written out here, and the fixture's path and sha256 are
// taken from the file the test wrote, not from the plan under test.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
)

const (
	bootSeatAction    = "BootFixtureCreateSeat"
	bootListingAction = "BootFixtureCreateListing"
	bootAuditSource   = "boot-fixture"
)

// bootFreeTermsStored is bootFreeTerms as the listing row stores it, decoded.
var bootFreeTermsStored = map[string]any{
	"pricing_model": "free", "permitted_functions": []any{"search"}, "attribution_required": false,
}

// bootSiteWithCDN is an ED25519 site behind cdn, listed on one seat with a path
// scope.
func bootSiteWithCDN(domain, cdn, tenantID string) string {
	return fmt.Sprintf("      - domain: %s\n        signing_scheme: ED25519\n        cdn: %s\n        listings:\n"+
		"          - tenant_id: %s\n            terms_file: terms.json\n            content_path_scope: [\"/articles/\"]\n",
		domain, cdn, tenantID)
}

// assertOneBootAuditRow requires exactly one row, written by the boot fixture,
// whose detail is want.
func assertOneBootAuditRow(t *testing.T, rows []repo.AuditRecord, want map[string]any) {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("boot audit rows = %d, want 1", len(rows))
	}
	if rows[0].SourceAddr != bootAuditSource || rows[0].Actor != nil {
		t.Fatalf("boot audit row source = %q, actor %v; want %q and none", rows[0].SourceAddr, rows[0].Actor, bootAuditSource)
	}
	if got := auditDetail(t, rows[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("boot audit detail = %v\nwant %v", got, want)
	}
}

func TestBootFixture_EachSeatAndListingItCreatesIsAuditedOnce(t *testing.T) {
	h := newRegisterHarness(t)
	seat, site := "audited-"+uuid.NewString()+".example", "boot-audited-"+uuid.NewString()+".example"
	fixture := bootFixtureYAML(
		[]bootSeat{{id: "boot-audited", domain: seat, keyRef: "boot-seat-key", activate: true}},
		"boot-audited-payee", bootSiteWithCDN(site, "cloudflare", "boot-audited"))
	path := writeBootFixture(t, fixture)
	sum := sha256.Sum256([]byte(fixture))
	plan := planBootFixtureAt(t, path)
	keys := bootKeyStore(t, "boot-seat-key")

	first, err := applyBoot(h.ctx, h.pool, h.queries, keys, plan)
	if err != nil || len(first.Created) != 4 {
		t.Fatalf("first Apply = %+v, err %v; want the seat, payee, site and listing created", first, err)
	}
	again, err := applyBoot(h.ctx, h.pool, h.queries, keys, plan)
	if err != nil || len(again.Created) != 0 {
		t.Fatalf("second Apply = %+v, err %v; want nothing created", again, err)
	}

	assertOneBootAuditRow(t, bootAuditRows(t, h.ctx, h.queries, "boot-audited", bootSeatAction), map[string]any{
		"fixture": path, "fixture_sha256": hex.EncodeToString(sum[:]), "domain": seat,
		"ed25519_key_ref": "boot-seat-key", "rsa_key_ref": "",
		"allow_broker_relay": false, "activate_new_agents_by_default": true,
	})
	assertOneBootAuditRow(t, bootAuditRows(t, h.ctx, h.queries, "boot-audited", bootListingAction), map[string]any{
		"fixture": path, "fixture_sha256": hex.EncodeToString(sum[:]), "domain": site,
		"publisher_id": "boot-audited-payee", "publisher_created": true, "site_created": true,
		"signing_scheme": "ED25519", "cdn": "cloudflare", "content_path_scope": []any{"/articles/"},
		"terms": bootFreeTermsStored, "state": "ACTIVE",
	})
}

// A fixture that lists a site already stored keeps the stored site, and its
// listing is sold under the stored site's settings, so the listing's row records
// those rather than the ones the file declares.
func TestBootFixture_AListingOnAStoredSiteRecordsTheStoredSite(t *testing.T) {
	h := newPushHarness(t)
	stored := "boot-stored-" + uuid.NewString() + ".example"
	repotest.SeedSite(t, h.ctx, h.pool, repotest.Site{
		SeatTenantID: h.tenantID, Domain: stored, PublisherID: harnessResourceOwner, CDN: "fastly", Unlisted: true,
	})
	plan := planBootFixture(t, bootFixtureYAML(
		[]bootSeat{harnessSeat(h)}, harnessResourceOwner, bootSiteWithCDN(stored, "cloudflare", h.tenantID)))

	report, err := applyBoot(h.ctx, h.pool, h.queries, h.keystore, plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !driftNames(report, "site "+stored, "cdn") {
		t.Fatalf("report = %+v, want the stored site's CDN reported as drift", report)
	}
	rows := bootAuditRows(t, h.ctx, h.queries, h.tenantID, bootListingAction)
	if len(rows) != 1 {
		t.Fatalf("the listing's audit rows = %d, want 1", len(rows))
	}
	if d := auditDetail(t, rows[0]); d["cdn"] != "fastly" || d["site_created"] != false {
		t.Fatalf("listing audit = %v, want the stored site's CDN (fastly) and site_created false", d)
	}
}
