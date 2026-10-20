package main

import (
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// controlHooks are what the e2e control service (ADR-028 D6) adds to an Exchange:
// per-tenant lifetimes the service reads, and the admin routes that set them and
// the tenant relay policy.
//
// e2eControls builds them. It has two definitions selected by the e2e build tag:
// controls_e2e.go builds the control service, and controls_production.go, which
// every production build compiles, returns the zero value. The control package
// is imported only by the tagged file, so a production binary cannot contain it.
type controlHooks struct {
	lifetimes   service.Lifetimes
	adminRoutes []transport.AdminRoute
}
