package bootfixture_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/bootfixture"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
)

const freeTerms = `{"pricing_model":"free","permitted_functions":["search"],"attribution_required":false}`

// validFixture declares one seat on a host and port with both key references, a
// second seat with no RSA key reference, and a publisher with an Ed25519 site
// listed on both seats and a CloudFront RSA site listed on the seat that holds an
// RSA key.
const validFixture = `version: 1
seats:
  - tenant_id: seat
    domain: exchange.example:8081
    ed25519_key_ref: exchange-primary
    rsa_key_ref: cf-rsa-primary
    allow_broker_relay: true
    activate_new_agents_by_default: true
  - tenant_id: seat-2
    domain: second.exchange.example
    ed25519_key_ref: exchange-primary
    allow_broker_relay: false
    activate_new_agents_by_default: false
publishers:
  - publisher_id: pub-1
    display_name: Example Publisher
    sites:
      - domain: www.publisher.example
        signing_scheme: ED25519
        cdn: cloudflare
        listings:
          - tenant_id: seat
            terms_file: terms.json
            content_path_scope: ["/articles/"]
          - tenant_id: seat-2
            terms_file: terms.json
      - domain: media.publisher.example
        signing_scheme: AWS_CLOUDFRONT_RSA
        cloudfront_key_pair_id: KEXAMPLE
        listings:
          - tenant_id: seat
            terms_file: terms.json
`

func termsFS() fstest.MapFS {
	return fstest.MapFS{"terms.json": {Data: []byte(freeTerms)}}
}

func TestAValidFixtureIsReadIntoItsRows(t *testing.T) {
	f, err := bootfixture.Parse([]byte(validFixture), termsFS())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.SHA256) != 64 {
		t.Errorf("SHA256 = %q, want the hex digest of the file", f.SHA256)
	}
	wantSeats := []bootfixture.Seat{
		{
			TenantID: "seat", Domain: "exchange.example:8081", Ed25519KeyRef: "exchange-primary",
			RSAKeyRef: "cf-rsa-primary", AllowBrokerRelay: true, ActivateNewAgentsByDefault: true,
		},
		{TenantID: "seat-2", Domain: "second.exchange.example", Ed25519KeyRef: "exchange-primary"},
	}
	if !reflect.DeepEqual(f.Seats, wantSeats) {
		t.Errorf("seats = %+v\nwant %+v", f.Seats, wantSeats)
	}
	listing := func(tenant string, scope ...string) bootfixture.Listing {
		return bootfixture.Listing{
			TenantID: tenant, TermsFile: "terms.json", Terms: []byte(freeTerms),
			ContentPathScope: append([]string{}, scope...),
		}
	}
	wantPublishers := []bootfixture.Publisher{{
		ID: "pub-1", DisplayName: "Example Publisher",
		Sites: []bootfixture.Site{
			{
				Domain: "www.publisher.example", SigningScheme: signing.SchemeEd25519, CDN: cdn.ProviderCloudflare,
				Listings: []bootfixture.Listing{listing("seat", "/articles/"), listing("seat-2")},
			},
			{
				Domain: "media.publisher.example", SigningScheme: signing.SchemeCFRSA,
				CloudFrontKeyPairID: "KEXAMPLE",
				Listings:            []bootfixture.Listing{listing("seat")},
			},
		},
	}}
	if !reflect.DeepEqual(f.Publishers, wantPublishers) {
		t.Errorf("publishers = %+v\nwant %+v", f.Publishers, wantPublishers)
	}
}

// replace edits validFixture once, failing the test when the edit does not apply,
// so every case below is the valid fixture with exactly one thing wrong.
func replace(t *testing.T, old, new string) string {
	t.Helper()
	if !strings.Contains(validFixture, old) {
		t.Fatalf("the valid fixture has no %q", old)
	}
	return strings.Replace(validFixture, old, new, 1)
}

