package importer

import (
	"context"
	"crypto/ed25519"

	connect "connectrpc.com/connect"
	sdkconnect "github.com/FORA-Protocol/protocol/sdk/go/connect"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/catalogid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest"
)

// NewCatalogClient builds the catalog client every push of the automatic
// import goes through: the contributor-signed client ingest.NewCatalogClient
// builds, sending requestID's value as every push's request id and labelling
// every push as the automatic import's. The Exchange records the rows a
// labelled push writes as rows the import may refresh on a later run.
//
// The manual path, fora-ingest, builds its client with ingest.NewCatalogClient
// directly, so its pushes never carry the label: the pages it writes stay the
// pages of whoever pushed them.
func NewCatalogClient(
	exchangeURL, signatureAgent string, priv ed25519.PrivateKey, requestID func() string,
) (*sdkconnect.CatalogClient, error) {
	return ingest.NewCatalogClient(exchangeURL, signatureAgent, priv, nil,
		sdkconnect.WithRequestIDFunc(requestID),
		sdkconnect.WithInterceptors(sourceLabel()),
	)
}

// sourceLabel sets the automatic import's label (catalogid.SourceHeader) on
// every request the client sends. CatalogService has unary procedures only,
// so a unary interceptor covers every call the client makes.
func sourceLabel() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Spec().IsClient {
				req.Header().Set(catalogid.SourceHeader, catalogid.SourceAutoImport)
			}
			return next(ctx, req)
		}
	}
}
