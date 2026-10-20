package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// DenialReason is the domain view of the fora.denial_reason PG enum. Its values
// are the proto DenialReason names without the DENIAL_REASON_ prefix, which is
// how the enum spells each one; the enum mirrors the proto value for value.
// The repo is the single owner of the sqlc enum type, so services translate to
// and from this label at the repository boundary.
type DenialReason string

// ErrInvalidDenialReason reports a denial reason that is not a value of the
// fora.denial_reason enum. Nothing is written for it.
var ErrInvalidDenialReason = errors.New("repo: not a fora.denial_reason value")

// DenialEvent is one attributed denied item, as fora.transaction_events records
// it: the decision time on the Exchange's clock, the seat and agent the request
// resolved to, the per-item key (the request's idempotency key, ':', and the
// offer id), the offer, and the resource and its owner as they were when the
// item was refused.
type DenialEvent struct {
	EventID         string
	CreatedAt       time.Time
	TenantID        string
	IdempotencyKey  string
	OfferID         string
	AgentID         string
	ResourceID      string
	ResourceOwnerID string
	Reason          DenialReason
}

// TransactionEventRepo is the journal's write port, the one ExecuteTransaction
// uses. The journal is append-only, so the port has no way to change or remove
// an event.
type TransactionEventRepo interface {
	// AppendDenial records ev as a BILLING_AUTHORIZATION event. It returns false,
	// with no error, when the journal already holds an event for the same agent
	// and item key: the first decision recorded for a key is the one that
	// stays.
	AppendDenial(ctx context.Context, ev DenialEvent) (bool, error)
}

// DenialCountQuery asks how many of one publisher's items one seat refused in
// [From, To).
type DenialCountQuery struct {
	TenantID    string
	PublisherID string
	From, To    time.Time
}

// DenialCount is how many items were refused for one reason.
type DenialCount struct {
	Reason DenialReason
	Count  int64
}

// DenialCountRepo is the journal's read port, the one the publisher console's
// revenue totals use.
type DenialCountRepo interface {
	// DenialCounts returns one count per reason that occurred, in no particular
	// order. The publisher is the resource owner recorded when each item was
	// refused, not the catalog entry's current owner.
	DenialCounts(ctx context.Context, q DenialCountQuery) ([]DenialCount, error)
}

// transactionEventRepo backs both ports over the one generated querier: the
// write port for the execute path and the read port for the console, so each
// caller depends on the half it uses.
type transactionEventRepo struct{ q sqlc.Querier }

// NewTransactionEventRepo returns the journal's write port.
func NewTransactionEventRepo(q sqlc.Querier) TransactionEventRepo {
	return &transactionEventRepo{q: q}
}

// NewDenialCountRepo returns the journal's read port.
func NewDenialCountRepo(q sqlc.Querier) DenialCountRepo {
	return &transactionEventRepo{q: q}
}

func (r *transactionEventRepo) AppendDenial(ctx context.Context, ev DenialEvent) (bool, error) {
	reason := sqlc.ForaDenialReason(ev.Reason)
	if !reason.Valid() {
		return false, fmt.Errorf("append denial event: %w: %q", ErrInvalidDenialReason, ev.Reason)
	}
	n, err := r.q.AppendTransactionEvent(ctx, sqlc.AppendTransactionEventParams{
		EventID:         ev.EventID,
		EventType:       sqlc.ForaTransactionEventTypeBILLINGAUTHORIZATION,
		CreatedAt:       pgTime(ev.CreatedAt),
		TenantID:        ev.TenantID,
		IdempotencyKey:  ev.IdempotencyKey,
		OfferID:         ev.OfferID,
		AgentID:         ev.AgentID,
		ResourceID:      ev.ResourceID,
		ResourceOwnerID: ev.ResourceOwnerID,
		DenialReason:    reason,
	})
	if err != nil {
		return false, fmt.Errorf("append denial event: %w", err)
	}
	return n == 1, nil
}

func (r *transactionEventRepo) DenialCounts(ctx context.Context, q DenialCountQuery) ([]DenialCount, error) {
	rows, err := r.q.CountPublisherDenialsByReason(ctx, sqlc.CountPublisherDenialsByReasonParams{
		TenantID:    q.TenantID,
		PublisherID: q.PublisherID,
		PeriodStart: pgTime(q.From),
		PeriodEnd:   pgTime(q.To),
	})
	if err != nil {
		return nil, fmt.Errorf("count publisher denials: %w", err)
	}
	out := make([]DenialCount, 0, len(rows))
	for _, row := range rows {
		out = append(out, DenialCount{Reason: DenialReason(row.DenialReason), Count: row.Denials})
	}
	return out, nil
}
