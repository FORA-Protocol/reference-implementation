//go:build integration

package transport_test

// The Exchange's boot fixture (ADR-029), driven through the surfaces its rows
// are for. The write leg calls the service chain run() uses at boot directly,
// not run() or its boot step: bootfixture.Load reads the file,
// service.PlanBootFixture checks it, and BootFixtureService.Apply writes it.
// That the boot stops on an error from Apply is not driven here. The read legs
// are public RPCs: a declared
// site's catalog push is accepted and an undeclared one refused
// (CatalogService.PushResources, read back through DiscoverResources), and a
// declared seat opens accounts under its declared activation policy
// (ExchangeService.Register and GetAccountStatus). The audit rows are read
// through the production repository port, because no public surface reads the
// audit log; the console registration suite takes the same documented fallback.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/bootfixture"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/denomination"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
)

const bootFreeTerms = `{"pricing_model":"free","permitted_functions":["search"],"attribution_required":false}`

// bootSeat is one seat of a fixture, written as the file declares it.
type bootSeat struct {
	id, domain, keyRef, rsaRef string
	relay, activate            bool
}

func (s bootSeat) yaml() string {
	out := fmt.Sprintf("  - tenant_id: %s\n    domain: %s\n    ed25519_key_ref: %s\n", s.id, s.domain, s.keyRef)
	if s.rsaRef != "" {
		out += fmt.Sprintf("    rsa_key_ref: %s\n", s.rsaRef)
	}
	return out + fmt.Sprintf("    allow_broker_relay: %t\n    activate_new_agents_by_default: %t\n", s.relay, s.activate)
}

// bootSite is one ED25519 site of the harness's payee, listed on one seat.
func bootSite(domain, tenantID string) string {
	return fmt.Sprintf("      - domain: %s\n        signing_scheme: ED25519\n        listings:\n"+
		"          - tenant_id: %s\n            terms_file: terms.json\n", domain, tenantID)
}

// bootFixtureYAML is a fixture with seats and, when sites are given, the payee
// publisherID owning them, under the display name the harness seeds its payee
// with, so declaring the harness's own payee reports no drift.
func bootFixtureYAML(seats []bootSeat, publisherID string, sites ...string) string {
	var b strings.Builder
	b.WriteString("version: 1\nseats:\n")
	for _, s := range seats {
		b.WriteString(s.yaml())
	}
	if len(sites) > 0 {
		fmt.Fprintf(&b, "publishers:\n  - publisher_id: %s\n    display_name: Stoa Press\n    sites:\n", publisherID)
		for _, site := range sites {
			b.WriteString(site)
		}
	}
	return b.String()
}

// writeBootFixture writes the fixture and its terms file to a directory of
// their own and returns the fixture's path.
func writeBootFixture(t *testing.T, fixture string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"fixture.yml": fixture, "terms.json": bootFreeTerms} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return filepath.Join(dir, "fixture.yml")
}

// planBootFixture writes the fixture and its terms file and reads them back the
// way run() does.
func planBootFixture(t *testing.T, fixture string) service.BootPlan {
	t.Helper()
	return planBootFixtureAt(t, writeBootFixture(t, fixture))
}

// planBootFixtureAt reads and plans the fixture at path the way run() does.
func planBootFixtureAt(t *testing.T, path string) service.BootPlan {
	t.Helper()
	f, err := bootfixture.Load(path)
	if err != nil {
		t.Fatalf("load the fixture: %v", err)
	}
	plan, err := service.PlanBootFixture(f, denomination.FromEnv())
	if err != nil {
		t.Fatalf("plan the fixture: %v", err)
	}
	return plan
}

func applyBoot(
	ctx context.Context, pool *pgxpool.Pool, queries *sqlc.Queries, keys service.BootKeys, plan service.BootPlan,
) (service.BootReport, error) {
	return service.NewBootFixtureServiceFromPool(pool, queries, keys).Apply(ctx, plan)
}

