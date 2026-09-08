package forawellknown_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

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

func TestFetchWBA_HappyPath(t *testing.T) {
	t.Parallel()
	priv, key := testutil.NewSigningKey("k", anchor.Add(-time.Hour), anchor.Add(time.Hour))
	origin := testutil.NewOrigin(nil)
	defer origin.Close()
	origin.SetWBA(testutil.MarshalWBA(testutil.WBAFile(key)))

	f, err := forawellknown.FetchWBA(context.Background(), origin.URL, forawellknown.FetchOptions{
		Client: testutil.Client(),
	})
	if err != nil {
		t.Fatalf("FetchWBA: %v", err)
	}
	if len(f.GetKeys()) != 1 {
		t.Fatalf("want 1 key, got %d", len(f.GetKeys()))
	}
	pub, err := forawellknown.PublicKey(f.GetKeys()[0])
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	if !pub.Equal(priv.Public()) {
		t.Fatal("fetched key mismatch")
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
			// A recognised version and nothing else: the shape is what fails.
			name:    "garbage body yields ErrSchemaInvalid",
			arrange: func(o *testutil.Origin) { o.SetManifest([]byte(`{"ver":"1.0"}`)) },
			wantErr: forawellknown.ErrSchemaInvalid,
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
