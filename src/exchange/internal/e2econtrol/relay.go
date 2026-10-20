//go:build e2e

package e2econtrol

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
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

type relayPolicyBody struct {
	TenantID         string `json:"tenant_id,omitempty"`
	AllowBrokerRelay *bool  `json:"allow_broker_relay"`
}

// relayPolicy serves RelayPolicyPath over the tenant repositories.
type relayPolicy struct {
	tenants repo.TenantReadRepo
	writes  repo.TenantRelayPolicyRepo
}

func (p relayPolicy) serveGet(w http.ResponseWriter, r *http.Request) {
	tenant, ok := tenantOf(w, r)
	if !ok {
		return
	}
	p.answer(w, r, tenant)
}

func (p relayPolicy) servePut(w http.ResponseWriter, r *http.Request) {
	tenant, ok := tenantOf(w, r)
	if !ok {
		return
	}
	var body relayPolicyBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.AllowBrokerRelay == nil {
		http.Error(w, "the body must be {allow_broker_relay}", http.StatusBadRequest)
		return
	}
	logger := reqctx.FromContext(r.Context())
	updated, err := p.writes.SetAllowBrokerRelay(r.Context(), tenant, *body.AllowBrokerRelay)
	if err != nil {
		logger.ErrorContext(r.Context(), "exchange.e2e_control.relay_policy_failed",
			slog.String("tenant_id", tenant), "err", err)
		http.Error(w, "the relay policy could not be written", http.StatusInternalServerError)
		return
	}
	if updated == 0 {
		http.Error(w, "no such tenant", http.StatusNotFound)
		return
	}
	logger.InfoContext(r.Context(), "exchange.e2e_control.relay_policy_set",
		slog.String("tenant_id", tenant), slog.Bool("allow_broker_relay", *body.AllowBrokerRelay))
	p.answer(w, r, tenant)
}

// answer reads the stored policy back, so the caller sees what the Exchange
// will act on, not an echo of its own request.
func (p relayPolicy) answer(w http.ResponseWriter, r *http.Request, tenant string) {
	stored, err := p.tenants.ByID(r.Context(), tenant)
	switch {
	case errors.Is(err, repo.ErrTenantNotFound):
		http.Error(w, "no such tenant", http.StatusNotFound)
		return
	case err != nil:
		reqctx.FromContext(r.Context()).ErrorContext(r.Context(), "exchange.e2e_control.relay_policy_read_failed",
			slog.String("tenant_id", tenant), "err", err)
		http.Error(w, "the relay policy could not be read", http.StatusInternalServerError)
		return
	}
	allow := stored.AllowBrokerRelay
	writeJSON(w, relayPolicyBody{TenantID: tenant, AllowBrokerRelay: &allow})
}