func TestAFixtureWithOneThingWrongIsRefusedNamingIt(t *testing.T) {
	cases := map[string]struct {
		edit func(t *testing.T) string
		want string
	}{
		"unknown top-level key": {func(t *testing.T) string { return validFixture + "extra: 1\n" }, "extra"},
		"unknown seat key": {func(t *testing.T) string {
			return replace(t, "    rsa_key_ref: cf-rsa-primary\n", "    rsa_key: cf-rsa-primary\n")
		}, "rsa_key"},
		"second document": {func(t *testing.T) string { return validFixture + "---\nversion: 1\n" }, "more than one"},
		"wrong version":   {func(t *testing.T) string { return replace(t, "version: 1", "version: 2") }, "version"},
		"bad tenant id": {func(t *testing.T) string {
			return replace(t, "tenant_id: seat-2\n", "tenant_id: seat 2\n")
		}, "seats[1].tenant_id"},
		"duplicate seat": {func(t *testing.T) string {
			return replace(t, "tenant_id: seat-2\n", "tenant_id: seat\n")
		}, "declared twice"},
		"duplicate seat domain": {func(t *testing.T) string {
			return replace(t, "domain: second.exchange.example", "domain: exchange.example:8081")
		}, "seats[1].domain"},
		"seat domain upper case": {func(t *testing.T) string {
			return replace(t, "domain: second.exchange.example", "domain: Second.exchange.example")
		}, "seats[1].domain"},
		"seat domain with a path": {func(t *testing.T) string {
			return replace(t, "domain: second.exchange.example", "domain: second.exchange.example/x")
		}, "seats[1].domain"},
		"seat domain port out of range": {func(t *testing.T) string {
			return replace(t, "exchange.example:8081", "exchange.example:99999")
		}, "port"},
		"no Ed25519 key reference": {func(t *testing.T) string {
			return strings.Replace(validFixture, "    ed25519_key_ref: exchange-primary\n", "", 1)
		}, "seats[0].ed25519_key_ref"},
		"no relay policy": {func(t *testing.T) string {
			return replace(t, "    allow_broker_relay: false\n", "")
		}, "seats[1].allow_broker_relay is required"},
		"no activation policy": {func(t *testing.T) string {
			return replace(t, "    activate_new_agents_by_default: false\n", "")
		}, "seats[1].activate_new_agents_by_default is required"},
		"no display name": {func(t *testing.T) string {
			return replace(t, "display_name: Example Publisher", "display_name: \"  \"")
		}, "publishers[0].display_name"},
		"site domain not normalized": {func(t *testing.T) string {
			return replace(t, "domain: www.publisher.example", "domain: WWW.publisher.example")
		}, "publishers[0].sites[0].domain"},
		"site domain with a port": {func(t *testing.T) string {
			return replace(t, "domain: www.publisher.example", "domain: www.publisher.example:443")
		}, "publishers[0].sites[0].domain"},
		"duplicate site": {func(t *testing.T) string {
			return replace(t, "domain: media.publisher.example", "domain: www.publisher.example")
		}, "declared twice"},
		"unknown scheme": {func(t *testing.T) string {
			return replace(t, "signing_scheme: ED25519", "signing_scheme: HMAC")
		}, "publishers[0].sites[0].signing_scheme"},
		"unknown cdn": {func(t *testing.T) string {
			return replace(t, "cdn: cloudflare", "cdn: akamai")
		}, "publishers[0].sites[0].cdn"},
		"RSA site without a key-pair id": {func(t *testing.T) string {
			return replace(t, "        cloudfront_key_pair_id: KEXAMPLE\n", "")
		}, "publishers[0].sites[1].cloudfront_key_pair_id"},
		"Ed25519 site with a key-pair id": {func(t *testing.T) string {
			return replace(t, "        cdn: cloudflare\n", "        cdn: cloudflare\n        cloudfront_key_pair_id: K1\n")
		}, "publishers[0].sites[0].cloudfront_key_pair_id"},
		"listing on an undeclared seat": {func(t *testing.T) string {
			return replace(t, "          - tenant_id: seat-2\n", "          - tenant_id: seat-3\n")
		}, "publishers[0].sites[0].listings[1].tenant_id"},
		"CloudFront RSA site on a seat with no RSA key": {func(t *testing.T) string {
			return replace(t, "    rsa_key_ref: cf-rsa-primary\n", "")
		}, "publishers[0].sites[1].listings[0].tenant_id"},
		"site listed twice on one seat": {func(t *testing.T) string {
			return replace(t, "          - tenant_id: seat-2\n", "          - tenant_id: seat\n")
		}, "listed on seat"},
		"unknown listing key": {func(t *testing.T) string {
			return replace(t, "            content_path_scope:", "            terms_file_note: x\n            content_path_scope:")
		}, "terms_file_note"},
		"terms file escaping": {func(t *testing.T) string {
			return strings.Replace(validFixture, "terms_file: terms.json", "terms_file: ../terms.json", 1)
		}, "publishers[0].sites[0].listings[0].terms_file"},
		"missing terms file": {func(t *testing.T) string {
			return strings.Replace(validFixture, "terms_file: terms.json", "terms_file: none.json", 1)
		}, "publishers[0].sites[0].listings[0].terms_file"},
		"terms file that is not an object": {func(t *testing.T) string {
			return strings.Replace(validFixture, "terms_file: terms.json", "terms_file: list.json", 1)
		}, "JSON object"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := termsFS()
			fsys["list.json"] = &fstest.MapFile{Data: []byte(`["search"]`)}
			_, err := bootfixture.Parse([]byte(tc.edit(t)), fsys)
			if err == nil {
				t.Fatalf("Parse accepted the fixture; want an error naming %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to name %q", err, tc.want)
			}
		})
	}
}

