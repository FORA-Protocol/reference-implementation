package consoleapi

import (
	"encoding/json"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/evidenceview"
)

// The publisher reads. Each one names the publisher it reads, and the Exchange
// answers not_found unless the acting person holds a grant for it, so no read
// can return another publisher's rows.
const (
	PathRevenueTotals     = PathPrefix + "revenue/totals"
	PathListTransactions  = PathPrefix + "transactions/list"
	PathTransactionDetail = PathPrefix + "transactions/detail"
	PathListCatalog       = PathPrefix + "catalog/list"
	PathCatalogDetail     = PathPrefix + "catalog/detail"
	PathPublisherTerms    = PathPrefix + "publishers/terms"
)

// The limits of the publisher reads. The Exchange enforces them and the console
// builds its pages to them, both from these constants.
const (
	// DefaultPageSize is the page a list request gets when it names no page
	// size.
	DefaultPageSize = 50
	// MaxPageSize is the largest page a list request may ask for. A larger
	// one is refused with invalid_request rather than silently capped, so a
	// caller never mistakes a capped page for the whole list; the refusal's
	// message names the maximum.
	MaxPageSize = 200
	// MaxBuckets is the most buckets a totals request may split its period
	// into: a year of days, with the leap day. A request that needs more is
	// refused with invalid_request.
	MaxBuckets = 366
)

// The groupings a totals request can split its period into, all in UTC. A week
// is an ISO week, starting on Monday. A request that would split its period
// into more buckets than the Exchange allows (a year of days) is refused with
// invalid_request.
const (
	GroupingDay   = "day"
	GroupingWeek  = "week"
	GroupingMonth = "month"
)

// The reporting status of a transaction, derived when it is read.
const (
	// ReportingNotRequired: the transaction minted no reporting obligation.
	ReportingNotRequired = "not_required"
	// ReportingPending: the agent still owes a usage report, and its deadline
	// has not passed.
	ReportingPending = "pending"
	// ReportingOverdue: the agent still owes a usage report after its deadline.
	ReportingOverdue = "overdue"
	// ReportingReceived: the agent's usage report was accepted.
	ReportingReceived = "received"
)

// AllReportingStatuses lists every reporting status, in the order a publisher
// is offered them.
func AllReportingStatuses() []string {
	return []string{ReportingPending, ReportingOverdue, ReportingReceived, ReportingNotRequired}
}

// Where a publisher's fee rate comes from.
const (
	// FeeRateSourcePublisher: the operator set a rate for this publisher.
	FeeRateSourcePublisher = "publisher"
	// FeeRateSourceTenant: the seat's default rate applies.
	FeeRateSourceTenant = "tenant"
)

// Period is a half-open UTC window: Start is inside it, End is not. Both bounds
// are required, so a read never means "everything since the beginning", and
// both must lie between the Unix epoch and the year 2262, the range the
// ledger's clock can represent.
type Period struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// RevenueTotalsRequest asks for one publisher's revenue in one currency over a
// period, split into buckets of Grouping. An empty Currency means the
// deployment's base currency: the first currency it keeps a ledger in, the one
// its well-known document advertises as base_currency.
//
// ResourceID and Domain narrow the money and the transaction counts to one of
// the publisher's resources or domains; both together narrow to the resource
// only if it is on that domain. A resource or domain that is not the
// publisher's counts nothing, the same answer as one that sold nothing. A
// domain is compared in the normalized form a domain is registered in, so its
// case does not matter; one no publisher could register is refused with
// invalid_request.
//
// CountOnly asks for the transaction counts alone. The Exchange then does not
// read the ledger, and the answer's money is zero with AbsenceMoneyNotRequested
// (or AbsenceNoTransactions when nothing sold).
type RevenueTotalsRequest struct {
	Actor       Actor  `json:"actor"`
	PublisherID string `json:"publisher_id"`
	Period      Period `json:"period"`
	Grouping    string `json:"grouping"`
	Currency    string `json:"currency"`
	ResourceID  string `json:"resource_id,omitempty"`
	Domain      string `json:"domain,omitempty"`
	CountOnly   bool   `json:"count_only,omitzero"`
}

