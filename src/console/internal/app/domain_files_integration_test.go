//go:build integration

package app_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/setupsteps"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup/setuptest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
)

// Downloading a domain's setup files, from its own page and from the wizard's
// last step.
//
// A saved domain's files are built from the registration the Exchange holds:
// each download reads it back through the Exchange's registrations/list route
// under the signed-in person's own actor, and the console builds the files from
// what comes back. Nothing in the request chooses what they say.

// rePageLink finds the download links a domain page renders.
var rePageLink = regexp.MustCompile(`href="(/domains/[^"]+/files/[^"]+)"`)

// sitePages is a site that can be read, so the files include the sample feed.
func sitePages() stubPageReader {
	return stubPageReader{page: pagefetch.Page{
		Title: "On rivers", CanonicalURL: "https://stoa.example/articles/on-rivers",
	}}
}

// TestConsole_AReturningPublisherDownloadsTheFilesTheDomainWasRegisteredWith
// signs in again in a new browser, after the wizard is long gone, and downloads
// every file from the domain page.
//
// The round trip is: the wizard's routes register the domain on the real
// Exchange; the domain page and each download read it back from the Exchange
// and build the files. The discovery document and the edge settings are
// compared with what the wizard handed over for the same answers, and the
// page shows the terms at the price the domain was registered at.
func TestConsole_AReturningPublisherDownloadsTheFilesTheDomainWasRegisteredWith(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(sitePages()))
	f.signIn(t)
	addDomain(t, f)
	handedOver := fileBodies(t, f.files(t, "stoa.example"))

	f.client = newBrowser(t)
	f.signIn(t)
	page := f.get2(t, "/domains/stoa.example")
	downloaded := map[string]string{}
	for _, m := range rePageLink.FindAllStringSubmatch(page, -1) {
		status, body := f.get(t, m[1])
		if status != http.StatusOK {
			t.Fatalf("downloading %s = %d", m[1], status)
		}
		downloaded[m[1][strings.LastIndex(m[1], "/")+1:]] = body
	}
	for _, name := range []string{setup.FileManifest, setup.FileEdgeConfig} {
		if downloaded[name] == "" || downloaded[name] != handedOver[name] {
			t.Fatalf("the downloaded %s is not the file the domain was set up with:\n%s", name, downloaded[name])
		}
	}
	owner, _, err := setup.ManifestReadsBack([]byte(downloaded[setup.FileManifest]), testExchangeDomain)
	if err != nil || owner != f.publisherID {
		t.Fatalf("the downloaded document attests %q (%v), want this publisher's payee id", owner, err)
	}
	// The edge settings describe the package only. The discovery document is the
	// file the publisher serves from its own site, which the package passes
	// through, so no setting restates it.
	var edge map[string]any
	if err := json.Unmarshal([]byte(downloaded[setup.FileEdgeConfig]), &edge); err != nil {
		t.Fatalf("the downloaded edge settings do not parse: %v", err)
	}
	for _, retired := range []string{"PROVIDER", "EXCHANGES_JSON", "CATALOG_CONTRIBUTORS_JSON", "RSL_BODY"} {
		if _, ok := edge[retired]; ok {
			t.Fatalf("the downloaded edge settings still carry %s:\n%s", retired, downloaded[setup.FileEdgeConfig])
		}
	}
	// The terms are shown as the domain was registered, in the form the
	// licensing step set them with, and none of it can be changed here. Beside
	// them is the offer they produce, for the page of the site the sample was
	// built from, sold by this Exchange.
	for _, want := range []string{
		`<fieldset class="sample terms-readonly" disabled>`, `value="0.02"`, "per fetch<",
		`<span class="val"> On rivers</span>`, `<span class="val"> ` + testExchangeDomain + `</span>`,
		`<span class="val"> https://stoa.example/articles/on-rivers</span>`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("the domain page does not show the registered terms (%q): %s", want, head(page))
		}
	}
	if _, offered := downloaded[setup.FileSampleFeed]; offered {
		t.Fatal("the domain page still offers the sample feed")
	}
}