// harnessSeat is the push harness's own seat, declared as it is stored.
func harnessSeat(h *pushHarness) bootSeat {
	return bootSeat{
		id: h.tenantID, domain: harnessExchangeDomain, keyRef: repotest.SeatEd25519KeyRef(h.tenantID),
		rsaRef: cloudFrontTestKeyRef, relay: true, activate: true,
	}
}

func assertFailedPrecondition(t *testing.T, err error, names string) {
	t.Helper()
	var exErr *exchange.Error
	if !errors.As(err, &exErr) || exErr.Kind != exchange.KindFailedPrecondition {
		t.Fatalf("Apply = %v, want a FailedPrecondition", err)
	}
	if !strings.Contains(err.Error(), names) {
		t.Fatalf("Apply = %q, want it to name %q", err, names)
	}
}

// bootKeyStore holds one fresh Ed25519 key under ref, for a seat a fixture
// creates.
func bootKeyStore(t *testing.T, ref string) *signing.InMemoryKeyStore {
	t.Helper()
	keys := signing.NewInMemoryKeyStore()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	keys.PutEd25519(ref, pub, priv)
	return keys
}

// bootAuditRows reads the boot fixture's audit rows of action on tenantID.
func bootAuditRows(t *testing.T, ctx context.Context, queries *sqlc.Queries, tenantID, action string) []repo.AuditRecord {
	t.Helper()
	return auditRecordsOf(t, ctx, repo.NewAuditRepo(queries), tenantID, action)
}

