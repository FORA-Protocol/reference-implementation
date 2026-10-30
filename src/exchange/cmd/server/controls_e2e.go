//go:build e2e

package main

import (
	"log/slog"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/e2econtrol"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// e2eControls in an e2e build: the control service, with the lifetimes the
// Exchange is configured with as the ceiling of every override. See controlHooks.
//
// The warning puts in the boot log of every binary built with the tag that it
// carries the control routes, so an e2e image deployed by mistake shows at once.
func e2eControls(logger *slog.Logger, deps controlDeps) controlHooks {
	lifetimes := e2econtrol.NewLifetimes(e2econtrol.Pair{
		Offer: service.DefaultOfferTTL,
		URL:   service.DefaultURLTTL,
	})
	logger.Warn("exchange.e2e_control.enabled",
		"lifetimes", e2econtrol.LifetimesPath, "relay_policy", e2econtrol.RelayPolicyPath,
		"agent_activation", e2econtrol.ActivationPolicyPath, "account_active", e2econtrol.AccountActivePath,
		"subscriptions", e2econtrol.SubscriptionsPath)
	return controlHooks{
		lifetimes: lifetimes,
		adminRoutes: e2econtrol.Routes(e2econtrol.Deps{
			Lifetimes:     lifetimes,
			Tenants:       repo.NewTenantReadRepo(deps.queries),
			RelayPolicy:   repo.NewTenantRelayPolicyRepo(deps.queries),
			Activation:    repo.NewTenantWriteRepo(deps.queries),
			Tx:            db.PoolRunner{Pool: deps.pool},
			Accounts:      deps.accounts,
			Subscriptions: repo.NewSubscriptionTrustRepo(deps.queries),
		}),
	}
}
