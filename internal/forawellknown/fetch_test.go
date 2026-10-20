package forawellknown_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

func TestFetch_HappyPathAssertsRole(t *testing.T) {
	t.Parallel()
	origin := testutil.NewOrigin(testutil.MarshalManifest(
		testutil.Manifest(forawellknown.RoleAgent, "agent.example"),
	))
	defer origin.Close()

	m, err := forawellknown.Fetch(context.Background(), origin.URL, forawellknown.FetchOptions{
		Client:     testutil.Client(),
		ExpectRole: forawellknown.RoleAgent,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if m.GetDomain() != "agent.example" || m.GetRole() != forawellknown.RoleAgent {
		t.Fatalf("unexpected manifest: domain=%s role=%s", m.GetDomain(), m.GetRole())
	}
}

// TestFetch_ZeroClientFailsLoud pins that omitting Client is a fail-loud
// ErrNoClient, NOT a silent fall-open. The SSRF-guarded client is SDK-owned;
// this package keeps no in-package guarded default, so a caller that forgets to
// inject a client gets a clear error rather than either a fail-open
// http.DefaultClient or a re-wrapped SDK factory.
func TestFetch_ZeroClientFailsLoud(t *testing.T) {
	t.Parallel()
	origin := testutil.NewOrigin(testutil.MarshalManifest(
		testutil.Manifest(forawellknown.RoleAgent, "agent.example"),
	))
	defer origin.Close()

	_, err := forawellknown.Fetch(context.Background(), origin.URL, forawellknown.FetchOptions{})
	if !errors.Is(err, forawellknown.ErrNoClient) {
		t.Fatalf("want ErrNoClient when Client is omitted, got %v", err)
	}
}

func TestFetch_Errors(t *testing.T) {
	t.Parallel()
	brokerManifest := testutil.MarshalManifest(
		testutil.Manifest(forawellknown.RoleBroker, "broker.example"),
	)

	tests := []struct {
		name    string
		arrange func(o *testutil.Origin)
		expect  forawellknown.Role
		wantErr error
	}{
		{
			name:    "404 yields ErrNoDocument",
			arrange: func(o *testutil.Origin) { o.SetManifestStatus(http.StatusNotFound) },
			wantErr: forawellknown.ErrNoDocument,
		},
		{
			// JSON, a document, and a version this reader accepts — the wire
			// shape is what fails. The next fetch returns the same bytes, so
			// this is a verdict about what the origin publishes.
			name:    "a document breaking the schema yields ErrSchemaInvalid",
			arrange: func(o *testutil.Origin) { o.SetManifest([]byte(`{"ver":"1.0"}`)) },
			wantErr: forawellknown.ErrSchemaInvalid,
		},
		{
			// Not a document at all, which is the other side of that line and the
			// reason the two sentinels differ. A proxy answering 200 with an error
			// page produces this, and it says nothing about what the origin
			// publishes — so it must not read as a verdict a caller stops retrying.
			// The version gate stands aside: a body that is not a JSON object has
			// no `ver` to read, so this stays one refusal instead of becoming a
			// second spelling of the same failure.
			name:    "a body that is not JSON yields ErrFetch",
			arrange: func(o *testutil.Origin) { o.SetManifest([]byte(`<html>502 Bad Gateway</html>`)) },
			wantErr: forawellknown.ErrFetch,
		},
		{
			// A complete, well-shaped document at a major this reader does not
			// implement: refused on the version, before role or shape are read.
			name: "unrecognised manifest version yields ErrVersionRefused",
			arrange: func(o *testutil.Origin) {
				o.SetManifest([]byte(`{"ver":"2.0","role":"ROLE_BROKER","domain":"broker.example"}`))
			},
			expect:  forawellknown.RoleBroker,
			wantErr: forawellknown.ErrVersionRefused,
		},
		{
			name:    "role mismatch yields ErrRoleMismatch",
			arrange: func(_ *testutil.Origin) {},
			expect:  forawellknown.RoleAgent,
			wantErr: forawellknown.ErrRoleMismatch,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			origin := testutil.NewOrigin(brokerManifest)
			defer origin.Close()
			tc.arrange(origin)
			_, err := forawellknown.Fetch(context.Background(), origin.URL, forawellknown.FetchOptions{
				Client:     testutil.Client(),
				ExpectRole: tc.expect,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}
