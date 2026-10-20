package transport

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/broker"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/purchase"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/resolve"
)

// brokerServiceDomain stamps the ErrorDetail.Domain on every Broker fault, on
// Resolve and on ExecuteTransaction, so a client can attribute the failure to
// this service.
const brokerServiceDomain = "fora.v1.BrokerService"

// BrokerConnectHandler adapts the Broker's two business cores to the generated
// foraconnect.BrokerServiceHandler interface: resolve.Service behind Resolve and
// purchase.Service behind ExecuteTransaction. It is the SOLE agent transport
// over both (ADR-019 — the bespoke resolve and execute routes were removed): it
// decodes via Connect, reads the verified caller the verify seam left on the
// context, calls the service, and renders the answer. Domain faults map to
// connect.Code via broker.ToConnect and carry a typed ErrorDetail.
type BrokerConnectHandler struct {
	foraconnect.UnimplementedBrokerServiceHandler
	h        *resolve.Service
	purchase *purchase.Service
}

// NewBrokerConnectHandler wires the Connect adapter over the shared resolver
// and the purchase service.
func NewBrokerConnectHandler(h *resolve.Service, p *purchase.Service) *BrokerConnectHandler {
	return &BrokerConnectHandler{h: h, purchase: p}
}

// ExecuteTransaction handles fora.v1.BrokerService/ExecuteTransaction: one
// purchase across any number of Exchanges. The verify seam has already
// verified the agent's request signature (the mount sends every
// ExecuteTransaction through it, signed or not), so the handler reads the
// verified directory and hands the request to the purchase service. The
// Broker's own refusals are non-OK with a typed ErrorDetail (executeFaultError);
// once anything was sent, every Exchange's answer rides in the OK body.
func (b *BrokerConnectHandler) ExecuteTransaction(
	ctx context.Context,
	creq *connect.Request[forav1.TransactionRequest],
) (*connect.Response[forav1.BrokerTransactionResponse], error) {
	v := helpers.FromContext(ctx)
	if v == nil || v.SignatureAgent == "" {
		// Unreachable through the mount, which verifies every ExecuteTransaction
		// before the handler runs; refused rather than bought for nobody.
		return nil, executeFaultError(broker.Newf(broker.KindUnauthenticated,
			"no verified caller in request context"))
	}
	resp, err := b.purchase.Execute(ctx, v.SignatureAgent, creq.Msg)
	if err != nil {
		return nil, executeFaultError(err)
	}
	return connect.NewResponse(resp), nil
}

// executeFaultError maps a purchase refusal to its Connect error. A requester
// that is not the signing directory carries request_auth_failure
// SIGNATURE_INVALID, the reason the verify seam gives any signature that does
// not verify, so an agent branches on one reason whichever check refused it.
// Every other refusal carries the generic envelope resolveFaultError builds,
// including metadata["exchange"] on an Exchange the Broker will not use.
func executeFaultError(err error) error {
	var be *broker.Error
	if !errors.As(err, &be) || be.Kind != broker.KindSignatureInvalid {
		return resolveFaultError(err)
	}
	d := helpers.RequestAuthFailureDetail(brokerServiceDomain, err.Error(),
		forav1.RequestAuthFailureReason_REQUEST_AUTH_FAILURE_REASON_SIGNATURE_INVALID)
	d.Metadata = be.Metadata
	cerr := broker.ToConnect(err)
	var ce *connect.Error
	if !errors.As(cerr, &ce) {
		return cerr
	}
	return connectserver.AttachDetail(ce, d)
}

// Resolve handles fora.v1.BrokerService/Resolve. A licensed result returns OK
// with the delivery fields populated on DiscoveryResponse (retrieval_endpoint
// present); a request that ran but yielded nothing licensable returns OK with
// the typed absence_reason set (ADR-019 §2 — "no result is a successful
// answer"). Malformed requests, auth failures, and internal faults are non-OK
// transport errors carrying a typed ErrorDetail (resolveFaultError).
func (b *BrokerConnectHandler) Resolve(
	ctx context.Context,
	creq *connect.Request[forav1.DiscoveryRequest],
) (*connect.Response[forav1.DiscoveryResponse], error) {
	req := foraRequestToInput(creq.Msg)
	if verr := validateResolveRequest(&req, creq.Msg.GetSearchFilters()); verr != nil {
		return nil, resolveFaultError(verr)
	}
	agentID, aerr := authenticatedAgentID(ctx)
	if aerr != nil {
		return nil, resolveFaultError(aerr)
	}
	// The core keys its budget counter and selection_log row on AgentID, so it is
	// the verified caller, never the requester.id label the agent wrote.
	req.AgentID = agentID
	resp, rerr := b.h.Resolve(ctx, reqctx.RequestID(ctx), req)
	if rerr != nil {
		return nil, resolveFaultError(rerr)
	}
	return connect.NewResponse(toDiscoveryResponse(resp)), nil
}

// resolveFaultError maps a Broker fault, on Resolve or on ExecuteTransaction,
// to its Connect transport error and attaches a typed proto ErrorDetail. These
// faults are the generic transport class (INVALID_ARGUMENT / UNAUTHENTICATED /
// PERMISSION_DENIED / FAILED_PRECONDITION / UNAVAILABLE / INTERNAL), so the
// detail carries Message + Domain with NO reason
// oneof — per the proto ErrorDetail comment, the typed reason is absent for
// generic transport-class failures that carry no domain-specific reason. Mirrors
// the Exchange's executeTxError (exchange_error_detail.go), the canonical
// ADR-019 pattern: the machine-readable fault travels as a typed detail, never
// as a server-side string the client must parse. Message is non-authoritative
// developer text.
func resolveFaultError(err error) error {
	cerr := broker.ToConnect(err)
	var ce *connect.Error
	if !errors.As(cerr, &ce) {
		return cerr
	}
	return connectserver.AttachDetail(ce, brokerDetail(err))
}
