package setup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/edge/edgerelease"
)

// The package a publisher installs. What these hold to is the property the
// whole design rests on: one build per release, and the only per-publisher
// part is settings that can be read.

// releaseDir stands in for what the image ships. The bodies are markers rather
// than real bundles -- what is under test is the assembly, and a real bundle
// would make every assertion here about esbuild's output instead.
func releaseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"cloudflare-worker.mjs": "export default { fetch() {} };\n",
		"lambda-edge.mjs":       "export { handler };\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func settings() map[string]string {
	return map[string]string{
		"PROVIDER":     "www.publisher.example",
		"EXCHANGE_URL": "https://exchange.example",
	}
}

// TestCloudflarePackageNeedsNothingFilledIn is what "we give a package, they
// install it" has to mean on this runtime. Handing over the worker alone left
// the publisher setting six variables by hand and writing a route, which is the
// work this exists to remove.
func TestCloudflarePackageNeedsNothingFilledIn(t *testing.T) {
	t.Parallel()
	compat, err := edgerelease.Compat()
	if err != nil {
		t.Fatal(err)
	}
	p := setup.NewEdgePackages(releaseDir(t))
	pkg, err := p.For(setup.Site{Domain: "www.publisher.example", CDN: "cloudflare"}, settings())
	if err != nil {
		t.Fatal(err)
	}
	entries := entriesOf(t, pkg.Body)
	if _, ok := entries["fora-worker.mjs"]; !ok {
		t.Fatalf("the package holds no worker: %v", keysOf(entries))
	}
	toml, ok := entries["wrangler.toml"]
	if !ok {
		t.Fatalf("the package holds no project file: %v", keysOf(entries))
	}
	// Everything wrangler would otherwise ask for.
	for _, want := range []string{
		`main = "fora-worker.mjs"`,
		`compatibility_date = "` + compat.Date + `"`,
		`pattern = "www.publisher.example/*"`,
		`zone_name = "publisher.example"`,
		`PROVIDER = "www.publisher.example"`,
		`EXCHANGE_URL = "https://exchange.example"`,
		// The worker refuses to start without an origin mode.
		`SAME_ZONE_ORIGIN = "true"`,
	} {
		if !strings.Contains(toml, want) {
			t.Fatalf("the project file is missing %s:\n%s", want, toml)
		}
	}
}

// TestTheWorkerIsTheSameForEveryPublisher is the property that makes one build
// enough: what differs between two publishers is the project file beside the
// worker, never the worker.
func TestTheWorkerIsTheSameForEveryPublisher(t *testing.T) {
	t.Parallel()
	p := setup.NewEdgePackages(releaseDir(t))
	first, err := p.For(setup.Site{Domain: "a.example", CDN: "cloudflare"}, settings())
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.For(setup.Site{Domain: "b.example", CDN: "cloudflare"},
		map[string]string{"PROVIDER": "b.example"})
	if err != nil {
		t.Fatal(err)
	}
	a, b := entriesOf(t, first.Body), entriesOf(t, second.Body)
	if a["fora-worker.mjs"] != b["fora-worker.mjs"] {
		t.Fatal("two publishers were handed different workers")
	}
	if strings.Contains(a["fora-worker.mjs"], "a.example") {
		t.Fatalf("a publisher's own domain reached the worker: %s", a["fora-worker.mjs"])
	}
	if a["wrangler.toml"] == b["wrangler.toml"] {
		t.Fatal("two publishers were handed the same project file, so one of them is wrong")
	}
}

// TestTheProjectFileKeepsAPublishersValuesAsValues is the Cloudflare half of
// the hazard the Lambda package has: a domain full of quotes must stay a value
// and not become configuration.
func TestTheProjectFileKeepsAPublishersValuesAsValues(t *testing.T) {
	t.Parallel()
	p := setup.NewEdgePackages(releaseDir(t))
	hostile := `a.example"\nadmin = true\nx = "`
	pkg, err := p.For(setup.Site{Domain: "a.example", CDN: "cloudflare"},
		map[string]string{"PROVIDER": hostile})
	if err != nil {
		t.Fatal(err)
	}
	toml := entriesOf(t, pkg.Body)["wrangler.toml"]
	encoded, err := json.Marshal(hostile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, string(encoded)) {
		t.Fatalf("the value is not in the file as the encoder writes it:\n%s", toml)
	}
	if strings.Contains(toml, "\nadmin = true") {
		t.Fatalf("a value became a setting:\n%s", toml)
	}
}

