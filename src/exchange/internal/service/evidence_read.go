package service

import (
	"context"
	"errors"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// EvidenceView is everything an evidence read returns for one transaction:
// the append-once evidence row, the transaction-log facts beside it, and the
// reporting obligation if the transaction minted one.
//
// It is a read-only projection assembled from three repositories. Nothing here
// is derived or recomputed — a value the caller sees is a value the store
// holds, which is the property that makes the row usable as evidence.
type EvidenceView struct {
	Evidence    repo.EvidenceRecord
	Transaction repo.TransactionRecord
	// Obligation is nil when the transaction minted none — a reachable state, not
	// only a defensive one: a term whose pricing meters nothing owes no usage
	// report and writes no obligation row. A read that finds none must say so
	// instead of rendering a zero one, on a surface whose only purpose is
	// stating what happened.
	Obligation *repo.Obligation
}

// EvidenceReadService serves the two evidence reads: the operator plane's,
// across tenants, and a publisher's, of its own transactions on the seat. It is
// separate from AdminService, which owns the control-plane setters: this
// service writes nothing, so it needs neither the transaction runner nor the
// audit repository those setters depend on.
type EvidenceReadService struct {
	evidence     repo.EvidenceRepo
	transactions repo.TransactionRepo
	obligations  repo.ObligationRepo
	owners       repo.PublisherTransactionOwnerRepo
}

// NewEvidenceReadService composes the read service over the generated queries.
// The service itself holds only narrow repository ports — the evidence, the
// transaction log, the obligations, and the check of which publisher a
// transaction belongs to; this is the composition-root convenience cmd/server
// and the integration harness share, so the repo wiring is written once.
func NewEvidenceReadService(queries sqlc.Querier) *EvidenceReadService {
	return &EvidenceReadService{
		evidence:     repo.NewEvidenceRepo(queries),
		transactions: repo.NewTransactionRepo(queries),
		obligations:  repo.NewObligationRepo(queries),
		owners:       repo.NewPublisherActivityRepo(queries),
	}
}

// TransactionEvidenceCrossTenant returns the evidence view for one transaction,
// whatever tenant owns it.
//
// The cross-tenant scope is what the operator plane is for: an operator
// investigating a delivery holds a transaction id and no tenant id, and the
// plane is reachable only from the internal listener behind its network
// allowlist. A counterparty-facing read is tenant-scoped instead — see
// TransactionEvidenceForPublisher — because counterparties legitimately hold
// transaction ids and the id alone must not open a row this sensitive.
func (s *EvidenceReadService) TransactionEvidenceCrossTenant(
	ctx context.Context, transactionID string,
) (EvidenceView, error) {
	// admin:cross_tenant — the operator plane has no per-tenant scope.
	evidence, err := s.evidence.ByTransactionCrossTenant(ctx, transactionID)
	if err != nil {
		return EvidenceView{}, evidenceReadErr(err, transactionID)
	}
	// admin:cross_tenant — the transaction-log row is read without a tenant
	// filter too.
	return s.assemble(ctx, evidence, s.transactions.ByID)
}

// TransactionEvidenceForPublisher returns the evidence view for one of the
// publisher's transactions on the seat.
//
// The ownership check comes first and answers not-found for a transaction that
// belongs to another publisher, with the same error as one that does not
// exist. A publisher holding another publisher's transaction id learns nothing
// from asking about it. Every read after the check is filtered by the seat in
// its own query, so the scope does not depend on the order of the calls.
func (s *EvidenceReadService) TransactionEvidenceForPublisher(
	ctx context.Context, tenantID, publisherID, transactionID string,
) (EvidenceView, error) {
	owned, err := s.owners.OwnsTransaction(ctx, tenantID, publisherID, transactionID)
	if err != nil {
		return EvidenceView{}, exchange.Wrap(exchange.KindInternal, err, "check transaction owner")
	}
	if !owned {
		return EvidenceView{}, errNoEvidence(transactionID)
	}
	evidence, err := s.evidence.ByTransaction(ctx, tenantID, transactionID)
	if err != nil {
		return EvidenceView{}, evidenceReadErr(err, transactionID)
	}
	return s.assemble(ctx, evidence, func(ctx context.Context, id string) (repo.TransactionRecord, error) {
		return s.transactions.ByTenantAndID(ctx, tenantID, id)
	})
}

// errNoEvidence is the not-found answer of both evidence reads, for a
// transaction that does not exist and for one outside the caller's scope
// alike.
func errNoEvidence(transactionID string) error {
	return exchange.Newf(exchange.KindNotFound, "no evidence for transaction %q", transactionID)
}

func evidenceReadErr(err error, transactionID string) error {
	if errors.Is(err, repo.ErrEvidenceNotFound) {
		return errNoEvidence(transactionID)
	}
	return exchange.Wrap(exchange.KindInternal, err, "read transaction evidence")
}

// transactionRead reads a transaction-log row by id under its caller's scope.
type transactionRead func(ctx context.Context, transactionID string) (repo.TransactionRecord, error)

// assemble reads the transaction-log row and the obligation beside an evidence
// row the caller has already read under its own scope. The caller also
// chooses the transaction-log read, so that read runs in the same scope. The
// obligation table carries no tenant: its rows are reached only through the
// transaction id the scoped reads above returned.
func (s *EvidenceReadService) assemble(
	ctx context.Context, evidence repo.EvidenceRecord, readTransaction transactionRead,
) (EvidenceView, error) {
	transactionID := evidence.TransactionID
	transaction, err := readTransaction(ctx, transactionID)
	if err != nil {
		if errors.Is(err, repo.ErrTransactionNotFound) {
			// The evidence row's transaction_id is a foreign key onto
			// transaction_log with ON DELETE RESTRICT, so a miss here means the
			// two tables disagree rather than that the caller asked for
			// something absent. Refuse rather than render half a chain.
			return EvidenceView{}, exchange.Wrap(exchange.KindInternal, err,
				"evidence row has no transaction_log row")
		}
		return EvidenceView{}, exchange.Wrap(exchange.KindInternal, err, "read transaction log")
	}

	view := EvidenceView{Evidence: evidence, Transaction: transaction}

	obligation, err := s.obligations.ByTransaction(ctx, transactionID)
	switch {
	case errors.Is(err, repo.ErrObligationNotFound):
		// Left nil: the transaction minted no obligation.
	case err != nil:
		return EvidenceView{}, exchange.Wrap(exchange.KindInternal, err, "read reporting obligation")
	default:
		view.Obligation = &obligation
	}
	return view, nil
}
