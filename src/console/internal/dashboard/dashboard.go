// Package dashboard reads what happens with a publisher's content and what
// they earn, and shapes it for the console's pages: the overview of a period,
// the transactions in it, one transaction's evidence, the catalog, and the
// terms the Exchange applies.
//
// Every number and status on those pages is the Exchange's. This package
// formats what the Exchange returned and never computes a total, a fee or a
// reporting status of its own, because a second computation is a second
// answer that can disagree with the ledger.
//
// It asserts who is asking and names the publisher that person holds, which the
// Exchange told it. Whether the person may see what a read asks for is still
// the Exchange's decision, made on every read from its own grants, so another
// publisher's transaction comes back not found and nothing is shown.
package dashboard

import (
	"context"
	"errors"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
)

// Reader is the Exchange's publisher reads.
type Reader interface {
	RevenueTotals(ctx context.Context, req exchangeclient.RevenueTotalsRequest) (exchangeclient.RevenueTotals, error)
	ListTransactions(
		ctx context.Context, req exchangeclient.ListTransactionsRequest,
	) (exchangeclient.TransactionPage, error)
	TransactionDetail(
		ctx context.Context, req exchangeclient.TransactionDetailRequest,
	) (exchangeclient.TransactionDetail, error)
	ListCatalog(ctx context.Context, req exchangeclient.ListCatalogRequest) (exchangeclient.CatalogPage, error)
	CatalogDetail(
		ctx context.Context, req exchangeclient.CatalogDetailRequest,
	) (exchangeclient.CatalogResource, error)
	PublisherTerms(
		ctx context.Context, req exchangeclient.PublisherTermsRequest,
	) (exchangeclient.PublisherTerms, error)
}

// DefaultPageSize is how many rows a list page shows: the default page of the
// Exchange's list routes.
const DefaultPageSize = consoleapi.DefaultPageSize

// Service builds the dashboard's pages.
type Service struct {
	reader   Reader
	clk      clock.Clock
	pageSize int
}

// NewService builds the dashboard over reader. clk decides which month a page
// with no period shows. pageSize is the rows per list page; zero is
// DefaultPageSize.
func NewService(reader Reader, clk clock.Clock, pageSize int) *Service {
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	return &Service{reader: reader, clk: clk, pageSize: pageSize}
}

// Nav is where a page in section stands for the person: the one publisher they
// hold and its domains, taken from regs. regs must be what the Exchange listed
// for the signed-in person on this request, never anything the browser sent:
// the publisher it names is the one every read on the page is made for. ok is
// false for a person whose account holds no publisher yet: every section reads
// one publisher's data, and there is none to read. A publisher with no domain
// yet has a Nav with no domains, and each section says it has nothing to show.
func (s *Service) Nav(regs exchangeclient.Registrations, section string) (Nav, bool) {
	if !regs.Registered {
		return Nav{}, false
	}
	domains := make([]string, 0, len(regs.Sites))
	for _, site := range regs.Sites {
		domains = append(domains, site.Domain)
	}
	return Nav{PublisherID: regs.PublisherID, Section: section, Domains: domains}, true
}

// HasVerifiedDomain reports whether any of the person's domains is verified,
// which is what anything can be sold from. The console decides from it whether
// a person opens on the overview, where sales are, or on their domains, where
// a domain that is not verified says what it still needs.
func HasVerifiedDomain(regs exchangeclient.Registrations) bool {
	for _, site := range regs.Sites {
		if site.Active() {
			return true
		}
	}
	return false
}

// notFoundText is what a page about something that is not the person's says.
const notFoundText = "There is nothing here, or it is not yours to see."

// failure classifies a read's error: the Exchange's not_found becomes a
// refusal.KindNotFound, and anything else passes through as it came.
func failure(err error) error {
	if xe, ok := errors.AsType[*exchangeclient.Error](err); ok && xe.Kind == consoleapi.ErrorKindNotFound {
		return refusal.Wrap(refusal.KindNotFound, notFoundText, err)
	}
	return err
}