// TestLambdaCarriesItsSettingsBesideTheReleaseBundle is the same property on
// the runtime that cannot take settings any other way: the bundle is in the
// package under its release name, byte-identical for everyone, and only the
// entry module beside it differs.
func TestLambdaCarriesItsSettingsBesideTheReleaseBundle(t *testing.T) {
	t.Parallel()
	dir := releaseDir(t)
	release, err := os.ReadFile(filepath.Join(dir, "lambda-edge.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	p := setup.NewEdgePackages(dir)
	pkg, err := p.For(setup.Site{Domain: "www.publisher.example", CDN: "cloudfront"}, settings())
	if err != nil {
		t.Fatal(err)
	}
	entries := entriesOf(t, pkg.Body)
	if got := keysOf(entries); !slices.Equal(got, []string{"index.mjs", "lambda-edge.mjs"}) {
		t.Fatalf("the package holds %v, want the entry and the release bundle", got)
	}
	if entries["lambda-edge.mjs"] != string(release) {
		t.Fatal("the release bundle in the package is not the release bundle")
	}
	entry := entries["index.mjs"]
	if !strings.Contains(entry, `"PROVIDER": "www.publisher.example"`) {
		t.Fatalf("the settings are not in the entry:\n%s", entry)
	}
	if !strings.Contains(entry, "export { handler } from './lambda-edge.mjs';") {
		t.Fatalf("the entry does not hand Lambda the release handler:\n%s", entry)
	}
}

// TestLambdaEntryIsTheReleaseEntry: the index.mjs in a publisher's download is
// the one edgerelease renders, which is pinned to a golden the edge suite runs
// against a real release bundle. So the console cannot hand out an entry the
// edge has not run.
func TestLambdaEntryIsTheReleaseEntry(t *testing.T) {
	t.Parallel()
	p := setup.NewEdgePackages(releaseDir(t))
	pkg, err := p.For(setup.Site{Domain: "www.publisher.example", CDN: "cloudfront"}, settings())
	if err != nil {
		t.Fatal(err)
	}
	want, err := edgerelease.LambdaEntry("www.publisher.example", "lambda-edge.mjs", settings())
	if err != nil {
		t.Fatal(err)
	}
	if got := entriesOf(t, pkg.Body)["index.mjs"]; got != string(want) {
		t.Fatalf("the package's index.mjs is not the release entry:\n%s", got)
	}
}

// TestTwoSitesInOneAccountDeployTwoWorkers: a deploy replaces the Worker of the
// same name, so two of a publisher's sites must never be handed one name, or
// the second deploy would take over the first site's Worker.
func TestTwoSitesInOneAccountDeployTwoWorkers(t *testing.T) {
	t.Parallel()
	p := setup.NewEdgePackages(releaseDir(t))
	names := map[string]string{}
	for _, domain := range []string{"a.example", "b.example"} {
		pkg, err := p.For(setup.Site{Domain: domain, CDN: "cloudflare"}, settings())
		if err != nil {
			t.Fatal(err)
		}
		toml := entriesOf(t, pkg.Body)["wrangler.toml"]
		want := `name = "` + edgerelease.WorkerName(domain) + `"`
		if !strings.Contains(toml, want) {
			t.Fatalf("%s: the project file does not say %s:\n%s", domain, want, toml)
		}
		if other, dup := names[want]; dup {
			t.Fatalf("%s and %s deploy as the same Worker", other, domain)
		}
		names[want] = domain
	}
}

// TestLambdaPackageKeepsAPublishersValuesAsValues covers the one hazard of
// putting somebody's typing into a file that will be executed. The settings are
// JSON, so a domain full of quotes and braces is a string when it arrives and
// never part of the program.
func TestLambdaPackageKeepsAPublishersValuesAsValues(t *testing.T) {
	t.Parallel()
	p := setup.NewEdgePackages(releaseDir(t))
	hostile := `a.example"};globalThis.stolen=1;Object.assign(process.env,{"x":"`
	pkg, err := p.For(setup.Site{Domain: "a.example", CDN: "cloudfront"},
		map[string]string{"PROVIDER": hostile})
	if err != nil {
		t.Fatal(err)
	}
	body := entriesOf(t, pkg.Body)["index.mjs"]
	// Held against what the encoder produces, not against a substring. Both
	// looser checks fail here for the same reason and in opposite directions:
	// the escaped form CONTAINS the unescaped one, and the hostile value
	// contains the assignment's own text, so neither "does it appear" nor
	// "how many times" can tell code from content.
	encoded, err := json.Marshal(hostile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, string(encoded)) {
		t.Fatalf("the value is not in the package as the encoder writes it:\n%s", body)
	}
	if strings.Contains(body, hostile) {
		t.Fatalf("the value appears unescaped, so it closed its string:\n%s", body)
	}
}

// TestADomainThatIsNotNormalizedGetsNoPackage covers the one value written
// outside the JSON: the domain in each package's header comment. A line break
// in it would end the comment and turn the rest of the line into code in
// index.mjs, or into configuration in wrangler.toml. A domain the Exchange
// registered is already normalized, so refusing anything else costs nothing.
func TestADomainThatIsNotNormalizedGetsNoPackage(t *testing.T) {
	t.Parallel()
	p := setup.NewEdgePackages(releaseDir(t))
	for _, domain := range []string{
		"a.example\nglobalThis.stolen = 1;",
		"a.example\r\nadmin = true",
		"A.Example",
		" a.example",
		"a.example.",
		"a.example/path",
		"",
	} {
		for _, network := range []string{"cloudfront", "cloudflare"} {
			_, err := p.For(setup.Site{Domain: domain, CDN: network}, settings())
			if err == nil {
				t.Errorf("%s: a package was built for the domain %q", network, domain)
			}
		}
	}
}

// TestADeploymentWithoutPackagesSaysSo: a deployment that ships none is not a
// publisher's fault and must not look like a broken download.
func TestADeploymentWithoutPackagesSaysSo(t *testing.T) {
	t.Parallel()
	if setup.NewEdgePackages("").Available() {
		t.Fatal("a deployment with no package directory reported packages")
	}
	_, err := setup.NewEdgePackages("").For(setup.Site{CDN: "cloudflare"}, nil)
	if err == nil {
		t.Fatal("asking for a package succeeded where none are shipped")
	}
}

// TestFastlyIsHandedNoPackage: recognised, and nothing to install. Handing over
// a Cloudflare worker would be worse than handing over nothing.
func TestFastlyIsHandedNoPackage(t *testing.T) {
	t.Parallel()
	_, err := setup.NewEdgePackages(releaseDir(t)).
		For(setup.Site{Domain: "a.example", CDN: "fastly"}, settings())
	if err == nil {
		t.Fatal("a network we ship no package for was handed one")
	}
}

// entriesOf reads every file out of a package.
func entriesOf(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("the package is not a zip: %v", err)
	}
	out := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(body)
	}
	return out
}

