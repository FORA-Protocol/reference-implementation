//go:build integration

package transport_test

// Fixture ARRANGEMENT for the Exchange integration suite: seeding agents and
// tenants, and minting the signed clients that act as them. Split out of
// integration_helper_test.go to keep both files inside the 800-line test cap.
//
// Agents are seeded through the production repository surface (repo.AgentRepo),
// and publisher sites through repotest, which drives the registration write
// ports, so the fixture and the code under test agree on their encoding by
// construction.
//
// Seats are not, and this is the file that says so. Every write that creates a
// tenant here calls the generated sqlc querier directly, because the repository
// has no tenant-creation port (the relay opt-in has a port of its own,
// repo.TenantRelayPolicyRepo, and goes through it): repo.TenantWriteRepo carries setters on a row that
// already exists and no way to make one. The Testing Doctrine's fallback tier
// is the repository interface; a raw querier is the tier below that, which it
// does not sanction at all. This is that tier, taken because the surface a test
// should reach does not exist. Adding the port and moving these arranges onto
// it is tracked as its own work — the call sites across this package predate
// this file.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"testing"

	connect "connectrpc.com/connect"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/reflect/protoreflect"

	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
)

// agentDirectoryURL mirrors the anchored WBA directory agentreg pins a key from
// (registry.RegisterFromDirectory resolves exactly this shape), so seeded agents
// carry the same key provenance production records rather than a NULL that would
// make provenance assertions vacuous.
func agentDirectoryURL(agentID string) string {
	return "https://" + agentID + "/.well-known/http-message-signatures-directory"
}

// seedAgent registers one agent row: its directory identity, its REAL public key
// (so a caller signing with the matching private key satisfies the identity↔key
// binding the Exchange enforces), its requester type, and the anchored directory
// the key is pinned from.
//
// One function rather than the block repeated at each fixture: the upsert
// overwrites discovery_url with whatever the caller passes, so a site that
// omitted it does not merely skip the column — it seeds NULL over a value an
// earlier seed established, turning key-provenance assertions vacuous.
func seedAgent(t *testing.T, ctx context.Context, q *sqlc.Queries, agentID string, pub ed25519.PublicKey) {
	t.Helper()
	seedAgentAs(t, ctx, q, agentID, pub, string(sqlc.ForaRequesterTypeAGENT))
}

// seedAgentAs is seedAgent for a caller that must be registered as something
// other than an AGENT (a relaying BROKER). It arranges through repo.AgentRepo,
// the surface production writes agents through, so fixture and code under test
// agree on the encoding — including that an empty discovery URL becomes NULL.
func seedAgentAs(
	t *testing.T, ctx context.Context, q *sqlc.Queries,
	agentID string, pub ed25519.PublicKey, kind string,
) {
	t.Helper()
	if _, err := repo.NewAgentRepo(q).Upsert(ctx, repo.Agent{
		ID:            agentID,
		PublicKey:     pub,
		DiscoveryURL:  agentDirectoryURL(agentID),
		RequesterType: kind,
	}); err != nil {
		t.Fatalf("seed agent %q: %v", agentID, err)
	}
}

// addCallerWithKey is addCaller that also returns the freshly-generated Ed25519
// private key, so a test can sign the new caller's OWN body offer-acceptance (not
// just its transport RFC 9421 envelope). Used by cross-caller replay tests that
// present a second agent's legitimately self-signed request.
func (h *testHarness) addCallerWithKey(
	t *testing.T, agentID, requesterType string,
) (foraconnect.ExchangeServiceClient, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("addCaller ed25519: %v", err)
	}
	// The httpsig resolver keys by RFC 9421 keyid (an RFC 7638 thumbprint after
	// the WBA split); the agents row keys by the directory identity (agentID).
	h.resolver.Put(rwtestutil.MustThumbprintPriv(priv), pub)
	seedAgentAs(t, h.ctx, h.queries, agentID, pub, requesterType)
	client := &http.Client{Transport: newSigningTransport(h.baseTransport, agentID, priv)}
	return foraconnect.NewExchangeServiceClient(client, h.server.URL, connect.WithGRPC()), priv
}

// addTenant inserts a SECOND seat row + signing key, and registers a caller for
// it via addCaller. Returns the new tenant_id + the new caller's
// ExchangeService client.
//
// The push path sells as exactly one seat, so nothing here can push. This exists
// for the tests that need a second tenant_id to prove a tenant-scoped read does
// not cross it — obligations, evidence, reporting policy — and those write their
// rows through the repositories, not through a push.
//
// The seat's domain is derived from the slug, never from harnessExchangeDomain:
// that value is the default seat's and tenants.domain is UNIQUE.
func (h *testHarness) addTenant(t *testing.T, tenantSlug, agentID string) (tenantID string, client foraconnect.ExchangeServiceClient) {
	t.Helper()
	tenantID = "t_" + tenantSlug
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("addTenant ed25519: %v", err)
	}
	keyRef := repotest.SeatEd25519KeyRef(tenantID)
	h.keystore.PutEd25519(keyRef, pub, priv)
	if _, err := h.queries.InsertTenant(h.ctx, sqlc.InsertTenantParams{
		TenantID:        tenantID,
		Domain:          "seat-" + tenantSlug + ".example",
		Ed25519KeyRef:   keyRef,
		ReportingPolicy: []byte(`{}`),
		RsaKeyRef:       pgtype.Text{},
	}); err != nil {
		t.Fatalf("insert seat %q: %v", tenantID, err)
	}
	client = h.addCaller(t, agentID, "AGENT")
	return tenantID, client
}