func TestAFixtureThatDeclaresNothingIsRefused(t *testing.T) {
	for name, raw := range map[string]string{
		"empty file":        "",
		"only a comment":    "# nothing here\n",
		"only a version":    "version: 1\n",
		"no seat, no sites": "version: 1\nseats: []\npublishers: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := bootfixture.Parse([]byte(raw), termsFS()); err == nil {
				t.Fatal("Parse accepted a fixture that declares nothing")
			}
		})
	}
}

func TestAPublisherWithoutSitesOrASiteWithoutListingsIsRefused(t *testing.T) {
	noSites := "version: 1\nseats: []\npublishers:\n  - publisher_id: p\n    display_name: P\n    sites: []\n"
	if _, err := bootfixture.Parse([]byte(noSites), termsFS()); err == nil ||
		!strings.Contains(err.Error(), "publishers[0].sites") {
		t.Errorf("a publisher with no site: err = %v", err)
	}
	noListings := strings.Replace(validFixture, `        listings:
          - tenant_id: seat
            terms_file: terms.json
            content_path_scope: ["/articles/"]
          - tenant_id: seat-2
            terms_file: terms.json
`, "        listings: []\n", 1)
	if _, err := bootfixture.Parse([]byte(noListings), termsFS()); err == nil ||
		!strings.Contains(err.Error(), "publishers[0].sites[0].listings") {
		t.Errorf("a site with no listing: err = %v", err)
	}
}

func TestATermsFileLargerThanTheCapIsRefused(t *testing.T) {
	fsys := termsFS()
	fsys["terms.json"] = &fstest.MapFile{Data: []byte(`{"x":"` + strings.Repeat("a", 64<<10) + `"}`)}
	if _, err := bootfixture.Parse([]byte(validFixture), fsys); err == nil ||
		!strings.Contains(err.Error(), "larger than") {
		t.Fatalf("an oversized terms file: err = %v", err)
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestLoadReadsTheFixtureAndItsTermsFromItsDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "terms.json", freeTerms)
	path := writeFile(t, dir, "fixture.yml", validFixture)

	f, err := bootfixture.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.Path != path || len(f.Seats) != 2 {
		t.Errorf("Load = %+v, want the fixture at %s", f, path)
	}
}

func TestLoadRefusesAMissingOrOversizedFixture(t *testing.T) {
	dir := t.TempDir()
	if _, err := bootfixture.Load(filepath.Join(dir, "absent.yml")); err == nil {
		t.Error("Load accepted a fixture that does not exist")
	}
	big := writeFile(t, dir, "big.yml", "version: 1\n# "+strings.Repeat("x", 1<<20)+"\n")
	if _, err := bootfixture.Load(big); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("an oversized fixture: err = %v", err)
	}
}

func TestATermsFileLinkedFromOutsideTheDirectoryIsRefused(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "terms.json", freeTerms)
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "terms.json"), filepath.Join(dir, "terms.json")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	path := writeFile(t, dir, "fixture.yml", validFixture)

	if _, err := bootfixture.Load(path); err == nil {
		t.Fatal("Load followed a terms file linked from outside the fixture's directory")
	}
}
