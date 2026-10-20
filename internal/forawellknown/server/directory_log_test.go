package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	logtest "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// failingSigner holds the keyid of the key the directory lists but cannot sign,
// the way a key custody that is down cannot.
type failingSigner struct{ keyID string }

func (s failingSigner) KeyID() string     { return s.keyID }
func (s failingSigner) Algorithm() string { return helpers.AlgEd25519 }
func (s failingSigner) Sign(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("key custody unavailable")
}

// TestWBAHandler_FailedDirectoryIsLoggedWithTheRequestID drives the two ways a
// key directory is answered 500 through the request-id middleware every service
// mounts the handler behind, and reads the one log line each leaves. The line
// carries the request's id, so an operator can tie it to the request. Its level
// tells the cause apart: a signer that fails is the service's own fault, an
// error; a request that names no host is the caller's doing and any client can
// send one, so it is a warning.
func TestWBAHandler_FailedDirectoryIsLoggedWithTheRequestID(t *testing.T) {
	t.Parallel()
	_, key := testutil.NewSigningKey("sign-fail", anchor.Add(-time.Hour), anchor.Add(time.Hour))
	h, err := server.NewWBAHandler(server.WBAConfig{
		Keys:    server.StaticKeys(key),
		Signers: []helpers.Signer{failingSigner{keyID: testutil.MustThumbprintKey(t, key)}},
	})
	if err != nil {
		t.Fatalf("NewWBAHandler: %v", err)
	}
	for _, tc := range []struct {
		name, host, requestID, level string
	}{
		{name: "the signer fails", host: "exchange.example", requestID: "wk-sign-fail-1", level: "ERROR"},
		{name: "the request names no host", host: "", requestID: "wk-no-host-1", level: "WARN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			capture, logger := logtest.NewLogCapture()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, forawellknown.WBAPath, nil)
			req.Host = tc.host
			req.Header.Set(helpers.RequestIDHeader, tc.requestID)
			rec := httptest.NewRecorder()
			reqctx.RequestIDMiddleware(logger, h).ServeHTTP(rec, req)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
			}
			logged := capture.AssertLoggedOnce(t, "wellknown.directory_sign_failed", tc.level)
			if logged["request_id"] != tc.requestID {
				t.Errorf("request_id = %v, want %q: %v", logged["request_id"], tc.requestID, logged)
			}
		})
	}
}
