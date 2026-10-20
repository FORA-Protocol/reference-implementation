package forasdk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/forasdk"
)

// TestAccountLegs_DialUnderTheSSRFGuard is the armed-guard proof for the two
// legs where a caller chooses the address.
//
// An authenticated agent names a domain, that domain's manifest names an
// endpoint, and a signed registration carrying the operator's business detail is
// sent there. Nothing else in this service decides where that goes, so the guard
// on this transport is the whole control -- and it is a deployment switch, so
// what has to be pinned is that the legs dial UNDER it rather than beside it.
//
// The two variables are CLEARED rather than assumed absent. Every MCP fixture
// opens them, because its peers are loopback servers, so a value inherited from
// the environment would turn this into a test that proves nothing. That is also
// why this test cannot live beside the tool tests: the fixture they share
// disarms the guard before the code under test is built.
//
// The endpoint resolver is a stub returning the peer directly. A real one would
// fetch a manifest first and be refused there, and the refusal would look
// identical while saying nothing about the RPC that follows -- which is the leg
// this test is for.
//
// The ZERO-REQUEST half is the load-bearing one. An unguarded transport still
// fails here for other reasons, so an error-only assertion passes either way.
func TestAccountLegs_DialUnderTheSSRFGuard(t *testing.T) {
	// No t.Parallel: t.Setenv forbids it, and the guard reads both variables when
	// the transport is constructed.
	for name, call := range map[string]func(context.Context, *forasdk.Client, string) error{
		"register": func(ctx context.Context, c *forasdk.Client, exchange string) error {
			_, err := c.Register(ctx, &forav1.RegisterRequest{
				Ver: helpers.ProtocolVersion, Exchange: exchange,
			})
			return err
		},
		"account status": func(ctx context.Context, c *forasdk.Client, exchange string) error {
			_, err := c.AccountStatus(ctx, &forav1.GetAccountStatusRequest{
				Ver: helpers.ProtocolVersion, Exchange: exchange,
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SKIP_SSRF", "")
			t.Setenv("ALLOW_INSECURE", "")

			peer := &recordingPeer{}
			srv := httptest.NewServer(peer)
			t.Cleanup(srv.Close)

			cfg := completeConfig(t)
			cfg.Endpoints = fixedEndpoint{endpoint: srv.URL}
			client, err := forasdk.New(cfg)
			if err != nil {
				t.Fatalf("forasdk.New: %v", err)
			}

			// The peer's host:port IS the exchange domain. The SDK re-checks that an
			// advertised endpoint sits on the host AND port that advertised it, so a
			// domain and an endpoint that disagree are refused at routing -- before
			// any transport is reached, which would make this test pass while
			// measuring nothing.
			if err := call(context.Background(), client, srv.Listener.Addr().String()); err == nil {
				t.Fatal("an account call reached a loopback plaintext endpoint with the guard armed")
			}
			if got := peer.calls.Load(); got != 0 {
				t.Errorf("the endpoint was dialled %d times; the guard must refuse before the "+
					"request leaves, and a signed registration that arrives anywhere has already "+
					"disclosed the operator's business detail", got)
			}
		})
	}
}

// recordingPeer answers nothing and counts what reached it. Counting is the
// whole job: the assertion is that this handler never runs.
type recordingPeer struct{ calls atomic.Int64 }

func (p *recordingPeer) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	p.calls.Add(1)
	w.WriteHeader(http.StatusOK)
}

// fixedEndpoint resolves every domain to one origin, so the dial the guard has
// to refuse is reached without a manifest fetch standing in front of it.
type fixedEndpoint struct{ endpoint string }

func (f fixedEndpoint) ResolveEndpoint(context.Context, string) (string, error) {
	return f.endpoint, nil
}
