//go:build integration

package exchangetest

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	foraadminv1 "github.com/FORA-Protocol/protocol/gen/go/fora/admin/v1"
	"github.com/FORA-Protocol/protocol/gen/go/fora/admin/v1/foraadminv1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/jackc/pgx/v5/pgxpool"

	audiencetest "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// setSeatFee sets the seat's platform fee the way the operator does: through
// the admin plane's SetTenantFeeRate, served over the production admin surface
// on a loopback listener for the one call. The rate change is audited like
// any other.
func setSeatFee(t testing.TB, ctx context.Context, pool *pgxpool.Pool, exchangeDomain, seatID string, bps int32) {
	t.Helper()
	queries := sqlc.New(pool)
	surface, err := transport.WrapAdminSurface(testutil.DiscardLogger(),
		service.NewAdminServiceFromPool(pool, queries), service.NewEvidenceReadService(queries),
		"127.0.0.0/8", audiencetest.MustInterceptor(t, exchangeDomain))
	if err != nil {
		t.Fatalf("exchangetest: build the admin surface: %v", err)
	}
	srv := httptest.NewServer(surface)
	defer srv.Close()
	admin := foraadminv1connect.NewAdminServiceClient(srv.Client(), srv.URL)
	if _, err := admin.SetTenantFeeRate(ctx, connect.NewRequest(&foraadminv1.SetTenantFeeRateRequest{
		Ver:  helpers.ProtocolVersion,
		Rate: &foraadminv1.TenantFeeRate{TenantId: seatID, FeeRateBps: bps},
	})); err != nil {
		t.Fatalf("exchangetest: set the seat's fee rate to %d bps: %v", bps, err)
	}
}
