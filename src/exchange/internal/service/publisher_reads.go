package service

import (
	"context"
	"errors"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// PublisherReadService serves what a publisher reads through the console: its
// revenue, its transactions and their evidence, its catalog, and the terms the
// Exchange applies to it. It writes nothing.
//
// Every method starts at the grant gate the registration plane uses, so a
// person reads only a publisher they hold a grant for, and a publisher they do
// not is answered not-found. Every database read below the gate is filtered by
// the seat and by the publisher. The ledger has no seats: its revenue read is
// keyed by the publisher alone, the one publisher the gate admitted.
type PublisherReadService struct {
	gate     *PublisherGate
	activity repo.PublisherActivityRepo
	denials  repo.DenialCountRepo
	catalog  repo.PublisherCatalogRepo
	tenants  repo.TenantReadRepo
	fees     repo.FeeOverrideReadRepo
	evidence *EvidenceReadService
	ledger   billing.RevenueLedger
	clk      clock.Clock
	cfg      PublisherReadConfig
}

// PublisherReadWiring is what the read service takes besides the database:
// the pieces a deployment and a test harness choose differently.
type PublisherReadWiring struct {
	// Gate is the grant gate, the same instance the registration service
	// holds.
	Gate *PublisherGate
	// Ledger is the revenue read of the ledger the deployment charges
	// through: billing.RevenueLedgerFor of its billing adapter.
	Ledger billing.RevenueLedger
	// Clk decides whether a pending report is overdue. Nil is the system
	// clock.
	Clk    clock.Clock
	Config PublisherReadConfig
}

// PublisherReadConfig carries the deployment values the reads fill defaults
// from.
type PublisherReadConfig struct {
	// Currencies are the currencies the deployment keeps a ledger in, in
	// configuration order. A totals read that names no currency is read in the
	// first of them, billing.BaseCurrency, so the default is always one the
	// ledger keeps. A totals read in any other currency answers
	// UNSUPPORTED_CURRENCY without reading anything.
	Currencies []string
	// ReportWindow is the reporting window an obligation gets when the seat's
	// policy sets none. The composition root passes the value it gives the
	// ExchangeService, which mints the obligations; zero is
	// DefaultReportWindow, that service's default too.
	ReportWindow time.Duration
	// SettlementBatch is how many settlements a narrowed totals read holds
	// back before it asks the transaction log which of them are inside the
	// narrowing: the most it keeps in memory, and how many billing ids one
	// query names. Zero is DefaultSettlementBatch.
	SettlementBatch int
}

// NewPublisherReadService composes the read service over the generated
// queries. Like NewEvidenceReadService it is the composition-root convenience
// cmd/server and the integration harness share, so the repository wiring is
// written once and a harness cannot build the service differently from
// production.
func NewPublisherReadService(queries sqlc.Querier, w PublisherReadWiring) *PublisherReadService {
	clk := w.Clk
	if clk == nil {
		clk = clock.System{}
	}
	cfg := w.Config
	if cfg.ReportWindow == 0 {
		cfg.ReportWindow = DefaultReportWindow
	}
	if cfg.SettlementBatch <= 0 {
		cfg.SettlementBatch = DefaultSettlementBatch
	}
	return &PublisherReadService{
		gate:     w.Gate,
		activity: repo.NewPublisherActivityRepo(queries),
		denials:  repo.NewDenialCountRepo(queries),
		catalog:  repo.NewPublisherCatalogRepo(queries),
		tenants:  repo.NewTenantReadRepo(queries),
		fees:     repo.NewFeeOverrideRepo(queries),
		evidence: NewEvidenceReadService(queries),
		ledger:   w.Ledger,
		clk:      clk,
		cfg:      cfg,
	}
}

// Narrowing is the resource and the domain a publisher read is narrowed to.
// An empty field narrows nothing.
type Narrowing struct {
	ResourceID string
	Domain     string
}

// scope checks the caller's grant on the publisher and returns the scope a
// read runs under: the seat the grant names, the publisher, and the narrowing.
// The domain is normalized the way every registered domain, and so every
// stored catalog domain, is, so a filter typed in another case still matches.
// A domain no publisher could have registered is refused rather than read as
// an empty page.
func (s *PublisherReadService) scope(
	ctx context.Context, caller ConsoleCaller, publisherID string, n Narrowing,
) (repo.PublisherScope, error) {
	seat, err := s.gate.RequireGrant(ctx, caller.Actor, publisherID)
	if err != nil {
		return repo.PublisherScope{}, err
	}
	domain := n.Domain
	if domain != "" {
		if domain, err = normalizeSiteDomain(domain); err != nil {
			return repo.PublisherScope{}, err
		}
	}
	return repo.PublisherScope{
		TenantID: seat, PublisherID: publisherID, ResourceID: n.ResourceID, Domain: domain,
	}, nil
}

// TransactionListQuery selects a page of a publisher's transactions.
type TransactionListQuery struct {
	PublisherID string
	Start, End  time.Time
	Narrowing
	// Status narrows the page to the transactions with this reporting status.
	// Empty reads every status.
	Status   ReportingStatus
	PageSize int
	Cursor   string
}

// PublisherTransactionRow is one listed transaction with its reporting status.
type PublisherTransactionRow struct {
	repo.PublisherTransaction
	Status ReportingStatus
}

// Transactions returns one page of the publisher's transactions in a period,
// newest first, and the cursors of the pages beside it.
func (s *PublisherReadService) Transactions(
	ctx context.Context, caller ConsoleCaller, q TransactionListQuery,
) ([]PublisherTransactionRow, PageLinks, error) {
	scope, err := s.scope(ctx, caller, q.PublisherID, q.Narrowing)
	if err != nil {
		return nil, PageLinks{}, err
	}
	period, err := NewPeriod(q.Start, q.End)
	if err != nil {
		return nil, PageLinks{}, err
	}
	size, pos, err := pageArgs(q.PageSize, q.Cursor)
	if err != nil {
		return nil, PageLinks{}, err
	}
	// One instant for the filter and for the statuses shown, so a deadline
	// passing while the page is read cannot list a row under a status its own
	// label then contradicts.
	now := statusInstant(s.clk.Now())
	var match *repo.ObligationMatch
	if q.Status != "" {
		m, err := obligationMatch(q.Status, now)
		if err != nil {
			return nil, PageLinks{}, err
		}
		match = &m
	}
	rows, err := s.activity.TransactionsPage(ctx, repo.PublisherTransactionQuery{
		PublisherScope: scope, From: period.Start, To: period.End,
		Obligation: match, PagePosition: pos, Limit: size + 1,
	})
	if err != nil {
		return nil, PageLinks{}, exchange.Wrap(exchange.KindInternal, err, "list publisher transactions")
	}
	rows, links := pageOf(rows, size, pos, func(t repo.PublisherTransaction) repo.Keyset {
		return repo.Keyset{CreatedAt: t.CreatedAt, ID: t.TransactionID}
	})
	out := make([]PublisherTransactionRow, 0, len(rows))
	for _, t := range rows {
		status, err := ReportingStatusOf(t.ObligationState, t.ObligationDeadline, now)
		if err != nil {
			return nil, PageLinks{}, err
		}
		out = append(out, PublisherTransactionRow{PublisherTransaction: t, Status: status})
	}
	return out, links, nil
}

// PublisherTransactionDetail is the evidence of one transaction and the
// reporting status the list shows for it.
type PublisherTransactionDetail struct {
	View   EvidenceView
	Status ReportingStatus
}

// TransactionDetail returns the evidence of one of the publisher's
// transactions. Another publisher's transaction is not found.
func (s *PublisherReadService) TransactionDetail(
	ctx context.Context, caller ConsoleCaller, publisherID, transactionID string,
) (PublisherTransactionDetail, error) {
	seat, err := s.gate.RequireGrant(ctx, caller.Actor, publisherID)
	if err != nil {
		return PublisherTransactionDetail{}, err
	}
	if transactionID == "" {
		return PublisherTransactionDetail{}, exchange.Newf(exchange.KindInvalidRequest,
			"transaction_id is required")
	}
	view, err := s.evidence.TransactionEvidenceForPublisher(ctx, seat, publisherID, transactionID)
	if err != nil {
		return PublisherTransactionDetail{}, err
	}
	status := ReportingNotRequired
	if o := view.Obligation; o != nil {
		status, err = ReportingStatusOf(o.State, &o.Deadline, statusInstant(s.clk.Now()))
		if err != nil {
			return PublisherTransactionDetail{}, err
		}
	}
	return PublisherTransactionDetail{View: view, Status: status}, nil
}

// CatalogListQuery selects a page of a publisher's resources.
type CatalogListQuery struct {
	PublisherID string
	Narrowing
	PageSize int
	Cursor   string
}

// Catalog returns one page of the publisher's resources, newest first, and
// the cursors of the pages beside it.
func (s *PublisherReadService) Catalog(
	ctx context.Context, caller ConsoleCaller, q CatalogListQuery,
) ([]repo.PublisherResource, PageLinks, error) {
	scope, err := s.scope(ctx, caller, q.PublisherID, q.Narrowing)
	if err != nil {
		return nil, PageLinks{}, err
	}
	size, pos, err := pageArgs(q.PageSize, q.Cursor)
	if err != nil {
		return nil, PageLinks{}, err
	}
	rows, err := s.catalog.Page(ctx, repo.PublisherCatalogQuery{
		PublisherScope: scope, PagePosition: pos, Limit: size + 1,
	})
	if err != nil {
		return nil, PageLinks{}, exchange.Wrap(exchange.KindInternal, err, "list publisher catalog")
	}
	rows, links := pageOf(rows, size, pos, func(r repo.PublisherResource) repo.Keyset {
		return repo.Keyset{CreatedAt: r.CreatedAt, ID: r.ResourceID}
	})
	return rows, links, nil
}

// Resource reads one of the publisher's catalog entries.
//
// A resource the publisher does not list answers the same not-found as one
// that does not exist: the grant check has already established which seat the
// caller may read, and beyond that the console is told nothing about what
// anybody else lists.
func (s *PublisherReadService) Resource(
	ctx context.Context, caller ConsoleCaller, publisherID, resourceID string,
) (repo.PublisherResource, error) {
	seat, err := s.gate.RequireGrant(ctx, caller.Actor, publisherID)
	if err != nil {
		return repo.PublisherResource{}, err
	}
	if resourceID == "" {
		return repo.PublisherResource{}, exchange.Newf(exchange.KindInvalidRequest, "resource_id is required")
	}
	res, err := s.catalog.Resource(ctx, repo.PublisherResourceQuery{
		TenantID: seat, PublisherID: publisherID, ResourceID: resourceID,
	})
	switch {
	case errors.Is(err, repo.ErrPublisherResourceNotFound):
		return repo.PublisherResource{}, exchange.Newf(exchange.KindNotFound, "resource not found")
	case err != nil:
		return repo.PublisherResource{}, exchange.Wrap(exchange.KindInternal, err, "read publisher resource")
	}
	return res, nil
}

// PublisherTerms is the fee the Exchange takes from a publisher's sales and
// the reporting duty it places on the agents that buy from it.
type PublisherTerms struct {
	FeeRate   FeeRate
	Reporting EffectivePolicy
}

// Terms returns the publisher's effective fee rate and the seat's effective
// reporting policy. The operator's notes on the rate stay with the operator.
func (s *PublisherReadService) Terms(
	ctx context.Context, caller ConsoleCaller, publisherID string,
) (PublisherTerms, error) {
	seat, err := s.gate.RequireGrant(ctx, caller.Actor, publisherID)
	if err != nil {
		return PublisherTerms{}, err
	}
	tenant, err := s.tenants.ByID(ctx, seat)
	if err != nil {
		return PublisherTerms{}, exchange.Wrap(exchange.KindInternal, err, "read seat")
	}
	rate, err := effectiveFeeRate(ctx, s.fees, tenant, publisherID)
	if err != nil {
		return PublisherTerms{}, err
	}
	return PublisherTerms{
		FeeRate:   rate,
		Reporting: effectiveReportingPolicy(tenant.ReportingPolicy, s.cfg.ReportWindow),
	}, nil
}

// pageArgs resolves a page size and cursor.
func pageArgs(requested int, cursor string) (int32, repo.PagePosition, error) {
	size, err := pageSize(requested)
	if err != nil {
		return 0, repo.PagePosition{}, err
	}
	pos, err := decodeCursor(cursor)
	if err != nil {
		return 0, repo.PagePosition{}, err
	}
	return size, pos, nil
}
