//go:build integration

package app_test

import (
	"errors"
	"net/http"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
)

// The wizard's domain step and the files it hands over. The checks that gate
// the later steps are in the sibling file, and so is the licensing step.
//
// Every test here asserts the same thing twice over: what the publisher is
// told, and that the Exchange still holds no domain. Nothing about a domain may
// be written before the last step, because a hostname is claimed for the whole
// deployment by whoever registers it first.

// errUnreadable stands for every reason a domain does not answer.
var errUnreadable = errors.New("the domain could not be read")

// TestConsole_WorksOutWhichNetworkServesTheDomain covers the step a publisher
// reaches first. They know their address and usually not which network is in
// front of it, so the address is all they are asked for.
func TestConsole_WorksOutWhichNetworkServesTheDomain(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withDetector(newStubDetector(cdn.ProviderCloudFront)))
	f.signIn(t)

	out := f.walkToDocument(t, "stoa.example")
	if out["provider"] != "cloudfront" {
		t.Fatalf("the lookup answered %v, want the network it found", out["provider"])
	}
	if out["supported"] != true {
		t.Fatalf("a network we ship a package for was refused: %v", out)
	}
	f.assertNoDomains(t, "the lookup")
}

// TestConsole_LookupReadsTheDomainsOwnHeaders is the property that makes the
// lookup work at an apex domain.
//
// A name like example.com cannot carry the delegation that identifies a
// network, so DNS says nothing and the headers the site answers with are the
// only evidence there is. A lookup that did not read them reported "we could
// not tell" for most publishers' home pages, which is where every one of them
// starts.
func TestConsole_LookupReadsTheDomainsOwnHeaders(t *testing.T) {
	detector := newStubDetector(cdn.ProviderCloudflare)
	served := http.Header{"X-Served-By": []string{"cache-fra-eddf8230051-FRA"}}
	f := startConsole(t, verifiedClaims(),
		withDetector(detector), withPages(stubPageReader{header: served}))
	f.signIn(t)

	f.walkToDocument(t, "stoa.example")
	hdr, called := detector.headers()
	if !called {
		t.Fatal("the lookup never ran")
	}
	if hdr.Get("X-Served-By") != "cache-fra-eddf8230051-FRA" {
		t.Fatalf("the lookup was given headers %v, want the domain's own", hdr)
	}
}

// TestConsole_EndsTheWizardWhenTheNetworkIsNotOneWeSupport covers the two ways
// the domain step stops, and the fact that they are one screen.
//
// Recognising a network and having a package for it are different things.
// Nothing here builds a package a publisher could install on Fastly Compute, so
// registering their domain would leave them holding settings and nothing to put
// them in -- the same dead end as a network we could not identify at all. Both
// end in a conversation with us, so both say so and give the address.
//
// There is no chooser. Letting the publisher pick a network we did not detect
// would be asking them to guess at the one question the wizard exists to answer
// for them, and a wrong guess produces a package that never runs.
func TestConsole_EndsTheWizardWhenTheNetworkIsNotOneWeSupport(t *testing.T) {
	cases := []struct {
		name     string
		provider cdn.Provider
		says     string
	}{
		{
			name: "a CDN we ship no package for", provider: cdn.ProviderFastly,
			// Named, because we know exactly what it is. Telling this
			// publisher we could not work it out would send them to look at
			// their own DNS for a gap that is ours.
			says: "Fastly serves this domain, and we do not support Fastly yet.",
		},
		{
			name: "nothing we recognise", provider: cdn.ProviderNone,
			// The other answer, and the one where there is something on their
			// side to look at. It must not read as a CDN called "none".
			says: "We could not detect a CDN in front of this domain.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := startConsole(t, verifiedClaims(),
				withDetector(newStubDetector(tc.provider)),
				withPages(stubPageReader{header: http.Header{"Server": []string{"nginx"}}}))
			f.signIn(t)
			out := f.walkToDocument(t, "behind-something-else.example")

			if out["supported"] != false {
				t.Fatalf("the wizard went on past a network we cannot equip: %v", out)
			}
			if msg, _ := out["message"].(string); msg != tc.says {
				t.Fatalf("the dead end says %q, want %q", msg, tc.says)
			}
			// Who to write to is on the page rather than in the message: it is
			// a deployment's constant, not a fact about this domain, and the
			// page makes it a link rather than a sentence with an address in
			// it. A dead end with nobody to write to is a dead end.
			if boot := f.openWizard(t); boot.SupportEmail != "help@exchange.example" {
				t.Fatalf("the wizard carries no address to write to: %q", boot.SupportEmail)
			}
			// The Exchange is never asked about a domain the wizard has
			// already stopped on: that would be a request to somebody else's
			// server made for a setup that is not going to happen.
			if got := f.exchange.Manifests.Calls(); got != 0 {
				t.Fatalf("the Exchange read a dead-end domain's document %d times", got)
			}
			f.assertNoDomains(t, "a dead-end domain")
		})
	}
}

