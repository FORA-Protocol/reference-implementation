package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// Keyset is a row's place in a newest-first list: its creation time and id.
type Keyset struct {
	CreatedAt time.Time
	ID        string
}

// PagePosition is where a page is read from. A nil Key is the first page.
// Otherwise the page is the rows strictly below Key, or, when Before is set,
// the rows strictly above it: the page before the one Key starts. Either way
// the rows come back newest first.
type PagePosition struct {
	Key    *Keyset
	Before bool
}

// PublisherScope is whose rows a publisher read returns: one publisher's on one
// seat, narrowed to one resource and to one of the publisher's domains when
// those are not empty. Every publisher read takes the same scope, so a
// narrowing means the same thing in the transaction list, the counts, the
// money and the catalog.
//
// Domain is compared with the stored, normalized catalog domain as it is. The
// service normalizes what the caller sent before building a scope.
type PublisherScope struct {
	TenantID    string
	PublisherID string
	ResourceID  string
	Domain      string
}

// Narrowed reports whether the scope names a resource or a domain.
func (s PublisherScope) Narrowed() bool { return s.ResourceID != "" || s.Domain != "" }

// PublisherTransactionQuery selects one page of a publisher's transactions.
// Limit is the number of rows to read.
type PublisherTransactionQuery struct {
	PublisherScope
	From, To time.Time
	// Obligation narrows the page to the transactions whose latest reporting
	// obligation it matches. Nil reads every transaction.
	Obligation *ObligationMatch
	PagePosition
	Limit int32
}

// ObligationMatch is a condition on a transaction's latest reporting
// obligation. A transaction matches when it minted none and None is set, when
// the obligation's state is in Open and its deadline has not passed AsOf, or
// when the state is in Past and the deadline has passed AsOf. It names states
// and deadlines only: which of them make up a reporting status is decided by
// the caller.
type ObligationMatch struct {
	None bool
	Open []ObligationState
	Past []ObligationState
	AsOf time.Time
}

// PublisherTransaction is one transaction as a publisher reads it. The
// obligation fields describe the transaction's latest reporting obligation:
// ObligationState is empty and ObligationDeadline nil when it minted none.
// Times are in UTC.
type PublisherTransaction struct {
	TransactionID      string
	CreatedAt          time.Time
	ResourceID         string
	ResourceURI        string
	ResourceTitle      string
	AgentID            string
	UnitCost           string
	Currency           string
	ObligationState    ObligationState
	ObligationDeadline *time.Time
}

// TransactionCountQuery asks how many of a publisher's transactions in one
// currency each bucket of [From, To) holds. BucketStarts is the sorted start
// of every bucket, and its first element is From.
type TransactionCountQuery struct {
	PublisherScope
	Currency     string
	From, To     time.Time
	BucketStarts []time.Time
}

// BillingScopeQuery asks which of BillingIDs are charges inside a publisher
// scope.
type BillingScopeQuery struct {
	PublisherScope
	BillingIDs []string
}

// PublisherTransactionOwnerRepo answers whether a transaction is one of a
// publisher's. A transaction that does not exist is not owned.
type PublisherTransactionOwnerRepo interface {
	OwnsTransaction(ctx context.Context, tenantID, publisherID, transactionID string) (bool, error)
}

// PublisherActivityRepo is the publisher-scoped read of the transaction log.
// Every method filters on the seat and on the publisher the catalog names as
// resource owner, so none of them can return a row of another publisher.
type PublisherActivityRepo interface {
	TransactionsPage(ctx context.Context, q PublisherTransactionQuery) ([]PublisherTransaction, error)
	// TransactionCounts returns one count per element of q.BucketStarts.
	TransactionCounts(ctx context.Context, q TransactionCountQuery) ([]int64, error)
	// ScopedBillingIDs returns the subset of q.BillingIDs that are charges
	// inside the query's scope. An id of another publisher, outside the
	// narrowing, or of no transaction is left out.
	ScopedBillingIDs(ctx context.Context, q BillingScopeQuery) (map[string]bool, error)
	PublisherTransactionOwnerRepo
}

