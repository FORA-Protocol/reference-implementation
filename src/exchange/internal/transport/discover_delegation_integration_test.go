//go:build integration

package transport_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// delegationHarness is the arrangement every subscription-gated discovery
// scenario shares: a publisher with gated terms and a direct agent holding a
// verified grant for the gated term's scope (gatedAccess).
type delegationHarness struct {
	*gatedAccess
	catalog  foraconnect.CatalogServiceClient
	callerID string
}

const (
	gatedPath    = "/sub/gated"
	mixedPath    = "/sub/mixed"
	regionedPath = "/sub/regioned"
)

func newDelegationHarness(t *testing.T) *delegationHarness {
	t.Helper()
	h := newPushHarness(t)
	scope := "subscription:" + h.publisherDom + ":premium"
	d := &delegationHarness{callerID: "caller.example"}
	d.catalog = setupTermContributor(t, h, d.callerID)
	pushTerms(t, h, d.catalog, d.callerID, gatedPath, scopedTerm("gated", scope))
	pushTerms(t, h, d.catalog, d.callerID, mixedPath, scopedTerm("gated", scope), scopedTerm("public"))
	pushTerms(t, h, d.catalog, d.callerID, regionedPath, scopedTerm("regioned", scope, "region:EU"))
	d.gatedAccess = newGatedAccess(t, h, scope)
	return d
}

func (d *delegationHarness) uri(path string) string { return "https://" + d.h.publisherDom + path }

// discoverWith queries uri through client as a requester carrying delegation
// (nil for none) and the stated scopes, and returns the single offer group.
func (d *delegationHarness) discoverWith(
	t *testing.T, client foraconnect.ExchangeServiceClient, uri string, delegation *forav1.Delegation, stated ...string,
) *forav1.OfferGroup {
	t.Helper()
	requester := newRequester(d.agentID, "agent.example", stated...)
	requester.Delegation = delegation
	resp, err := client.DiscoverResources(d.h.ctx, connect.NewRequest(newResourceQuery(requester, []string{uri})))
	if err != nil {
		t.Fatalf("discover %s: %v", uri, err)
	}
	groups := resp.Msg.GetOfferGroups()
	if len(groups) != 1 {
		t.Fatalf("offer_groups = %d, want 1", len(groups))
	}
	if groups[0].AbsenceReason != nil {
		t.Fatalf("absence_reason = %v, want unset: a term the delegation does not reach is never explained", groups[0].GetAbsenceReason())
	}
	return groups[0]
}

func (d *delegationHarness) labelsDirect(t *testing.T, path string, token []byte, stated ...string) []string {
	t.Helper()
	var delegation *forav1.Delegation
	if token != nil {
		delegation = &forav1.Delegation{Token: token}
	}
	group := d.discoverWith(t, d.direct, d.uri(path), delegation, stated...)
	return labels(offerTerms(t, d.uri(path), group.GetOffers()))
}

// assertRefusalJournaled finds the audit line a refused chain leaves: the
// discover outcome, the failure name and the record it was verified against.
func (d *delegationHarness) assertRefusalJournaled(t *testing.T, failure, recordID string) {
	t.Helper()
	findLogRecord(t, d.h.logs.String(), "exchange.discover_resources", func(rec map[string]any) bool {
		msg, _ := rec["err"].(string)
		return rec["outcome"] == "DELEGATION_REFUSED" && strings.Contains(msg, failure) && strings.Contains(msg, recordID)
	})
}

