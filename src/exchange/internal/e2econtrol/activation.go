//go:build e2e

package e2econtrol

import (
	"context"

	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// ActivationPolicyPath is the seat's agent-activation policy
// (tenants.activate_new_agents_by_default): whether an agent's first Register
// opens an active account, or one held for the operator's review. GET reads it
// and PUT replaces it; each answers with the policy now stored, read back through
// the tenant repository. A tenant that does not exist is 404.
//
// No admin RPC writes the policy: a deployment sets it when it creates the seat.
// The e2e seed holds every seat for review while it opens the credit-less buyer's
// accounts, so they open inactive and are granted no welcome credit, and then
// puts the seats back. Both values are production states, so setting the policy
// weakens no check. It decides only how an account starts; the Exchange still
// refuses a purchase on an inactive account.
const ActivationPolicyPath = "/e2e/v1/tenants/{tenant}/agent-activation"

// ActivationWriter is the one write the activation control needs. The tenant
// write port's setter takes a transaction, so the control runs it in one of its
// own.
type ActivationWriter interface {
	SetActivateNewAgentsByDefault(ctx context.Context, tx pgx.Tx, tenantID string, active bool) (int64, error)
}

// agentActivation serves ActivationPolicyPath over the tenant repositories.
func agentActivation(tenants repo.TenantReadRepo, writes ActivationWriter, runner db.TxRunner) flagResource {
	return seatFlag("activate_new_agents_by_default", "exchange.e2e_control.agent_activation", tenants,
		func(ctx context.Context, tenantID string, active bool) (int64, error) {
			var updated int64
			err := runner.WithTx(ctx, func(tx pgx.Tx) error {
				var err error
				updated, err = writes.SetActivateNewAgentsByDefault(ctx, tx, tenantID, active)
				return err
			})
			return updated, err
		},
		func(t repo.Tenant) bool { return t.ActivateNewAgentsByDefault },
	)
}