// PublisherCatalogQuery selects one page of a publisher's resources.
type PublisherCatalogQuery struct {
	PublisherScope
	PagePosition
	Limit int32
}

// PublisherResource is one catalog entry as its publisher reads it. Terms is
// the stored license-term list verbatim; each term carries its own pricing.
// Times are in UTC.
type PublisherResource struct {
	ResourceID string
	URI        string
	Title      *string
	Terms      []byte
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// PublisherResourceQuery selects exactly one of a publisher's resources, where
// PublisherCatalogQuery selects a page of them.
type PublisherResourceQuery struct {
	TenantID    string
	PublisherID string
	ResourceID  string
}

// ErrPublisherResourceNotFound is returned when a publisher lists no resource
// of the requested id on the seat.
var ErrPublisherResourceNotFound = errors.New("repo: publisher resource not found")

// PublisherCatalogRepo is the publisher-scoped read of the catalog.
type PublisherCatalogRepo interface {
	Page(ctx context.Context, q PublisherCatalogQuery) ([]PublisherResource, error)
	// Resource returns the one resource, or ErrPublisherResourceNotFound. A
	// resource this publisher does not list under this seat is not found, the
	// same as one that does not exist, so no caller can tell the two apart.
	Resource(ctx context.Context, q PublisherResourceQuery) (PublisherResource, error)
}

// NewPublisherActivityRepo composes the transaction-log read over the fat
// sqlc.Querier.
func NewPublisherActivityRepo(q sqlc.Querier) PublisherActivityRepo {
	return &publisherScopedReads{q: q}
}

// NewPublisherCatalogRepo composes the catalog read over the fat sqlc.Querier.
func NewPublisherCatalogRepo(q sqlc.Querier) PublisherCatalogRepo {
	return &publisherScopedReads{q: q}
}

// publisherScopedReads implements both publisher-scoped read ports over one
// Querier.
type publisherScopedReads struct{ q sqlc.Querier }

func (r *publisherScopedReads) TransactionsPage(
	ctx context.Context, q PublisherTransactionQuery,
) ([]PublisherTransaction, error) {
	keyAt, keyID := keysetParams(q.Key)
	var match ObligationMatch
	if q.Obligation != nil {
		match = *q.Obligation
	}
	params := sqlc.ListPublisherTransactionsPageParams{
		TenantID:         q.TenantID,
		PublisherID:      q.PublisherID,
		PeriodStart:      pgTime(q.From),
		PeriodEnd:        pgTime(q.To),
		ResourceID:       pgText(q.ResourceID),
		Domain:           pgText(q.Domain),
		MatchStatus:      q.Obligation != nil,
		MatchNone:        match.None,
		AsOf:             pgTime(match.AsOf),
		MatchOpen:        stateTexts(match.Open),
		MatchPast:        stateTexts(match.Past),
		KeyCreatedAt:     keyAt,
		KeyTransactionID: keyID,
		RowLimit:         q.Limit,
	}
	rows, err := r.transactionRows(ctx, params, q.Before)
	if err != nil {
		return nil, fmt.Errorf("list publisher transactions: %w", err)
	}
	out := make([]PublisherTransaction, 0, len(rows))
	for _, row := range rows {
		cost, err := decimalFromNumeric(row.UnitCost)
		if err != nil {
			return nil, fmt.Errorf("decode unit cost of %s: %w", row.TransactionID, err)
		}
		out = append(out, PublisherTransaction{
			TransactionID:      row.TransactionID,
			CreatedAt:          row.CreatedAt.Time.UTC(),
			ResourceID:         row.ResourceID,
			ResourceURI:        row.ResourceUri,
			ResourceTitle:      row.ResourceTitle,
			AgentID:            row.AgentID,
			UnitCost:           cost,
			Currency:           row.Currency,
			ObligationState:    ObligationState(row.ObligationState),
			ObligationDeadline: utcTimeFromPG(row.ObligationDeadline),
		})
	}
	return out, nil
}

// transactionRows reads one page of transaction rows, newest first. A page
// before the key is read oldest first -- the rows nearest the key are the ones
// it holds -- and turned around. The backward query takes and returns exactly
// the forward one's shapes, so each converts to the other.
func (r *publisherScopedReads) transactionRows(
	ctx context.Context, p sqlc.ListPublisherTransactionsPageParams, before bool,
) ([]sqlc.ListPublisherTransactionsPageRow, error) {
	if !before {
		return r.q.ListPublisherTransactionsPage(ctx, p)
	}
	back, err := r.q.ListPublisherTransactionsPageBefore(ctx, sqlc.ListPublisherTransactionsPageBeforeParams(p))
	if err != nil {
		return nil, err
	}
	return newestFirst(back, func(row sqlc.ListPublisherTransactionsPageBeforeRow) sqlc.ListPublisherTransactionsPageRow {
		return sqlc.ListPublisherTransactionsPageRow(row)
	}), nil
}

// stateTexts is states as the query's text array takes them. It is never nil:
// a nil slice would be sent as NULL, and a state compared with NULL matches
// nothing by accident rather than by the empty list it stands for.
func stateTexts(states []ObligationState) []string {
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, string(s))
	}
	return out
}