// TestDiscover_GatedTermNeedsVerifiedDelegation pins the gate on the direct
// discovery path through the public DiscoverResources RPC: a gated term is
// presented only to a requester whose delegation chain verifies against the
// record that owns the term's subscription scope and is bound to the key that
// signed the request. Stating the scope in Requester.scopes proves nothing, and
// changes nothing when a chain is present. The group never names the delegation
// as the reason a term is absent.
func TestDiscover_GatedTermNeedsVerifiedDelegation(t *testing.T) {
	d := newDelegationHarness(t)
	valid := testutil.MintDelegation(t, d.chain())

	assertStrs(t, "verified chain", d.labelsDirect(t, gatedPath, valid), []string{"gated"})
	assertStrs(t, "verified chain, scope also stated", d.labelsDirect(t, gatedPath, valid, d.scope), []string{"gated"})
	assertStrs(t, "verified chain, unrelated scope stated", d.labelsDirect(t, gatedPath, valid, "dist:US"), []string{"gated"})
	assertStrs(t, "stated scope, no chain", d.labelsDirect(t, gatedPath, nil, d.scope), nil)
	assertStrs(t, "stated scope, no chain: the public term is still offered", d.labelsDirect(t, mixedPath, nil, d.scope), []string{"public"})
	assertStrs(t, "verified chain on the mixed resource", d.labelsDirect(t, mixedPath, valid), []string{"gated", "public"})
	// The chain covers the subscription scope only; a term that also requires
	// region:EU is not covered, whatever Requester.scopes says.
	assertStrs(t, "chain does not cover the term's second scope", d.labelsDirect(t, regionedPath, valid, "region:EU"), nil)
	wide := d.chain()
	wide.Scope = d.scope + " region:*"
	assertStrs(t, "chain covering both scopes", d.labelsDirect(t, regionedPath, testutil.MintDelegation(t, wide)), []string{"regioned"})
}

// TestDiscover_DelegationFailuresHideTheTerm drives one chain per failure class
// of the verifier through DiscoverResources: each hides the gated term, the
// public sibling stays offered, and the journal carries the failure name and
// the record. Nothing in the response says why.
func TestDiscover_DelegationFailuresHideTheTerm(t *testing.T) {
	d := newDelegationHarness(t)
	otherPub, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	// One bit of the signature inverted and re-encoded: still a well-formed
	// token, so only the signature check can refuse it.
	tampered := flipDelegationSignature(t, testutil.MintDelegation(t, d.chain()))
	past := d.chain()
	past.Exp = time.Now().Add(-2 * time.Hour)
	notYet := d.chain()
	notYet.Tokens = []testutil.DelegationToken{{Claims: map[string]any{"nbf": float64(time.Now().Add(time.Hour).Unix())}}}
	holder := d.chain()
	holder.Holder = otherPub
	untrusted := d.chain()
	untrusted.Keys = []ed25519.PrivateKey{otherPriv}
	widening := d.chain()
	widening.Keys = []ed25519.PrivateKey{d.rootPriv, otherPriv}
	widening.Tokens = []testutil.DelegationToken{{}, {Claims: map[string]any{"scope": "subscription:" + d.h.publisherDom + ":*"}}}
	expiredParent := d.chain()
	expiredParent.Keys = []ed25519.PrivateKey{d.rootPriv, otherPriv}
	expiredParent.Tokens = []testutil.DelegationToken{{Claims: map[string]any{"exp": float64(time.Now().Add(-2 * time.Hour).Unix())}}, {}}
	capped := d.chain()
	capped.Tokens = []testutil.DelegationToken{{Claims: map[string]any{"fora_max_accesses": 10}}}
	unknown := d.chain()
	unknown.Tokens = []testutil.DelegationToken{{Claims: map[string]any{"fora_tier": "gold"}}}
	foreignIssuer := d.chain()
	foreignIssuer.RootIssuer = "someone-else.example"

	cases := []struct {
		name, failure string
		token         []byte
	}{
		{"holder mismatch", "holder_mismatch", testutil.MintDelegation(t, holder)},
		{"bad signature", "bad_signature", tampered},
		// The root names the record's issuer identity but is signed by a key the
		// record does not list, so the verifier reports the signature it could
		// not verify under any trusted key.
		{"untrusted root key", "bad_signature", testutil.MintDelegation(t, untrusted)},
		{"unknown root issuer identity", "untrusted_issuer", testutil.MintDelegation(t, foreignIssuer)},
		{"expired", "expired", testutil.MintDelegation(t, past)},
		{"not yet valid", "not_yet_valid", testutil.MintDelegation(t, notYet)},
		{"child widens the parent's scope", "scope_widened", testutil.MintDelegation(t, widening)},
		{"expired ancestor", "expired", testutil.MintDelegation(t, expiredParent)},
		{"unsupported restriction", "unsupported_restriction", testutil.MintDelegation(t, capped)},
		{"unknown claim", "unknown_claim", testutil.MintDelegation(t, unknown)},
		{"no token", "malformed", []byte{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertStrs(t, tc.name, d.labelsDirect(t, mixedPath, tc.token, d.scope), []string{"public"})
			d.assertRefusalJournaled(t, tc.failure, d.recordID)
		})
	}
}

