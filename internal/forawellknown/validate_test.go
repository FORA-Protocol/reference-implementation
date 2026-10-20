package forawellknown_test

import (
	"bytes"
	"errors"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	sharedtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// validJWK is a schema-conformant WBA JWK literal (x is 43 base64url chars, no kid).
const validJWK = `{"kty":"OKP","crv":"Ed25519","use":"sig","alg":"EdDSA",` +
	`"x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo",` +
	`"not_before":"2026-01-01T00:00:00Z","not_after":"2027-01-01T00:00:00Z"}`

func TestValidateManifest_ValidRoles(t *testing.T) {
	t.Parallel()
	// Overlay manifests are keyless after the WBA split; only ver/role/domain
	// are required for every role.
	cases := map[string]*forawellknown.Manifest{
		"agent":    testutil.Manifest(forawellknown.RoleAgent, "a.example"),
		"broker":   testutil.Manifest(forawellknown.RoleBroker, "b.example"),
		"exchange": testutil.Manifest(forawellknown.RoleExchange, "x.example"),
	}
	pub := testutil.Manifest(forawellknown.RolePublisher, "p.example")
	pub.Exchanges = []*forav1.AuthorizedExchange{
		testutil.PublisherExchange("x.example", "https://x.example/v1",
			forav1.ProviderRelationship_PROVIDER_RELATIONSHIP_DIRECT),
	}
	cases["publisher"] = pub

	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := forawellknown.ValidateManifest(testutil.MarshalManifest(m)); err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
		})
	}
}

// TestParseManifest_OverlayCannotCarryIdentityKeys pins the split's security
// intent from the consumer side: identity keys come ONLY from the WBA directory,
// never from the fora.json overlay. A misconfigured or hostile overlay that
// carries public_keys / invalidation_url is accepted by the forward-compatible
// overlay schema, but the consumer drops those members (the typed overlay has no
// field for them), so no identity key is resolvable through the overlay parse
// surface. Verified by round-trip: the parsed manifest re-marshals with neither
// member present.
func TestParseManifest_OverlayCannotCarryIdentityKeys(t *testing.T) {
	t.Parallel()
	hostile := []byte(`{"ver":"1.0","role":"ROLE_AGENT","domain":"a.example",` +
		`"public_keys":[` + validJWK + `],"invalidation_url":"https://a.example/rev.json"}`)

	m, err := forawellknown.ParseManifest(hostile, forawellknown.RoleAgent)
	if err != nil {
		t.Fatalf("overlay with stray identity fields should parse (forward-compat), got %v", err)
	}
	round := testutil.MarshalManifest(m)
	if bytes.Contains(round, []byte("public_keys")) {
		t.Fatalf("parsed overlay must not carry public_keys; round-trip = %s", round)
	}
	if bytes.Contains(round, []byte("invalidation_url")) {
		t.Fatalf("parsed overlay must not carry invalidation_url; round-trip = %s", round)
	}
}

func TestValidateManifest_MinimalRolesAreValid(t *testing.T) {
	t.Parallel()
	// After the WBA split no role publishes keys in the overlay; a minimal
	// {ver,role,domain} manifest is valid for every role.
	for _, role := range []string{"ROLE_AGENT", "ROLE_BROKER", "ROLE_EXCHANGE", "ROLE_PUBLISHER"} {
		raw := []byte(`{"ver":"1.0","role":"` + role + `","domain":"p.example"}`)
		if err := forawellknown.ValidateManifest(raw); err != nil {
			t.Fatalf("minimal %s manifest should validate: %v", role, err)
		}
	}
}

func TestValidateWBA(t *testing.T) {
	t.Parallel()
	good := []byte(`{"keys":[` + validJWK + `]}`)
	if err := forawellknown.ValidateWBA(good); err != nil {
		t.Fatalf("valid WBA directory rejected: %v", err)
	}
	withRevocation := []byte(`{"keys":[` + validJWK + `],"revocation_url":"https://a.example/rev.json"}`)
	if err := forawellknown.ValidateWBA(withRevocation); err != nil {
		t.Fatalf("valid WBA directory with revocation_url rejected: %v", err)
	}

	rejections := map[string]string{
		"empty keys":   `{"keys":[]}`,
		"missing keys": `{}`,
		"jwk carries no x": `{"keys":[{"kty":"OKP","crv":"Ed25519","use":"sig","alg":"EdDSA",` +
			`"not_before":"2026-01-01T00:00:00Z","not_after":"2027-01-01T00:00:00Z"}]}`,
		"short x": `{"keys":[{"kty":"OKP","crv":"Ed25519","use":"sig","alg":"EdDSA","x":"tooshort",` +
			`"not_before":"2026-01-01T00:00:00Z","not_after":"2027-01-01T00:00:00Z"}]}`,
	}
	for name, js := range rejections {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := forawellknown.ValidateWBA([]byte(js)); !errors.Is(err, forawellknown.ErrSchemaInvalid) {
				t.Fatalf("want ErrSchemaInvalid, got %v", err)
			}
		})
	}
}

