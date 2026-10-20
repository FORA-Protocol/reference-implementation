package transport

import (
	"math/big"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forareason"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/money"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// The publisher reads put the service's values on the wire through the
// mappings below, as the registration routes do with wireReason. A value with
// no mapping is a server fault: the service produced something the console
// has no word for, and sending it on would leave the console guessing.

func wireGrouping(g service.Grouping) (string, error) {
	switch g {
	case service.GroupDay:
		return consoleapi.GroupingDay, nil
	case service.GroupWeek:
		return consoleapi.GroupingWeek, nil
	case service.GroupMonth:
		return consoleapi.GroupingMonth, nil
	default:
		return "", unmapped("grouping", string(g))
	}
}

// reportingStatusFromWire is the inverse of wireReportingStatus, for a status a
// request names. Empty is no status. A value that is not a consoleapi status is
// the caller's mistake, refused with every status that is.
func reportingStatusFromWire(s string) (service.ReportingStatus, error) {
	switch s {
	case "":
		return "", nil
	case consoleapi.ReportingNotRequired:
		return service.ReportingNotRequired, nil
	case consoleapi.ReportingPending:
		return service.ReportingPending, nil
	case consoleapi.ReportingOverdue:
		return service.ReportingOverdue, nil
	case consoleapi.ReportingReceived:
		return service.ReportingReceived, nil
	default:
		return "", service.UnknownReportingStatus(s)
	}
}

func wireReportingStatus(s service.ReportingStatus) (string, error) {
	switch s {
	case service.ReportingNotRequired:
		return consoleapi.ReportingNotRequired, nil
	case service.ReportingPending:
		return consoleapi.ReportingPending, nil
	case service.ReportingOverdue:
		return consoleapi.ReportingOverdue, nil
	case service.ReportingReceived:
		return consoleapi.ReportingReceived, nil
	default:
		return "", unmapped("reporting status", string(s))
	}
}

// wireAbsence maps an absence reason. AbsenceNone is the empty string, which
// the response leaves out.
func wireAbsence(a service.AbsenceReason) (string, error) {
	switch a {
	case service.AbsenceNone:
		return "", nil
	case service.AbsenceNoTransactions:
		return consoleapi.AbsenceNoTransactions, nil
	case service.AbsenceUnsupportedCurrency:
		return consoleapi.AbsenceUnsupportedCurrency, nil
	case service.AbsenceInternalError:
		return consoleapi.AbsenceInternalError, nil
	case service.AbsenceMoneyNotRequested:
		return consoleapi.AbsenceMoneyNotRequested, nil
	default:
		return "", unmapped("absence reason", string(a))
	}
}

// wireDenials renders the denial counts under the protocol's own reason names,
// the tokens an agent receives. The list is never nil, so the console reads []
// rather than null when there were none. A reason with no declared name,
// UNSPECIFIED included, has no wire value: forareason.EnumName decides that
// here as it does for the journal.
func wireDenials(counts []service.DenialCount) ([]consoleapi.DenialCount, error) {
	out := make([]consoleapi.DenialCount, 0, len(counts))
	for _, c := range counts {
		name := forareason.EnumName(c.Reason)
		if name == "" {
			return nil, unmapped("denial reason", c.Reason.String())
		}
		out = append(out, consoleapi.DenialCount{Reason: name, Count: c.Count})
	}
	return out, nil
}

func unmapped(what, value string) error {
	return exchange.Newf(exchange.KindInternal, "%s %q has no wire value", what, value)
}

// wireFigures renders minor units as the protocol's canonical money strings,
// the form every other amount the Exchange sends takes.
func wireFigures(f service.RevenueFigures) (consoleapi.RevenueFigures, error) {
	amounts := []*big.Int{f.Gross, f.PlatformFee, f.Net, f.Refunded, f.RefundedPlatformFee, f.NetPayable}
	rendered := make([]string, 0, len(amounts))
	for _, a := range amounts {
		s, err := money.WireString(a)
		if err != nil {
			return consoleapi.RevenueFigures{}, exchange.Wrap(exchange.KindInternal, err, "render revenue amount")
		}
		rendered = append(rendered, s)
	}
	return consoleapi.RevenueFigures{
		GrossRevenue:        rendered[0],
		PlatformFee:         rendered[1],
		NetRevenue:          rendered[2],
		RefundedAmount:      rendered[3],
		RefundedPlatformFee: rendered[4],
		NetPayable:          rendered[5],
		TransactionCount:    f.TransactionCount,
		RefundCount:         f.RefundCount,
	}, nil
}

// wireUnitCost renders a stored per-unit price as the protocol's canonical
// money string.
func wireUnitCost(stored string) (string, error) {
	s, err := helpers.CanonicalizeMoney(stored)
	if err != nil {
		return "", exchange.Wrap(exchange.KindInternal, err, "render unit cost")
	}
	return s, nil
}
