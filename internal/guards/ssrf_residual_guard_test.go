// This file pins the "hand-rolled SSRF guard" disease closed on the Go side.
// The SSRF-guarded HTTP client lives in the FORA SDK
// (sdk/go/resolvers.NewGuardedClientFromEnv) — app code must not re-implement
// the classification, the scheme guard, the redirect anchoring, the env-gate
// option builder, or a package-level guarded default. Every outbound caller
// constructs its client from the SDK factory; nothing in the app re-wraps it.
package guards

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// residualSSRFGuard matches every hand-rolled SSRF-guard identifier that must
// NOT survive in app source. The two package-qualified forms
// (forawellknown.NewGuardedClient, forawellknown.GuardOptions) are anchored to
// the forawellknown package on purpose: the bare token "NewGuardedClient" is a
// SUBSTRING of the RETAINED SDK call resolvers.NewGuardedClientFromEnv (used by
// internal/agentkeys after the rewire), so an unqualified ban would false-fail.
// forawellknown.NewGuardedClient still catches forawellknown.NewGuardedClientFromEnv
// (it is a prefix), and forawellknown.GuardOptions catches ...GuardOptionsFromEnv.
// The remaining names are guard.go-local and unambiguous.
var residualSSRFGuard = regexp.MustCompile(
	`forawellknown\.NewGuardedClient|` +
		`forawellknown\.GuardOptions|` +
		`\bguardControl\b|` +
		`\bschemeGuard\b|` +
		`\bguardRedirect\b|` +
		`\bGuardOptionsFromEnv\b|` +
		`\bdefaultGuardedClient\b`,
)

// TestNoResidualHandRolledSSRFGuard fails when any app source file still carries
// hand-rolled SSRF-guard code — the guard belongs to sdk/go/resolvers, never to
// the app (internal/forawellknown/guard.go and its callers must be gone).
func TestNoResidualHandRolledSSRFGuard(t *testing.T) {
	t.Parallel()
	assertNoMatchOutsideAllowlist(t, residualSSRFGuard, nil,
		"still carries hand-rolled SSRF-guard code — construct the client "+
			"from the SDK factory resolvers.NewGuardedClientFromEnv() and delete "+
			"internal/forawellknown/guard.go")
}

// TestSSRFGuardFileStaysDeleted fails while the deleted disease home reappears.
func TestSSRFGuardFileStaysDeleted(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"internal/forawellknown/guard.go",
		"internal/forawellknown/guard_test.go",
		"internal/forawellknown/guard_internal_test.go",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Errorf("%s exists — the SSRF guard is SDK-owned (sdk/go/resolvers); "+
				"do not keep the app copy", rel)
		}
	}
}

// --- meta-tests: prove the detector itself works ---------------------------

// TestMeta_ResidualSSRFDetectorFlagsHandRolledCode is the positive meta-test:
// package-qualified guard construction and a guard.go-local name must both match.
func TestMeta_ResidualSSRFDetectorFlagsHandRolledCode(t *testing.T) {
	t.Parallel()
	for _, dirty := range []string{
		`client = forawellknown.NewGuardedClient(forawellknown.GuardOptions{Insecure: true})`,
		`fetchClient := forawellknown.NewGuardedClientFromEnv()`,
		`Control: guardControl(opts.Insecure),`,
		`Transport: schemeGuard{base: base}`,
		`CheckRedirect: guardRedirect(opts.Insecure),`,
		`return NewGuardedClient(GuardOptionsFromEnv())`,
		`var defaultGuardedClient = sync.OnceValue(...)`,
	} {
		if !residualSSRFGuard.MatchString(dirty) {
			t.Fatalf("residual-SSRF detector missed hand-rolled code: %q", dirty)
		}
	}
}

// TestMeta_ResidualSSRFDetectorPassesSDKCall is the negative meta-test: the
// RETAINED SDK factory call must NOT match, even though it contains the
// substring "NewGuardedClient" and "GuardedClientFromEnv".
func TestMeta_ResidualSSRFDetectorPassesSDKCall(t *testing.T) {
	t.Parallel()
	for _, clean := range []string{
		`client, err := resolvers.NewGuardedClientFromEnv()`,
		`cfg.HTTP, err = resolvers.NewGuardedClientFromEnv()`,
	} {
		if residualSSRFGuard.MatchString(clean) {
			t.Fatalf("residual-SSRF detector wrongly flagged the retained SDK call: %q", clean)
		}
	}
}
