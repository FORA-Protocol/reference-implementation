//go:build !e2e

package main

import (
	"log/slog"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// e2eControls in a production build: no control service, no routes, and every
// tenant issues with the configured lifetimes. See controlHooks.
func e2eControls(*slog.Logger, sqlc.Querier) controlHooks {
	return controlHooks{}
}
