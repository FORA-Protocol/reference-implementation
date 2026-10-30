//go:build e2e

package e2econtrol

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// SubscriptionsPath is the seat's subscription records: the trust anchors a
// subscription-gated term's delegation chain is verified against. PUT creates
// the record for the body's scope or, when the seat already has one for that
// scope, replaces its issuer identity and trusted root keys, in one repository
// write the database decides between (the (tenant, scope) constraint); it
// answers with the row that write returned. A seat that does not exist is 404,
// and a body that is not {scope, issuer_identity, issuer_keys} with a concrete
// scope and raw 32-byte Ed25519 keys in base64url is 400.
//
// No admin RPC writes a subscription record yet; the administration plane is
// owned by later work. Both of its states are production states, so setting it
// weakens no check: a record only names whose chains the seat trusts for one
// scope, and every chain is still verified in full.
const SubscriptionsPath = "/e2e/v1/tenants/{tenant}/subscriptions"

// subscriptionBody is the PUT body and the answer's shape.
type subscriptionBody struct {
	Scope          string   `json:"scope"`
	IssuerIdentity string   `json:"issuer_identity"`
	IssuerKeys     []string `json:"issuer_keys"`
	RecordID       string   `json:"record_id,omitempty"`
}

// subscriptions serves SubscriptionsPath over the subscription trust repository.
type subscriptions struct {
	tenants repo.TenantReadRepo
	trust   repo.SubscriptionTrustRepo
}

func (s subscriptions) routes() []transport.AdminRoute {
	return []transport.AdminRoute{{Pattern: "PUT " + SubscriptionsPath, Handler: http.HandlerFunc(s.servePut)}}
}

func (s subscriptions) servePut(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := pathValue(w, r, "tenant")
	if !ok {
		return
	}
	ctx := r.Context()
	if _, err := seatByID(ctx, s.tenants, tenantID); err != nil {
		s.failure("_no_seat").failed(w, r, tenantID, err)
		return
	}
	var body subscriptionBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "the body must be {scope, issuer_identity, issuer_keys}", http.StatusBadRequest)
		return
	}
	keys, err := decodeKeys(body.IssuerKeys)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rec, err := s.trust.Put(ctx, repo.SubscriptionRecord{
		TenantID: tenantID, Scope: body.Scope, IssuerIdentity: body.IssuerIdentity, IssuerKeys: keys,
	})
	if err != nil {
		// A scope or key the repository refuses is the caller's mistake, 400;
		// anything else is the store failing, 500.
		if errors.Is(err, repo.ErrSubscriptionRecordInvalid) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.failure("_failed").failed(w, r, tenantID, err)
		return
	}
	reqctx.FromContext(ctx).InfoContext(ctx, "exchange.e2e_control.subscription_set",
		slog.String("tenant_id", tenantID), slog.String("scope", rec.Scope), slog.String("record_id", rec.ID))
	stored := make([]string, 0, len(rec.IssuerKeys))
	for _, k := range rec.IssuerKeys {
		stored = append(stored, base64.RawURLEncoding.EncodeToString(k))
	}
	writeJSON(w, subscriptionBody{
		Scope: rec.Scope, IssuerIdentity: rec.IssuerIdentity, IssuerKeys: stored, RecordID: rec.ID,
	})
}

func (s subscriptions) failure(suffix string) subjectFailure {
	return subjectFailure{
		pathKey: "tenant", event: "exchange.e2e_control.subscription" + suffix, subject: "tenant_id",
		what: "the subscription record could not be written",
	}
}

// decodeKeys reads base64url (no padding) raw Ed25519 public keys.
func decodeKeys(encoded []string) ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, len(encoded))
	for _, k := range encoded {
		raw, err := base64.RawURLEncoding.DecodeString(k)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, errors.New("issuer_keys must be base64url raw 32-byte Ed25519 public keys")
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys, nil
}
