package transport

import (
	"context"

	connect "connectrpc.com/connect"
	foraadminv1 "github.com/FORA-Protocol/protocol/gen/go/fora/admin/v1"
	"github.com/FORA-Protocol/protocol/gen/go/fora/admin/v1/foraadminv1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// AdminHandler adapts AdminService to the generated
// foraadminv1connect.AdminServiceHandler interface. It is mounted on the separate
// admin listener with the emit-unpopulated codec and the bidirectional
// protovalidate interceptor and NO request-signing (ADR-022); the interceptor
// enforces every buf.validate bound on both the request and the echoed response.
type AdminHandler struct {
	foraadminv1connect.UnimplementedAdminServiceHandler
	svc *service.AdminService
}

// NewAdminHandler wires the handler.
func NewAdminHandler(svc *service.AdminService) *AdminHandler {
	return &AdminHandler{svc: svc}
}

// SetTenantFeeRate handles fora.admin.v1.AdminService/SetTenantFeeRate.
func (h *AdminHandler) SetTenantFeeRate(
	ctx context.Context,
	req *connect.Request[foraadminv1.SetTenantFeeRateRequest],
) (*connect.Response[foraadminv1.SetTenantFeeRateResponse], error) {
	rate := req.Msg.GetRate()
	// feeRateBps is validated to [0, 10000) by the protovalidate interceptor
	// before this handler runs; under full replace it is exactly what persists,
	// so it is the value echoed back (no lossy re-narrowing of the service int).
	feeRateBps := rate.GetFeeRateBps()
	res, err := h.svc.SetTenantFeeRate(ctx, adminCaller(ctx, req.Peer().Addr), service.FeeRateValues{
		TenantID:   rate.GetTenantId(),
		FeeRateBps: int(feeRateBps),
		Notes:      rate.Notes,
	})
	if err != nil {
		return nil, exchange.ToConnect(err)
	}
	return connect.NewResponse(&foraadminv1.SetTenantFeeRateResponse{
		Ver: helpers.ProtocolVersion,
		Rate: &foraadminv1.TenantFeeRate{
			TenantId:   res.TenantID,
			FeeRateBps: feeRateBps,
			Notes:      res.Notes,
		},
	}), nil
}

// SetReportingPolicy handles fora.admin.v1.AdminService/SetReportingPolicy.
func (h *AdminHandler) SetReportingPolicy(
	ctx context.Context,
	req *connect.Request[foraadminv1.SetReportingPolicyRequest],
) (*connect.Response[foraadminv1.SetReportingPolicyResponse], error) {
	policy := req.Msg.GetPolicy()
	res, err := h.svc.SetReportingPolicy(ctx, adminCaller(ctx, req.Peer().Addr), service.ReportingPolicyValues{
		TenantID:          policy.GetTenantId(),
		RequiredFields:    policy.GetRequiredFields(),
		QuantityTolerance: policy.QuantityTolerance,
		WindowSeconds:     policy.WindowSeconds,
	})
	if err != nil {
		return nil, exchange.ToConnect(err)
	}
	return connect.NewResponse(&foraadminv1.SetReportingPolicyResponse{
		Ver: helpers.ProtocolVersion,
		Policy: &foraadminv1.ReportingPolicy{
			TenantId:          res.TenantID,
			RequiredFields:    res.RequiredFields,
			QuantityTolerance: res.QuantityTolerance,
			WindowSeconds:     res.WindowSeconds,
		},
	}), nil
}

// adminCaller builds the audit attribution from the transport: the Connect peer
// address and the request-scoped correlation id. Actor is left nil — v1 has no
// per-operator identity, the network allowlist is the only gate (ADR-022).
func adminCaller(ctx context.Context, peerAddr string) service.AdminCaller {
	return service.AdminCaller{
		SourceAddr: peerAddr,
		RequestID:  reqctx.RequestID(ctx),
	}
}
