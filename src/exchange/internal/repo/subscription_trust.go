package repo

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/subscriptionscope"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// ErrSubscriptionRecordInvalid is returned when a record's scope fails the
// concrete-scope rule, or when a key offered as a trusted root is not a raw
// 32-byte Ed25519 public key, or when the key list is empty. The wrapped error
// names the clause. One sentinel for every caller's mistake, so a transport maps
// it with one errors.Is.
var ErrSubscriptionRecordInvalid = errors.New("repo: subscription record invalid")

// ErrSubscriptionRecordExists is returned by Create when the seat already holds
// a record for the scope.
var ErrSubscriptionRecordExists = errors.New("repo: subscription record already exists for this scope")

// ErrSubscriptionRecordNotFound is returned by a write that names no record of
// the seat.
var ErrSubscriptionRecordNotFound = errors.New("repo: subscription record not found")

// SubscriptionRecord is one subscription's trust anchor under one seat: the
// scope a gated term names and the root keys a delegation chain for it must be
// rooted in. Ended records keep their keys.
type SubscriptionRecord struct {
	ID             string
	TenantID       string
	Scope          string
	IssuerIdentity string
	IssuerKeys     []ed25519.PublicKey
	EndedAt        *time.Time
}

// SubscriptionTrust is what the verifier reads: the record id for evidence and
// journaling, and the trusted issuers in the shape helpers.VerifyDelegation takes.
type SubscriptionTrust struct {
	RecordID string
	Ended    bool
	Issuers  []helpers.TrustedIssuer
}

// SubscriptionTrustRepo reads and writes subscription records. No request path
// writes one today: Create, Put and End exist for the boot fixture, the e2e
// control plane and tests that arrange trust, key replacement and withdrawal.
// Every write is keyed by the tenant: a record id alone names nothing.
type SubscriptionTrustRepo interface {
	// TrustedIssuersFor returns the trust the record for (tenant, scope) carries,
	// or nil when the seat has no record for that scope. Read per request.
	TrustedIssuersFor(ctx context.Context, tenantID, scope string) (*SubscriptionTrust, error)
	// Create inserts a record and refuses a second one for the same (tenant,
	// scope) with ErrSubscriptionRecordExists. An empty ID is minted here.
	Create(ctx context.Context, rec SubscriptionRecord) (SubscriptionRecord, error)
	// Put creates the seat's record for rec.Scope, or replaces the issuer and keys
	// of the one it holds; a key left out is withdrawn. Returns the stored row.
	Put(ctx context.Context, rec SubscriptionRecord) (SubscriptionRecord, error)
	// End marks the seat's record ended and leaves its keys in place.
	End(ctx context.Context, tenantID, recordID string) error
}

// NewSubscriptionTrustRepo composes a SubscriptionTrustRepo over a sqlc.Querier.
func NewSubscriptionTrustRepo(q sqlc.Querier) SubscriptionTrustRepo {
	return &subscriptionTrustRepo{q: q}
}

type subscriptionTrustRepo struct{ q sqlc.Querier }

func (r *subscriptionTrustRepo) TrustedIssuersFor(
	ctx context.Context, tenantID, scope string,
) (*SubscriptionTrust, error) {
	row, err := r.q.GetSubscriptionRecordByTenantAndScope(ctx, sqlc.GetSubscriptionRecordByTenantAndScopeParams{
		TenantID: tenantID, Scope: scope,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get subscription record: %w", err)
	}
	t := &SubscriptionTrust{
		RecordID: row.RecordID, Ended: row.EndedAt.Valid,
		Issuers: make([]helpers.TrustedIssuer, 0, len(row.IssuerKeys)),
	}
	for _, k := range row.IssuerKeys {
		t.Issuers = append(t.Issuers, helpers.TrustedIssuer{Identity: row.IssuerIdentity, Key: ed25519.PublicKey(k)})
	}
	return t, nil
}

func (r *subscriptionTrustRepo) Create(ctx context.Context, rec SubscriptionRecord) (SubscriptionRecord, error) {
	params, err := insertParams(rec)
	if err != nil {
		return SubscriptionRecord{}, err
	}
	row, err := r.q.InsertSubscriptionRecord(ctx, params)
	if err != nil {
		return SubscriptionRecord{}, mapInsertErr(err, "create subscription record", ErrSubscriptionRecordExists,
			fkSentinel{"subscription_records_tenant_fk", ErrTenantNotFound})
	}
	return subscriptionRecordFromRow(row), nil
}

func (r *subscriptionTrustRepo) Put(ctx context.Context, rec SubscriptionRecord) (SubscriptionRecord, error) {
	params, err := insertParams(rec)
	if err != nil {
		return SubscriptionRecord{}, err
	}
	row, err := r.q.UpsertSubscriptionRecord(ctx, sqlc.UpsertSubscriptionRecordParams(params))
	if err != nil {
		return SubscriptionRecord{}, mapInsertErr(err, "put subscription record", nil,
			fkSentinel{"subscription_records_tenant_fk", ErrTenantNotFound})
	}
	return subscriptionRecordFromRow(row), nil
}

func (r *subscriptionTrustRepo) End(ctx context.Context, tenantID, recordID string) error {
	n, err := r.q.EndSubscriptionRecord(ctx, sqlc.EndSubscriptionRecordParams{TenantID: tenantID, RecordID: recordID})
	if err != nil {
		return fmt.Errorf("end subscription record: %w", err)
	}
	if n == 0 {
		return ErrSubscriptionRecordNotFound
	}
	return nil
}

// insertParams validates the record and mints its id when it has none. The scope
// must satisfy the concrete-scope rule; every key must be a raw Ed25519 public
// key and the list must not be empty, because a record with no key would gate
// its term behind nothing.
func insertParams(rec SubscriptionRecord) (sqlc.InsertSubscriptionRecordParams, error) {
	if err := subscriptionscope.Validate(rec.Scope); err != nil {
		return sqlc.InsertSubscriptionRecordParams{}, fmt.Errorf("%w: %w", ErrSubscriptionRecordInvalid, err)
	}
	if len(rec.IssuerKeys) == 0 {
		return sqlc.InsertSubscriptionRecordParams{}, fmt.Errorf("%w: no issuer key", ErrSubscriptionRecordInvalid)
	}
	keys := make([][]byte, 0, len(rec.IssuerKeys))
	for _, k := range rec.IssuerKeys {
		if len(k) != ed25519.PublicKeySize {
			return sqlc.InsertSubscriptionRecordParams{}, fmt.Errorf(
				"%w: issuer key must be a raw Ed25519 public key", ErrSubscriptionRecordInvalid)
		}
		keys = append(keys, []byte(k))
	}
	id := rec.ID
	if id == "" {
		id = "sub_" + uuid.NewString()
	}
	return sqlc.InsertSubscriptionRecordParams{
		RecordID: id, TenantID: rec.TenantID, Scope: rec.Scope, IssuerIdentity: rec.IssuerIdentity, IssuerKeys: keys,
	}, nil
}

func subscriptionRecordFromRow(row sqlc.ForaSubscriptionRecord) SubscriptionRecord {
	rec := SubscriptionRecord{
		ID: row.RecordID, TenantID: row.TenantID, Scope: row.Scope, IssuerIdentity: row.IssuerIdentity,
		IssuerKeys: make([]ed25519.PublicKey, 0, len(row.IssuerKeys)), EndedAt: timeFromPG(row.EndedAt),
	}
	for _, k := range row.IssuerKeys {
		rec.IssuerKeys = append(rec.IssuerKeys, ed25519.PublicKey(k))
	}
	return rec
}
