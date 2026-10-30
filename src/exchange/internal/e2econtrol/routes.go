//go:build e2e

package e2econtrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// LifetimesPath is the per-tenant lifetimes resource. GET reads what the tenant's
// offers and URLs are issued with, PUT replaces its overrides and DELETE clears
// them. Each answers with the lifetimes now in force. A tenant that does not
// exist is 404, checked before a PUT's body is read; the boolean controls read
// the body first.
const LifetimesPath = "/e2e/v1/tenants/{tenant}/lifetimes"

// maxBodyBytes bounds a PUT body; the largest message is two integers.
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
	// Tenants reads a seat back. RelayPolicy writes its relay opt-in, and
	// Activation its agent-activation policy, inside a transaction of Tx.
	Tenants     repo.TenantReadRepo
	RelayPolicy repo.TenantRelayPolicyRepo
	Activation  ActivationWriter
	Tx          db.TxRunner
	// Accounts is the reference SoR's store: the operator's out-of-band switch
	// of an account's active flag, and its uncached read.
	Accounts AccountSwitch
	// Subscriptions writes and reads the seat's subscription records.
	Subscriptions repo.SubscriptionTrustRepo
}

// Routes are the control routes, for the admin listener. Building them calls
// nothing on d, so a test that drives only some routes may leave the others'
// dependencies nil. The lifetimes routes read d.Tenants as well as d.Lifetimes:
// each answers only for a path that names a seat (seatOnly).
func Routes(d Deps) []transport.AdminRoute {
	routes := d.Lifetimes.routes()
	for i := range routes {
		routes[i].Handler = seatOnly(d.Tenants, routes[i].Handler)
	}
	routes = append(routes, relayPolicy(d.Tenants, d.RelayPolicy).routes(RelayPolicyPath)...)
	routes = append(routes, agentActivation(d.Tenants, d.Activation, d.Tx).routes(ActivationPolicyPath)...)
	routes = append(routes, subscriptions{tenants: d.Tenants, trust: d.Subscriptions}.routes()...)
	return append(routes, accountActive(d.Accounts).routes(AccountActivePath)...)
}

// routes are the lifetimes resource's GET, PUT and DELETE on LifetimesPath, over
// the overrides alone. Routes puts the seat check in front of each.
func (l *Lifetimes) routes() []transport.AdminRoute {
	return []transport.AdminRoute{
		{Pattern: "GET " + LifetimesPath, Handler: http.HandlerFunc(l.serveGet)},
		{Pattern: "PUT " + LifetimesPath, Handler: http.HandlerFunc(l.servePut)},
		{Pattern: "DELETE " + LifetimesPath, Handler: http.HandlerFunc(l.serveDelete)},
	}
}

// seatOnly serves next only for a path whose {tenant} names a seat the Exchange
// holds. The id is checked by pathValue first, so an empty or over-long one is
// refused 400 before any store is asked; a tenant that does not exist is 404.
func seatOnly(tenants repo.TenantReadRepo, next http.Handler) http.Handler {
	failure := subjectFailure{
		pathKey: "tenant", event: "exchange.e2e_control.seat_read_failed",
		subject: "tenant_id", what: "the seat could not be read",
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant, ok := pathValue(w, r, "tenant")
		if !ok {
			return
		}
		if _, err := seatByID(r.Context(), tenants, tenant); failure.failed(w, r, tenant, err) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// seatByID reads the seat tenantID names. The repository's not-found becomes
// errNoSubject, which every control answers 404.
func seatByID(ctx context.Context, tenants repo.TenantReadRepo, tenantID string) (repo.Tenant, error) {
	seat, err := tenants.ByID(ctx, tenantID)
	if errors.Is(err, repo.ErrTenantNotFound) {
		return repo.Tenant{}, errNoSubject
	}
	return seat, err
}

// subjectFailure is how a control answers a store error for the subject its path
// names: the path wildcard that names it, and the log event, attribute and
// message of a failure.
type subjectFailure struct {
	pathKey string
	event   string
	subject string
	what    string
}

// failed answers err for the subject key and reports whether it answered. A
// subject that does not exist (errNoSubject) is 404; any other error is a 500
// the log records. A nil err answers nothing, and the caller goes on.
func (s subjectFailure) failed(w http.ResponseWriter, r *http.Request, key string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNoSubject):
		http.Error(w, "no such "+s.pathKey, http.StatusNotFound)
	default:
		ctx := r.Context()
		reqctx.FromContext(ctx).ErrorContext(ctx, s.event, slog.String(s.subject, key), "err", err)
		http.Error(w, s.what, http.StatusInternalServerError)
	}
	return true
}

func (l *Lifetimes) serveGet(w http.ResponseWriter, r *http.Request) {
	tenant, ok := pathValue(w, r, "tenant")
	if !ok {
		return
	}
	l.answer(w, tenant)
}

func (l *Lifetimes) servePut(w http.ResponseWriter, r *http.Request) {
	tenant, ok := pathValue(w, r, "tenant")
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
	tenant, ok := pathValue(w, r, "tenant")
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

// maxPathValueBytes bounds the id a path may name: 255 bytes, the admin plane's
// 255-character bound on a tenant id (fora.admin.v1 in the pinned protocol
// module), exact for the ASCII ids the boot fixture's id pattern allows. A
// billing_ref is a 36-character UUID the Exchange mints. Neither id's shape is
// checked beyond its length.
const maxPathValueBytes = 255

// pathValue reads the id the path wildcard key names. An empty id, or one longer
// than maxPathValueBytes, is refused before it reaches a store.
func pathValue(w http.ResponseWriter, r *http.Request, key string) (string, bool) {
	value := r.PathValue(key)
	if value == "" || len(value) > maxPathValueBytes {
		http.Error(w, "the path must name a "+key, http.StatusBadRequest)
		return "", false
	}
	return value, true
}
