package main

import (
	"context"
	"errors"
	"log/slog"
	"math/big"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// defaultTenantDomain resolves the single default tenant's domain the Register
// flow reads its activation policy from: EXCHANGE_DEFAULT_TENANT, falling back to
// EXCHANGE_DOMAIN when the operator's tenant domain and the Exchange domain
// coincide (ADR-021 §5 decision 1).
func defaultTenantDomain() string {
	return runhttp.EnvOr("EXCHANGE_DEFAULT_TENANT", exchangeDomain())
}

// bootTenantConfig runs the default-tenant boot steps: surface a missing
// default tenant early (warns and continues — a deployment may seed tenants
// after the process starts) and parse the EXCHANGE_DEFAULT_AGENT_CREDIT
// configuration (unset means no grant), whose malformed values fail boot. The
// parsed credits are returned for ExchangeConfig.DefaultAgentCredits; nothing is
// written to the seat, so the credit applies whenever the seat is created.
//
// ledgerCurrencies are the currencies the selected billing backend bills in. A
// welcome credit configured in any other currency fails boot: the grant would
// be rejected by the billing gate at the first Register, so the operator hears
// about it at startup instead of from a failing registration.
func bootTenantConfig(
	ctx context.Context, logger *slog.Logger, queries *sqlc.Queries, ledgerCurrencies []string,
) (map[string]*big.Rat, error) {
	warnIfDefaultTenantMissing(ctx, logger, repo.NewTenantReadRepo(queries))
	return loadDefaultAgentCredits(logger, ledgerCurrencies)
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