// TestConsole_RefusesAnotherPublishersFiles is the same download, asked for a
// domain somebody else registered. It is answered the way an unregistered
// domain is, and none of the file is sent.
func TestConsole_RefusesAnotherPublishersFiles(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	addDomain(t, f.otherPerson(t, otherClaims()))
	f.signIn(t)
	f.registerBusiness(t)

	status, body := f.get(t, "/domains/stoa.example/files/"+setup.FileManifest)
	if status != http.StatusNotFound {
		t.Fatalf("another publisher's file = %d, want %d", status, http.StatusNotFound)
	}
	if strings.Contains(body, "resource_owner_id") {
		t.Fatalf("the refusal carries the file: %s", head(body))
	}
}

// TestConsole_RefusesAFileTheDomainDoesNotHave asks a registered domain for a
// name the builder never produces.
func TestConsole_RefusesAFileTheDomainDoesNotHave(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	addDomain(t, f)

	if status, _ := f.get(t, "/domains/stoa.example/files/secrets.json"); status != http.StatusNotFound {
		t.Fatalf("an unknown file = %d, want %d", status, http.StatusNotFound)
	}
}

// TestConsole_TheLastStepsFilesAreOnlyForASavedDomain: the last step's files
// are built from a saved domain, so an address that is not a hostname is
// refused as one, and a domain nobody saved is answered the way another
// publisher's is. Neither answer carries a file.
func TestConsole_TheLastStepsFilesAreOnlyForASavedDomain(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToTerms(t, "stoa.example")

	for _, tc := range []struct {
		name, domain string
		want         int
	}{
		{"an address that is not a hostname", "https://stoa.example/x", http.StatusBadRequest},
		{"a domain nobody saved", "harmonia.example", http.StatusNotFound},
	} {
		status, out := f.api(t, web.APIFilesPath, map[string]any{"domain": tc.domain})
		if status != tc.want {
			t.Fatalf("%s = %d, want %d: %v", tc.name, status, tc.want, out)
		}
		if strings.Contains(stringOf(out), "resource_owner_id") {
			t.Fatalf("%s: the refusal carries a file: %v", tc.name, out)
		}
	}
}

// TestConsole_AProhibitionInTheSitesLicenceIsWhatTheExchangeStores follows a
// use the site's own licensing document prohibits from the prefill to the
// registration. A prohibition is a statement the publisher made; dropping it on
// the way would leave the use merely unpermitted.
//
// The prefill is read from the wizard's page route and sent back unchanged,
// the way the browser does; what the Exchange stored is read back through its
// registrations/list route.
func TestConsole_AProhibitionInTheSitesLicenceIsWhatTheExchangeStores(t *testing.T) {
	pages := sitePages()
	pages.license = `<rsl xmlns="https://rslstandard.org/rsl">
  <content url="https://stoa.example/articles/">
    <license>
      <permits type="usage">search</permits>
      <prohibits type="usage">train-ai</prohibits>
      <payment type="free"></payment>
    </license>
  </content>
</rsl>`
	f := startConsole(t, verifiedClaims(), withPages(pages))
	f.signIn(t)
	f.walkToTerms(t, "stoa.example")
	prefill := f.ok(t, web.APIPagePath, map[string]any{"domain": "stoa.example"})
	terms, _ := prefill["terms"].(map[string]any)
	if terms == nil {
		t.Fatalf("the licence was not read: %v", prefill)
	}

	f.setTerms(t, "stoa.example", map[string]any{"terms": terms})
	sites := f.mine(t).Sites
	if len(sites) != 1 || !slices.Contains(sites[0].Terms.ProhibitedFunctions, "ai-train") {
		t.Fatalf("the Exchange stored %+v, want ai-train prohibited", sites)
	}
}

