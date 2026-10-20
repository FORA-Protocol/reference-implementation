package forasdk_test

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/forasdk"
)

// TestNew_RefusesEachCollaboratorItCannotBuildItself pins the construction
// contract of the package that now owns every outbound FORA leg.
//
// Each of these is an application-owned collaborator the SDK is deliberately not
// allowed to construct for itself — a key source, an offer-key resolver, an
// endpoint resolver, a requirements reader — so the only thing standing between a
// missing one and a nil dereference on the first tool call is New refusing. The
// leg being retired carried the same rule and a test that held it; that test went
// with the leg, and this package had none at all.
//
// Two of the refusals are not merely defensive, and the messages say why. Without
// an offer-key resolver every relayed offer is unverifiable, so discovery answers
// "nothing on offer" for a reason no log line explains. Without a requirements
// reader the SDK's own would be used, and that one applies no Exchange allowlist
// — a defaulted reader fetches the manifest of any domain an authenticated agent
// names, on the one leg that dials before an endpoint resolver could apply the
// policy for it.
//
// Written as one table over a complete config with a single field cleared, rather
// than a stanza per field. The stanza form is five near-identical blocks, which is
// duplication the copy-paste gate reads at a threshold of zero, and it also hides
// the property being asserted: that each field is INDEPENDENTLY load-bearing.
//
// Clock is absent on purpose and its absence is asserted below. It is defaulted
// rather than required, so listing it here would pin a refusal that does not
// exist and would contradict the injected-clock rule that says a caller may leave
// the system clock in place.
func TestNew_RefusesEachCollaboratorItCannotBuildItself(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// clear removes the one collaborator under test from an otherwise
		// complete config.
		clear func(*forasdk.Config)
		// names is a fragment the refusal must contain, so the message points at
		// the field rather than only reporting that something was missing.
		names string
	}{
		{"the key source", func(c *forasdk.Config) { c.Keys = nil }, "Keys"},
		{"the broker origin", func(c *forasdk.Config) { c.BrokerURL = "" }, "BrokerURL"},
		{"the offer-key resolver", func(c *forasdk.Config) { c.OfferKeys = nil }, "OfferKeys"},
		{"the endpoint resolver", func(c *forasdk.Config) { c.Endpoints = nil }, "Endpoints"},
		{"the requirements reader", func(c *forasdk.Config) { c.Requirements = nil }, "Requirements"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := completeConfig(t)
			tc.clear(&cfg)

			client, err := forasdk.New(cfg)
			if err == nil {
				t.Fatalf("New built a client with no %s; the first tool call would "+
					"dereference it instead of failing at the composition root", tc.names)
			}
			if client != nil {
				t.Error("New returned both a client and an error")
			}
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("refusal %q does not name the missing field, so an operator "+
					"reading a failed boot cannot tell which one to supply", err)
			}
		})
	}
}

// TestNew_DefaultsTheClockRatherThanRequiringIt is the negative that keeps the
// table above honest.
//
// A clock is a port, not a collaborator the caller must own: production wires the
// system clock and a test wires a deterministic one. Requiring it would make every
// caller state a value with one sensible answer, and the table above would grow a
// row that passes for the wrong reason. This asserts the opposite arm directly,
// because a defaulted field is invisible to a test that only ever clears fields.
func TestNew_DefaultsTheClockRatherThanRequiringIt(t *testing.T) {
	t.Parallel()

	cfg := completeConfig(t)
	cfg.Clock = nil

	if _, err := forasdk.New(cfg); err != nil {
		t.Fatalf("New refused a config with no clock: %v", err)
	}
}

// completeConfig returns a config New accepts, so each case above differs from a
// working client by exactly one field.
//
// The collaborators are the narrowest thing satisfying each port. None is dialled:
// New only records them, and every refusal under test happens before anything is
// built, so a stub that answers nothing is enough to prove the field was present.
func completeConfig(t *testing.T) forasdk.Config {
	t.Helper()
	return forasdk.Config{
		Keys:         testutil.AgentKeySource(t, "https://agent.example"),
		BrokerURL:    "http://broker.example",
		OfferKeys:    stubKeys{},
		Endpoints:    stubEndpoints{},
		Requirements: stubRequirements{},
	}
}

// stubKeys resolves no key. Discovery would refuse every offer through it, which
// is the fail-closed answer the real resolver's absence is meant to avoid — and
// irrelevant here, because no call is made.
type stubKeys struct{}

func (stubKeys) Resolve(context.Context, string) (ed25519.PublicKey, error) { return nil, nil }

// stubEndpoints resolves no endpoint.
type stubEndpoints struct{}

func (stubEndpoints) ResolveEndpoint(context.Context, string) (string, error) { return "", nil }

// stubRequirements reads no requirements.
type stubRequirements struct{}

func (stubRequirements) ResolveRegistrationRequirements(
	context.Context, string,
) (resolvers.RegistrationRequirements, error) {
	return resolvers.RegistrationRequirements{}, nil
}