func (r *publisherScopedReads) TransactionCounts(ctx context.Context, q TransactionCountQuery) ([]int64, error) {
	starts := make([]pgtype.Timestamptz, 0, len(q.BucketStarts))
	for _, s := range q.BucketStarts {
		starts = append(starts, pgTime(s))
	}
	rows, err := r.q.CountPublisherTransactionsByBucket(ctx, sqlc.CountPublisherTransactionsByBucketParams{
		BucketStarts: starts,
		TenantID:     q.TenantID,
		PublisherID:  q.PublisherID,
		Currency:     q.Currency,
		PeriodStart:  pgTime(q.From),
		PeriodEnd:    pgTime(q.To),
		ResourceID:   pgText(q.ResourceID),
		Domain:       pgText(q.Domain),
	})
	if err != nil {
		return nil, fmt.Errorf("count publisher transactions: %w", err)
	}
	counts := make([]int64, len(q.BucketStarts))
	for _, row := range rows {
		// width_bucket numbers the buckets from 1.
		i := int(row.Bucket) - 1
		if i < 0 || i >= len(counts) {
			return nil, fmt.Errorf("count publisher transactions: bucket %d is outside the %d requested",
				row.Bucket, len(counts))
		}
		counts[i] = row.Transactions
	}
	return counts, nil
}

