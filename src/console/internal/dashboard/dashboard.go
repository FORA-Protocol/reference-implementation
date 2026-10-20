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

// Registry is where the dashboard learns which publisher a person holds: the
// registrations the Exchange lists for them.
type Registry interface {
	ListRegistrations(ctx context.Context, actor exchangeclient.Actor) (exchangeclient.Registrations, error)
}

// DefaultPageSize is how many rows a list page shows: the default page of the
// Exchange's list routes.
const DefaultPageSize = consoleapi.DefaultPageSize

// Service builds the dashboard's pages.
type Service struct {
	reader   Reader
	registry Registry
	clk      clock.Clock
	pageSize int
}

// NewService builds the dashboard over reader and registry. clk decides which
// month a page with no period shows. pageSize is the rows per list page; zero
// is DefaultPageSize.
func NewService(reader Reader, registry Registry, clk clock.Clock, pageSize int) *Service {
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	return &Service{reader: reader, registry: registry, clk: clk, pageSize: pageSize}
}

// ErrNoDomain means the person's publisher has registered no domain yet, so
// no dashboard section has anything to show. A page answers it by sending the
// person to add their first domain.
var ErrNoDomain = errors.New("dashboard: no domain registered yet")

// Nav is where a dashboard page in section stands for the person: the one
// publisher they hold and its domains, as the Exchange lists them. It is
// ErrNoDomain for a person with no domain yet.
func (s *Service) Nav(ctx context.Context, actor exchangeclient.Actor, section string) (Nav, error) {
	regs, err := s.registry.ListRegistrations(ctx, actor)
	if err != nil {
		return Nav{}, err
	}
	if !exchangeclient.HasDomains(regs) {
		return Nav{}, ErrNoDomain
	}
	domains := make([]string, 0, len(regs.Sites))
	for _, site := range regs.Sites {
		domains = append(domains, site.Domain)
	}
	return Nav{PublisherID: regs.PublisherID, Section: section, Domains: domains}, nil
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
