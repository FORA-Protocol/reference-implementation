// Package xclient wraps the Connect-Go ExchangeService client. Handlers call
// Pool.For(endpoint) to get a cached client keyed on the exchange endpoint,
// avoiding per-request dial overhead.
package xclient

import (
	"context"
	"net/http"
	"sync"

	"connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/broker"
)

// ExchangeCaller is the narrow interface the Broker depends on.
type ExchangeCaller interface {
	DiscoverResources(
		ctx context.Context, endpoint string, req *forav1.ResourceQuery,
	) (*forav1.ResourceResponse, error)
	ExecuteTransaction(
		ctx context.Context, endpoint string, req *forav1.TransactionRequest,
	) (*forav1.TransactionResponse, error)
}

// Pool caches one forav1connect.ExchangeServiceClient per endpoint.
type Pool struct {
	http    *http.Client
	mu      sync.RWMutex
	clients map[string]forav1connect.ExchangeServiceClient
}

// NewPool constructs an empty pool using the given HTTP client. The client is
// REQUIRED: it is the caller-influenced Broker→Exchange relay client the
// composition root injects (a nil client is a wiring bug, so fail loud at boot
// rather than silently fabricating a fail-open &http.Client that would downgrade
// the relay to an unguarded default).
func NewPool(httpClient *http.Client) *Pool {
	if httpClient == nil {
		panic("xclient: NewPool requires a non-nil HTTP client")
	}
	return &Pool{
		http:    httpClient,
		clients: make(map[string]forav1connect.ExchangeServiceClient),
	}
}

// For returns a cached client for endpoint, creating it on first use.
func (p *Pool) For(endpoint string) forav1connect.ExchangeServiceClient {
	p.mu.RLock()
	c, ok := p.clients[endpoint]
	p.mu.RUnlock()
	if ok {
		return c
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok = p.clients[endpoint]; ok {
		return c
	}
	// The Connect protocol (the connect-go default — deliberately NOT
	// connect.WithGRPC()): the relay crosses real proxies/LBs on a plain
	// HTTP/1.1 client, and gRPC depends on HTTP trailers, which are reliable
	// in-process (where the WithGRPC integration tests run) but not across
	// intermediaries; Connect has no trailer dependency. The ingest CLI made
	// the same switch for the same reason after its gRPC push died at a TLS
	// proxy's HTTP/1.1 upstream hop. This is also the protocol the full e2e
	// stack proves.
	c = forav1connect.NewExchangeServiceClient(p.http, endpoint)
	p.clients[endpoint] = c
	return c
}

// seatRequestID copies this request's correlation id onto an upstream request, so
// the Exchange records the id the Broker minted or accepted rather than minting a
// second one nobody upstream ever saw.
//
// It matters most on the execute leg: the Exchange writes the id it sees into an
// append-once evidence row and documents it as the key that correlates that row
// outward against the edge delivery log and the reconciliation sweep. Without
// this the deployed agent → MCP → Broker → Exchange path stores a value that
// appears in no other log, and the row's own provenance flag reports it honestly
// as minted — which is to say, as a join that resolves to nothing.
//
// Nothing is set when the context carries no id (a direct service call, a queue
// consumer, any driver not behind RequestIDMiddleware): the Exchange mints one in
// that case, which is the correct outcome rather than a blank header.
//
// Safe against the RFC 9421 gate for the reason the middleware documents: no FORA
// client signs x-request-id, and the verifier rebuilds the signature base from
// the signer's own component list.
func seatRequestID(ctx context.Context, h http.Header) {
	if id := reqctx.RequestID(ctx); id != "" {
		h.Set(helpers.RequestIDHeader, id)
	}
}

// DiscoverResources sends a ResourceQuery the Broker authored to the given
// endpoint. The pool's HTTP client signs it with the Broker's own RFC 9421
// signature, the only signature it carries.
func (p *Pool) DiscoverResources(
	ctx context.Context, endpoint string, req *forav1.ResourceQuery,
) (*forav1.ResourceResponse, error) {
	creq := connect.NewRequest(req)
	seatRequestID(ctx, creq.Header())
	resp, err := p.For(endpoint).DiscoverResources(ctx, creq)
	if err != nil {
		return nil, broker.Wrapf(broker.KindUpstreamUnavailable, err, "exchange discover")
	}
	return resp.Msg, nil
}

// ExecuteTransaction sends a TransactionRequest to the given endpoint.
//
// A failure is returned as the Connect client produced it, not wrapped in a
// broker.Error: the purchase service reports the Exchange's own code and
// ErrorDetail to the agent, and a Broker kind around it would name a cause —
// "upstream unavailable" for an Exchange that answered permission_denied — that
// is not what happened. The error never reaches the Broker's transport as it is.
func (p *Pool) ExecuteTransaction(
	ctx context.Context, endpoint string, req *forav1.TransactionRequest,
) (*forav1.TransactionResponse, error) {
	creq := connect.NewRequest(req)
	seatRequestID(ctx, creq.Header())
	resp, err := p.For(endpoint).ExecuteTransaction(ctx, creq)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}
