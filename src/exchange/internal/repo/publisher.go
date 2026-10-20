package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// Publisher is the domain view of a fora.publishers row: the payee that one or
// more sites settle their catalog revenue to.
//
// ID is the value the protocol calls resource_owner_id. It is the same string
// fora.catalog.resource_owner_id carries and the same string a site's manifest
// attests, so nothing here renames it.
type Publisher struct {
	ID          string
	DisplayName string
	CreatedAt   time.Time
}

// PublisherReadRepo is the read-only payee contract. Two consumers hold it and
// neither may write: the catalog push path, which resolves the payee a pushed
// entry settles to, and the console's read RPCs, which resolve the payee a
// request names before filtering anything by it.
//
// There is no tenant_id parameter, and that is the record's design rather than
// a missing filter: the publisher id is what groups one owner's sites across
// every seat that sells them. The tenant-scoped state is on the listing and the
// grant, and both have their own ports below.
type PublisherReadRepo interface {
	ByID(ctx context.Context, publisherID string) (Publisher, error)
}

// PublisherWriteRepo is the registration write contract, held by the
// registration service and by nothing else.
//
// Create takes an explicit pgx.Tx because the payee and the grant that makes it
// reachable must commit together: a publisher committed without its grant is a
// row nobody can act for and no later call can adopt, since the console never
// asserts ownership.
//
// Lock takes the payee's row lock until tx ends, so two registrations for one
// payee that each count what it already holds cannot both count before either
// has written. It returns ErrPublisherNotFound when there is no such payee.
type PublisherWriteRepo interface {
	Create(ctx context.Context, tx pgx.Tx, publisherID, displayName string) (Publisher, error)
	Lock(ctx context.Context, tx pgx.Tx, publisherID string) error
}

// ErrPublisherNotFound is returned when a payee lookup has no match.
var ErrPublisherNotFound = errors.New("repo: publisher not found")

// ErrPublisherExists is returned when creating a payee collides with one that
// is already registered. The id is minted per registration, so this is a broken
// minter rather than a normal race, and it surfaces instead of returning the
// existing row.
var ErrPublisherExists = errors.New("repo: publisher already exists")

// NewPublisherReadRepo composes the read-only payee port over the fat
// sqlc.Querier.
func NewPublisherReadRepo(q sqlc.Querier) PublisherReadRepo { return &publisherRepo{q: q} }

// NewPublisherWriteRepo composes the registration write port over the fat
// sqlc.Querier. Same concrete type as NewPublisherReadRepo -- the split is at
// the interface, not the implementation, as it is for tenants.
func NewPublisherWriteRepo(q sqlc.Querier) PublisherWriteRepo { return &publisherRepo{q: q} }

type publisherRepo struct{ q sqlc.Querier }

func (r *publisherRepo) ByID(ctx context.Context, publisherID string) (Publisher, error) {
	row, err := r.q.GetPublisher(ctx, publisherID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Publisher{}, ErrPublisherNotFound
		}
		return Publisher{}, fmt.Errorf("get publisher: %w", err)
	}
	return publisherFromRow(row), nil
}

func (r *publisherRepo) Create(
	ctx context.Context, tx pgx.Tx, publisherID, displayName string,
) (Publisher, error) {
	row, err := sqlc.New(tx).CreatePublisher(ctx, sqlc.CreatePublisherParams{
		PublisherID: publisherID,
		DisplayName: displayName,
	})
	if err != nil {
		return Publisher{}, mapInsertErr(err, "create publisher", ErrPublisherExists)
	}
	return publisherFromRow(row), nil
}

func (r *publisherRepo) Lock(ctx context.Context, tx pgx.Tx, publisherID string) error {
	if _, err := sqlc.New(tx).LockPublisher(ctx, publisherID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPublisherNotFound
		}
		return fmt.Errorf("lock publisher: %w", err)
	}
	return nil
}

func publisherFromRow(row sqlc.ForaPublisher) Publisher {
	return Publisher{
		ID:          row.PublisherID,
		DisplayName: row.DisplayName,
		CreatedAt:   row.CreatedAt.Time,
	}
}