func keysOf(m map[string]string) []string {
	return slices.Sorted(maps.Keys(m))
}

// TestThePackageReplacesTheSettingsFile is the swap the package made possible.
//
// The settings file told a publisher the values to build or deploy with. The
// package carries those values inside it, so handing both over is the same
// values twice with only one of them to act on -- and the one to act on is the
// one that cannot be typed wrong. A deployment that ships no package is the
// only place the file is still the publisher's only way to get them.
func TestThePackageReplacesTheSettingsFile(t *testing.T) {
	t.Parallel()
	site := setup.Site{Domain: "www.publisher.example", PublisherID: "pub_1", CDN: "cloudflare"}
	deployment := setup.Deployment{
		ExchangeDomain: "exchange.example", ExchangeEndpoint: "https://exchange.example",
		ContributorHost: "catalog-contributor.exchange.example",
	}

	withPackages := deployment
	withPackages.EdgePackageDir = releaseDir(t)
	got := fileNames(t, setup.NewBuilder(withPackages, nil, termspreset.Defaults{}), site)
	if slices.Contains(got, setup.FileEdgeConfig) {
		t.Fatalf("the settings file was handed over beside the package: %v", got)
	}
	if !slices.Contains(got, setup.FileCloudflarePackage) {
		t.Fatalf("no package was handed over: %v", got)
	}

	got = fileNames(t, setup.NewBuilder(deployment, nil, termspreset.Defaults{}), site)
	if !slices.Contains(got, setup.FileEdgeConfig) {
		t.Fatalf("a deployment with no package handed over no settings either: %v", got)
	}
}