func TestBootFixture_ADeclaredSiteIsSellableAndAnUndeclaredOneIsNot(t *testing.T) {
	h := newPushHarness(t)
	declared := "boot-" + uuid.NewString() + ".example"
	plan := planBootFixture(t, bootFixtureYAML(
		[]bootSeat{harnessSeat(h)}, harnessResourceOwner, bootSite(declared, h.tenantID)))

	report, err := applyBoot(h.ctx, h.pool, h.queries, h.keystore, plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(report.Created) != 2 || len(report.Drift) != 0 {
		t.Fatalf("report = %+v, want the site and its listing created and nothing drifted", report)
	}
	listed := bootAuditRows(t, h.ctx, h.queries, h.tenantID, bootListingAction)
	if len(listed) != 1 {
		t.Fatalf("the listing's audit rows = %d, want 1", len(listed))
	}
	if d := auditDetail(t, listed[0]); d["publisher_created"] != false || d["site_created"] != true {
		t.Fatalf("listing audit = %v, want the stored payee kept and the site created", d)
	}

	const caller = "boot-contributor.example"
	uri, err := newSiteWithContributor(t, h, declared, caller).pushOne(t, h, caller, "/articles/boot")
	if err != nil {
		t.Fatalf("a push for the declared site was refused: %v", err)
	}
	assertOfferCount(t, h, uri, 1)

	undeclared := "boot-undeclared-" + uuid.NewString() + ".example"
	const other = "boot-other-contributor.example"
	uri, err = newSiteWithContributor(t, h, undeclared, other).pushOne(t, h, other, "/articles/boot")
	assertRegistrationRefusal(t, h, uri, err, service.RejectionReasonUnregisteredPublisherDomain)

	again, err := applyBoot(h.ctx, h.pool, h.queries, h.keystore, plan)
	if err != nil || len(again.Created) != 0 || len(again.Kept) != 4 {
		t.Fatalf("a second Apply = %+v, err %v; want everything kept and nothing created", again, err)
	}
	if got := len(bootAuditRows(t, h.ctx, h.queries, h.tenantID, bootListingAction)); got != 1 {
		t.Fatalf("the listing's audit rows after a second Apply = %d, want still 1", got)
	}
}

// heldAndOpenSeats applies a fixture declaring one seat that holds new agents
// for review and one that activates them, and returns their domains.
func heldAndOpenSeats(t *testing.T, h *registerHarness) (string, string) {
	t.Helper()
	keys := bootKeyStore(t, "boot-seat-key")
	held, open := "held-"+uuid.NewString()+".example", "open-"+uuid.NewString()+".example"
	plan := planBootFixture(t, bootFixtureYAML([]bootSeat{
		{id: "boot-held", domain: held, keyRef: "boot-seat-key"},
		{id: "boot-open", domain: open, keyRef: "boot-seat-key", activate: true},
	}, ""))
	if _, err := applyBoot(h.ctx, h.pool, h.queries, keys, plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return held, open
}

func TestBootFixture_ADeclaredSeatOpensAccountsUnderItsDeclaredPolicy(t *testing.T) {
	base := newRegisterHarness(t)
	held, open := heldAndOpenSeats(t, base)

	for _, tc := range []struct {
		seat, agent, balance string
		active               bool
	}{
		{held, "boot-held-agent.example", "0", false},
		{open, "boot-open-agent.example", "5", true},
	} {
		h := base.restartedWith(t, func(d *exchangeServerDeps) {
			d.defaultTenantDomain = tc.seat
			d.defaultAgentCredits = harnessCredit(t, "5")
		})
		a := h.newAgent(t, tc.agent)
		resp, err := a.client.Register(h.ctx, connect.NewRequest(newRegisterRequest(nil)))
		if err != nil {
			t.Fatalf("Register at %s: %v", tc.seat, err)
		}
		if resp.Msg.GetActive() != tc.active {
			t.Fatalf("Register at %s: active = %v, want %v", tc.seat, resp.Msg.GetActive(), tc.active)
		}
		assertOnlyBalance(t, statusBalances(t, h.ctx, a.client), tc.balance)
	}
}

func TestBootFixture_AStoredSeatKeepsItsPolicyAndReportsTheDrift(t *testing.T) {
	base := newRegisterHarness(t)
	held, _ := heldAndOpenSeats(t, base)
	keys := bootKeyStore(t, "boot-seat-key")

	redeclared := planBootFixture(t, bootFixtureYAML(
		[]bootSeat{{id: "boot-held", domain: held, keyRef: "boot-seat-key", activate: true}}, ""))
	report, err := applyBoot(base.ctx, base.pool, base.queries, keys, redeclared)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(report.Drift) != 1 || report.Drift[0].Row != "seat boot-held" ||
		strings.Join(report.Drift[0].Fields, ",") != "activate_new_agents_by_default" {
		t.Fatalf("drift = %+v, want the held seat's activation policy", report.Drift)
	}

	h := base.restartedWith(t, func(d *exchangeServerDeps) { d.defaultTenantDomain = held })
	a := h.newAgent(t, "boot-kept-agent.example")
	resp, err := a.client.Register(h.ctx, connect.NewRequest(newRegisterRequest(nil)))
	if err != nil || resp.Msg.GetActive() {
		t.Fatalf("Register at the kept seat = %v, err %v; want an inactive account", resp, err)
	}
}

// driftNames reports whether row drifted in field.
func driftNames(report service.BootReport, row, field string) bool {
	for _, d := range report.Drift {
		if d.Row == row && slices.Contains(d.Fields, field) {
			return true
		}
	}
	return false
}

func TestBootFixture_APendingListingStaysPending(t *testing.T) {
	h := newPushHarness(t)
	pending := "boot-pending-" + uuid.NewString() + ".example"
	repotest.SeedSite(t, h.ctx, h.pool, repotest.Site{
		SeatTenantID: h.tenantID, Domain: pending, PublisherID: harnessResourceOwner, Pending: true,
	})
	plan := planBootFixture(t, bootFixtureYAML(
		[]bootSeat{harnessSeat(h)}, harnessResourceOwner, bootSite(pending, h.tenantID)))

	report, err := applyBoot(h.ctx, h.pool, h.queries, h.keystore, plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(report.Created) != 0 || !driftNames(report, "listing of "+pending+" on seat "+h.tenantID, "state") {
		t.Fatalf("report = %+v, want the pending listing kept and its state reported", report)
	}
	const caller = "boot-pending-contributor.example"
	uri, err := newSiteWithContributor(t, h, pending, caller).pushOne(t, h, caller, "/articles/pending")
	assertRegistrationRefusal(t, h, uri, err, service.RejectionReasonListingNotActive)
}

func TestBootFixture_ASiteOfAnotherPayeeRefusesTheWholeFixture(t *testing.T) {
	h := newPushHarness(t)
	owned := "boot-owned-" + uuid.NewString() + ".example"
	repotest.SeedSite(t, h.ctx, h.pool, repotest.Site{
		SeatTenantID: h.tenantID, Domain: owned, PublisherID: "boot-other-payee", NewPublisher: true,
	})
	fresh := "boot-fresh-" + uuid.NewString() + ".example"
	plan := planBootFixture(t, bootFixtureYAML([]bootSeat{harnessSeat(h)}, harnessResourceOwner,
		bootSite(fresh, h.tenantID), bootSite(owned, h.tenantID)))

	_, err := applyBoot(h.ctx, h.pool, h.queries, h.keystore, plan)
	assertFailedPrecondition(t, err, owned)
	if rows := bootAuditRows(t, h.ctx, h.queries, h.tenantID, bootListingAction); len(rows) != 0 {
		t.Fatalf("a refused fixture left %d listing audit row(s)", len(rows))
	}

	// The fresh site came first in the file and was rolled back with the rest.
	const caller = "boot-fresh-contributor.example"
	uri, err := newSiteWithContributor(t, h, fresh, caller).pushOne(t, h, caller, "/articles/fresh")
	assertRegistrationRefusal(t, h, uri, err, service.RejectionReasonUnregisteredPublisherDomain)
}

func TestBootFixture_ASeatWhoseIdentityIsStoredDifferentlyRefusesTheFixture(t *testing.T) {
	h := newPushHarness(t)
	takenDomain := harnessSeat(h)
	takenDomain.id = "boot-usurper"
	_, err := applyBoot(h.ctx, h.pool, h.queries, h.keystore, planBootFixture(t, bootFixtureYAML(
		[]bootSeat{takenDomain}, "")))
	assertFailedPrecondition(t, err, harnessExchangeDomain)

	moved := harnessSeat(h)
	moved.domain = "moved-" + uuid.NewString() + ".example"
	_, err = applyBoot(h.ctx, h.pool, h.queries, h.keystore, planBootFixture(t, bootFixtureYAML(
		[]bootSeat{moved}, "")))
	assertFailedPrecondition(t, err, moved.domain)
	for _, tenant := range []string{takenDomain.id, moved.id} {
		if rows := bootAuditRows(t, h.ctx, h.queries, tenant, bootSeatAction); len(rows) != 0 {
			t.Fatalf("a refused fixture left %d seat audit row(s) on %s", len(rows), tenant)
		}
	}
}

func TestBootFixture_ASeatNamingAnEd25519KeyNotHeldWritesNothing(t *testing.T) {
	base := newRegisterHarness(t)
	seat := "unkeyed-" + uuid.NewString() + ".example"
	plan := planBootFixture(t, bootFixtureYAML(
		[]bootSeat{{id: "boot-unkeyed", domain: seat, keyRef: "no-such-key", activate: true}}, ""))

	_, err := applyBoot(base.ctx, base.pool, base.queries, signing.NewInMemoryKeyStore(), plan)
	assertFailedPrecondition(t, err, "no-such-key")
	if rows := bootAuditRows(t, base.ctx, base.queries, "boot-unkeyed", bootSeatAction); len(rows) != 0 {
		t.Fatalf("a refused fixture left %d seat audit row(s)", len(rows))
	}

	// No seat was written: Register at that domain finds no seat to open under.
	h := base.restartedWith(t, func(d *exchangeServerDeps) { d.defaultTenantDomain = seat })
	a := h.newAgent(t, "boot-unkeyed-agent.example")
	_, err = a.client.Register(h.ctx, connect.NewRequest(newRegisterRequest(nil)))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("Register at a seat the refused fixture declared: code = %v, want FailedPrecondition (err=%v)", got, err)
	}
}
