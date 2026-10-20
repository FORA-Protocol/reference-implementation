package exchangeclient

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
)

// The publisher reads' shapes, aliased for the same reason as the registration
// shapes: consoleapi is the one definition the Exchange encodes with.
type (
	// Period is a half-open UTC window.
	Period = consoleapi.Period
	// RevenueTotalsRequest asks for one publisher's revenue over a period.
	RevenueTotalsRequest = consoleapi.RevenueTotalsRequest
	// RevenueTotals is the money a period settled, in buckets and in total.
	RevenueTotals = consoleapi.RevenueTotals
	// RevenueBucket is one grouping interval of a totals answer.
	RevenueBucket = consoleapi.RevenueBucket
	// ListTransactionsRequest pages through one publisher's transactions.
	ListTransactionsRequest = consoleapi.ListTransactionsRequest
	// TransactionPage is one page of transactions.
	TransactionPage = consoleapi.TransactionPage
	// TransactionRow is one transaction as the list shows it.
	TransactionRow = consoleapi.TransactionRow
	// TransactionDetailRequest reads one transaction.
	TransactionDetailRequest = consoleapi.TransactionDetailRequest
	// TransactionDetail is the evidence of one transaction.
	TransactionDetail = consoleapi.TransactionDetail
	// ListCatalogRequest pages through the publisher's resources.
	ListCatalogRequest = consoleapi.ListCatalogRequest
	// CatalogPage is one page of resources.
	CatalogPage = consoleapi.CatalogPage
	// CatalogDetailRequest reads one of the publisher's resources.
	CatalogDetailRequest = consoleapi.CatalogDetailRequest
	// CatalogResource is one resource the publisher listed.
	CatalogResource = consoleapi.CatalogResource
	// PublisherTermsRequest reads the terms the Exchange applies to a
	// publisher.
	PublisherTermsRequest = consoleapi.PublisherTermsRequest
	// PublisherTerms is the fee and the reporting duty the Exchange applies.
	PublisherTerms = consoleapi.PublisherTerms
)

// Reads are the publisher reads, over the same signed client as the
// registration calls. Each one names the publisher it reads, and the Exchange
// answers not_found unless the acting person holds a grant for it.
//
// They are a type of their own rather than more methods on Client, which
// already carries the registration calls.
type Reads struct {
	c *Client
}

// Reads returns the publisher reads over c.
func (c *Client) Reads() *Reads { return &Reads{c: c} }

// RevenueTotals reads the money one publisher's sales settled over a period.
func (r *Reads) RevenueTotals(ctx context.Context, req RevenueTotalsRequest) (RevenueTotals, error) {
	var out RevenueTotals
	err := r.c.call(ctx, consoleapi.PathRevenueTotals, req, &out)
	return out, err
}

// ListTransactions reads one page of one publisher's transactions.
func (r *Reads) ListTransactions(ctx context.Context, req ListTransactionsRequest) (TransactionPage, error) {
	var out TransactionPage
	err := r.c.call(ctx, consoleapi.PathListTransactions, req, &out)
	return out, err
}

// TransactionDetail reads the evidence of one of the publisher's transactions.
func (r *Reads) TransactionDetail(ctx context.Context, req TransactionDetailRequest) (TransactionDetail, error) {
	var out TransactionDetail
	err := r.c.call(ctx, consoleapi.PathTransactionDetail, req, &out)
	return out, err
}

// ListCatalog reads one page of the publisher's resources.
func (r *Reads) ListCatalog(ctx context.Context, req ListCatalogRequest) (CatalogPage, error) {
	var out CatalogPage
	err := r.c.call(ctx, consoleapi.PathListCatalog, req, &out)
	return out, err
}

// CatalogDetail reads one of the publisher's resources.
func (r *Reads) CatalogDetail(ctx context.Context, req CatalogDetailRequest) (CatalogResource, error) {
	var out CatalogResource
	err := r.c.call(ctx, consoleapi.PathCatalogDetail, req, &out)
	return out, err
}

// PublisherTerms reads the fee and reporting policy the Exchange applies to
// the publisher.
func (r *Reads) PublisherTerms(ctx context.Context, req PublisherTermsRequest) (PublisherTerms, error) {
	var out PublisherTerms
	err := r.c.call(ctx, consoleapi.PathPublisherTerms, req, &out)
	return out, err
}