// TestValidate_PureCallersAreNotToldAFetchFailed pins the half of the sentinel
// split that has no other test, and the one a caller actually reads.
//
// These three entry points are documented as the path for someone who already
// has the bytes -- a producer checking its own output, a service reading an
// operator's file off disk. The Broker does exactly that: it reads a revocation
// file with os.ReadFile and renders whatever comes back as "revocation file
// invalid". While the validator answered ErrFetch, that sentence continued
// "well-known document fetch failed", sending an operator to look at a network
// for a file sitting in front of them.
//
// Asserting the ABSENCE of ErrFetch is the load-bearing half. The presence of
// ErrNotJSON alone would still pass if the wrap were reinstated here, because
// both sentinels match through it -- which is exactly how a fetch-boundary
// wrap is supposed to behave, and exactly why it must not happen at this layer.
func TestValidate_PureCallersAreNotToldAFetchFailed(t *testing.T) {
	t.Parallel()
	notJSON := []byte(`<html>502 Bad Gateway</html>`)
	for name, validate := range map[string]func([]byte) error{
		"a manifest":        forawellknown.ValidateManifest,
		"a WBA directory":   forawellknown.ValidateWBA,
		"a revocation list": forawellknown.ValidateRevocation,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := validate(notJSON)
			if !errors.Is(err, forawellknown.ErrNotJSON) {
				t.Fatalf("err = %v, want it to name a body that is not JSON", err)
			}
			if errors.Is(err, forawellknown.ErrFetch) {
				t.Errorf("err = %v claims a fetch failed; nothing was fetched, and a "+
					"caller that read a local file is sent to look at a network", err)
			}
		})
	}
}

func TestValidateRevocation(t *testing.T) {
	t.Parallel()
	good := testutil.MarshalRevocation(anchor, "revoked-thumbprint")
	if err := forawellknown.ValidateRevocation(good); err != nil {
		t.Fatalf("valid revocation list rejected: %v", err)
	}
	// A "nothing revoked" snapshot serializes to just {as_of} (protojson omits
	// the empty repeated field); it MUST validate as an empty revocation set.
	empty := testutil.MarshalRevocation(anchor)
	if err := forawellknown.ValidateRevocation(empty); err != nil {
		t.Fatalf("empty revocation list ({as_of} only) rejected: %v", err)
	}
	missingAsOf := `{"revoked":["tp1"]}`
	if err := forawellknown.ValidateRevocation([]byte(missingAsOf)); !errors.Is(err, forawellknown.ErrSchemaInvalid) {
		t.Fatalf("want ErrSchemaInvalid for missing as_of, got %v", err)
	}
}

// TestParseManifest_RefusesProtocolRuleViolations drives the consume path's
// second check. ParseManifest runs the embedded JSON Schema first, which decides
// the wire shape, and then the protocol's protovalidate constraints, which
// decide the field and cross-field rules. Every document below passes the first
// check and breaks the second: the schema types terms_uri and terms_digest as
// plain strings and states no rule about either, so the refusal can only come
// from protovalidate.
//
// This is the leg that defends a consumer against a remote participant serving a
// document its own producer would have refused. Without these cases the
// protovalidate call can be deleted or reordered and the whole package still
// passes, because every other test here feeds ParseManifest a document this repo
// built and already validated.
func TestParseManifest_RefusesProtocolRuleViolations(t *testing.T) {
	t.Parallel()
	const head = `{"ver":"1.0","role":"ROLE_EXCHANGE","domain":"x.example"`
	for _, tc := range sharedtestutil.MalformedTermsDigests {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			raw := head + `,"terms_digest":"` + tc.Digest + `"`
			if tc.URI != "" {
				raw += `,"terms_uri":"` + tc.URI + `"`
			}
			raw += `}`

			// The wire shape is valid on its own — that is what makes the case
			// reach the protovalidate call rather than stopping one check short.
			if err := forawellknown.ValidateManifest([]byte(raw)); err != nil {
				t.Fatalf("the fixture fails the wire-shape schema, so it never reaches the protocol rules: %v", err)
			}
			if _, err := forawellknown.ParseManifest([]byte(raw), forawellknown.RoleExchange); !errors.Is(err, forawellknown.ErrSchemaInvalid) {
				t.Fatalf("ParseManifest accepted a document breaking the protocol's terms rules, want ErrSchemaInvalid, got %v", err)
			}
		})
	}
}