// RevenueFigures are the money a period settled. Amounts are the protocol's
// canonical money strings in the currency's major unit ("0.9", "1", never
// "0.90000000"), so no amount is rounded through a float on either side.
// TransactionCount counts transactions, including free ones, which add a count
// and no money.
type RevenueFigures struct {
	GrossRevenue        string `json:"gross_revenue"`
	PlatformFee         string `json:"platform_fee"`
	NetRevenue          string `json:"net_revenue"`
	RefundedAmount      string `json:"refunded_amount"`
	RefundedPlatformFee string `json:"refunded_platform_fee"`
	NetPayable          string `json:"net_payable"`
	TransactionCount    int64  `json:"transaction_count"`
	RefundCount         int64  `json:"refund_count"`
}

// RevenueBucket is one grouping interval, clipped to the requested period.
type RevenueBucket struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	RevenueFigures
}

// RevenueTotals answers a totals request. AbsenceReason is set whenever the
// figures are empty or could not be read, so a reader never has to guess
// whether zero means "nothing sold" or "nothing could be counted".
//
// Denials counts the items of the publisher's resources the Exchange refused
// in the whole period, per reason, ordered by the protocol's enum number. It is
// never null: [] means none were recorded. It is not split into buckets, and it
// is not scoped to Currency, because a refused item moves no money. It is a
// lower bound, because the Exchange records a denial best-effort. It is the
// whole publisher's: ResourceID and Domain do not narrow it. It is [] when
// AbsenceReason is UNSUPPORTED_CURRENCY, which is answered before anything is
// read, and when the request is CountOnly, which reads the counts alone. A
// denial belongs to the publisher that owned the resource when it was refused;
// the transaction figures follow the catalog's current owner.
type RevenueTotals struct {
	Period        Period          `json:"period"`
	Grouping      string          `json:"grouping"`
	Currency      string          `json:"currency"`
	Buckets       []RevenueBucket `json:"buckets"`
	Total         RevenueFigures  `json:"total"`
	Denials       []DenialCount   `json:"denials"`
	AbsenceReason string          `json:"absence_reason,omitempty"`
}

// DenialCount is how many of the publisher's items were refused for one
// reason. Reason is the protocol's DenialReason name, such as
// DENIAL_REASON_OFFER_EXPIRED: the token the agent itself received in the
// refused item's denial_reason. This package defines no constants for it,
// because the vocabulary is the protocol's and a second copy here could drift
// from it.
type DenialCount struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

// The reasons a totals answer carries no figures.
const (
	// AbsenceNoTransactions: the publisher sold nothing in the period.
	AbsenceNoTransactions = "NO_TRANSACTIONS_IN_PERIOD"
	// AbsenceUnsupportedCurrency: this Exchange keeps no ledger in the
	// requested currency.
	AbsenceUnsupportedCurrency = "UNSUPPORTED_CURRENCY"
	// AbsenceInternalError: the ledger could not be read. The transaction
	// count is still reported; the money is not.
	AbsenceInternalError = "INTERNAL_ERROR"
	// AbsenceMoneyNotRequested: the request asked for the counts only, so the
	// ledger was not read.
	AbsenceMoneyNotRequested = "MONEY_NOT_REQUESTED"
)

// AllAbsenceReasons lists every absence reason, so a consumer can prove it has
// an answer for each one.
func AllAbsenceReasons() []string {
	return []string{
		AbsenceNoTransactions, AbsenceUnsupportedCurrency, AbsenceInternalError, AbsenceMoneyNotRequested,
	}
}

// ListTransactionsRequest pages through one publisher's transactions in a
// period, newest first. ResourceID narrows the list to one resource. Cursor is
// the NextCursor or PrevCursor of a page, empty for the first page.
type ListTransactionsRequest struct {
	Actor       Actor  `json:"actor"`
	PublisherID string `json:"publisher_id"`
	Period      Period `json:"period"`
	ResourceID  string `json:"resource_id,omitempty"`
	// Domain narrows the list to one of the publisher's domains. Empty
	// reads every domain.
	Domain string `json:"domain,omitempty"`
	// ReportingStatus narrows the list to the transactions with this
	// reporting status, one of the Reporting* constants, decided at the moment
	// of the read like the status each row carries. Empty reads every status;
	// any other value is refused with invalid_request.
	ReportingStatus string `json:"reporting_status,omitempty"`
	PageSize        int    `json:"page_size,omitzero"`
	Cursor          string `json:"cursor,omitempty"`
}