func fileNames(t *testing.T, b *setup.Builder, s setup.Site) []string {
	t.Helper()
	bundle, err := b.Build(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(bundle.Files))
	for _, f := range bundle.Files {
		out = append(out, f.Name)
	}
	return out
}

// TestThePackageCarriesTheLicensedPaths: the paths a publisher licenses are the
// only ones the edge sends AI bots to negotiate for, so they must reach the
// package. Without them, a site licensing /paid/ and one licensing /other/
// would be handed identical packages that gate the whole site.
func TestThePackageCarriesTheLicensedPaths(t *testing.T) {
	t.Parallel()
	b := packagingBuilder(t)
	for _, network := range []struct{ cdn, file string }{
		{"cloudflare", "wrangler.toml"},
		{"cloudfront", "index.mjs"},
	} {
		files := map[string]string{}
		for _, scope := range []string{"/paid/", "/other/"} {
			site := setup.Site{
				Domain: "www.publisher.example", PublisherID: "pub_1",
				CDN: network.cdn, ContentPathScope: []string{scope},
			}
			body := packageFile(t, b, site, network.file)
			if !strings.Contains(body, "LICENSED_PATHS_JSON") || !strings.Contains(body, scope) {
				t.Fatalf("%s: the package for %s does not carry it:\n%s", network.cdn, scope, body)
			}
			files[scope] = body
		}
		if files["/paid/"] == files["/other/"] {
			t.Fatalf("%s: two scopes were handed the same package", network.cdn)
		}
	}
}

// TestNoScopeGatesTheWholeSite: a publisher who named no paths licenses the
// whole site, which is the edge's default when the setting is absent.
func TestNoScopeGatesTheWholeSite(t *testing.T) {
	t.Parallel()
	site := setup.Site{Domain: "www.publisher.example", PublisherID: "pub_1", CDN: "cloudflare"}
	body := packageFile(t, packagingBuilder(t), site, "wrangler.toml")
	if strings.Contains(body, "LICENSED_PATHS_JSON") {
		t.Fatalf("a site with no scope was handed one:\n%s", body)
	}
}

func packagingBuilder(t *testing.T) *setup.Builder {
	t.Helper()
	return setup.NewBuilder(setup.Deployment{
		ExchangeDomain: "exchange.example", ExchangeEndpoint: "https://exchange.example",
		ContributorHost: "catalog-contributor.exchange.example", EdgePackageDir: releaseDir(t),
	}, nil, termspreset.Defaults{})
}

// packageFile builds the site's bundle and reads one file out of its package.
func packageFile(t *testing.T, b *setup.Builder, s setup.Site, name string) string {
	t.Helper()
	bundle, err := b.Build(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range bundle.Files {
		if f.Name == setup.FileCloudflarePackage || f.Name == setup.FileLambdaPackage {
			return entriesOf(t, f.Body)[name]
		}
	}
	t.Fatalf("no package in the bundle for %s", s.CDN)
	return ""
}
