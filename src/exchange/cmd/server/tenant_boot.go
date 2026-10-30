package main

import (
	"context"
	"errors"
	"log/slog"
	"math/big"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// defaultTenantDomain resolves the single default tenant's domain the Register
// flow reads its activation policy from: EXCHANGE_DEFAULT_TENANT, falling back to
// EXCHANGE_DOMAIN when the operator's tenant domain and the Exchange domain
// coincide (ADR-021 §5 decision 1).
func defaultTenantDomain() string {
	return runhttp.EnvOr("EXCHANGE_DEFAULT_TENANT", exchangeDomain())
}

// tenantBoot is what the tenant boot step acts on.
type tenantBoot struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
	// keys are the signing keys this Exchange holds; the boot fixture's seats
	// must name an Ed25519 key among them.
	keys service.BootKeys
	// ledgerCurrencies are the currencies the selected billing backend bills in.
	ledgerCurrencies []string
	// fixture is the boot fixture's plan, nil when none is configured.
	fixture *service.BootPlan
}

// bootTenantConfig runs the tenant boot steps, in this order. It parses the
// EXCHANGE_DEFAULT_AGENT_CREDIT configuration (unset means no grant), whose
// malformed values fail boot. It then creates what the boot fixture declares and
// is not stored yet, if one is configured, and finally surfaces a missing
// default tenant (warns and continues — a deployment may seed tenants after the
// process starts). Credits come first so a bad credit setting stops the boot
// before the fixture writes anything; the fixture comes before the warning so
// the warning reports what the fixture left. The parsed credits are returned for
// ExchangeConfig.DefaultAgentCredits; nothing is written to the seat, so the
// credit applies whenever the seat is created.
//
// A welcome credit configured in a currency the billing backend does not bill in
// fails boot: the grant would be rejected by the billing gate at the first
// Register, so the operator hears about it at startup instead of from a failing
// registration.
func bootTenantConfig(ctx context.Context, logger *slog.Logger, b tenantBoot) (map[string]*big.Rat, error) {
	credits, err := loadDefaultAgentCredits(logger, b.ledgerCurrencies)
	if err != nil {
		return nil, err
	}
	if err := applyBootFixture(ctx, logger, b); err != nil {
		return nil, err
	}
	warnIfDefaultTenantMissing(ctx, logger, repo.NewTenantReadRepo(b.queries))
	return credits, nil
}

// warnIfDefaultTenantMissing probes the configured default tenant at boot and
// logs a clear warning if it is not seeded yet. It deliberately does NOT fail
// boot: a deployment may seed tenants after the process starts, and the first
// Register then succeeds. Without this probe an unseeded default tenant surfaces
// only as an error on the first Register; the warning makes the setup gap
// visible at startup instead.
func warnIfDefaultTenantMissing(ctx context.Context, logger *slog.Logger, tenants repo.TenantReadRepo) {
	domain := defaultTenantDomain()
	_, err := tenants.ByDomain(ctx, domain)
	switch {
	case err == nil:
		return
	case errors.Is(err, repo.ErrTenantNotFound):
		logger.Warn("default tenant not found yet — Register will fail until it is seeded",
			"default_tenant_domain", domain)
	default:
		logger.Warn("could not verify default tenant at boot",
			"default_tenant_domain", domain, "err", err)
	}
}
