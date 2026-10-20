package dashboard

import (
	"context"
	"net/url"
	"slices"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// TransactionQuery is what a transaction list page's address asks for:
// its filters, and where in the list the page is.
type TransactionQuery struct {
	Period     Period
	ResourceID string
	// Domain narrows the list to one of the publisher's sites.
	Domain string
	// Status narrows the list to one reporting status, one of
	// consoleapi.AllReportingStatuses. Empty lists every status.
	Status   string
	Position Position
}

// values is the query's filters as an address carries them, without the
// page's position.
func (q TransactionQuery) values() url.Values {
	v := url.Values{}
	q.Period.query(v)
	if q.ResourceID != "" {
		v.Set("resource", q.ResourceID)
	}
	if q.Domain != "" {
		v.Set("domain", q.Domain)
	}
	if q.Status != "" {
		v.Set("status", q.Status)
	}
	return v
}

// TransactionList is one page of the publisher's transactions, newest first.
type TransactionList struct {
	Period     Period
	ResourceID string
	Rows       []TransactionRow
	Page       Page
	// Domain is the site the list is narrowed to, empty for all of them, and
	// Domains are the ones the filter offers.
	Domain  string
	Domains []string
	// Status is the reporting status the list is narrowed to, empty for all
	// of them, and Statuses are the ones the filter offers.
	Status   string
	Statuses []Status
	// Unfiltered is the list over the same period without the resource
	// filter, for the link that clears it.
	Unfiltered string
}

// TransactionRow is one transaction as the list shows it.
type TransactionRow struct {
	ID          string
	When        string
	ResourceURI string
	// Title is the page's headline. Empty when the resource carries none, and
	// the row then falls back to the address.
	Title string
	Agent string
	// UnitPrice is the per-unit price the offer quoted.
	UnitPrice string
	Status    Status
	Link      string
}

// Transactions reads one page of the publisher's transactions. Unless the page
// is narrowed by report status, it also reads the count of the period's
// transactions under the same resource and domain filters, which is the
// "of N" beside the row numbers.
func (s *Service) Transactions(
	ctx context.Context, actor exchangeclient.Actor, nav Nav, q TransactionQuery,
) (TransactionList, error) {
	if q.Status != "" && !slices.Contains(consoleapi.AllReportingStatuses(), q.Status) {
		return TransactionList{}, invalid("That is not a report status the list can show.")
	}
	of := int64(-1)
	// The count narrows by resource and by domain but not by status, so a
	// list narrowed by status claims no total rather than showing the count
	// of every transaction beside a subset of the rows.
	if q.Status == "" {
		// Counts only: the ledger's money is not shown here. Monthly buckets,
		// because only the total is used and a year of them is a dozen
		// buckets rather than a year of days.
		totals, err := s.totals(ctx, actor, nav, totalsQuery{
			Period: q.Period, Grouping: consoleapi.GroupingMonth, ResourceID: q.ResourceID, Domain: q.Domain,
			CountOnly: true,
		})
		if err != nil {
			return TransactionList{}, err
		}
		of = totals.Total.TransactionCount
	}
	got, err := s.reader.ListTransactions(ctx, exchangeclient.ListTransactionsRequest{
		Actor: actor, PublisherID: nav.PublisherID, Period: q.Period.Window(),
		ResourceID: q.ResourceID, Domain: q.Domain, ReportingStatus: q.Status,
		PageSize: s.pageSize, Cursor: q.Position.Cursor,
	})
	if err != nil {
		return TransactionList{}, failure(err)
	}
	rows := make([]TransactionRow, 0, len(got.Transactions))
	for _, t := range got.Transactions {
		rows = append(rows, transactionRow(nav, t))
	}
	return TransactionList{
		Period:     q.Period,
		ResourceID: q.ResourceID,
		Domain:     q.Domain,
		Domains:    nav.Domains,
		Status:     q.Status,
		Statuses:   reportingStatuses(),
		Rows:       rows,
		Page: paging{
			path: nav.Transactions(), filters: q.values(), pos: q.Position,
			rows: len(rows), size: s.pageSize, of: of, next: got.NextCursor, prev: got.PrevCursor,
		}.page(),
		Unfiltered: periodLink(nav.Transactions(), q.Period),
	}, nil
}

func transactionRow(nav Nav, t exchangeclient.TransactionRow) TransactionRow {
	return TransactionRow{
		ID:          t.TransactionID,
		When:        timeText(t.CreatedAt),
		ResourceURI: t.ResourceURI,
		Title:       t.ResourceTitle,
		Agent:       t.AgentID,
		UnitPrice:   t.UnitCost + " " + t.Currency,
		Status:      statusOf(t.ReportingStatus),
		Link:        nav.Transaction(t.TransactionID),
	}
}

// Status is a transaction's reporting status as the Exchange returned it, with
// the words and the badge tone the page shows it in.
type Status struct {
	Value string
	Label string
	// Tone is the badge's Bootstrap colour: success, warning, danger or
	// secondary.
	Tone string
}

// statusOf labels a reporting status. It answers every status consoleapi
// lists; one it does not know is shown as the Exchange wrote it.
func statusOf(value string) Status {
	switch value {
	case consoleapi.ReportingReceived:
		return Status{Value: value, Label: "Received", Tone: "success"}
	case consoleapi.ReportingPending:
		return Status{Value: value, Label: "Pending", Tone: "warning"}
	case consoleapi.ReportingOverdue:
		return Status{Value: value, Label: "Overdue", Tone: "danger"}
	case consoleapi.ReportingNotRequired:
		return Status{Value: value, Label: "Not required", Tone: "secondary"}
	default:
		return Status{Value: value, Label: value, Tone: "secondary"}
	}
}

// reportingStatuses is every status the list can be narrowed to, labelled as
// its rows show them.
func reportingStatuses() []Status {
	all := consoleapi.AllReportingStatuses()
	out := make([]Status, 0, len(all))
	for _, value := range all {
		out = append(out, statusOf(value))
	}
	return out
}

// timeText writes an instant in UTC, labelled so.
func timeText(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04 UTC")
}