// TestConsole_LookupSurvivesAnUnreadableDomain covers the state a domain is in
// while it is being set up: not published yet, or behind a staging password.
// The lookup falls back to DNS rather than failing, because a site that cannot
// be read is not a reason to refuse to guess.
func TestConsole_LookupSurvivesAnUnreadableDomain(t *testing.T) {
	detector := newStubDetector(cdn.ProviderCloudflare)
	f := startConsole(t, verifiedClaims(),
		withDetector(detector), withPages(stubPageReader{err: errUnreadable}))
	f.signIn(t)

	out := f.walkToDocument(t, "stoa.example")
	hdr, called := detector.headers()
	if !called {
		t.Fatal("an unreadable domain stopped the lookup running at all")
	}
	if hdr != nil {
		t.Fatalf("an unreadable domain produced headers %v", hdr)
	}
	if out["supported"] != true {
		t.Fatalf("the DNS-only lookup did not carry what it found: %v", out)
	}
}

// TestConsole_HandsOverTheDocumentBeforeAnythingIsRegistered: the wizard hands
// the discovery document over on a step before the last button, which is the
// only one that saves the domain. So the file has to be built for a domain that
// has no row to build it from -- everything in it comes from what they typed
// plus the payee id their account already has.
func TestConsole_HandsOverTheDocumentBeforeAnythingIsRegistered(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")

	out := f.ok(t, "/api/domain/files", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "/articles/",
	})
	files, _ := out["files"].([]any)
	bodies := map[string]string{}
	for _, raw := range files {
		f, _ := raw.(map[string]any)
		name, _ := f["name"].(string)
		body, _ := f["body"].(string)
		bodies[name] = body
	}
	for _, want := range []string{"fora.json", "edge-config.json"} {
		if bodies[want] == "" {
			t.Fatalf("the wizard did not produce %s: %v", want, out)
		}
	}
	// The document has to satisfy the reader that will later fetch it, so it is
	// held to that reader rather than to a second opinion about the bytes.
	owner, contributors, err := setup.ManifestReadsBack([]byte(bodies["fora.json"]), testExchangeDomain)
	if err != nil {
		t.Fatalf("the document handed over does not read back: %v", err)
	}
	if owner != f.publisherID {
		t.Fatalf("the document attests %q, want this publisher's payee id", owner)
	}
	if len(contributors) != 1 || contributors[0] != "catalog-contributor.exchange.example" {
		t.Fatalf("the document authorises %v", contributors)
	}
	f.assertNoDomains(t, "handing over the files")
}

// TestConsole_DownloadsTheFilesMidWizard proves the links beside those files
// resolve. The answers travel in the address because there is no row to read
// them back from, so this is also what pins that they survive the round trip.
func TestConsole_DownloadsTheFilesMidWizard(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")
	out := f.ok(t, "/api/domain/files", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "/articles/",
	})

	files, _ := out["files"].([]any)
	if len(files) == 0 {
		t.Fatalf("no files to download: %v", out)
	}
	for _, raw := range files {
		file, _ := raw.(map[string]any)
		link, _ := file["url"].(string)
		name, _ := file["name"].(string)
		status, body := f.get(t, link)
		if status != http.StatusOK {
			t.Fatalf("downloading %s = %d", name, status)
		}
		if body != file["body"] {
			t.Fatalf("the downloaded %s is not the one shown on the page", name)
		}
	}
}

// TestConsole_RefusesFilesForANetworkItCannotEquip closes the door the download
// link opens. Its answers travel in the address, so they are the caller's to
// choose; a network we ship no package for must be refused there as firmly as
// it is at the step that asks.
func TestConsole_RefusesFilesForANetworkItCannotEquip(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")

	status, out := f.api(t, "/api/domain/files", map[string]any{
		"domain": "stoa.example", "cdn": "fastly", "content_paths": "",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("files for an unsupported network = %d, want %d", status, http.StatusBadRequest)
	}
	if out["error"] == nil {
		t.Fatalf("the refusal says nothing: %v", out)
	}
}
