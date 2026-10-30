//go:build e2e

package e2econtrol

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// RelayPolicyPath is the tenant relay policy resource: whether the seat accepts
// purchases a Broker relays (tenants.allow_broker_relay). GET reads it and PUT
// replaces it; each answers with the policy now stored, read back through the
// tenant repository. A tenant that does not exist is 404.
//
// No admin RPC writes the policy, so before this route a test could set it only
// by writing the database. The policy is a seller's choice and both of its values
// are production states, so setting it weakens no check: with it off, the
// Exchange refuses relayed purchases, and with it on, it still verifies the
// Broker's signature and every agent acceptance.
const RelayPolicyPath = "/e2e/v1/tenants/{tenant}/relay-policy"

// relayPolicy serves RelayPolicyPath over the tenant repositories.
func relayPolicy(tenants repo.TenantReadRepo, writes repo.TenantRelayPolicyRepo) flagResource {
	return seatFlag("allow_broker_relay", "exchange.e2e_control.relay_policy", tenants,
		func(ctx context.Context, tenantID string, allow bool) (int64, error) {
			return writes.SetAllowBrokerRelay(ctx, tenantID, allow)
		},
		func(t repo.Tenant) bool { return t.AllowBrokerRelay },
	)
}
