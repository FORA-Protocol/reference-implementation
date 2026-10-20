//go:build integration

// What RegisterFromDirectory answers when the agent's key directory is no use,
// one row per way it can fail, each through the SDK read the registry makes and
// each checking that nothing was registered.
//
// The caller-fault rows are the ones whose repair is the agent's: it publishes no
// directory (404), what it publishes breaks a rule of the read (a redirect, the
// wrong media type, an unsigned response), or it lists no key that signed the
// response or that is currently valid. The next read returns the same answer, so
// the caller is told the identity is not registrable.
//
// The transient rows establish nothing about what the agent publishes: the origin
// answered 5xx, sent a body that is not a key directory at all (not JSON, JSON of
// another shape, or over 1 MiB), or did not answer within the registry's time
// limit. A proxy answering 200 with an error page produces such a body, and the
// next read may return the real directory, so the caller is told to retry.
package agentreg_test

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/agentreg"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

const (
	failingAgentID = "agent.fixture.test"
	// failingDirectoryURL is the address the registry reads the agent's directory
	// at, which every read failure names.
	failingDirectoryURL = "https://agent.fixture.test/.well-known/http-message-signatures-directory"
)

// registryNow is inside every fixture key's window unless a row says otherwise.
var registryNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// setupRegistryWith is setupRegistry with the registry's read time limit set;
// zero is the registry's default.
func setupRegistryWith(
	t *testing.T, clock agentreg.Clock, timeout time.Duration,
) (agentreg.Registry, *sqlc.Queries, *hostRewriter) {
	t.Helper()
	q := newTestQueries(t)
	rw := newHostRewriter()
	reg := agentreg.New(agentreg.Config{
		Repo:    repo.NewAgentRepo(q),
		HTTP:    &http.Client{Transport: rw},
		Clock:   clock,
		Timeout: timeout,
	})
	return reg, q, rw
}

// agentOrigin starts a plaintext origin the rewriter routes the agent's identity
// to: the registry dials https://agent.fixture.test, and the rewriter keeps that
// Host, so a response signature over it still verifies.
func agentOrigin(t *testing.T, rw *hostRewriter, host string) *rwtestutil.Origin {
	t.Helper()
	o := rwtestutil.NewOrigin(nil)
	t.Cleanup(o.Close)
	rw.set(host, o.URL)
	return o
}

// publishSigned serves a one-key directory signed by that key; notAfter ends the
// key's validity window.
func publishSigned(o *rwtestutil.Origin, seed string, notAfter time.Time) {
	priv, key := rwtestutil.NewSigningKey(seed, registryNow.Add(-48*time.Hour), notAfter)
	o.SetWBA(rwtestutil.MarshalWBA(rwtestutil.WBAFile(key)))
	o.SetWBASigners(priv)
}

type directoryFailure struct {
	name string
	// serve arranges what the agent's origin answers. A non-nil check it returns
	// runs after the registration.
	serve func(t *testing.T, rw *hostRewriter) (check func(t *testing.T))
	// timeout is the registry's read time limit; zero is the default.
	timeout time.Duration
	// want is agentreg's sentinel (or, for a transient row, the SDK's), and
	// cause, when set, the SDK sentinel the error must still carry underneath.
	want, cause error
	callerFault bool
	// namesURL is whether the error names the directory's URL: every failed read
	// does, while a directory that was read and then refused by key selection
	// carries no URL.
	namesURL bool
}

func serveBody(body []byte) func(*testing.T, *hostRewriter) func(*testing.T) {
	return func(t *testing.T, rw *hostRewriter) func(*testing.T) {
		agentOrigin(t, rw, failingAgentID).SetWBA(body)
		return nil
	}
}

func serveStatus(code int) func(*testing.T, *hostRewriter) func(*testing.T) {
	return func(t *testing.T, rw *hostRewriter) func(*testing.T) {
		agentOrigin(t, rw, failingAgentID).SetWBAStatus(code)
		return nil
	}
}

// serveRedirect answers the agent's directory with a redirect to a host that
// serves a genuine signed directory, through a client that would follow it.
func serveRedirect(t *testing.T, rw *hostRewriter) func(*testing.T) {
	target := agentOrigin(t, rw, "target.fixture.test")
	publishSigned(target, "redirect-target", registryNow.Add(24*time.Hour))
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://target.fixture.test"+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	rw.set(failingAgentID, redirector.URL)
	return func(t *testing.T) {
		if n := target.WBAHits(); n != 0 {
			t.Fatalf("the redirect target was read %d times, want 0", n)
		}
	}
}