// enableBrokerRelay flips tenants.allow_broker_relay = true for the given
// tenant through the production relay-policy repository port.
func (h *testHarness) enableBrokerRelay(t *testing.T, tenantID string) {
	t.Helper()
	n, err := repo.NewTenantRelayPolicyRepo(h.queries).SetAllowBrokerRelay(h.ctx, tenantID, true)
	if err != nil {
		t.Fatalf("set allow_broker_relay: %v", err)
	}
	if n != 1 {
		t.Fatalf("set allow_broker_relay: %d tenant rows updated for %s, want 1", n, tenantID)
	}
}

// wireBound reads a repeated field's max_items rule off the pinned protocol
// descriptor, so a bound this package's cap tests drive is the wire's own and
// cannot drift from it.
//
// It is the one wrapper over that read for the whole package. There were two,
// added the same day the reader itself was unified one level down, and they had
// already diverged: only one refused a bound a list cannot be split at. The
// check is here so both callers get it.
//
// ingest.RepeatedMaxItems is the reader; the CLI chunks a feed with the same
// call. A cap a test drives that has gone from the wire is a finding, not a
// zero, and that judgement is made in one place for the CLI and the tests
// alike.
func wireBound(t *testing.T, message, field protoreflect.Name) int {
	t.Helper()
	bound, err := ingest.RepeatedMaxItems(message, field)
	if err != nil {
		t.Fatalf("read %s.%s off the pinned descriptor: %v", message, field, err)
	}
	if bound < 2 {
		t.Fatalf("%s.%s bound = %d; a list cannot be split at it", message, field, bound)
	}
	return bound
}

// insertTenantRow writes one seat row under a generated id and key ref, and
// returns both. `domain` is the hostname the seat answers on, never a
// publisher's site.
//
// It is the writer for the generated-id path, not for every seat this file
// seeds: addTenant above writes its own InsertTenantParams literal for the
// scenarios that need a caller-chosen id and key ref. Two literals, and a change
// to the tenants table has to reach both.
//
// It calls the InsertTenant query directly, for the reason the file header
// gives: there is no repository port that creates a tenant.
func insertTenantRow(t *testing.T, ctx context.Context, q *sqlc.Queries, domain string) (tenantID, keyRef string) {
	t.Helper()
	tenantID = "t_" + uuid.NewString()
	keyRef = repotest.SeatEd25519KeyRef(tenantID)
	if _, err := q.InsertTenant(ctx, sqlc.InsertTenantParams{
		TenantID:        tenantID,
		Domain:          domain,
		Ed25519KeyRef:   keyRef,
		ReportingPolicy: []byte(`{}`),
		RsaKeyRef:       pgtype.Text{},
	}); err != nil {
		t.Fatalf("insert seat for %s: %v", domain, err)
	}
	return tenantID, keyRef
}

// seedSiteForDomain registers one publisher site and lists it ACTIVE on the
// harness's seat, which is what a push for that domain needs. It returns the
// SEAT's tenant id: every entry a push accepts is stored under the seat, so that
// is the tenant a caller should name on the wire and the tenant an assertion
// should expect on the row.
//
// Every site registers under harnessResourceOwner, because that is the payee
// every publisher origin in this package attests. The payee row itself is
// created once by the harness bring-up, so this call only attaches another site
// to it.
func seedSiteForDomain(t *testing.T, h *pushHarness, domain string) string {
	t.Helper()
	repotest.SeedSite(t, h.ctx, h.pool, repotest.Site{
		SeatTenantID: h.tenantID,
		Domain:       domain,
		PublisherID:  harnessResourceOwner,
	})
	return h.tenantID
}

// seedSite is seedSiteForDomain under a fresh random domain, for a test that
// needs a second publisher site but does not care which domain it answers to.
// Sibling tests never collide on the publisher_domains primary key.
func seedSite(t *testing.T, h *pushHarness) (tenantID, domain string) {
	t.Helper()
	domain = "pub-" + uuid.NewString() + ".example"
	return seedSiteForDomain(t, h, domain), domain
}

// seedForeignSeat inserts a SECOND seat row, one this deployment does not sell
// as. Nothing can push under it — the push path resolves the seat from
// EXCHANGE_DOMAIN — which is exactly what the tests that name it on the wire
// are proving.
func seedForeignSeat(t *testing.T, h *pushHarness) string {
	t.Helper()
	tenantID, _ := insertTenantRow(t, h.ctx, h.queries, "seat-"+uuid.NewString()+".example")
	return tenantID
}
