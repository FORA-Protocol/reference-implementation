package dashboard

import (
	"context"
	"net/url"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// Overview is one period at a glance: how many transactions, what they
// grossed, the Exchange's fee and what is left, and one column per day.
type Overview struct {
	Period Period
	// Figures are the period's totals. Total.Count counts every transaction,
	// free ones included.
	Figures
	Chart Chart
}

// Overview reads the period's totals for the publisher.
func (s *Service) Overview(
	ctx context.Context, actor exchangeclient.Actor, nav Nav, period Period,
) (Overview, error) {
	totals, err := s.totals(ctx, actor, nav, totalsQuery{Period: period, Grouping: consoleapi.GroupingDay})
	if err != nil {
		return Overview{}, err
	}
	return Overview{Period: period, Figures: figuresOf(totals), Chart: newChart(totals.Buckets)}, nil
}

// absenceText says why a totals answer carries no amounts, in a sentence for
// the publisher. It answers every reason consoleapi lists, and an empty one
// with nothing.
func absenceText(reason, currency string) string {
	switch reason {
	case "":
		return ""
	case consoleapi.AbsenceNoTransactions:
		return "No transactions in this period."
	case consoleapi.AbsenceUnsupportedCurrency:
		return "The exchange keeps no ledger in " + currency + ", so there are no amounts to show."
	case consoleapi.AbsenceInternalError:
		return "The amounts could not be read from the ledger just now. The count of transactions is still right."
	case consoleapi.AbsenceMoneyNotRequested:
		return "Only the count of transactions was read for this page."
	default:
		return "The amounts are not available for this period (" + reason + ")."
	}
}

// periodLink is the address of path over period.
func periodLink(path string, period Period) string {
	q := url.Values{}
	period.query(q)
	return withQuery(path, q)
}
