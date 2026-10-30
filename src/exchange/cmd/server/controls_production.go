//go:build !e2e

package main

import (
	"log/slog"
)

// e2eControls in a production build: no control service, no routes, and every
// tenant issues with the configured lifetimes. See controlHooks.
func e2eControls(*slog.Logger, controlDeps) controlHooks {
	return controlHooks{}
}
