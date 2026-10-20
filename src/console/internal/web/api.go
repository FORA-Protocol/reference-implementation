// The JSON routes the add-a-domain wizard calls, and the plumbing they share.
// The pages themselves are still ordinary server-rendered HTML; what moved to
// JSON is the wizard, because it holds the publisher's answers in the browser
// until the last step, and only that step's buttons save anything.

package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

// The wizard's routes. They are grouped under one prefix so the composition
// root can gate them as a set: every one of them answers in JSON, so every one
// of them needs the refusals that answer in JSON too.
const (
	APIPrefix = "/api/"

	APIPublisherPath     = "/api/publisher"
	APILookupPath        = "/api/domain/lookup"
	APIFilesPath         = "/api/domain/files"
	APIFilePath          = "/api/domain/file"
	APICheckManifestPath = "/api/domain/check-manifest"
	APICheckEdgePath     = "/api/domain/check-edge"
	APIPagePath          = "/api/domain/page"
	APITermsPreviewPath  = "/api/domain/terms-preview"
	APIRegisterPath      = "/api/domain"
	APIProgressPath      = "/api/domain/progress"
)

// WizardAPI answers the wizard's JSON routes. Each method decodes one request,
// makes one call to the wizard's services and encodes its answer; what the
// answer is, is the services' to decide.
type WizardAPI struct {
	wizard  *wizard.Service
	resumer *wizard.Resumer
}

// NewWizardAPI builds the wizard's JSON routes over the wizard and the
// reopening of saved domains.
func NewWizardAPI(svc *wizard.Service, resumer *wizard.Resumer) (*WizardAPI, error) {
	switch {
	case svc == nil:
		return nil, errors.New("web: the wizard is required")
	case resumer == nil:
		return nil, errors.New("web: the reopening of saved domains is required")
	}
	return &WizardAPI{wizard: svc, resumer: resumer}, nil
}

// maxAPIBodyBytes bounds what one wizard call can make the console read. The
// largest of these bodies is a hostname, a network, a handful of paths and the
// licensing answers.
const maxAPIBodyBytes = 16 * 1024

// apiError is what every refusal looks like, so the wizard has one place to
// read the sentence it shows the publisher.
type apiError struct {
	Error string `json:"error"`
}

// readAPI decodes the request's body. Where the request came from is checked
// before any route sees it: the console refuses a state-changing request that
// another site's page caused.
//
// Unknown fields are refused rather than ignored. The browser and this package
// ship together, so a field neither side expects is a bug in one of them, and
// the version where it is silently dropped is the one that is hard to find.
func readAPI(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAPIBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeAPIError(w, http.StatusBadRequest, "That request could not be read.")
		return false
	}
	return true
}

// writeAPI sends one answer.
func writeAPI(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		reqctx.FromContext(r.Context()).ErrorContext(r.Context(), "console.api_encode_failed",
			"path", r.URL.Path, "err", err)
	}
}

// writeAPIError sends one refusal.
func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(mustJSON(apiError{Error: message}))
}

// mustJSON encodes a value that cannot fail to encode: apiError is one string.
func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"error":"internal error"}`)
	}
	return raw
}

// apiFailure answers a call that did not succeed, with the status and words a
// page would show for the same failure (see renderFailure).
func apiFailure(w http.ResponseWriter, r *http.Request, err error) {
	if re, ok := errors.AsType[*refusal.Error](err); ok {
		writeAPIError(w, refusalStatus(re.Kind), re.Message)
		return
	}
	_, message := failureText(err)
	reqctx.FromContext(r.Context()).ErrorContext(r.Context(), "console.exchange_call_failed",
		"path", r.URL.Path, "err", err)
	writeAPIError(w, statusFor(err), message)
}

// APIUnauthorized answers a wizard call made without a session.
func APIUnauthorized(w http.ResponseWriter, _ *http.Request) {
	writeAPIError(w, http.StatusUnauthorized, "Your session has ended. Please sign in again.")
}

// APICrossOrigin answers a wizard call that another site's page caused. The
// console's own pages never make one, so nothing was changed and there is
// nothing for the person to repair.
func APICrossOrigin(w http.ResponseWriter, _ *http.Request) {
	writeAPIError(w, http.StatusForbidden,
		"That request came from another site, so nothing was changed.")
}

// APIEmailUnverified answers a wizard call from someone whose address the login
// provider has not checked. The Exchange refuses such an actor on a grant, so
// there is nothing this call could do for them.
func APIEmailUnverified(w http.ResponseWriter, _ *http.Request) {
	writeAPIError(w, http.StatusForbidden,
		"Your login provider has not confirmed your email address yet.")
}
