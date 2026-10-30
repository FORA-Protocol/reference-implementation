//go:build integration

package app_test

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
)

// The wizard's domain step: the lookup, and the Next that saves the domain.
// The licensing step and the last step's checks are in the sibling files.
//
// The lookup writes nothing, and the tests about it say so: a hostname is held
// for the whole deployment by whoever saves it first, so only the step's Next
// may save one. What the Exchange holds is read back through its signed
// registrations route with the console's own client; whether a save was
// recorded is read one tier lower, through the Exchange's audit repository,
// because the Exchange publishes no read surface for its audit log
// (exchangetest.AuditActions says so on itself).

// errUnreadable stands for every reason a domain does not answer.
var errUnreadable = errors.New("the domain could not be read")

// TestConsole_WorksOutWhichNetworkServesTheDomain covers the step a publisher
// reaches first. They know their address and usually not which network is in
// front of it, so the address is all they are asked for.
func TestConsole_WorksOutWhichNetworkServesTheDomain(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withDetector(newStubDetector(cdn.ProviderCloudFront)))
	f.signIn(t)

	out := f.lookUp(t, "stoa.example")
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

	f.lookUp(t, "stoa.example")
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
			out := f.lookUp(t, "behind-something-else.example")

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

	out := f.lookUp(t, "stoa.example")
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

// TestConsole_TheDomainStepSavesTheDomainNotVerified covers the step's Next,
// and a publisher who closes the tab right after it: the domain is saved with
// no terms, held for this publisher and listed as not verified with its setup
// to finish, which reopens on the licensing step. Nothing about the site has
// been checked, and the Exchange recorded the one save.
func TestConsole_TheDomainStepSavesTheDomainNotVerified(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)

	saved := f.walkToTerms(t, "stoa.example")
	if saved["domain"] != "stoa.example" || saved["cdn"] != "cloudflare" ||
		saved["content_paths"] != "/articles/" || saved["setup"] != "/domains/stoa.example/setup" {
		t.Fatalf("the domain step's Next answered %v; want stoa.example as saved, and its setup page", saved)
	}
	sites := f.mine(t).Sites
	if len(sites) != 1 || sites[0].Active() || sites[0].Terms != nil || sites[0].LastCheckedAt != nil ||
		sites[0].ImportReplacesManualTerms || !slices.Equal(sites[0].ContentPathScope, []string{"/articles/"}) {
		t.Fatalf("the Exchange holds %+v; want stoa.example saved with /articles/, no terms, never checked", sites)
	}
	if n := f.exchange.CountAuditActions(t, "RegisterDomain"); n != 1 {
		t.Fatalf("the Exchange recorded %d saves, want 1", n)
	}
	mustContain(t, "the domains list", f.get2(t, "/domains"), "Not verified", `href="/domains/stoa.example/setup"`)
	if boot := f.bootOn(t, "/domains/stoa.example/setup"); boot.Resume == nil || boot.Resume.Step != "terms" ||
		boot.Resume.TermsSet {
		t.Fatalf("the setup page reopens with %+v; want the licensing step of a domain with no terms", boot.Resume)
	}
}

// TestConsole_SavingTheDomainAgainKeepsWhatItWasSavedWith: a second save of a
// domain this publisher already saved -- a double press, or a direct call with
// other paths -- answers with the domain as it was saved and writes nothing.
// Its network and paths are fixed by the first save.
func TestConsole_SavingTheDomainAgainKeepsWhatItWasSavedWith(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToTerms(t, "stoa.example")

	again := f.ok(t, web.APIRegisterPath, map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "/news/",
	})
	if again["content_paths"] != "/articles/" {
		t.Fatalf("saving again answered %v; want the paths it was saved with", again)
	}
	if sites := f.mine(t).Sites; len(sites) != 1 || !slices.Equal(sites[0].ContentPathScope, []string{"/articles/"}) {
		t.Fatalf("the Exchange holds %+v; want stoa.example as it was first saved", sites)
	}
	if n := f.exchange.CountAuditActions(t, "RegisterDomain"); n != 1 {
		t.Fatalf("the Exchange recorded %d saves, want the first one only", n)
	}
}

// TestConsole_TheDomainStepRefusesANetworkItCannotEquip closes the door the
// save opens. The network travels in the request, so it is the caller's to
// choose; a network we ship no package for is refused there as firmly as the
// lookup refuses it, and nothing is saved.
func TestConsole_TheDomainStepRefusesANetworkItCannotEquip(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.registerBusiness(t)

	status, out := f.api(t, web.APIRegisterPath, map[string]any{
		"domain": "stoa.example", "cdn": "fastly", "content_paths": "",
	})
	if status != http.StatusBadRequest || out["error"] == nil {
		t.Fatalf("saving behind an unsupported network = %d %v, want 400 with a sentence", status, out)
	}
	f.assertNoDomains(t, "a save behind an unsupported network")
	if n := f.exchange.CountAuditActions(t, "RegisterDomain"); n != 0 {
		t.Fatalf("the Exchange recorded %d saves for a refused one", n)
	}
}

// TestConsole_TheDomainStepShowsTheLicensedPathsField reads the domain step as
// the page draws it. The licensed paths are an ordinary field under the domain,
// marked optional and showing an example path, rather than a question folded
// away behind a disclosure: a publisher who licenses only part of a site sees
// where to say so.
func TestConsole_TheDomainStepShowsTheLicensedPathsField(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.registerBusiness(t)

	page := f.get2(t, web.NewDomainPath)
	mustContain(t, "the domain step", page,
		`<label class="form-label" for="paths">Licensed paths`, "(optional)",
		`id="paths"`, `placeholder="/news/"`, "Leave this empty to license all of it.")
	mustNotContain(t, "the domain step", page, "licensed-paths", "License only part of this site")
}
