package edgerelease_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/edge/edgerelease"
)

// The files a site's edge package is configured by. They are generated text
// read by other programs -- wrangler, Node, workerd -- so each is pinned byte
// for byte to a golden under testdata/. The edge test suite runs those same
// goldens: tests/node/release-package.test.ts puts lambda-index.mjs.golden in front of
// a real Lambda bundle, and tests/node/cloudflare-package.test.ts runs
// wrangler.toml with a real Worker bundle in Miniflare. So a change here that
// the edge cannot run fails there, and a change there that this package does
// not write fails here.
//
// If a change to either file is intended, write the new output into the golden
// and run the edge tests against it.

const goldenDomain = "www.publisher.example"

// goldenSettings are complete enough for the edge suite to run the manifest
// route, a signed read, a denial and a pass-through outside the licensed paths.
// The verify key is the public half of a key the edge tests derive from a fixed
// seed, so the edge can check a URL signed there without fetching a directory.
// The second licensed path carries an "&", which Go's encoder would escape and
// JSON.stringify does not; the two renderers must agree on it.
func goldenSettings() map[string]string {
	return map[string]string{
		"EXCHANGE_URL":              "https://exchange.example",
		"EXCHANGE_WBA_URL":          "https://exchange.example/.well-known/http-message-signatures-directory",
		"PROVIDER":                  goldenDomain,
		"EXCHANGES_JSON":            `[{"domain":"exchange.example","endpoint":"https://exchange.example"}]`,
		"CATALOG_CONTRIBUTORS_JSON": "[]",
		"FORA_ENFORCE_BINDING":      "true",
		"FORA_VERIFY_KEYS":          `[{"kty":"OKP","crv":"Ed25519","x":"l3cHOINRwzU9vsPaa4_UlbRlCQeb1OpNNXOdC3sjlb4"}]`,
		"LICENSED_PATHS_JSON":       `["/article/","/q&a/"]`,
	}
}

func golden(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestLambdaEntryMatchesTheGolden(t *testing.T) {
	t.Parallel()
	got, err := edgerelease.LambdaEntry(goldenDomain, "lambda-edge.mjs", goldenSettings())
	if err != nil {
		t.Fatal(err)
	}
	if want := golden(t, "lambda-index.mjs.golden"); string(got) != want {
		t.Fatalf("index.mjs differs from testdata/lambda-index.mjs.golden; this package wrote:\n%s", got)
	}
}

func TestWranglerConfigMatchesTheGolden(t *testing.T) {
	t.Parallel()
	settings := goldenSettings()
	// The Worker sits on a route in front of the publisher's own zone.
	settings["SAME_ZONE_ORIGIN"] = "true"
	got, err := edgerelease.WranglerConfig(goldenDomain, "fora-worker.mjs", settings)
	if err != nil {
		t.Fatal(err)
	}
	if want := golden(t, "wrangler.toml"); string(got) != want {
		t.Fatalf("wrangler.toml differs from testdata/wrangler.toml; this package wrote:\n%s", got)
	}
}

// TestTheProjectFileRunsUnderTheTestedRuntime: the date and flags in a
// publisher's project are the ones in workers-compat.json, which is what the
// edge's own test configuration and local server run under.
func TestTheProjectFileRunsUnderTheTestedRuntime(t *testing.T) {
	t.Parallel()
	var compat struct {
		Date  string   `json:"compatibility_date"`
		Flags []string `json:"compatibility_flags"`
	}
	raw, err := os.ReadFile("workers-compat.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &compat); err != nil {
		t.Fatal(err)
	}
	if compat.Date == "" || len(compat.Flags) == 0 {
		t.Fatalf("workers-compat.json names no date or no flags: %s", raw)
	}
	toml, err := edgerelease.WranglerConfig(goldenDomain, "fora-worker.mjs", nil)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := json.Marshal(compat.Flags)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`compatibility_date = "` + compat.Date + `"`,
		"compatibility_flags = " + string(flags),
	} {
		if !strings.Contains(string(toml), want) {
			t.Fatalf("the project file is missing %s:\n%s", want, toml)
		}
	}
}

var validWorkerName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// TestEachDomainGetsItsOwnWorker: a deploy replaces the Worker of the same
// name, so two sites in one account must never share one, and the same site
// must always get the same one.
func TestEachDomainGetsItsOwnWorker(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 60) + ".publisher.example"
	domains := []string{
		"a.example", "b.example",
		// Read the same once the dots become dashes.
		"a-b.example", "a.b-example",
		// Share far more than the readable part of the name holds.
		long, "b" + long[1:], long[:len(long)-len("example")] + "exampla",
		"xn--bcher-kva.example",
	}
	seen := map[string]string{}
	for _, d := range domains {
		name := edgerelease.WorkerName(d)
		if !validWorkerName.MatchString(name) || len(name) > 63 {
			t.Errorf("%s: %q is not a name Cloudflare accepts for a Worker", d, name)
		}
		if other, dup := seen[name]; dup {
			t.Errorf("%s and %s both deploy as %q", other, d, name)
		}
		seen[name] = d
		if again := edgerelease.WorkerName(d); again != name {
			t.Errorf("%s: named %q, then %q", d, name, again)
		}
	}
	if got := edgerelease.WorkerName(goldenDomain); !strings.HasPrefix(got, "fora-www-publisher-example-") {
		t.Errorf("the name %q does not say which site it serves", got)
	}
}

// TestTheProjectFilesOfTwoDomainsNameTwoWorkers is the same property where it
// matters: in the file wrangler deploys from.
func TestTheProjectFilesOfTwoDomainsNameTwoWorkers(t *testing.T) {
	t.Parallel()
	names := map[string]bool{}
	for _, d := range []string{"a.example", "b.example"} {
		toml, err := edgerelease.WranglerConfig(d, "fora-worker.mjs", nil)
		if err != nil {
			t.Fatal(err)
		}
		line := regexp.MustCompile(`(?m)^name = "([^"]+)"$`).FindStringSubmatch(string(toml))
		if line == nil {
			t.Fatalf("%s: the project file names no Worker:\n%s", d, toml)
		}
		names[line[1]] = true
	}
	if len(names) != 2 {
		t.Fatalf("two domains deploy as %v", slicesOf(names))
	}
}

// TestTheLambdaEntryKeepsValuesAsValues: a value that looks like markup or
// like one of the template's own placeholders is written as the string it is.
func TestTheLambdaEntryKeepsValuesAsValues(t *testing.T) {
	t.Parallel()
	got, err := edgerelease.LambdaEntry("a.example", "lambda-edge.mjs",
		map[string]string{"PROVIDER": "<a>&{{BUNDLE}}"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"PROVIDER": "<a>&{{BUNDLE}}"`) {
		t.Fatalf("the value was rewritten:\n%s", got)
	}
}

// TestTheLambdaEntryRefusesADomainThatCouldEndItsComment: the domain is
// written into a comment, so a line break in it would turn the rest into code.
func TestTheLambdaEntryRefusesADomainThatCouldEndItsComment(t *testing.T) {
	t.Parallel()
	for _, d := range []string{"", "a.example\nglobalThis.stolen = 1;", "a example"} {
		if _, err := edgerelease.LambdaEntry(d, "lambda-edge.mjs", nil); err == nil {
			t.Errorf("an entry was written for the domain %q", d)
		}
	}
}

func slicesOf(m map[string]bool) []string {
	return slices.Sorted(maps.Keys(m))
}