// TestConsole_ATickedUseIsNoLongerProhibited is the other direction: the
// publisher ticks a use the defaults prohibit. The tick is the latest thing
// they said, so the use is registered as permitted rather than refused as a
// contradiction.
func TestConsole_ATickedUseIsNoLongerProhibited(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	addDomainWith(t, f, "harmonia.example", map[string]any{
		"pricing_model":        "free",
		"permitted_functions":  []string{"search", "ai-train"},
		"prohibited_functions": []string{"ai-train"},
	})

	got := f.mine(t).Sites[0].Terms
	if !slices.Contains(got.PermittedFunctions, "ai-train") || slices.Contains(got.ProhibitedFunctions, "ai-train") {
		t.Fatalf("the Exchange stored %+v, want ai-train permitted and not prohibited", got)
	}
}

// TestConsole_TheLastStepOnCloudFrontHandsOverTheLambdaPackage: a domain saved
// behind AWS CloudFront gets the same last step with its own package: the
// Lambda@Edge zip beside the discovery file, the commands that create the
// function from that zip, and the sentence that says where the package runs
// once it answers. The zip downloads from the domain's own page, and holds the
// function's entry module with the release bundle beside it.
func TestConsole_TheLastStepOnCloudFrontHandsOverTheLambdaPackage(t *testing.T) {
	f := startConsole(t, verifiedClaims(),
		withDetector(newStubDetector(cdn.ProviderCloudFront)), withEdgePackages(t))
	f.signIn(t)
	f.walkToConnect(t)

	out := f.files(t, "stoa.example")
	names := slices.Sorted(maps.Keys(fileBodies(t, out)))
	if want := []string{setup.FileLambdaPackage, "fora.json"}; !slices.Equal(names, want) {
		t.Fatalf("the last step on CloudFront hands over %v, want %v", names, want)
	}
	if out["deployed"] != "Deployed on AWS CloudFront, on the cache behaviours you associated it with." {
		t.Fatalf("the step says the package runs %q", out["deployed"])
	}
	steps := stringOf(out["edge_steps"])
	if !strings.Contains(steps, "aws lambda create-function") ||
		!strings.Contains(steps, "fileb://"+setup.FileLambdaPackage) || strings.Contains(steps, "wrangler") {
		t.Fatalf("the deploy commands are not CloudFront's: %s", steps)
	}

	status, body := f.get(t, "/domains/stoa.example/files/"+setup.FileLambdaPackage)
	if status != http.StatusOK {
		t.Fatalf("downloading %s = %d", setup.FileLambdaPackage, status)
	}
	entries := setuptest.Entries(t, []byte(body))
	for _, want := range []string{"index.mjs", "lambda-edge.mjs"} {
		if _, ok := entries[want]; !ok {
			t.Fatalf("%s holds %v, without %s", setup.FileLambdaPackage, slices.Sorted(maps.Keys(entries)), want)
		}
	}
}

