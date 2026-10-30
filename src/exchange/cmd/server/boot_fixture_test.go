package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureSeat is the seat every fixture below declares; each case varies one
// listing or one key so exactly one thing about it is unusable.
const fixtureSeat = `version: 1
seats:
  - tenant_id: seat
    domain: exchange.example
    ed25519_key_ref: exchange-primary
    allow_broker_relay: true
    activate_new_agents_by_default: true
`

const fixtureListing = `publishers:
  - publisher_id: pub-1
    display_name: Example Publisher
    sites:
      - domain: www.publisher.example
        signing_scheme: ED25519
        listings:
          - tenant_id: seat
            terms_file: terms.json
`

// TestUnusableBootFixtureStopsTheBoot drives the leg an operator relies on: the
// process does not come up on a fixture it could not apply. run() is called with
// no database configured, so it would fail on EXCHANGE_DSN if it got that far,
// and the assertion that it fails on the fixture instead is what pins the
// fixture being read and planned BEFORE anything connects. Moving the load below
// db.Setup fails this test.
func TestUnusableBootFixtureStopsTheBoot(t *testing.T) {
	cases := map[string]struct {
		fixture string // "" writes no fixture file at all
		terms   string
		names   string
	}{
		"missing file": {names: "fixture.yml"},
		"unknown field": {
			fixture: fixtureSeat + "    surcharge: 1\n", names: "surcharge",
		},
		"terms that permit nothing": {
			fixture: fixtureSeat + fixtureListing,
			terms:   `{"pricing_model":"free","permitted_functions":[]}`,
			names:   "at least one permitted use",
		},
		"CloudFront RSA site on a seat with no RSA key": {
			fixture: fixtureSeat + strings.Replace(fixtureListing, "        signing_scheme: ED25519\n",
				"        signing_scheme: AWS_CLOUDFRONT_RSA\n        cloudfront_key_pair_id: KEXAMPLE\n", 1),
			names: "publishers[0].sites[0].listings[0].tenant_id",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "fixture.yml")
			writeIfSet(t, path, tc.fixture)
			writeIfSet(t, filepath.Join(dir, "terms.json"), tc.terms)
			t.Setenv("EXCHANGE_BOOT_FIXTURE_FILE", path)
			t.Setenv("EXCHANGE_DSN", "")

			err := run(t.Context(), discardLogger())
			if err == nil {
				t.Fatal("run() came up with a boot fixture it could not apply")
			}
			if !strings.Contains(err.Error(), "EXCHANGE_BOOT_FIXTURE_FILE") || !strings.Contains(err.Error(), tc.names) {
				t.Fatalf("run() failed with %q, want a failure naming EXCHANGE_BOOT_FIXTURE_FILE and %q", err, tc.names)
			}
		})
	}
}

func writeIfSet(t *testing.T, path, content string) {
	t.Helper()
	if content == "" {
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
