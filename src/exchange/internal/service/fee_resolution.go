package service

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// ResolveFeeRateBps returns the effective platform commission rate in basis
// points for a transaction: the per-(tenant, resource_owner) override when one is
// configured, otherwise the tenant-level default. It is pure and is called at
// Authorize, where the tenant (hence its default rate) is already loaded; the
// resolved rate is then frozen on the billing hold.
//
// An override of 0 is honoured as an explicit "no commission for this owner" and
// is distinct from the absence of an override (override == nil), which falls back
// to the tenant default.
func ResolveFeeRateBps(tenantDefault int, override *int) int {
	if override != nil {
		return *override
	}
	return tenantDefault
}

// FeeRate is the platform commission applied to one resource owner's sales on
// a tenant, and whether it is that owner's own override.
type FeeRate struct {
	Bps        int
	Overridden bool
}

// effectiveFeeRate reads the owner's override on the tenant and resolves it
// against the tenant's default. The charge path and the publisher's terms read
// both call it, so the rate a publisher is shown is the rate its sales are
// charged at.
func effectiveFeeRate(
	ctx context.Context, overrides repo.FeeOverrideReadRepo, tenant repo.Tenant, ownerID string,
) (FeeRate, error) {
	override, err := overrides.ByOwner(ctx, tenant.ID, ownerID)
	if err != nil {
		return FeeRate{}, exchange.Wrap(exchange.KindInternal, err, "resolve fee override")
	}
	return FeeRate{Bps: ResolveFeeRateBps(tenant.FeeRateBps, override), Overridden: override != nil}, nil
}
