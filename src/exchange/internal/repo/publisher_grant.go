package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// PublisherGrant is the domain view of a fora.publisher_grants row: one person
// may act for one payee on one seat.
//
// The person is the OIDC (issuer, subject) pair the console asserts on every
// call. The console says who someone is; the Exchange decides what they may do,
// and this row is what it decides from. That is why the grant is stored here and
// not in the console's own database.
type PublisherGrant struct {
	TenantID    string
	PublisherID string
	OIDCIssuer  string
	OIDCSubject string
	CreatedAt   time.Time
}

// PublisherGrantReadRepo is the read-only authorization contract, held by the
// interceptor and every publisher-scoped handler. Both ports are tenant-scoped:
// listing one payee on two seats creates two grant sets, and a grant on one seat
// says nothing about the other.
type PublisherGrantReadRepo interface {
	// Has answers whether this person may act for this payee on this seat. It
	// is a boolean rather than a lookup returning a row, because every caller
	// asks the yes/no question and returning the row would invite one of them to
	// read authorization out of its fields.
	Has(ctx context.Context, tenantID, publisherID, oidcIssuer, oidcSubject string) (bool, error)
	// PublisherIDsForActor lists every payee this person may act for on this
	// seat, ordered by id. Empty is the normal answer for someone who has
	// registered nothing yet, not an error.
	PublisherIDsForActor(ctx context.Context, tenantID, oidcIssuer, oidcSubject string) ([]string, error)
}

// PublisherGrantWriteRepo is the grant write contract, held by the
// RegisterPublisher handler's service and by nothing else.
//
// Grant takes an explicit pgx.Tx because it commits with the publisher it
// grants: a payee committed without its grant is unreachable forever, since the
// console never asserts ownership and no later call could adopt it.
type PublisherGrantWriteRepo interface {
	// Grant returns ErrGrantExists when the person already acts for another
	// payee on this seat. The unique violation aborts tx, so the caller has to
	// roll back before it reads anything.
	Grant(ctx context.Context, tx pgx.Tx, g PublisherGrant) error
}

// ErrGrantExists is returned by Grant when the person already holds a grant for
// a different payee on the same seat. A person acts for one publisher per seat,
// and the unique index publisher_grants_one_actor_idx is what holds that: two
// registrations in flight together both pass the service's read-first check,
// and this is how the second one learns it lost.
var ErrGrantExists = errors.New("repo: person already acts for a publisher on this tenant")

// NewPublisherGrantReadRepo composes the read-only grant ports over the fat
// sqlc.Querier.
func NewPublisherGrantReadRepo(q sqlc.Querier) PublisherGrantReadRepo {
	return &publisherGrantRepo{q: q}
}

// NewPublisherGrantWriteRepo composes the grant write port over the fat
// sqlc.Querier.
func NewPublisherGrantWriteRepo(q sqlc.Querier) PublisherGrantWriteRepo {
	return &publisherGrantRepo{q: q}
}

type publisherGrantRepo struct{ q sqlc.Querier }

func (r *publisherGrantRepo) Has(
	ctx context.Context, tenantID, publisherID, oidcIssuer, oidcSubject string,
) (bool, error) {
	ok, err := r.q.HasPublisherGrant(ctx, sqlc.HasPublisherGrantParams{
		TenantID:    tenantID,
		PublisherID: publisherID,
		OidcIssuer:  oidcIssuer,
		OidcSubject: oidcSubject,
	})
	if err != nil {
		return false, fmt.Errorf("has publisher grant: %w", err)
	}
	return ok, nil
}

func (r *publisherGrantRepo) PublisherIDsForActor(
	ctx context.Context, tenantID, oidcIssuer, oidcSubject string,
) ([]string, error) {
	ids, err := r.q.ListPublisherGrantsForActor(ctx, sqlc.ListPublisherGrantsForActorParams{
		TenantID:    tenantID,
		OidcIssuer:  oidcIssuer,
		OidcSubject: oidcSubject,
	})
	if err != nil {
		return nil, fmt.Errorf("list publisher grants for actor: %w", err)
	}
	return ids, nil
}

func (r *publisherGrantRepo) Grant(ctx context.Context, tx pgx.Tx, g PublisherGrant) error {
	err := sqlc.New(tx).CreatePublisherGrant(ctx, sqlc.CreatePublisherGrantParams{
		TenantID:    g.TenantID,
		PublisherID: g.PublisherID,
		OidcIssuer:  g.OIDCIssuer,
		OidcSubject: g.OIDCSubject,
	})
	// The query absorbs a repeat of the same (seat, payee, person) row as a
	// no-op, because its ON CONFLICT names the primary key. A grant of a second
	// payee to a person who already holds one on this seat is a different
	// conflict, on publisher_grants_one_actor_idx, and surfaces as
	// ErrGrantExists. What is left to map is a grant naming a tenant or a payee
	// that is not there.
	return mapInsertErr(err, "create publisher grant", ErrGrantExists,
		fkSentinel{"publisher_grants_publisher_fk", ErrPublisherNotFound},
		fkSentinel{"publisher_grants_tenant_fk", ErrTenantNotFound})
}
