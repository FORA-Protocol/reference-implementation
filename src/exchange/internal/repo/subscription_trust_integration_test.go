//go:build integration

package repo_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

func newRootKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// TestSubscriptionTrustRepo_ScopeIsTenantScoped pins the read the delegation
// verifier depends on: the keys returned are exactly those of the (tenant,
// scope) asked for. The same scope under another seat, with its own keys, is
// never returned, and a scope with no record answers nil.
//
// Persistence round-trip: writes and the read go through the repository port
// and stop there. No request path creates or reads a subscription record yet;
// the admin surface for these rows is owned by the subscription-administration
// work that follows this one.
func TestSubscriptionTrustRepo_ScopeIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	r := repo.NewSubscriptionTrustRepo(sqlc.New(pool))
	seedTenantRow(t, ctx, pool, "t-sub-a", "exchange.example")
	seedTenantRow(t, ctx, pool, "t-sub-b", "other-exchange.example")
	const scope = "subscription:publisher.example:premium"
	keyA, keyB := newRootKey(t), newRootKey(t)
	if _, err := r.Create(ctx, repo.SubscriptionRecord{TenantID: "t-sub-a", Scope: scope, IssuerIdentity: "issuer.example", IssuerKeys: []ed25519.PublicKey{keyA}}); err != nil {
		t.Fatalf("Create A: %v", err)
	}
	if _, err := r.Create(ctx, repo.SubscriptionRecord{TenantID: "t-sub-b", Scope: scope, IssuerIdentity: "issuer.example", IssuerKeys: []ed25519.PublicKey{keyB}}); err != nil {
		t.Fatalf("Create B: %v", err)
	}

	got, err := r.TrustedIssuersFor(ctx, "t-sub-a", scope)
	if err != nil || got == nil {
		t.Fatalf("TrustedIssuersFor A = %v, %v", got, err)
	}
	if len(got.Issuers) != 1 || !got.Issuers[0].Key.Equal(keyA) || got.Issuers[0].Identity != "issuer.example" {
		t.Fatalf("seat A returned %+v, want only keyA under issuer.example", got.Issuers)
	}
	if none, err := r.TrustedIssuersFor(ctx, "t-sub-a", "subscription:publisher.example:other"); err != nil || none != nil {
		t.Fatalf("unknown scope = %v, %v; want nil, nil", none, err)
	}
}