// TestValidateManifest_Refusals is the one table of documents ValidateManifest
// refuses, with the sentinel each refusal must carry — and must NOT carry,
// because the two sentinels name different repairs and a document that reads as
// both, or as the wrong one, sends an operator to fix the wrong thing.
//
// The version rows pin the document-version rule on the consumer side. The rule
// is the SDK's, not a schema const: a manifest at the same major and a higher
// minor is accepted (the case pinned first), because a minor revision of the
// manifest is additive and a reader ignores members it does not know. An
// unrecognised major, a value that is not MAJOR.MINOR, and an absent ver are
// refused — absent included, because the document sits at a fixed, unversioned
// path and a document with no version is one whose layout this reader cannot
// classify. The gate runs before the schema, so a refused document is refused
// without being parsed further.
//
// The shape rows pin the other half: a body the version gate cannot read as an
// object — null, an array, a string — is a shape failure the schema reports, not
// a version refusal. null is the row that matters most: it decodes into a struct
// as a no-op, so a gate that read `ver` through a struct refused it as "ver is
// absent" and diagnosed an origin serving nothing as a version mismatch.
//
// A body that is not JSON at all is the third sentinel and neither of the other
// two. Those two are facts about a document that arrived and broke a rule, so
// the next fetch returns the same bytes and the same answer; bytes that never
// parsed establish nothing about what the origin publishes, and the fetch paths
// state them as transient for that reason.
func TestValidateManifest_Refusals(t *testing.T) {
	t.Parallel()
	t.Run("same major, higher minor is accepted", func(t *testing.T) {
		t.Parallel()
		if err := forawellknown.ValidateManifest([]byte(`{"ver":"1.7","role":"ROLE_AGENT","domain":"a"}`)); err != nil {
			t.Fatalf("a 1.x manifest must be accepted, got %v", err)
		}
	})
	refused := []struct {
		name string
		json string
		want error
	}{
		{"unrecognised major", `{"ver":"2.0","role":"ROLE_AGENT","domain":"a"}`, forawellknown.ErrVersionRefused},
		{"pre-1.0 major", `{"ver":"0.3","role":"ROLE_AGENT","domain":"a"}`, forawellknown.ErrVersionRefused},
		{"not MAJOR.MINOR", `{"ver":"1","role":"ROLE_AGENT","domain":"a"}`, forawellknown.ErrVersionRefused},
		{"absent ver", `{"role":"ROLE_AGENT","domain":"a"}`, forawellknown.ErrVersionRefused},
		{"ver that is not a string", `{"ver":1,"role":"ROLE_AGENT","domain":"a"}`, forawellknown.ErrVersionRefused},
		// The body has NO other valid member, so a refusal here can only be the
		// version gate having run first — the schema would have named the shape.
		{"version gate precedes the shape check", `{"ver":"2.0"}`, forawellknown.ErrVersionRefused},

		{"unknown role", `{"ver":"1.0","role":"ROLE_FOO","domain":"a"}`, forawellknown.ErrSchemaInvalid},
		{"missing domain", `{"ver":"1.0","role":"ROLE_AGENT"}`, forawellknown.ErrSchemaInvalid},
		{
			"short relationship form rejected",
			`{"ver":"1.0","role":"ROLE_PUBLISHER","domain":"p","exchanges":[` +
				`{"domain":"x","endpoint":"https://x/v1","relationship":"DIRECT"}]}`,
			forawellknown.ErrSchemaInvalid,
		},
		{"not JSON", `{`, forawellknown.ErrNotJSON},
		{"JSON null", `null`, forawellknown.ErrSchemaInvalid},
		{"JSON array", `[]`, forawellknown.ErrSchemaInvalid},
		{"JSON string", `"x"`, forawellknown.ErrSchemaInvalid},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := forawellknown.ValidateManifest([]byte(tc.json))
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			for _, other := range []error{
				forawellknown.ErrVersionRefused,
				forawellknown.ErrSchemaInvalid,
				forawellknown.ErrNotJSON,
			} {
				if errors.Is(tc.want, other) {
					continue
				}
				if errors.Is(err, other) {
					t.Fatalf("a refusal must name one failure, not several: got %v, which also reads as %v",
						err, other)
				}
			}
		})
	}
}
