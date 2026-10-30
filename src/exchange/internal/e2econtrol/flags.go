//go:build e2e

package e2econtrol

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// errNoSubject reports that the seat or account a control path names does not
// exist. Every route answers it 404.
var errNoSubject = errors.New("e2econtrol: no such subject")

// flagResource serves one boolean the e2e suite sets through this service and no
// admin RPC reaches: a seat's relay policy or activation policy, or an account's
// active flag. GET reads the value back from where the Exchange keeps it; PUT
// replaces it and answers with the value now stored, read back the same way, not
// an echo of its own request. A seat's policies are read from its row on every
// request, so the next Register or purchase acts on the stored value; an account's
// flag reaches a purchase only once the Exchange's read-through cache entry for it
// expires (AccountActivePath). A subject that does not exist is 404, and a PUT
// body that is not exactly {member: true|false} is 400.
type flagResource struct {
	// pathKey is the path wildcard that names the subject.
	pathKey string
	// subject is the JSON member the answer names the subject in.
	subject string
	// member is the JSON member the value travels in.
	member string
	// event prefixes the log events a write emits.
	event string
	// write stores the value; read reads it back. Each returns errNoSubject for
	// a subject that does not exist.
	write func(ctx context.Context, key string, value bool) error
	read  func(ctx context.Context, key string) (bool, error)
}

// routes are the resource's GET and PUT on path.
func (f flagResource) routes(path string) []transport.AdminRoute {
	return []transport.AdminRoute{
		{Pattern: "GET " + path, Handler: http.HandlerFunc(f.serveGet)},
		{Pattern: "PUT " + path, Handler: http.HandlerFunc(f.servePut)},
	}
}

func (f flagResource) serveGet(w http.ResponseWriter, r *http.Request) {
	key, ok := pathValue(w, r, f.pathKey)
	if !ok {
		return
	}
	f.answer(w, r, key)
}

func (f flagResource) servePut(w http.ResponseWriter, r *http.Request) {
	key, ok := pathValue(w, r, f.pathKey)
	if !ok {
		return
	}
	value, ok := decodeFlag(w, r, f.member)
	if !ok {
		return
	}
	ctx := r.Context()
	if f.failure("_failed", "written").failed(w, r, key, f.write(ctx, key, value)) {
		return
	}
	reqctx.FromContext(ctx).InfoContext(ctx, f.event+"_set", slog.String(f.subject, key), slog.Bool(f.member, value))
	f.answer(w, r, key)
}

func (f flagResource) answer(w http.ResponseWriter, r *http.Request, key string) {
	value, err := f.read(r.Context(), key)
	if f.failure("_read_failed", "read").failed(w, r, key, err) {
		return
	}
	writeJSON(w, map[string]any{f.subject: key, f.member: value})
}

// failure is how f answers a store error when its setting could not be done
// (written or read), logged under f.event and suffix.
func (f flagResource) failure(suffix, done string) subjectFailure {
	return subjectFailure{
		pathKey: f.pathKey, event: f.event + suffix, subject: f.subject,
		what: "the " + f.member + " setting could not be " + done,
	}
}

// decodeFlag reads a PUT body that must be exactly {member: true|false}. A
// missing, null or mistyped value, or any other member, is answered 400.
func decodeFlag(w http.ResponseWriter, r *http.Request, member string) (bool, bool) {
	var body map[string]*bool
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&body)
	value, present := body[member]
	if err != nil || len(body) != 1 || !present || value == nil {
		http.Error(w, "the body must be {"+member+"}", http.StatusBadRequest)
		return false, false
	}
	return *value, true
}

// seatFlag is a flagResource over one boolean setting of a seat. write stores it
// and reports the rows it changed, so a seat that does not exist is a detectable
// no-op; field reads it from the seat, which is read back through the tenant
// repository.
func seatFlag(
	member, event string,
	tenants repo.TenantReadRepo,
	write func(ctx context.Context, tenantID string, value bool) (int64, error),
	field func(repo.Tenant) bool,
) flagResource {
	return flagResource{
		pathKey: "tenant",
		subject: "tenant_id",
		member:  member,
		event:   event,
		write: func(ctx context.Context, tenantID string, value bool) error {
			n, err := write(ctx, tenantID, value)
			if err == nil && n == 0 {
				return errNoSubject
			}
			return err
		},
		read: func(ctx context.Context, tenantID string) (bool, error) {
			stored, err := seatByID(ctx, tenants, tenantID)
			return field(stored), err
		},
	}
}