// TestConsole_EachPackageCarriesTheLastStepsInstructions downloads each
// network's package the way a publisher does and reads the README inside it
// against what the wizard's last step showed for the same domain.
//
// Both legs go through the console's public routes, in front of the real
// Exchange harness: POST /api/domain/files for what the wizard draws, and the
// domain's file route for the zip. The step text itself is compiled in, so
// what this holds is the two renderings of it to each other: a command the
// wizard shows that the README leaves out, or a check the README drops, fails
// here. The wizard itself shows no check, because it asks the site those
// questions and reports the answers; the README keeps them, because it is read
// without the console.
func TestConsole_EachPackageCarriesTheLastStepsInstructions(t *testing.T) {
	for _, tc := range []struct {
		network cdn.Provider
		pkg     string
		// foreign is a command of the other network, which this one's README
		// must not carry.
		foreign string
	}{
		{cdn.ProviderCloudflare, setup.FileCloudflarePackage, "aws lambda"},
		{cdn.ProviderCloudFront, setup.FileLambdaPackage, "wrangler"},
	} {
		t.Run(string(tc.network), func(t *testing.T) {
			f := startConsole(t, verifiedClaims(), withDetector(newStubDetector(tc.network)), withEdgePackages(t))
			f.signIn(t)
			f.walkToConnect(t)
			shown := shownEdgeSteps(t, f.files(t, "stoa.example"))

			status, body := f.get(t, "/domains/stoa.example/files/"+tc.pkg)
			if status != http.StatusOK {
				t.Fatalf("downloading %s = %d", tc.pkg, status)
			}
			readme := setuptest.Entries(t, []byte(body))["README.md"]

			var want []string
			for _, step := range shown {
				for _, c := range step.Commands {
					if c.Checked || strings.HasPrefix(c.Run, "curl ") {
						t.Fatalf("the wizard shows %q, a check its last step asks the site itself", c.Label)
					}
				}
				want = append(want, step.Summary)
				want = append(want, setuptest.CommandTexts(step.Commands)...)
			}
			if len(want) < 2 {
				t.Fatalf("the wizard showed no deploy commands for %s: %+v", tc.network, shown)
			}
			want = append(want, setuptest.CommandTexts(setupsteps.EdgeStep(tc.network, "stoa.example").Checks())...)
			want = append(want, "Write to us at help@exchange.example")
			setuptest.AssertInOrder(t, tc.pkg+"'s README", readme, want)
			for _, never := range []string{"<host>", tc.foreign} {
				if strings.Contains(readme, never) {
					t.Fatalf("%s's README carries %q:\n%s", tc.pkg, never, readme)
				}
			}
			if !strings.Contains(readme, "built for stoa.example") {
				t.Fatalf("%s's README does not name the domain it is for:\n%s", tc.pkg, readme)
			}
			// Its own check, not part of the order above: the values come
			// before the deploy commands.
			if got := setuptest.ReadmeItems(t, readme, "## Your values")["Exchange"]; got != "https://"+testExchangeDomain {
				t.Fatalf("%s's README gives the exchange as %q, not this deployment's:\n%s", tc.pkg, got, readme)
			}
		})
	}
}

// TestConsole_RefusesAnotherPublishersPackage is the files refusal for the
// package: a domain somebody else registered is answered the way an
// unregistered one is, and no zip, README included, is sent.
func TestConsole_RefusesAnotherPublishersPackage(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withEdgePackages(t))
	addDomain(t, f.otherPerson(t, otherClaims()))
	f.signIn(t)
	f.registerBusiness(t)

	status, body := f.get(t, "/domains/stoa.example/files/"+setup.FileCloudflarePackage)
	if status != http.StatusNotFound {
		t.Fatalf("another publisher's package = %d, want %d", status, http.StatusNotFound)
	}
	if strings.HasPrefix(body, "PK") || strings.Contains(body, "README") {
		t.Fatalf("the refusal carries the package: %s", head(body))
	}
}

// shownEdgeSteps reads the edge steps a files answer carries, in the shape the
// wizard's page draws them from.
func shownEdgeSteps(t *testing.T, out map[string]any) []setupsteps.Step {
	t.Helper()
	raw, err := json.Marshal(out["edge_steps"])
	if err != nil {
		t.Fatal(err)
	}
	var steps []setupsteps.Step
	if err := json.Unmarshal(raw, &steps); err != nil {
		t.Fatalf("the edge steps do not read as steps: %v", err)
	}
	return steps
}

// fileBodies reads the files a wizard answer hands over, by name.
func fileBodies(t *testing.T, out map[string]any) map[string]string {
	t.Helper()
	bodies := map[string]string{}
	list, _ := out["files"].([]any)
	for _, raw := range list {
		file, _ := raw.(map[string]any)
		name, _ := file["name"].(string)
		body, _ := file["body"].(string)
		bodies[name] = body
	}
	return bodies
}
