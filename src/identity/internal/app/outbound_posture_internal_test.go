package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/account"
)

// TestBuildOutboundPosture_EndpointResolverCarriesThePolicy pins the WIRING, not
// the wrapper.
//
// The wrapper has its own tests in exchpolicy, and they would keep passing if the
// composition root stopped using it -- which is the failure worth catching,
// because the SDK client takes whatever resolver this function returns and every
// routing leg then resolves through it. Nothing else in the tree can see the
// difference: both existing policy tests drive the MCP tools, and the tool layer
// refuses an excluded domain before the service is reached, so neither ever
// reaches a resolver at all.
//
// This is an in-package test because buildOutboundPosture is unexported, and it
// stays that way deliberately -- the posture is one decision assembled in one
// place, and exporting it to be testable would make it something a second caller
// could assemble differently.
//
// No origin is needed: the refusal precedes the delegate, so nothing is dialled.
func TestBuildOutboundPosture_EndpointResolverCarriesThePolicy(t *testing.T) {
	posture, err := buildOutboundPosture(Config{
		Logger: slog.New(slog.DiscardHandler),
		MCP:    &MCPConfig{ExchangeAllowlist: "listed.example"},
	}, clock.System{})
	if err != nil {
		t.Fatalf("buildOutboundPosture: %v", err)
	}

	endpoint, err := posture.endpoints.ResolveEndpoint(context.Background(), "excluded.example")
	if err == nil {
		t.Fatalf("the shared resolver answered %q for a domain outside the allowlist", endpoint)
	}
	// Ours, so the account service names the operator's policy on every leg rather
	// than only on the one that reads requirements first.
	if !errors.Is(err, account.ErrNotPermitted) {
		t.Errorf("err = %v does not wrap ErrNotPermitted, so the resolver handed to the "+
			"SDK client is not the one carrying this deployment's policy", err)
	}
	// The SDK's, so its routing tier still calls the refusal final.
	if !errors.Is(err, resolvers.ErrNoEndpoint) {
		t.Errorf("err = %v does not wrap a routing sentinel the SDK recognises", err)
	}
}

// TestBuildOutboundPosture_RequirementsReaderCarriesThePolicy is the same pin on
// the second seam, and the one that matters most.
//
// Two collaborators consult the Exchange allowlist, and only one of them was
// pinned. The requirements reader is the leg that DIALS before an endpoint
// resolver could apply the policy for it — which is the reason its Allow hook
// exists at all — so a composition root that stopped passing the policy would
// fetch the manifest of any domain an authenticated agent named, on the one leg
// with nothing in front of it.
//
// Nothing else in the tree sees the difference. The reader's own contract table
// builds a reader with its own Allow, which proves the reader honours a policy it
// is GIVEN, not that this function gives it one; and the MCP policy tests are
// blind because the tool layer refuses an excluded domain before the service is
// reached.
//
// No origin: the refusal precedes the fetch, which is the property.
func TestBuildOutboundPosture_RequirementsReaderCarriesThePolicy(t *testing.T) {
	posture, err := buildOutboundPosture(Config{
		Logger: slog.New(slog.DiscardHandler),
		MCP:    &MCPConfig{ExchangeAllowlist: "listed.example"},
	}, clock.System{})
	if err != nil {
		t.Fatalf("buildOutboundPosture: %v", err)
	}

	_, err = posture.requirements.ResolveRegistrationRequirements(
		context.Background(), "excluded.example")
	if err == nil {
		t.Fatal("the shared reader read requirements from a domain outside the allowlist")
	}
	if !errors.Is(err, account.ErrNotPermitted) {
		t.Errorf("err = %v does not wrap ErrNotPermitted, so the reader handed to the SDK "+
			"client is not the one carrying this deployment's policy — and this is the leg "+
			"that dials before any resolver could apply it", err)
	}
}

// TestBuildOutboundPosture_NoPolicyResolvesEverything keeps the default posture
// honest: a deployment that sets no allowlist must reach the delegate, which
// means the wrapper is transparent rather than a second gate to configure.
//
// The delegate then fails on its own terms -- there is no manifest at that host --
// and that failure must NOT carry our policy sentinel, or an operator reading a
// dead Exchange would be sent to edit an allowlist they never set.
func TestBuildOutboundPosture_NoPolicyResolvesEverything(t *testing.T) {
	posture, err := buildOutboundPosture(Config{
		Logger: slog.New(slog.DiscardHandler),
		MCP:    &MCPConfig{},
	}, clock.System{})
	if err != nil {
		t.Fatalf("buildOutboundPosture: %v", err)
	}

	_, err = posture.endpoints.ResolveEndpoint(context.Background(), "unreachable.invalid")
	if err == nil {
		t.Fatal("a reserved-invalid host resolved to an endpoint")
	}
	if errors.Is(err, account.ErrNotPermitted) {
		t.Errorf("err = %v reports a policy refusal although no allowlist is set; an "+
			"empty policy must permit every domain and let the fetch answer", err)
	}
}
