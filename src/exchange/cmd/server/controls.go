package main

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sor"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// controlHooks are what the e2e control service (ADR-028 D6) adds to an Exchange:
// per-tenant lifetimes the service reads, and the admin routes that set them, the
// tenant relay and agent-activation policies, and an account's active flag.
//
// e2eControls builds them. It has two definitions selected by the e2e build tag:
// controls_e2e.go builds the control service, and controls_production.go, which
// every production build compiles, returns the zero value. The control package
// is imported only by the tagged file, so a production binary cannot contain it.
type controlHooks struct {
	lifetimes   service.Lifetimes
	adminRoutes []transport.AdminRoute
}

// controlDeps are what the control service acts on. A production build ignores
// them.
type controlDeps struct {
	pool    *pgxpool.Pool
	queries sqlc.Querier
	// accounts is the reference SoR's own store, not the cached adapter the
	// Exchange reads through: the control models the operator's out-of-band
	// write of an account's active flag.
	accounts *sor.PostgresAdapter
}
