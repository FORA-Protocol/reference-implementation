package mcp

import (
	"context"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
)

// The outbound legs arrive through a port each rather than through one
// interface, one per operation a tool performs. Every one of them is the
// protocol SDK's today, served by forasdk, but each tool depends only on the
// operation it calls, so a test double stands in for exactly one leg.
//
// The account workflow crosses no port in this file. Opening an account and
// asking after one are exchacct's, reached as that service rather than through a
// port here: a mirror interface repeating its methods would narrow nothing, and
// the layer below a transport is the one place this package is allowed to name
// concretely — the same seam oauthserver has on signup.Service. exchacct
// declares the outbound port those two RPCs travel on, next to the workflow that
// uses it.

// purchaser buys offers through the Broker (BrokerService.ExecuteTransaction).
//
// req carries the requester, each item's offer and the idempotency key; the
// implementation signs the acceptances with the calling agent's key, so the
// tool never handles key material. The answer is the Broker's combined
// response: one result per offer, including the items an Exchange refused as a
// whole, which carry an UpstreamRefusal instead of an error.
//
// Declared here rather than imported as *forasdk.Client because the consumer is
// the one that knows how narrow the seam really is.
type purchaser interface {
	Execute(ctx context.Context, req *forav1.TransactionRequest) (*forav1.BrokerTransactionResponse, error)
}

// discoverer resolves offers through the Broker.
//
// It returns the verified/rejected split rather than the raw response, and that
// is the whole reason it is a distinct port: an agent on this surface runs no
// verifier of its own, so the offers it is shown have to have been checked here.
// A type returning the bare response could not express that.
type discoverer interface {
	Resolve(ctx context.Context, req *forav1.DiscoveryRequest) (core.DiscoveryResult, error)
}

// reporter files a usage report with the Exchange that issued the offer.
//
// There is no exchange argument. The destination is read off the report's own
// exchange field, which the offer signed, and resolved from that Exchange's own
// manifest — so there is no parameter a configured origin could arrive as, which
// is what keeps the routing rule structural rather than conventional.
type reporter interface {
	ReportUsage(ctx context.Context, report *forav1.UsageReport) (*forav1.UsageReportResponse, error)
}

// contentFetch retrieves the bytes ONE signed delivery URL names, presenting the
// calling agent's custodied key.
//
// A function rather than an interface because the seam is one operation, and an
// alias rather than a defined type so the provider can name the same shape
// without either package importing the other.
type contentFetch = func(ctx context.Context, signedURL string) (resolvers.Content, error)

// contentFetcher opens the content leg for one purchase.
//
// It hands back a fetch rather than performing one, so a batch resolves the
// calling agent's key once and dials on one connection instead of once per item.
// Nothing here can ask for a different agent's key — the identity travels on the
// context, exactly as it does on the FORA legs.
type contentFetcher interface {
	ContentSession(ctx context.Context) (contentFetch, error)
}

// exchangeAllowed answers whether this deployment will speak to an Exchange at
// all.
//
// A function rather than an interface because the seam is one question, matching
// contentFetch above. It is the same predicate the shared endpoint resolver
// carries, so the tool layer's readable refusal and the structural one below it
// cannot come to disagree.
type exchangeAllowed = func(domain string) bool