// TestSubscriptionTrustRepo_PutReplacesTheSeatsRecordOnly pins the write the
// e2e control plane and the verifier's withdrawal tests rely on: Put replaces
// the issuer and the keys of the seat's record for the scope, a key it drops is
// gone from the read (explicit withdrawal), and the same scope under another
// seat is untouched. Observed through TrustedIssuersFor.
func TestSubscriptionTrustRepo_PutReplacesTheSeatsRecordOnly(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	r := repo.NewSubscriptionTrustRepo(sqlc.New(pool))
	seedTenantRow(t, ctx, pool, "t-sub-a", "exchange.example")
	seedTenantRow(t, ctx, pool, "t-sub-b", "other-exchange.example")
	const scope = "subscription:publisher.example:premium"
	oldKey, newKey, keyB := newRootKey(t), newRootKey(t), newRootKey(t)
	rec, err := r.Create(ctx, repo.SubscriptionRecord{TenantID: "t-sub-a", Scope: scope, IssuerIdentity: "issuer.example", IssuerKeys: []ed25519.PublicKey{oldKey}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := r.Create(ctx, repo.SubscriptionRecord{TenantID: "t-sub-b", Scope: scope, IssuerIdentity: "issuer.example", IssuerKeys: []ed25519.PublicKey{keyB}}); err != nil {
		t.Fatalf("Create B: %v", err)
	}

	put, err := r.Put(ctx, repo.SubscriptionRecord{TenantID: "t-sub-a", Scope: scope, IssuerIdentity: "rotated.example", IssuerKeys: []ed25519.PublicKey{newKey}})
	if err != nil || put.ID != rec.ID || put.IssuerIdentity != "rotated.example" {
		t.Fatalf("Put over the existing record = %+v, %v; want the same id with the new issuer", put, err)
	}
	got, _ := r.TrustedIssuersFor(ctx, "t-sub-a", scope)
	if len(got.Issuers) != 1 || !got.Issuers[0].Key.Equal(newKey) || got.Issuers[0].Identity != "rotated.example" {
		t.Fatalf("after Put: %+v, want only newKey under rotated.example", got.Issuers)
	}
	if other, _ := r.TrustedIssuersFor(ctx, "t-sub-b", scope); len(other.Issuers) != 1 || !other.Issuers[0].Key.Equal(keyB) {
		t.Fatalf("Put under seat A touched seat B: %+v", other.Issuers)
	}
}

// TestSubscriptionTrustRepo_EndIsTenantKeyed pins that End leaves the key list
// in place, and that End with the right record id but the wrong tenant updates
// nothing: a record id alone names no record.
func TestSubscriptionTrustRepo_EndIsTenantKeyed(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	r := repo.NewSubscriptionTrustRepo(sqlc.New(pool))
	seedTenantRow(t, ctx, pool, "t-sub-a", "exchange.example")
	seedTenantRow(t, ctx, pool, "t-sub-b", "other-exchange.example")
	const scope = "subscription:publisher.example:premium"
	newKey := newRootKey(t)
	rec, err := r.Create(ctx, repo.SubscriptionRecord{TenantID: "t-sub-a", Scope: scope, IssuerIdentity: "issuer.example", IssuerKeys: []ed25519.PublicKey{newKey}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := r.End(ctx, "t-sub-b", rec.ID); !errors.Is(err, repo.ErrSubscriptionRecordNotFound) {
		t.Fatalf("End with the wrong tenant = %v, want ErrSubscriptionRecordNotFound", err)
	}
	if got, _ := r.TrustedIssuersFor(ctx, "t-sub-a", scope); got.Ended {
		t.Fatal("End with the wrong tenant ended seat A's record")
	}
	if err := r.End(ctx, "t-sub-a", rec.ID); err != nil {
		t.Fatalf("End: %v", err)
	}
	got, _ := r.TrustedIssuersFor(ctx, "t-sub-a", scope)
	if !got.Ended || len(got.Issuers) != 1 || !got.Issuers[0].Key.Equal(newKey) {
		t.Fatalf("after End: %+v, want ended with keys kept", got)
	}
	if err := r.End(ctx, "t-sub-a", "sub_missing"); !errors.Is(err, repo.ErrSubscriptionRecordNotFound) {
		t.Fatalf("End unknown = %v", err)
	}
}

// TestSubscriptionTrustRepo_RefusalsAreSentinels pins the errors a caller maps:
// Create refuses a second record for the (tenant, scope) and an unknown tenant
// with the sentinels the e2e route and the admin plane read, and a bad scope or
// key is one ErrSubscriptionRecordInvalid whichever write carries it.
func TestSubscriptionTrustRepo_RefusalsAreSentinels(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	r := repo.NewSubscriptionTrustRepo(sqlc.New(pool))
	seedTenantRow(t, ctx, pool, "t-sub-a", "exchange.example")
	const scope = "subscription:publisher.example:premium"
	newKey := newRootKey(t)
	if _, err := r.Create(ctx, repo.SubscriptionRecord{TenantID: "t-sub-a", Scope: scope, IssuerIdentity: "issuer.example", IssuerKeys: []ed25519.PublicKey{newKey}}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := r.Create(ctx, repo.SubscriptionRecord{TenantID: "t-sub-a", Scope: scope, IssuerIdentity: "x", IssuerKeys: []ed25519.PublicKey{newKey}}); !errors.Is(err, repo.ErrSubscriptionRecordExists) {
		t.Fatalf("second Create for the (tenant, scope) = %v, want ErrSubscriptionRecordExists", err)
	}
	if _, err := r.Create(ctx, repo.SubscriptionRecord{TenantID: "t-missing", Scope: scope, IssuerIdentity: "x", IssuerKeys: []ed25519.PublicKey{newKey}}); !errors.Is(err, repo.ErrTenantNotFound) {
		t.Fatalf("Create under an unknown tenant = %v, want ErrTenantNotFound", err)
	}
	for name, bad := range map[string]repo.SubscriptionRecord{
		"no key":         {TenantID: "t-sub-a", Scope: scope, IssuerIdentity: "x"},
		"short key":      {TenantID: "t-sub-a", Scope: scope, IssuerIdentity: "x", IssuerKeys: []ed25519.PublicKey{[]byte("short")}},
		"wildcard scope": {TenantID: "t-sub-a", Scope: "subscription:p:*", IssuerIdentity: "x", IssuerKeys: []ed25519.PublicKey{newKey}},
	} {
		if _, err := r.Put(ctx, bad); !errors.Is(err, repo.ErrSubscriptionRecordInvalid) {
			t.Fatalf("Put %s = %v, want ErrSubscriptionRecordInvalid", name, err)
		}
	}
}
