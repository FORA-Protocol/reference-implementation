//go:build integration

package testutil

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"
	tcvault "github.com/testcontainers/testcontainers-go/modules/vault"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/tcstart"
)

// devRootToken is the root token the dev-mode Vault container boots with. Dev
// mode is in-memory and unsealed, which is exactly what a test wants and exactly
// what production must never be.
const devRootToken = "root-token-for-tests"

// SharedVault is a single dev-mode Vault container reset to an empty KV v2 mount
// between tests. It is the secret-store analogue of db.SharedPostgres and
// SharedRedis: brought up once from a package's TestMain so container startup is
// paid once per package, then reset before each test.
//
// The reset is an unmount + remount of the KV engine rather than a per-key
// delete: it wipes secrets AND their version history in one call, which matters
// here because KV v2 keeps superseded versions of a secret and a test that only
// deleted the current version would leak state — including private keys — into
// the next one.
type SharedVault struct {
	// Client addresses the shared Vault, already authenticated as root. It is
	// stable across Reset, which replaces the mount without dropping the
	// connection.
	Client *vaultapi.Client
	// Mount is the KV v2 mount path the tests write to.
	Mount string
}

// Reset replaces the shared Vault's KV v2 mount with an empty one, discarding
// every secret and every superseded version the previous test wrote. Call it at
// the start of each Vault-backed test's setup. Issued from this test-infra layer
// it is fixture lifecycle — the secret-store analogue of SharedPostgres.Reset —
// not test arrange/assert state access.
func (v *SharedVault) Reset(ctx context.Context) error {
	sys := v.Client.Sys()
	if err := sys.UnmountWithContext(ctx, v.Mount); err != nil {
		return fmt.Errorf("unmount %s: %w", v.Mount, err)
	}
	if err := mountKV(ctx, v.Client, v.Mount); err != nil {
		return err
	}
	return nil
}

// ScopedClient writes rules as the Vault policy name and returns a client for the
// same Vault whose token carries that policy and nothing else. The "default" policy
// is left out, as the e2e stack leaves it out, so a test measures exactly what the
// named policy grants. The token lives ten minutes, longer than any test.
func (v *SharedVault) ScopedClient(tb testing.TB, name, rules string) *vaultapi.Client {
	tb.Helper()
	ctx := tb.Context()
	if err := v.Client.Sys().PutPolicyWithContext(ctx, name, rules); err != nil {
		tb.Fatalf("write policy %q: %v", name, err)
	}
	secret, err := v.Client.Auth().Token().CreateWithContext(ctx, &vaultapi.TokenCreateRequest{
		Policies:        []string{name},
		NoDefaultPolicy: true,
		TTL:             "10m",
	})
	if err != nil {
		tb.Fatalf("create token for policy %q: %v", name, err)
	}
	client, err := v.Client.Clone()
	if err != nil {
		tb.Fatalf("clone vault client: %v", err)
	}
	client.SetToken(secret.Auth.ClientToken)
	return client
}

// IdentityServiceClient returns a client whose token carries only the Identity
// Service's policy for this Vault's mount, rendered from the template production
// and the e2e stack write (deploy/storage/vault/identity-policy.hcl). A suite that
// builds its store over this client runs under the policy production grants, so it
// fails the day the service needs a capability the template does not give.
func (v *SharedVault) IdentityServiceClient(tb testing.TB) *vaultapi.Client {
	tb.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		tb.Fatal("locate the testutil source file")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..",
		"deploy", "storage", "vault", "identity-policy.hcl"))
	if err != nil {
		tb.Fatalf("read the shared policy template: %v", err)
	}
	return v.ScopedClient(tb, "identity-service", strings.ReplaceAll(string(raw), "${KV_MOUNT}", v.Mount))
}

// mountKV mounts the KV v2 engine at path with Vault's DEFAULT version retention
// (ten versions), deliberately not a hardened max_versions=1.
//
// KV v2 keeps superseded versions of a secret, and a superseded version of a key
// record still holds the private key. Nothing destroys them today — custody writes
// each key once, at its own thumbprint path, so no record is ever superseded — but
// revocation will have to, and the mount in production belongs to the operator, who
// cannot be assumed to have configured history away. A max_versions=1 test mount
// would let Vault discard old versions itself, and the suite would then pass whether
// or not the store ever destroyed one. Keeping Vault's default here is what will give
// that future test its teeth.
func mountKV(ctx context.Context, client *vaultapi.Client, path string) error {
	err := client.Sys().MountWithContext(ctx, path, &vaultapi.MountInput{
		Type:    "kv",
		Options: map[string]string{"version": "2"},
	})
	if err != nil {
		return fmt.Errorf("mount kv-v2 at %s: %w", path, err)
	}
	return nil
}

// StartSharedVault runs one dev-mode Vault container, mounts a KV v2 engine, and
// returns a handle plus a cleanup func for the caller (TestMain) to defer. The
// per-test reset is (*SharedVault).Reset. Sibling of StartSharedRedis.
func StartSharedVault(ctx context.Context, logger *slog.Logger, mount string) (*SharedVault, func(), error) {
	container, err := tcstart.Run(ctx, func(ctx context.Context) (*tcvault.VaultContainer, error) {
		return tcvault.Run(ctx, "hashicorp/vault:1.20", tcvault.WithToken(devRootToken))
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start vault: %w", err)
	}
	terminate := func() {
		if termErr := container.Terminate(context.Background()); termErr != nil {
			logger.Warn("terminate shared vault", "err", termErr)
		}
	}
	addr, err := container.HttpHostAddress(ctx)
	if err != nil {
		terminate()
		return nil, nil, fmt.Errorf("vault address: %w", err)
	}
	cfg := vaultapi.DefaultConfig()
	cfg.Address = addr
	client, err := vaultapi.NewClient(cfg)
	if err != nil {
		terminate()
		return nil, nil, fmt.Errorf("vault client: %w", err)
	}
	client.SetToken(devRootToken)
	if err := mountKV(ctx, client, mount); err != nil {
		terminate()
		return nil, nil, err
	}
	return &SharedVault{Client: client, Mount: mount}, terminate, nil
}
