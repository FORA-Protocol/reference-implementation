package transport

import (
	"log/slog"
	"net/http"

	"github.com/FORA-Protocol/protocol/gen/go/fora/admin/v1/foraadminv1connect"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// WrapAdminSurface assembles the admin control-plane HTTP surface shared by
// cmd/server (production) and the integration harness so both exercise ONE wiring:
// the emit-unpopulated JSON codec + the SDK bidirectional protovalidate interceptor
// (validates requests AND responses, per ADR-022 §2 and ADR-019) + NO request-signing,
// wrapped RequestID (outermost, so a rejected call still correlates) then IP-allowlist.
// The admin plane has no verified signer; the network allowlist is the only gate
// (ADR-022 §4). It is deliberately NOT wrapped by WrapPublicSurface and NOT mounted
// through the connectserver verify seam. An empty allowedCIDRs denies everything
// (fail-closed).
//
// The evidence read is mounted beside the Connect route as a plain JSON GET, so it
// carries neither the codec nor the protovalidate interceptor — it is not a protocol
// RPC. It shares the two middlewares, which is what confines it to this listener.
//
// audience travels with the shared mount options even though no admin message
// carries a recipient today. Passing it is what makes that a fact about the admin
// contract rather than an exemption this surface was granted: the day an admin
// request gains the field, it is already checked.
//
// extra mounts further routes on the same mux, behind the same two middlewares.
// Only the e2e build passes any: its control service (ADR-028 D6) is reachable
// only where the admin plane is, so the allowlist that confines the operator
// setters confines it too. Production passes none.
func WrapAdminSurface(
	logger *slog.Logger,
	svc *service.AdminService,
	evidence *service.EvidenceReadService,
	allowedCIDRs string,
	audience *foraaudience.Interceptor,
	extra ...AdminRoute,
) (http.Handler, error) {
	opts, err := RawValidatedMountOptions(audience)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	path, h := foraadminv1connect.NewAdminServiceHandler(NewAdminHandler(svc), opts...)
	mux.Handle(path, h)
	// The method is part of the pattern, so net/http answers 405 for anything but
	// GET without a check in the handler.
	mux.HandleFunc("GET "+TransactionEvidencePath, transactionEvidenceHandler(evidence))
	for _, route := range extra {
		mux.Handle(route.Pattern, route.Handler)
	}

	allow, err := ParseAllowlist(allowedCIDRs)
	if err != nil {
		return nil, err
	}
	return RequestIDMiddleware(logger, IPAllowlistMiddleware(allow, mux)), nil
}

// AdminRoute is one further route WrapAdminSurface mounts on the admin listener:
// a net/http ServeMux pattern and its handler.
type AdminRoute struct {
	Pattern string
	Handler http.Handler
}