// TestDiscover_PlaintextMirrorsChangeNothing pins the mirrors_disagree_no_effect
// rule of the shared corpus at the RPC: Delegation.scopes, expires_at,
// principal_domain and the caps are never read for trust. Mirrors wider than the
// chain do not widen access (the regioned term stays hidden) and do not stop a
// valid chain from presenting the term it covers. A chain whose exp is 30s past
// still verifies under the verifier's leeway.
func TestDiscover_PlaintextMirrorsChangeNothing(t *testing.T) {
	d := newDelegationHarness(t)
	mirrors := &forav1.Delegation{
		Token:           testutil.MintDelegation(t, d.chain()),
		Scopes:          []string{"*"},
		ExpiresAt:       timestamppb.New(time.Now().Add(1000 * time.Hour)),
		PrincipalDomain: "attacker.example",
	}
	group := d.discoverWith(t, d.direct, d.uri(gatedPath), mirrors, "*")
	assertStrs(t, "wide mirrors, covered term", labels(offerTerms(t, d.uri(gatedPath), group.GetOffers())), []string{"gated"})
	group = d.discoverWith(t, d.direct, d.uri(regionedPath), mirrors, "*")
	assertStrs(t, "wide mirrors, uncovered term", labels(offerTerms(t, d.uri(regionedPath), group.GetOffers())), nil)

	inLeeway := d.chain()
	inLeeway.Exp = time.Now().Add(-30 * time.Second)
	assertStrs(t, "exp 30s past, inside leeway", d.labelsDirect(t, gatedPath, testutil.MintDelegation(t, inLeeway)), []string{"gated"})
}

// TestDiscover_TrustIsPerRecord pins that a root key grants exactly the record it
// sits on: a second subscription of the same seat under another key, and the
// same scope under another seat trusting this key, both refuse a chain rooted in
// this record's key. Withdrawing the key from the record refuses the next query.
func TestDiscover_TrustIsPerRecord(t *testing.T) {
	d := newDelegationHarness(t)
	h := d.h
	goldPub, _, _ := ed25519.GenerateKey(rand.Reader)
	goldScope := "subscription:" + h.publisherDom + ":gold"
	pushTerms(t, h, d.catalog, d.callerID, "/sub/gold", scopedTerm("gold", goldScope))
	gold, err := d.trust.Create(h.ctx, repo.SubscriptionRecord{TenantID: h.tenantID, Scope: goldScope, IssuerIdentity: issuerID, IssuerKeys: []ed25519.PublicKey{goldPub}})
	if err != nil {
		t.Fatal(err)
	}
	// Another seat trusts this record's root key for the very same scope string.
	otherSeat := repotest.SeedSeat(t, h.ctx, h.pool, repotest.Seat{Domain: "other-exchange.example"})
	rootPub, _ := d.rootPriv.Public().(ed25519.PublicKey)
	if _, err := d.trust.Create(h.ctx, repo.SubscriptionRecord{TenantID: otherSeat, Scope: goldScope, IssuerIdentity: issuerID, IssuerKeys: []ed25519.PublicKey{rootPub}}); err != nil {
		t.Fatal(err)
	}
	forGold := d.chain()
	forGold.Scope = goldScope
	assertStrs(t, "root of record A, term of record B", d.labelsDirect(t, "/sub/gold", testutil.MintDelegation(t, forGold)), nil)
	d.assertRefusalJournaled(t, "bad_signature", gold.ID)
	assertStrs(t, "control: root of record A, term of record A", d.labelsDirect(t, gatedPath, testutil.MintDelegation(t, d.chain())), []string{"gated"})

	// Explicit withdrawal: the key leaves the list, the next query is refused.
	if _, err := d.trust.Put(h.ctx, repo.SubscriptionRecord{
		TenantID: h.tenantID, Scope: d.scope, IssuerIdentity: issuerID, IssuerKeys: []ed25519.PublicKey{goldPub},
	}); err != nil {
		t.Fatal(err)
	}
	assertStrs(t, "after key withdrawal", d.labelsDirect(t, gatedPath, testutil.MintDelegation(t, d.chain())), nil)
}

