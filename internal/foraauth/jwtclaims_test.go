package foraauth_test

import (
	"context"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraauth"
)

func TestJWTClaimsRoundTrip(t *testing.T) {
	ctx := foraauth.WithJWTClaims(context.Background(), foraauth.JWTClaims{
		Sub: "user_bob_123",
		Org: "acme",
	})
	got := foraauth.JWTClaimsFromContext(ctx)
	if got.Sub != "user_bob_123" || got.Org != "acme" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestJWTClaimsZeroValueFromEmptyCtx(t *testing.T) {
	got := foraauth.JWTClaimsFromContext(context.Background())
	if got.Sub != "" || got.Org != "" {
		t.Fatalf("want zero, got %+v", got)
	}
}
