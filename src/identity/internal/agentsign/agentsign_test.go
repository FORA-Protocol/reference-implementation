package agentsign_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/agentsign"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/keystore"
)

// nonEd25519Custody stands in for a custody backend whose signer is not an
// ed25519 private key. The shipped Vault backend cannot produce this — it stores
// the key itself — so the branch is only reachable through a stub. It is worth a
// test anyway: this is exactly what an HSM or Transit backend WILL return, and the
// failure has to be a legible error rather than a panic at the signing library.
type nonEd25519Custody struct{}

func (nonEd25519Custody) Active(_ context.Context, subdomain string) (keystore.Key, error) {
	return keystore.Key{
		Ref:    keystore.Ref{Subdomain: subdomain, Thumbprint: "aXJyZWxldmFudC1idXQtd2VsbC1mb3JtZWQtdmFsdWU"},
		Window: keystore.Window{NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)},
	}, nil
}

func (nonEd25519Custody) Signer(_ context.Context, _ keystore.Ref) (crypto.Signer, error) {
	// Any signer that is not an ed25519 private key exercises the same branch.
	return rsa.GenerateKey(rand.Reader, 2048)
}

// TestKeyFor_RejectsAKeyItCannotRead pins the honest failure when custody will not
// surrender private key bytes: the request fails with ErrNotSignable rather than
// panicking inside the RFC 9421 signer or, worse, going out unsigned.
func TestKeyFor_RejectsAKeyItCannotRead(t *testing.T) {
	t.Parallel()
	resolver, err := agentsign.New(agentsign.Config{Keys: nonEd25519Custody{}, BaseDomain: "foramcp.org"})
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}

	_, err = resolver.KeyFor(t.Context(), "alice.foramcp.org")

	if !errors.Is(err, agentsign.ErrNotSignable) {
		t.Fatalf("err = %v, want ErrNotSignable", err)
	}
}

// TestSource_RefusesAContextWithoutAnIdentity pins the fail-closed rule at the
// seam itself, independently of any transport: no authenticated agent means no
// signature, never a default one.
func TestSource_RefusesAContextWithoutAnIdentity(t *testing.T) {
	t.Parallel()
	resolver, err := agentsign.New(agentsign.Config{Keys: nonEd25519Custody{}, BaseDomain: "foramcp.org"})
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}

	_, err = resolver.Source()(t.Context())

	if !errors.Is(err, agentsign.ErrNoIdentity) {
		t.Fatalf("err = %v, want ErrNoIdentity", err)
	}
}

// TestNew_RequiresCustody pins that a misconfigured composition root fails at
// startup rather than nil-panicking on the first signed request.
func TestNew_RequiresCustody(t *testing.T) {
	t.Parallel()
	if _, err := agentsign.New(agentsign.Config{}); err == nil {
		t.Fatal("a Resolver was built with no custody backend")
	}
}

// TestNew_RefusesAZoneWhoseAgentOriginsAreNotHTTPSOrigins pins the boot
// refusal: every agent signs as https://<slug>.<zone>, so a zone that cannot
// form an https origin would put a Signature-Agent every verifier refuses on
// every request the service signs.
func TestNew_RefusesAZoneWhoseAgentOriginsAreNotHTTPSOrigins(t *testing.T) {
	t.Parallel()
	for _, zone := range []string{"", "FORAMCP.ORG", "foramcp.org/agents", "foramcp.org:0"} {
		if _, err := agentsign.New(agentsign.Config{Keys: nonEd25519Custody{}, BaseDomain: zone}); err == nil {
			t.Errorf("zone %q: a Resolver was built", zone)
		}
	}
}

// TestDirectoryOrigin_IsTheAgentsHTTPSOrigin pins the Signature-Agent member an
// agent signs with: https://<subdomain>, whatever scheme the deployment serves
// other documents over.
func TestDirectoryOrigin_IsTheAgentsHTTPSOrigin(t *testing.T) {
	t.Parallel()
	resolver, err := agentsign.New(agentsign.Config{Keys: nonEd25519Custody{}, BaseDomain: "foramcp.org"})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolver.DirectoryOrigin("alice.foramcp.org"); got != "https://alice.foramcp.org" {
		t.Fatalf("DirectoryOrigin = %q, want https://alice.foramcp.org", got)
	}
}

// TestSubdomainContext_RoundTrips pins the carrier the authenticating layer uses;
// an absent identity reads as empty, which is what Source refuses on.
func TestSubdomainContext_RoundTrips(t *testing.T) {
	t.Parallel()
	if got := agentsign.SubdomainFromContext(t.Context()); got != "" {
		t.Errorf("bare context carried %q, want empty", got)
	}
	ctx := agentsign.WithSubdomain(t.Context(), "alice.foramcp.org")
	if got := agentsign.SubdomainFromContext(ctx); got != "alice.foramcp.org" {
		t.Errorf("subdomain = %q, want alice.foramcp.org", got)
	}
}

// interfaceGuard keeps the narrow custody port honest: the shipped Vault store
// must satisfy it, so a change to either side is a compile error here rather than
// a wiring failure in the composition root.
var _ agentsign.Custody = (*keystore.VaultStore)(nil)
