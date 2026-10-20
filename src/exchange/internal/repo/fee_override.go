package repo

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// ErrFeeRateOutOfRange is returned when a fee rate violates the
// 0 <= fee_rate_bps < 10000 CHECK constraint at write time.
var ErrFeeRateOutOfRange = errors.New("repo: fee rate out of range")

// guardFeeRateBps validates a basis-points value before it is written and
// narrows it to the column's int32. Two distinct rejections both surface as
// ErrFeeRateOutOfRange: bps < 0 fast-fails the business CHECK's lower bound
// (0 <= bps) before the round-trip, and bps > math.MaxInt32 is pure column
// representability. The DB CHECK enforces the tighter upper bound (< 10000) for
// representable values. Shared by every fee-rate write port (tenant default and
// per-owner override) so the boundary lives in one place.
func guardFeeRateBps(bps int) (int32, error) {
	if bps < 0 || bps > math.MaxInt32 {
		return 0, ErrFeeRateOutOfRange
	}
	return int32(bps), nil
}

// FeeOverrideRepo reads and writes the per-(tenant, resource_owner) commission
// override. The tenant-level default lives on repo.Tenant (FeeRateBps); a row
// here supersedes it for one resource owner under one tenant, so an aggregator
// tenant can charge a different commission per owner it represents. The rate is
// a server-side commercial term, never on the wire.
type FeeOverrideRepo interface {
	FeeOverrideReadRepo
	// Set upserts the override rate for (tenant, owner). Returns
	// ErrFeeRateOutOfRange when bps violates the CHECK bound.
	Set(ctx context.Context, tenantID, resourceOwnerID string, bps int) error
}

// FeeOverrideReadRepo is the read half of FeeOverrideRepo, for a caller that
// only resolves the rate.
type FeeOverrideReadRepo interface {
	// ByOwner returns the override rate in basis points for (tenant, owner), or
	// nil when no override row exists — the caller then falls back to the tenant
	// default. A missing override is the normal "no override" outcome, not an
	// error.
	ByOwner(ctx context.Context, tenantID, resourceOwnerID string) (*int, error)
}

// NewFeeOverrideRepo composes a FeeOverrideRepo over a sqlc.Querier.
func NewFeeOverrideRepo(q sqlc.Querier) FeeOverrideRepo { return &feeOverrideRepo{q: q} }

type feeOverrideRepo struct{ q sqlc.Querier }

func (r *feeOverrideRepo) ByOwner(ctx context.Context, tenantID, resourceOwnerID string) (*int, error) {
	bps, err := r.q.GetResourceOwnerFeeOverride(ctx, sqlc.GetResourceOwnerFeeOverrideParams{
		TenantID:        tenantID,
		ResourceOwnerID: resourceOwnerID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // no override → caller uses the tenant default
		}
		return nil, fmt.Errorf("get resource-owner fee override: %w", err)
	}
	v := int(bps)
	return &v, nil
}

func (r *feeOverrideRepo) Set(ctx context.Context, tenantID, resourceOwnerID string, bps int) error {
	v, err := guardFeeRateBps(bps)
	if err != nil {
		return err
	}
	err = r.q.UpsertResourceOwnerFeeOverride(ctx, sqlc.UpsertResourceOwnerFeeOverrideParams{
		TenantID:        tenantID,
		ResourceOwnerID: resourceOwnerID,
		FeeRateBps:      v,
	})
	return mapCheckWriteErr(err, "upsert resource-owner fee override", ErrFeeRateOutOfRange)
}
