package agentreg

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

// TestMapFetchError_SplitsAServedDocumentFromNoDocumentAtAll pins how each way a
// directory read fails lands on one side of IsCallerFault: a permanent caller
// fault (the agent has to publish something else) or a transient failure (the
// next read may return the real directory).
//
// No status is named, deliberately. IsCallerFault answers which SIDE a failure
// falls on and nothing about what any caller does with the answer, and its three
// callers do different things -- the register endpoint answers one pair of codes
// and the two lazy-registration paths another.
//
// The errors come from the real read rather than being written by hand, and that
// is the whole point of the test. A hand-built sentinel would keep passing after
// the classification it assumes had moved underneath it -- it would assert that
// this package handles something it no longer receives. The read is the one
// registry.go makes, forawellknown.NewDirectoryFetcher over the SDK's fetcher.
//
// mapFetchError is unexported and this test is in-package for that reason: the
// translation is where the SDK's sentinels meet agentreg's, and asserting it
// through RegisterFromDirectory would need a database to reach a pure function.
// The integration table in registry_directory_failures_integration_test.go drives
// the same failures through RegisterFromDirectory, with what it records.
func TestMapFetchError_SplitsAServedDocumentFromNoDocumentAtAll(t *testing.T) {
	t.Parallel()

	_, key := rwtestutil.NewSigningKey("unsigned", time.Unix(0, 0), time.Unix(4_000_000_000, 0))
	listedButUnsigned := rwtestutil.MarshalWBA(rwtestutil.WBAFile(key))
	for _, tc := range []struct {
		name string
		// serve arranges what the origin answers.
		serve func(o *rwtestutil.Origin)
		// sentinel is what the read must hand this package. Asserted alongside the
		// classification because IsCallerFault cannot see it: every transient
		// failure falls to the same default arm, so a row checking only the
		// boolean stays green after the sentinel underneath it moves.
		sentinel    error
		callerFault bool
	}{
		{
			name:        "the origin publishes no directory",
			serve:       func(o *rwtestutil.Origin) { o.SetWBAStatus(http.StatusNotFound) },
			sentinel:    resolvers.ErrDocumentNotFound,
			callerFault: true,
		},
		{
			name:        "the directory answers with a redirect",
			serve:       func(o *rwtestutil.Origin) { o.SetWBAStatus(http.StatusFound) },
			sentinel:    resolvers.ErrRedirectRefused,
			callerFault: true,
		},
		{
			name: "the directory is served under another media type",
			serve: func(o *rwtestutil.Origin) {
				o.SetWBA(listedButUnsigned)
				o.SetWBAContentType("application/jwk-set+json")
			},
			sentinel:    resolvers.ErrMediaTypeRefused,
			callerFault: true,
		},
		{
			// A reader hands out only keys that signed, and the next read returns the
			// same unsigned response: the agent has to sign its directory first.
			name:        "the response carries no signature",
			serve:       func(o *rwtestutil.Origin) { o.SetWBA(listedButUnsigned) },
			sentinel:    helpers.ErrDirectoryResponseUnsigned,
			callerFault: true,
		},
		{
			name:     "the origin answers 503",
			serve:    func(o *rwtestutil.Origin) { o.SetWBAStatus(http.StatusServiceUnavailable) },
			sentinel: resolvers.ErrDirectoryUnavailable,
		},
		{
			// A proxy or a CDN answering 200 with an error page. Nothing was read,
			// so nothing is known about what the agent publishes.
			name:     "a body that is not JSON",
			serve:    func(o *rwtestutil.Origin) { o.SetWBA([]byte(`<html>502 Bad Gateway</html>`)) },
			sentinel: resolvers.ErrDirectoryUnavailable,
		},
		{
			// The SDK states a body that does not decode as a key directory as an
			// unavailable document, whatever JSON it is.
			name:     "a body that is JSON of another shape",
			serve:    func(o *rwtestutil.Origin) { o.SetWBA([]byte(`{"keys":"x"}`)) },
			sentinel: resolvers.ErrDirectoryUnavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			origin := rwtestutil.NewTLSOrigin(nil)
			t.Cleanup(origin.Close)
			tc.serve(origin)

			_, readErr := forawellknown.NewDirectoryFetcher(origin.HTTPClient(), nil, 0)(context.Background(), origin.Host())
			if !errors.Is(readErr, tc.sentinel) {
				t.Fatalf("the read answered %v, want it to wrap %v; this package classifies on that "+
					"sentinel, so a different one means it is reading a rule that has moved", readErr, tc.sentinel)
			}
			directoryURL := resolvers.WBADirectoryURL("https", origin.Host())
			mapped := mapFetchError(directoryURL, readErr)

			if got := IsCallerFault(mapped); got != tc.callerFault {
				t.Errorf("IsCallerFault = %v, want %v; %v", got, tc.callerFault, explainSide(tc.callerFault))
			}
			// The cause and the address survive either way, so an operator reading
			// the log still sees which rule the document broke, and where.
			if !errors.Is(mapped, readErr) || !strings.Contains(mapped.Error(), directoryURL) {
				t.Errorf("mapFetchError dropped the cause or the URL: %v", mapped)
			}
		})
	}
}

// explainSide names the consequence of the wrong answer, so a failure says what
// breaks rather than only which boolean moved.
func explainSide(callerFault bool) string {
	if callerFault {
		return "a directory that will never be accepted would be retried against the agent's origin"
	}
	return "an agent would be told its identity is not registrable because someone else's proxy hiccupped"
}