func (r *publisherScopedReads) ScopedBillingIDs(
	ctx context.Context, q BillingScopeQuery,
) (map[string]bool, error) {
	ids, err := r.q.PublisherScopedBillingIDs(ctx, sqlc.PublisherScopedBillingIDsParams{
		TenantID:    q.TenantID,
		PublisherID: q.PublisherID,
		ResourceID:  pgText(q.ResourceID),
		Domain:      pgText(q.Domain),
		BillingIds:  q.BillingIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("publisher scoped billing ids: %w", err)
	}
	owned := make(map[string]bool, len(ids))
	for _, id := range ids {
		owned[id] = true
	}
	return owned, nil
}

func (r *publisherScopedReads) OwnsTransaction(
	ctx context.Context, tenantID, publisherID, transactionID string,
) (bool, error) {
	ok, err := r.q.PublisherOwnsTransaction(ctx, sqlc.PublisherOwnsTransactionParams{
		TenantID:      tenantID,
		PublisherID:   publisherID,
		TransactionID: transactionID,
	})
	if err != nil {
		return false, fmt.Errorf("publisher owns transaction: %w", err)
	}
	return ok, nil
}

func (r *publisherScopedReads) Page(ctx context.Context, q PublisherCatalogQuery) ([]PublisherResource, error) {
	keyAt, keyID := keysetParams(q.Key)
	rows, err := r.catalogRows(ctx, sqlc.ListPublisherCatalogPageParams{
		TenantID:      q.TenantID,
		PublisherID:   q.PublisherID,
		KeyCreatedAt:  keyAt,
		KeyResourceID: keyID,
		ResourceID:    pgText(q.ResourceID),
		Domain:        pgText(q.Domain),
		RowLimit:      q.Limit,
	}, q.Before)
	if err != nil {
		return nil, fmt.Errorf("list publisher catalog: %w", err)
	}
	out := make([]PublisherResource, 0, len(rows))
	for _, row := range rows {
		out = append(out, PublisherResource{
			ResourceID: row.ResourceID,
			URI:        row.Uri,
			Title:      textFromPG(row.Title),
			Terms:      row.Terms,
			CreatedAt:  row.CreatedAt.Time.UTC(),
			UpdatedAt:  row.UpdatedAt.Time.UTC(),
		})
	}
	return out, nil
}

func (r *publisherScopedReads) Resource(
	ctx context.Context, q PublisherResourceQuery,
) (PublisherResource, error) {
	row, err := r.q.GetPublisherCatalogResource(ctx, sqlc.GetPublisherCatalogResourceParams{
		TenantID:    q.TenantID,
		PublisherID: q.PublisherID,
		ResourceID:  q.ResourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PublisherResource{}, ErrPublisherResourceNotFound
	}
	if err != nil {
		return PublisherResource{}, fmt.Errorf("get publisher catalog resource: %w", err)
	}
	return PublisherResource{
		ResourceID: row.ResourceID,
		URI:        row.Uri,
		Title:      textFromPG(row.Title),
		Terms:      row.Terms,
		CreatedAt:  row.CreatedAt.Time.UTC(),
		UpdatedAt:  row.UpdatedAt.Time.UTC(),
	}, nil
}

// catalogRows reads one page of catalog rows, newest first, the way
// transactionRows reads transactions.
func (r *publisherScopedReads) catalogRows(
	ctx context.Context, p sqlc.ListPublisherCatalogPageParams, before bool,
) ([]sqlc.ListPublisherCatalogPageRow, error) {
	if !before {
		return r.q.ListPublisherCatalogPage(ctx, p)
	}
	back, err := r.q.ListPublisherCatalogPageBefore(ctx, sqlc.ListPublisherCatalogPageBeforeParams(p))
	if err != nil {
		return nil, err
	}
	return newestFirst(back, func(row sqlc.ListPublisherCatalogPageBeforeRow) sqlc.ListPublisherCatalogPageRow {
		return sqlc.ListPublisherCatalogPageRow(row)
	}), nil
}

// newestFirst turns rows a backward query read oldest first around, each in
// the forward query's shape.
func newestFirst[B, F any](back []B, forward func(B) F) []F {
	out := make([]F, len(back))
	for i, row := range back {
		out[len(back)-1-i] = forward(row)
	}
	return out
}

// keysetParams encodes an optional keyset as the two nullable query arguments.
// A nil keyset is two NULLs, which the queries read as "first page".
func keysetParams(k *Keyset) (pgtype.Timestamptz, pgtype.Text) {
	if k == nil {
		return pgtype.Timestamptz{}, pgtype.Text{}
	}
	return pgTime(k.CreatedAt), pgtype.Text{String: k.ID, Valid: true}
}

// utcTimeFromPG is timeFromPG with the instant in UTC. pgx returns a
// timestamptz in the process's local zone; the publisher reads answer in UTC
// throughout, so a row's times and the period and bucket bounds beside them
// share one zone.
func utcTimeFromPG(t pgtype.Timestamptz) *time.Time {
	v := timeFromPG(t)
	if v == nil {
		return nil
	}
	return new(v.UTC())
}