// TransactionRow is one transaction as the list shows it. UnitCost is the
// per-unit price the offer quoted, as a canonical money string.
// ReportingStatus is one of the Reporting* constants.
type TransactionRow struct {
	TransactionID string    `json:"transaction_id"`
	CreatedAt     time.Time `json:"created_at"`
	ResourceID    string    `json:"resource_id"`
	ResourceURI   string    `json:"resource_uri"`
	// ResourceTitle is the headline of the page that was sold, empty when the
	// resource was listed without one.
	ResourceTitle   string `json:"resource_title,omitempty"`
	AgentID         string `json:"agent_id"`
	UnitCost        string `json:"unit_cost"`
	Currency        string `json:"currency"`
	ReportingStatus string `json:"reporting_status"`
}

// TransactionPage is one page of transactions. NextCursor is the cursor of the
// page after it, empty on the last page, and PrevCursor that of the page
// before it, empty on the first. Either goes back as a request's Cursor.
type TransactionPage struct {
	Transactions []TransactionRow `json:"transactions"`
	NextCursor   string           `json:"next_cursor"`
	PrevCursor   string           `json:"prev_cursor,omitempty"`
}

// TransactionDetailRequest reads one transaction of the publisher.
type TransactionDetailRequest struct {
	Actor         Actor  `json:"actor"`
	PublisherID   string `json:"publisher_id"`
	TransactionID string `json:"transaction_id"`
}

// TransactionDetail is the evidence of one transaction, in the same shape the
// operator's evidence read serves: the offer and acceptance bytes with their
// signatures and keys, the hash of the signed URL and never the URL, and the
// reporting obligation. ReportingStatus is the status the list shows for it.
type TransactionDetail struct {
	evidenceview.Response
	ReportingStatus string `json:"reporting_status"`
}

// ListCatalogRequest pages through the publisher's resources, newest first.
type ListCatalogRequest struct {
	Actor       Actor  `json:"actor"`
	PublisherID string `json:"publisher_id"`
	// Domain narrows the page to one of the publisher's domains. Empty
	// reads every domain.
	Domain   string `json:"domain,omitempty"`
	PageSize int    `json:"page_size,omitzero"`
	Cursor   string `json:"cursor,omitempty"`
}

// CatalogResource is one resource the publisher listed. Terms is the license
// term list the resource was pushed with, verbatim in the protocol's JSON form;
// each term carries its own pricing, which is where a resource's price is.
type CatalogResource struct {
	ResourceID string          `json:"resource_id"`
	URI        string          `json:"uri"`
	Title      string          `json:"title,omitempty"`
	Terms      json.RawMessage `json:"terms"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// CatalogPage is one page of resources, with the cursors of the pages beside
// it as TransactionPage has them.
type CatalogPage struct {
	Resources  []CatalogResource `json:"resources"`
	NextCursor string            `json:"next_cursor"`
	PrevCursor string            `json:"prev_cursor,omitempty"`
}

// CatalogDetailRequest reads one of the publisher's resources. A resource the
// publisher does not own answers not_found, the same as one that does not
// exist, so the read tells nobody what another publisher lists.
type CatalogDetailRequest struct {
	Actor       Actor  `json:"actor"`
	PublisherID string `json:"publisher_id"`
	ResourceID  string `json:"resource_id"`
}

// PublisherTermsRequest reads the commercial terms this Exchange applies to the
// publisher.
type PublisherTermsRequest struct {
	Actor       Actor  `json:"actor"`
	PublisherID string `json:"publisher_id"`
}

// PublisherTerms is the fee this Exchange takes from the publisher's sales and
// the reporting duty it places on agents that buy from it. FeeRateSource says
// whether the rate is the publisher's own override or the seat's default.
type PublisherTerms struct {
	FeeRateBps      int             `json:"fee_rate_bps"`
	FeeRateSource   string          `json:"fee_rate_source"`
	ReportingPolicy ReportingPolicy `json:"reporting_policy"`
}

// ReportingPolicy is the effective policy, with the deployment's defaults
// filled in: it is what an agent is held to, not what an operator configured.
type ReportingPolicy struct {
	RequiredFields    []string `json:"required_fields"`
	QuantityTolerance float64  `json:"quantity_tolerance"`
	WindowSeconds     int32    `json:"window_seconds"`
}
