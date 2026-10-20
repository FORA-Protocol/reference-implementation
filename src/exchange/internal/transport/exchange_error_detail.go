package transport

import (
	"errors"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// exchangeServiceDomain is the ErrorInfo-compatible grouping key stamped onto
// ErrorDetail.Domain for every exchange fault (ADR-019). It mirrors the broker's
// brokerServiceDomain so a generic client reading ErrorDetail.domain attributes
// exchange and broker failures the same way.
const exchangeServiceDomain = "fora.v1.ExchangeService"

// catalogServiceDomain is the same grouping key for the catalog surface. It is a
// separate service on the wire, so a client filtering on domain must be able to
// tell a catalog-push fault from an exchange one.
//
// Until this existed the catalog answered every fault through bare
// exchange.ToConnect, carrying no ErrorDetail at all — so ADR-019 §1's "every
// fault attributes via Domain" held for the exchange and the broker but not here,
// and any field metadata a catalog refusal set was computed and dropped.
const catalogServiceDomain = "fora.v1.CatalogService"

// catalogFaultError maps a catalog error to its connect.Error and stamps the
// shared ErrorDetail envelope: Domain, the non-authoritative message, and any
// structured field metadata the domain error carries. It is genericFaultError's
// twin for the catalog surface — the catalog has no machine-readable reason
// oneof, so the generic shape is the whole shape here.
func catalogFaultError(err error) error {
	return faultEnvelope(err, catalogServiceDomain)
}

// txDenialReason returns the canonical DenialReason for a transaction-denial
// error, or ok=false when the failure is not a denial. It delegates to the
// service layer's single source of truth (service.DenialReasonForKind) so the
// single-offer transport detail and the batch per-item denial_reason share one
// vocabulary and cannot drift.
func txDenialReason(err error) (forav1.DenialReason, bool) {
	return service.DenialReasonForKind(err)
}

// exchangeMeta returns the structured field metadata a service error recorded via
// exchange.Error.WithField, or nil when there is none. Per ADR-019 the offending
// field identity that the validator used to embed in the free-text message rides
// ErrorDetail.metadata, machine-readable, never the message. It is the single
// metadata-ride source both generic fault sinks share.
func exchangeMeta(err error) map[string]string {
	var de *exchange.Error
	if errors.As(err, &de) && len(de.Metadata) > 0 {
		return de.Metadata
	}
	return nil
}

// genericFaultError maps a service error to its connect.Error and stamps the
// shared ErrorDetail envelope (Domain + non-authoritative Message + any structured
// field metadata) with no typed reason oneof, via the shared SDK
// connectserver.AttachErrorDetail. It is the fault shape for the
// ExchangeService paths that carry no machine-readable reason — DiscoverResources
// and non-denial ExecuteTransaction — so EVERY ExchangeService fault attributes via
// ErrorDetail.Domain (ADR-019 §1), mirroring the broker stamping fora.v1.BrokerService
// on every fault.
func genericFaultError(err error) error {
	return faultEnvelope(err, exchangeServiceDomain)
}

// faultEnvelope is the ONE place the shared envelope is built. Both surfaces
// stamp the same three things and differ only in the grouping key, so the domain
// is a parameter rather than a reason to write the body twice — a structural
// guard in this package pins that this call appears exactly once.
func faultEnvelope(err error, domain string) error {
	return connectserver.AttachErrorDetail(
		exchange.ToConnect(err), domain, err.Error(), exchangeMeta(err),
	)
}

// executeTxError maps a service error to its connect.Error. A transaction denial
// attaches the typed proto ErrorDetail carrying the canonical DenialReason — per
// ADR-019 the machine-readable reason travels as a typed detail (the same
// mechanism protovalidate uses for its violations), never a server-side string
// the client must parse — alongside Domain + the non-authoritative Message. The
// denial envelope is built by the shared SDK helpers.TransactionDenialDetail
// (which carries NO metadata — the machine-readable denial payload is the typed
// reason; field metadata rides only the generic path) and attached via the shared
// SDK connectserver.AttachDetail; a non-denial failure routes through
// genericFaultError so it still carries Domain.
//
// The denial also NAMES the Exchange that refused. TransactionDenial.exchange is
// the field the proto reserves for it, and helpers.TransactionDenialDetail does
// not take it, so it is set on the returned message. exchangeHost is this
// Exchange's published identity, read from the service so the denial names the
// same Exchange the refused offers carry.
//
// It is called a HOST, not a domain, because the body below hands two unrelated
// values to the same detail one line apart. The protocol defines
// TransactionDenial.exchange as the bare host of the Exchange that produced the
// denial — an address an agent may dial once it has checked the value against
// one it already trusts. exchangeServiceDomain is the ADR-019 fault-attribution
// key "fora.v1.ExchangeService". Naming both "domain" is how an RPC service
// name ends up where a caller expects an address.
//
// ExecuteTransaction has one pipeline: every request, including a single-offer
// one, is handled as a batch, and the batch loop folds each denial kind into an
// in-body TransactionResultItem instead of returning it. The one denial that
// reaches this function is raised BEFORE the loop: the relay gate at admission
// refuses a Broker-relayed request whose tenant does not accept relayed
// purchases, as KindRelayNotAccepted, so the whole request is PERMISSION_DENIED
// with a transaction_denial carrying DENIAL_REASON_RELAY_NOT_ACCEPTED and this
// Exchange's host. The Broker carries that detail to the agent inside the
// UpstreamRefusal on each refused item. Every other error is a non-denial and
// takes the generic path.
func executeTxError(err error, exchangeHost string) error {
	reason, ok := txDenialReason(err)
	if !ok {
		return genericFaultError(err)
	}
	d := helpers.TransactionDenialDetail(exchangeServiceDomain, err.Error(), reason)
	d.GetTransactionDenial().Exchange = &exchangeHost
	return connectserver.AttachDetail(exchange.ToConnect(err), d)
}

// registerError maps a Register service error to its connect.Error. A refused
// registration — a payload that does not conform to the published data_schema, or
// a terms_digest that is not the published one — attaches the typed proto
// ErrorDetail carrying the canonical RegistrationFailureReason, the same
// mechanism executeTxError uses for a denial. A schema refusal also carries the
// per-field list the SDK validator produced; a stale digest carries none, because
// the proto allows field_errors only alongside INVALID_REGISTRATION_DATA.
//
// Every OTHER Register failure carries no reason and must still attribute via
// ErrorDetail.Domain (ADR-019), so it routes through genericFaultError: the
// registration_data bounds check (a malformed request, not a schema failure), an
// unseeded default tenant, a non-agent caller, and every SoR, ledger and internal
// fault.
func registerError(err error) error {
	reason, fields, ok := service.RegistrationFailureForError(err)
	if !ok {
		return genericFaultError(err)
	}
	d := helpers.RegistrationFailureDetail(exchangeServiceDomain, err.Error(), reason, fields...)
	return connectserver.AttachDetail(exchange.ToConnect(err), d)
}

// accountStatusError maps a GetAccountStatus service error to its connect.Error.
// The status read has no machine-readable reason oneof of its own, so it routes
// through the same shared fault shape (genericFaultError) that stamps Domain +
// the non-authoritative Message. Its reasonless sibling is reportUsageError
// below, NOT registerError above: Register now attaches a typed
// RegistrationFailureReason whenever the schema or the terms gate refuses. It
// stays a distinct per-RPC mapper to keep the file's one-mapper-per-RPC
// convention intact rather than sharing a single name across two RPCs.
func accountStatusError(err error) error {
	return genericFaultError(err)
}

// reportUsageError maps a ReportUsage service error to its connect.Error and
// attaches the shared no-reason ErrorDetail envelope. It delegates to
// genericFaultError so the ReportUsage fault shape has the SAME single source of
// truth as every other reasonless ExchangeService fault — the envelope body is
// never duplicated. The named seam is kept because the per-domain
// UsageReportRejection reason oneof is a separate ADR-019-completion step (tracked
// with the catalog CatalogRejectionReason adoption): when that lands, only this
// function grows the typed reason, mirroring executeTxError.
func reportUsageError(err error) error {
	return genericFaultError(err)
}
