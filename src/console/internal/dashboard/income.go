package dashboard

import (
	"context"
	"encoding/csv"
	"io"
	"math/big"
	"net/url"
	"slices"
	"strconv"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// The income report: what the ledger settled to the publisher on each day of a
// period, beside how many transactions the day logged, and the period's total.
// It reads the same totals the overview does, split by day, and adds nothing
// up itself: the Total row is the Exchange's own total, not a sum of the rows.

// IncomeQuery is what an income page's address asks for.
type IncomeQuery struct {
	Period Period
	// ResourceID narrows the report to one of the publisher's resources, and
	// Domain to one of their sites. Empty reports every one.
	ResourceID string
	Domain     string
	// AllDays shows every day of the period. By default only the days that
	// earned something are shown.
	AllDays bool
}

// values is the query as an address carries it.
func (q IncomeQuery) values() url.Values {
	v := url.Values{}
	q.Period.query(v)
	if q.ResourceID != "" {
		v.Set("resource", q.ResourceID)
	}
	if q.Domain != "" {
		v.Set("domain", q.Domain)
	}
	if q.AllDays {
		v.Set("days", daysAll)
	}
	return v
}

// The values of the days parameter. The form sends daysAll from a hidden field
// and daysProfitable from the checkbox after it, so a ticked box sends both
// and an unticked one only daysAll: a checkbox left unticked sends nothing at
// all, which could not otherwise be told apart from a page opened with no
// choice made, where only the profitable days are the default.
const (
	daysAll        = "all"
	daysProfitable = "profitable"
)

// ParseAllDays reads the days parameter's values: every day is shown only when
// the address asks for it and the profitable-days box was not ticked.
func ParseAllDays(values []string) bool {
	return len(values) > 0 && !slices.Contains(values, daysProfitable)
}

// IncomeReport is one period's income, a row per day and the total.
type IncomeReport struct {
	Period     Period
	ResourceID string
	// Domain is the site the report is narrowed to, empty for all of them,
	// and Domains are the ones the filter offers.
	Domain  string
	Domains []string
	// AllDays is set when every day is shown; otherwise the days that earned
	// nothing are left out of the table and the file.
	AllDays bool
	Days    []Row
	// Figures are the period's totals. Without amounts the report is not
	// exported, since a file of zeros would be added up as though it were
	// true.
	Figures
	// CSV is the address of this same report as a file.
	CSV string
	// Unfiltered is the report over the same period for every resource, for
	// the link that clears the resource filter.
	Unfiltered string
}

// Income reads the publisher's income in the period, one row per day.
func (s *Service) Income(
	ctx context.Context, actor exchangeclient.Actor, nav Nav, q IncomeQuery,
) (IncomeReport, error) {
	totals, err := s.totals(ctx, actor, nav, totalsQuery{
		Period: q.Period, Grouping: consoleapi.GroupingDay, ResourceID: q.ResourceID, Domain: q.Domain,
	})
	if err != nil {
		return IncomeReport{}, err
	}
	figures := figuresOf(totals)
	days := make([]Row, 0, len(totals.Buckets))
	for _, b := range totals.Buckets {
		// Without amounts there is no telling which days earned, so every
		// day is shown rather than none.
		if !q.AllDays && figures.HasAmounts && !earned(b.NetRevenue) {
			continue
		}
		days = append(days, rowOf(b.Start.UTC().Format(dateLayout), b.RevenueFigures))
	}
	return IncomeReport{
		Period:     q.Period,
		ResourceID: q.ResourceID,
		Domain:     q.Domain,
		Domains:    nav.Domains,
		AllDays:    q.AllDays,
		Days:       days,
		Figures:    figures,
		CSV:        withQuery(nav.IncomeCSV(), q.values()),
		Unfiltered: periodLink(nav.Income(), q.Period),
	}, nil
}

// earned reports whether a day's net is above zero. The amount is only
// compared, never added: the Exchange's figures are the only arithmetic. One
// that does not read as a number is kept on the page rather than hidden.
func earned(net string) bool {
	amount, ok := new(big.Rat).SetString(net)
	return !ok || amount.Sign() > 0
}

// FileName is the name the report downloads under.
func (r IncomeReport) FileName() string {
	return "income-" + r.Period.FromValue() + "-to-" + r.Period.ToValue() + ".csv"
}

// WriteCSV writes the report as the page shows it: a header, a line per day,
// and the total as the last line, each with the currency.
func (r IncomeReport) WriteCSV(w io.Writer) error {
	out := csv.NewWriter(w)
	if err := out.Write([]string{"date", "transactions", "gross", "exchange_fee", "net", "currency"}); err != nil {
		return err
	}
	for _, row := range slices.Concat(r.Days, []Row{r.Total}) {
		line := []string{row.Date, strconv.FormatInt(row.Count, 10), row.Gross, row.Fee, row.Net, r.Currency}
		if err := out.Write(line); err != nil {
			return err
		}
	}
	out.Flush()
	return out.Error()
}
