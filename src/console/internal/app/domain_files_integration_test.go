//go:build integration

package app_test

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
)

// Downloading a domain's setup files, both after it is registered and while the
// wizard is still running.
//
// A registered domain's files are built from the registration the Exchange
// holds: each download reads it back through the Exchange's registrations/list
// route under the signed-in person's own actor, and the console builds the
// files from what comes back. Nothing in the request chooses what they say.

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
	handedOver := fileBodies(t, f.ok(t, "/api/domain/files", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "/articles/",
	}))

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
	// The terms are shown as the domain was registered, in the form the
	// licensing step set them with, and none of it can be changed here. Beside
	// them is the offer they produce, for the page of the site the sample was
	// built from, sold by this Exchange.
	for _, want := range []string{
		`<fieldset class="sample terms-readonly" disabled>`, `value="0.02"`, "per fetches",
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

// TestConsole_TheWizardDownloadRefusesWhatItCannotBuild drives the mid-wizard
// download link with each answer it carries set to something it must refuse.
// The answers travel in the address, so they are the caller's to choose.
func TestConsole_TheWizardDownloadRefusesWhatItCannotBuild(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")

	for _, tc := range []struct {
		name, domain, cdn, file string
		want                    int
	}{
		{"a network we ship no package for", "stoa.example", "fastly", setup.FileManifest, http.StatusBadRequest},
		{"an address that is not a hostname", "https://stoa.example/x", "cloudflare", setup.FileManifest, http.StatusBadRequest},
		{"a file the builder never produces", "stoa.example", "cloudflare", "secrets.json", http.StatusNotFound},
	} {
		q := url.Values{"name": {tc.file}, "domain": {tc.domain}, "cdn": {tc.cdn}, "content_paths": {""}}
		status, body := f.get(t, "/api/domain/file?"+q.Encode())
		if status != tc.want {
			t.Fatalf("%s = %d, want %d: %s", tc.name, status, tc.want, head(body))
		}
		if strings.Contains(body, "resource_owner_id") {
			t.Fatalf("%s: the refusal carries a file: %s", tc.name, head(body))
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
	f.walkToDocument(t, "stoa.example")
	prefill := f.ok(t, "/api/domain/page", map[string]any{"domain": "stoa.example"})
	terms, _ := prefill["terms"].(map[string]any)
	if terms == nil {
		t.Fatalf("the licence was not read: %v", prefill)
	}

	addDomainWith(t, f, "stoa.example", terms)
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