func callerFaultRows() []directoryFailure {
	return []directoryFailure{
		{
			name: "the origin publishes no directory", serve: serveStatus(http.StatusNotFound),
			want: agentreg.ErrNoDirectory, cause: resolvers.ErrDocumentNotFound, callerFault: true, namesURL: true,
		},
		{
			name: "the directory answers with a redirect", serve: serveRedirect,
			want: agentreg.ErrMalformedDirectory, cause: resolvers.ErrRedirectRefused, callerFault: true, namesURL: true,
		},
		{
			name: "the directory is served under another media type",
			serve: func(t *testing.T, rw *hostRewriter) func(*testing.T) {
				o := agentOrigin(t, rw, failingAgentID)
				publishSigned(o, "media-type", registryNow.Add(24*time.Hour))
				o.SetWBAContentType("application/jwk-set+json")
				return nil
			},
			want: agentreg.ErrMalformedDirectory, cause: resolvers.ErrMediaTypeRefused, callerFault: true, namesURL: true,
		},
		{
			name: "the response carries no signature",
			serve: func(t *testing.T, rw *hostRewriter) func(*testing.T) {
				_, key := rwtestutil.NewSigningKey("unsigned", registryNow.Add(-time.Hour), registryNow.Add(time.Hour))
				agentOrigin(t, rw, failingAgentID).SetWBA(rwtestutil.MarshalWBA(rwtestutil.WBAFile(key)))
				return nil
			},
			want: agentreg.ErrMalformedDirectory, cause: helpers.ErrDirectoryResponseUnsigned, callerFault: true, namesURL: true,
		},
		{
			name: "the response is signed only by a key the directory does not list",
			serve: func(t *testing.T, rw *hostRewriter) func(*testing.T) {
				_, listed := rwtestutil.NewSigningKey("listed", registryNow.Add(-time.Hour), registryNow.Add(time.Hour))
				signer, _ := rwtestutil.NewSigningKey("signer", registryNow.Add(-time.Hour), registryNow.Add(time.Hour))
				o := agentOrigin(t, rw, failingAgentID)
				o.SetWBA(rwtestutil.MarshalWBA(rwtestutil.WBAFile(listed)))
				o.SetWBASigners(signer)
				return nil
			},
			want: agentreg.ErrMalformedDirectory, cause: helpers.ErrUnknownKey, callerFault: true,
		},
		{
			name: "the directory lists no key", serve: serveBody([]byte(`{"keys":[]}`)),
			want: agentreg.ErrMalformedDirectory, cause: helpers.ErrUnknownKey, callerFault: true,
		},
		{
			name: "the directory carries only members a directory does not have", serve: serveBody([]byte(`{"ver":"0.3"}`)),
			want: agentreg.ErrMalformedDirectory, cause: helpers.ErrUnknownKey, callerFault: true,
		},
		{
			name: "every key's validity window has ended",
			serve: func(t *testing.T, rw *hostRewriter) func(*testing.T) {
				publishSigned(agentOrigin(t, rw, failingAgentID), "expired", registryNow.Add(-time.Hour))
				return nil
			},
			want: agentreg.ErrNoValidKey, callerFault: true,
		},
	}
}

func transientRows() []directoryFailure {
	return []directoryFailure{
		{name: "the origin answers 503", serve: serveStatus(http.StatusServiceUnavailable)},
		{name: "the body is not JSON", serve: serveBody([]byte("{not valid fora json"))},
		{name: "the body is JSON of another shape", serve: serveBody([]byte(`{"keys":"x"}`))},
		{name: "the body exceeds 1 MiB", serve: serveBody(bytes.Repeat([]byte{' '}, 1<<20+1))},
		{
			name: "the origin does not answer within the time limit",
			serve: func(t *testing.T, rw *hostRewriter) func(*testing.T) {
				o := agentOrigin(t, rw, failingAgentID)
				publishSigned(o, "slow", registryNow.Add(24*time.Hour))
				t.Cleanup(o.BlockWBA()) // after the origin's Close, so it runs first
				return nil
			},
			timeout: 100 * time.Millisecond,
		},
	}
}

func TestRegistry_DirectoryFailures(t *testing.T) {
	rows := callerFaultRows()
	for _, tc := range transientRows() {
		tc.want, tc.namesURL = resolvers.ErrDirectoryUnavailable, true
		rows = append(rows, tc)
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			reg, q, rw := setupRegistryWith(t, &fixedClock{now: registryNow}, tc.timeout)
			check := tc.serve(t, rw)

			start := time.Now()
			err := reg.RegisterFromDirectory(t.Context(), failingAgentID, failingAgentID)
			assertDirectoryFailure(t, tc, err, time.Since(start))
			if check != nil {
				check(t)
			}
			requireNoAgentRow(t, t.Context(), q, failingAgentID, tc.name)
		})
	}
}

func assertDirectoryFailure(t *testing.T, tc directoryFailure, err error, elapsed time.Duration) {
	t.Helper()
	if !errors.Is(err, tc.want) {
		t.Fatalf("got %v, want it to wrap %v", err, tc.want)
	}
	if tc.cause != nil && !errors.Is(err, tc.cause) {
		t.Errorf("got %v, want the SDK's %v still underneath", err, tc.cause)
	}
	if got := agentreg.IsCallerFault(err); got != tc.callerFault {
		t.Errorf("IsCallerFault = %v, want %v: %v", got, tc.callerFault, err)
	}
	if tc.namesURL && !strings.Contains(err.Error(), failingDirectoryURL) {
		t.Errorf("the error does not name %s: %v", failingDirectoryURL, err)
	}
	if tc.timeout > 0 && elapsed > 2*time.Second {
		t.Errorf("the read took %v; the %v limit did not end it", elapsed, tc.timeout)
	}
}