// TestDiscover_BrokerOriginatedQueryHidesGatedTerms pins the interim rule for a
// query the configured Broker signs: the Exchange cannot bind the chain to the
// agent's key on that path, so it skips verification and hides every gated term,
// a valid delegation included. Public terms are unaffected.
func TestDiscover_BrokerOriginatedQueryHidesGatedTerms(t *testing.T) {
	d := newDelegationHarness(t)
	valid := &forav1.Delegation{Token: testutil.MintDelegation(t, d.chain())}
	group := d.discoverWith(t, d.h.exchange, d.uri(mixedPath), valid, d.scope)
	assertStrs(t, "Broker-signed query", labels(offerTerms(t, d.uri(mixedPath), group.GetOffers())), []string{"public"})
	if strings.Contains(d.h.logs.String(), "DELEGATION_REFUSED") {
		t.Fatal("a Broker-originated query must not run the verifier")
	}
}

// TestPushResources_RefusesMalformedSubscriptionScopes pins the ingest-time
// control of the concrete-scope rule: a term with two subscription scopes, or a
// subscription scope carrying a wildcard, is refused at push with the rule and
// field named, so the publisher learns about it before any requester could.
// Term selection hides such a term as a backstop should one reach storage; that
// path has no public arrange step and is pinned by the validator's unit tests.
func TestPushResources_RefusesMalformedSubscriptionScopes(t *testing.T) {
	h := newPushHarness(t)
	client := setupTermContributor(t, h, "caller.example")
	for _, tc := range []struct {
		name, path string
		scopes     []string
	}{
		{"two subscription scopes", "/sub/two", []string{"subscription:a:x", "subscription:a:y"}},
		{"wildcard subscription scope", "/sub/wild", []string{"subscription:a:*"}},
		{"bare prefix", "/sub/bare", []string{"subscription:"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, "caller.example", []*forav1.ResourceEntry{{
				Domain: h.publisherDom, Path: tc.path, Terms: []*forav1.LicenseTerm{scopedTerm("gated", tc.scopes...)},
			}})))
			if err == nil {
				t.Fatal("want rejection")
			}
			assertConnectCode(t, err, connect.CodeInvalidArgument)
			assertCatalogRejectLogged(t, h, tc.path, service.RejectionReasonInvalidTerms)
			uri := "https://" + h.publisherDom + tc.path
			line := findLogRecord(t, h.logs.String(), "license term refused at ingest",
				func(rec map[string]any) bool { return rec["uri"] == uri })
			if line["rule"] != "subscription_scope_concrete" || line["path"] != "terms[0].scopes" {
				t.Fatalf("refusal line = %v", line)
			}
		})
	}
}

// flipDelegationSignature inverts one bit of a compact JWS's signature and
// re-encodes it as unpadded base64url, so the token still decodes and only the
// signature check can refuse it. Editing the last character is not safe: for a
// 64-byte signature it holds two signature bits and four padding bits that must
// be zero, and the verifier decodes strictly, so a token with a padding bit set
// is refused as malformed before its signature is checked.
func flipDelegationSignature(t *testing.T, token []byte) []byte {
	t.Helper()
	dot := bytes.LastIndexByte(token, '.')
	if dot < 0 {
		t.Fatalf("flipDelegationSignature: %q is not a compact JWS", token)
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(string(token[dot+1:]))
	if err != nil || len(sig) != ed25519.SignatureSize {
		t.Fatalf("flipDelegationSignature: the last segment is not a 64-byte signature (err %v)", err)
	}
	sig[0] ^= 0x01
	return []byte(string(token[:dot+1]) + base64.RawURLEncoding.EncodeToString(sig))
}
