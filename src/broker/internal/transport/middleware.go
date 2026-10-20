package transport

import (
	"log/slog"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
)

// maxAgentBodyBytes caps the request body every agent-facing POST will buffer
// before processing. The broker's agent endpoints carry small protobuf-JSON
// payloads (a DiscoveryRequest or a TransactionRequest of a few offers is a few
// kilobytes), so a 64 KiB ceiling does not truncate a realistic request. The cap
// is a DoS guard: a signature check needs the whole body first, so without it
// an unauthenticated caller could make the Broker buffer an arbitrarily large
// body. The BrokerService mount passes it to the verify seam
// (BrokerMountOptions).
const maxAgentBodyBytes int64 = 64 * 1024

// WrapPublicSurface assembles the Broker's outermost HTTP middleware — the
// shared runhttp.WrapPublicSurface stack (request-id → URL normalization →
// opt-in proxy trust) around the Broker's mux, which needs no extra inner
// layer. The stack fixes the @target-uri before every verifying surface inside
// the mux (the BrokerService connectserver seam) sees the request; layer
// order and rationale are documented
// on runhttp.WrapPublicSurface. Shared by cmd/server and the integration
// harness so both exercise identical wiring.
func WrapPublicSurface(
	logger *slog.Logger,
	mux http.Handler,
	opts runhttp.PublicSurfaceOptions,
) http.Handler {
	return runhttp.WrapPublicSurface(logger, mux, opts)
}

// LogHTTPSigReject is the broker's connectserver.WithOnReject observer — the
// shared reqctx.NewRejectLogger body keyed on the "broker" audit namespace. See
// reqctx.NewRejectLogger for the request-id correlation + outcome-classification
// contract.
var LogHTTPSigReject = reqctx.NewRejectLogger("broker")
