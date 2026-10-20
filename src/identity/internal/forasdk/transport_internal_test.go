package forasdk

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forahttpsig"
)

// TestNew_BoundsTheGuardedLegsIdleSockets pins the one cost this service can bound
// on the legs an authenticated agent paces.
//
// The SDK composes its SSRF guard by cloning the base transport, so each call on
// that leg gets its own pool and abandons it on return. The abandoned transport
// keeps its own idle timer, and Go's default holds the socket open for 90 seconds
// after nothing is using it. The bound has to be set on the template, because the
// clone is made inside the SDK and this value is the only thing that reaches it.
//
// Deleting the assignment leaves every other test passing: nothing else in this
// tree reads the field, and the calls all still work -- they just leave sockets
// behind for the default window.
func TestNew_BoundsTheGuardedLegsIdleSockets(t *testing.T) {
	t.Parallel()
	client := newForTest(t)

	if got := client.guardedBase.IdleConnTimeout; got != guardedIdleTimeout {
		t.Errorf("the guarded leg's transports are cloned from a template whose idle "+
			"timeout is %v, want %v -- a socket left by a call an agent paced stays open "+
			"that much longer with nothing on it", got, guardedIdleTimeout)
	}
}

// TestNew_LeavesTheBrokerLegsPoolAlone is the other half, and the reason the bound
// above is set on one transport rather than in clonedTransport.
//
// The Broker leg's transport is SHARED and keeps its pool, so its idle
// connections are reused by the next call rather than abandoned. Shortening it
// would throw away connections the next call wants, which is a cost rather than a
// bound.
func TestNew_LeavesTheBrokerLegsPoolAlone(t *testing.T) {
	t.Parallel()
	client := newForTest(t)

	def, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("the process default transport is not an *http.Transport, so there is " +
			"no default to compare against")
	}
	if got := client.homeBase.IdleConnTimeout; got != def.IdleConnTimeout {
		t.Errorf("the Broker leg's idle timeout is %v, want the default %v -- that "+
			"transport is shared and its idle connections are reused, so shortening it "+
			"discards connections instead of bounding anything", got, def.IdleConnTimeout)
	}
}

func newForTest(t *testing.T) *Client {
	t.Helper()
	client, err := New(Config{
		Keys: func(context.Context) (forahttpsig.AgentKey, error) {
			return forahttpsig.AgentKey{}, nil
		},
		BrokerURL:    "http://broker.example",
		OfferKeys:    noKeys{},
		Endpoints:    noEndpoints{},
		Requirements: noRequirements{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// New only checks these for presence -- no call here reaches one -- so the
// smallest thing that satisfies each port is what the construction under test
// needs.

type noKeys struct{}

func (noKeys) Resolve(context.Context, string) (ed25519.PublicKey, error) { return nil, nil }

type noEndpoints struct{}

func (noEndpoints) ResolveEndpoint(context.Context, string) (string, error) { return "", nil }

type noRequirements struct{}

func (noRequirements) ResolveRegistrationRequirements(
	context.Context, string,
) (resolvers.RegistrationRequirements, error) {
	return resolvers.RegistrationRequirements{}, nil
}
