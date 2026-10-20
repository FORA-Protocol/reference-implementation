//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// The caller_id spellings a catalog push may carry, on both sides of the
// contributor gate: the caller's own spelling, the publisher manifest's, and a
// value that names no host at all. Each runs on the push harness defined beside
// the other PushResources scenarios.

// TestPushResources_SchemedCallerIDAuthorizesAsItsHost closes the gap between the
// two gates a push passes through. Gate 1 resolves the caller's key through
// agentreg, which canonicalizes; Gate 2 compares caller_id against the manifest's
// contributor domains verbatim. A publisher spelling its own caller_id as a full
// origin therefore AUTHENTICATED as pub.example and was then refused with
// caller_not_in_catalog_contributors — a rejection naming a condition it
// satisfies, and one no error message could have explained.
//
// The manifest lists the bare host, as a publisher writes it; only the caller
// side folds. The push must be accepted and the entry must become discoverable,
// which is what separates "both gates passed" from "the request died quietly
// somewhere else".
func TestPushResources_SchemedCallerIDAuthorizesAsItsHost(t *testing.T) {
	h := newPushHarness(t)
	const callerHost = "caller.example"
	// The publisher authorizes the bare host — the spelling a manifest carries.
	h.publisher.setContributors(callerHost)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	h.publishAgent(t, callerHost, priv)

	// The caller spells itself as a full origin, on the wire and in the message.
	const callerAsSpelled = "https://" + callerHost
	client := h.signedCat(callerAsSpelled, priv)
	if _, err := client.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, callerAsSpelled, []*forav1.ResourceEntry{{
		Domain: h.publisherDom, Path: "/articles/schemed",
		Terms: []*forav1.LicenseTerm{seedPricedTerm()},
	}}))); err != nil {
		t.Fatalf("push with caller_id %q: %v — the caller authenticates as %q, so it must "+
			"also authorize as %q", callerAsSpelled, err, callerHost, callerHost)
	}
	if got := discoverOfferCount(t, h, "https://"+h.publisherDom+"/articles/schemed"); got != 1 {
		t.Fatalf("offers = %d, want 1 — the push was accepted but the entry did not land", got)
	}
}

// TestPushResources_ManifestSpellingsAuthorizeOneContributor drives the mirror of
// the test above: the CALLER sends the bare host and the PUBLISHER writes the
// contributor entry however it likes.
//
// This is the direction that was broken and untested. Canonicalizing only the
// caller made the comparison asymmetric — folded on one side, raw on the other —
// so a publisher that had written "https://caller.example" in its own manifest,
// or used mixed case, a trailing dot, or an explicit :443, had every push refused
// after the change. Silently, too: contributor rejection is per-entry, so the RPC
// still answers 200 and only the entry count betrays it.
//
// Every other fixture in this file writes bare hosts on both sides, which is why
// the whole suite stayed green with the regression in place.
func TestPushResources_ManifestSpellingsAuthorizeOneContributor(t *testing.T) {
	const callerHost = "mirror-caller.example"
	for _, manifestSpelling := range []string{
		"https://" + callerHost,
		"Mirror-Caller.Example",
		callerHost + ".",
		callerHost + ":443",
	} {
		t.Run(manifestSpelling, func(t *testing.T) {
			h := newPushHarness(t)
			// The publisher authorizes the contributor in ITS spelling.
			h.publisher.setContributors(manifestSpelling)

			_, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("gen: %v", err)
			}
			h.publishAgent(t, callerHost, priv)

			// The caller sends the bare host, as the transport canonicalizes it.
			client := h.signedCat(callerHost, priv)
			if _, err := client.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, callerHost, []*forav1.ResourceEntry{{
				Domain: h.publisherDom, Path: "/articles/mirror",
				Terms: []*forav1.LicenseTerm{seedPricedTerm()},
			}}))); err != nil {
				t.Fatalf("push: %v", err)
			}
			// The RPC answers 200 whether or not the entry was authorized, so the
			// offer count is what proves the contributor gate admitted it.
			if got := discoverOfferCount(t, h, "https://"+h.publisherDom+"/articles/mirror"); got != 1 {
				t.Fatalf("offers = %d, want 1 — the publisher spelled its contributor %q and "+
					"the caller resolves to %q; both name one party",
					got, manifestSpelling, callerHost)
			}
		})
	}
}

// TestPushResources_CallerIDNamingNoHostIsInvalidArgument is the negative path
// for the canonicalization above: a caller_id that names no host cannot be an
// identity, so the push is refused before the contributor gate is consulted.
//
// The value is the sf-dictionary form, which nothing in the stack unwraps — the
// same choice the sibling Broker and Execute tests make, so the refusal is pinned
// rather than a parsing gap a later change would close.
//
// Without this the refusal is unpinned: a regression letting it fall through to
// verifyCallerSignature (a different code) or return nil would leave the suite
// green, because the only caller_id coverage is the positive path.
func TestPushResources_CallerIDNamingNoHostIsInvalidArgument(t *testing.T) {
	h := newPushHarness(t)
	const callerHost = "nohost-caller.example"
	h.publisher.setContributors(callerHost)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	h.publishAgent(t, callerHost, priv)

	// Authenticates as the host; sends a caller_id that names none.
	client := h.signedCat(callerHost, priv)
	_, err = client.PushResources(h.ctx, connect.NewRequest(newPushRequest(h.tenantID, `agent2="https://`+callerHost+`"`, []*forav1.ResourceEntry{{
		Domain: h.publisherDom, Path: "/articles/nohost",
		Terms: []*forav1.LicenseTerm{seedPricedTerm()},
	}})))
	if err == nil {
		t.Fatal("push accepted a caller_id that names no host; want InvalidArgument")
	}
	// InvalidArgument, matching the Broker's agent_id and /agents/register: the
	// caller sent a field that is not a host, which is a malformed argument rather
	// than a failure to authenticate.
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument (err=%v)", got, err)
	}
	// The code alone proves nothing, and that is the trap this assertion exists to
	// avoid: delete the canonicalization refusal and the raw caller_id travels on
	// to the signature gate, which also refuses it and also writes nothing. The
	// field metadata is what separates them — only the canonicalization branch
	// names caller_id — and it reaches the wire only because the catalog surface
	// now stamps an ErrorDetail envelope like every other FORA fault.
	assertCatalogRejectionField(t, err, "caller_id")
	// The curated message stands alone: the parser chain that produced it is
	// logged, not returned to an unauthenticated caller.
	if msg := err.Error(); strings.Contains(msg, "forawellknown") || strings.Contains(msg, "parse ") {
		t.Errorf("internal wrapping reached the caller: %q", msg)
	}
	// And the refusal precedes the catalog write rather than merely accompanying it.
	if got := discoverOfferCount(t, h, "https://"+h.publisherDom+"/articles/nohost"); got != 0 {
		t.Fatalf("offers = %d, want 0 — the entry landed despite a refused caller_id", got)
	}
}
