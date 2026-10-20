package testutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// CallerGoneServer serves h with every request's context already cancelled,
// which is what net/http does to a request whose client has disconnected. No
// client can time a disconnect deterministically, so a test that needs "the
// caller has gone" sends its request through this server instead: the request
// still travels over real HTTP, and the only arrangement is the one a
// disconnect makes. The server is closed when the test ends.
func CallerGoneServer(tb testing.TB, h http.Handler) *httptest.Server {
	tb.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
	tb.Cleanup(srv.Close)
	return srv
}
