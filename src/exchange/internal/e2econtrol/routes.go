//go:build e2e

package e2econtrol

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// LifetimesPath is the per-tenant lifetimes resource. GET reads what the tenant's
// offers and URLs are issued with, PUT replaces its overrides and DELETE clears
// them. Each answers with the lifetimes now in force.
const LifetimesPath = "/e2e/v1/tenants/{tenant}/lifetimes"

// maxBodyBytes bounds a PUT body; the message is two integers.
const maxBodyBytes = 1 << 10

// lifetimesBody is the wire form of a tenant's lifetimes, in whole seconds. On a
// PUT a zero or absent member states no override for that lifetime.
type lifetimesBody struct {
	TenantID        string `json:"tenant_id,omitempty"`
	OfferTTLSeconds int64  `json:"offer_ttl_seconds"`
	URLTTLSeconds   int64  `json:"url_ttl_seconds"`
}

// Deps are what the control routes act on.
type Deps struct {
	// Lifetimes holds the per-tenant lifetime overrides the Exchange reads.
	Lifetimes *Lifetimes
	// Tenants reads a seat back, and RelayPolicy writes its relay opt-in.
	Tenants     repo.TenantReadRepo
	RelayPolicy repo.TenantRelayPolicyRepo
}

// Routes are the control routes, for the admin listener.
func Routes(d Deps) []transport.AdminRoute {
	l := d.Lifetimes
	relay := relayPolicy{tenants: d.Tenants, writes: d.RelayPolicy}
	return []transport.AdminRoute{
		{Pattern: "GET " + LifetimesPath, Handler: http.HandlerFunc(l.serveGet)},
		{Pattern: "PUT " + LifetimesPath, Handler: http.HandlerFunc(l.servePut)},
		{Pattern: "DELETE " + LifetimesPath, Handler: http.HandlerFunc(l.serveDelete)},
		{Pattern: "GET " + RelayPolicyPath, Handler: http.HandlerFunc(relay.serveGet)},
		{Pattern: "PUT " + RelayPolicyPath, Handler: http.HandlerFunc(relay.servePut)},
	}
}

func (l *Lifetimes) serveGet(w http.ResponseWriter, r *http.Request) {
	tenant, ok := tenantOf(w, r)
	if !ok {
		return
	}
	l.answer(w, tenant)
}

func (l *Lifetimes) servePut(w http.ResponseWriter, r *http.Request) {
	tenant, ok := tenantOf(w, r)
	if !ok {
		return
	}
	var body lifetimesBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, "the body must be {offer_ttl_seconds, url_ttl_seconds}", http.StatusBadRequest)
		return
	}
	pair := Pair{
		Offer: time.Duration(body.OfferTTLSeconds) * time.Second,
		URL:   time.Duration(body.URLTTLSeconds) * time.Second,
	}
	if err := l.validate(pair); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	l.set(tenant, pair)
	reqctx.FromContext(r.Context()).InfoContext(r.Context(), "exchange.e2e_control.lifetimes_set",
		slog.String("tenant_id", tenant),
		slog.Duration("offer_ttl", pair.Offer), slog.Duration("url_ttl", pair.URL))
	l.answer(w, tenant)
}

func (l *Lifetimes) serveDelete(w http.ResponseWriter, r *http.Request) {
	tenant, ok := tenantOf(w, r)
	if !ok {
		return
	}
	l.set(tenant, Pair{})
	reqctx.FromContext(r.Context()).InfoContext(r.Context(), "exchange.e2e_control.lifetimes_cleared",
		slog.String("tenant_id", tenant))
	l.answer(w, tenant)
}

// validate refuses an override that is negative, or not shorter than the
// lifetime the deployment is configured with: a control may shorten a lifetime,
// never extend one.
func (l *Lifetimes) validate(pair Pair) error {
	for _, check := range []struct {
		name                 string
		override, configured time.Duration
	}{
		{"offer_ttl_seconds", pair.Offer, l.configured.Offer},
		{"url_ttl_seconds", pair.URL, l.configured.URL},
	} {
		switch {
		case check.override < 0:
			return fmt.Errorf("%s must not be negative", check.name)
		case check.override > 0 && check.override >= check.configured:
			return fmt.Errorf("%s must be shorter than the configured %s", check.name, check.configured)
		}
	}
	return nil
}

func (l *Lifetimes) answer(w http.ResponseWriter, tenant string) {
	effective := l.Effective(tenant)
	writeJSON(w, lifetimesBody{
		TenantID:        tenant,
		OfferTTLSeconds: int64(effective.Offer / time.Second),
		URLTTLSeconds:   int64(effective.URL / time.Second),
	})
}

// writeJSON answers 200 with body. The encode error is discarded: the status and
// headers are already sent, so nothing could be answered, and the caller sees a
// truncated body as a decode failure.
func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

// maxTenantIDBytes bounds the tenant id a path may name. A tenant id is the
// seat's own opaque text key, so its shape is not checked beyond its length.
const maxTenantIDBytes = 200

// tenantOf reads the tenant id from the path. An id longer than any seat's is
// refused before it can be stored.
func tenantOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	tenant := r.PathValue("tenant")
	if tenant == "" || len(tenant) > maxTenantIDBytes {
		http.Error(w, "the path must name a tenant id", http.StatusBadRequest)
		return "", false
	}
	return tenant, true
}
