package purchase

import (
	"context"
	"log/slog"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/logging"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
)

// auditMessage is the structured-log message every purchase outcome is recorded
// under, broker-namespaced like the Exchange's "exchange.<op>" convention.
const auditMessage = "broker.execute_transaction"

// outcome is the value of a purchase audit record's "outcome" attribute. Every
// value the purchase service records is declared here, and the Broker's runbook
// lists the same set. Every refusal begins with REJECTED_, so one filter on that
// prefix catches all of them.
type outcome string

const (
	// outcomeValidated: every item passed admission and the sub-requests were sent.
	outcomeValidated outcome = "VALIDATED"
	// outcomeRejectedAuthz: requester.domain is not the host of the directory
	// the request was signed from.
	outcomeRejectedAuthz outcome = "REJECTED_AUTHZ"
	// outcomeRejectedInvalid: the request passed the wire checks but cannot be
	// executed as sent, for example an item with no agent acceptance.
	outcomeRejectedInvalid outcome = "REJECTED_INVALID"
	// outcomeRejectedExchange: an item named an Exchange the Broker will not
	// send a purchase to.
	outcomeRejectedExchange outcome = "REJECTED_EXCHANGE"
	// outcomeRejectedInternal: the Broker's own fault, such as a registry read
	// that failed.
	outcomeRejectedInternal outcome = "REJECTED_INTERNAL"
	// outcomeUpstreamRefused: an Exchange refused its sub-request, or could not
	// be reached.
	outcomeUpstreamRefused outcome = "UPSTREAM_REFUSED"
	// outcomeUpstreamUnattributable: an Exchange answered, but not item by item.
	outcomeUpstreamUnattributable outcome = "UPSTREAM_UNATTRIBUTABLE"
	// outcomeTotalsOmitted: the charged items' costs could not be totalled, so
	// the answer carries no totals.
	outcomeTotalsOmitted outcome = "TOTALS_OMITTED"
)

// level is the one rule for the level of a purchase audit record. A success is
// INFO. A refusal of the caller is WARN and never moves: a caller must not be
// able to take its refused attempt out of an operator's view by hanging up. A
// fault is ERROR on the Broker's own side and WARN at an Exchange, unless its
// cause is the request's own context ending: the agent left and nothing failed
// on either side, so the record is DEBUG.
func (o outcome) level(ctx context.Context, err error) slog.Level {
	switch o {
	case outcomeValidated:
		return slog.LevelInfo
	case outcomeRejectedInternal:
		return logging.LevelFor(ctx, err, slog.LevelError)
	case outcomeUpstreamRefused:
		return logging.LevelFor(ctx, err, slog.LevelWarn)
	default:
		return slog.LevelWarn
	}
}

// audit writes one purchase outcome to the request-scoped logger, at the level
// the outcome's rule gives. exchange is empty when the outcome concerns the
// whole request.
func audit(ctx context.Context, o outcome, exchange string, err error, extra ...any) {
	attrs := []any{"outcome", string(o)}
	if exchange != "" {
		attrs = append(attrs, "exchange", exchange)
	}
	if err != nil {
		attrs = append(attrs, "err", err.Error())
	}
	attrs = append(attrs, extra...)
	reqctx.FromContext(ctx).Log(ctx, o.level(ctx, err), auditMessage, attrs...)
}
