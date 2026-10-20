//go:build integration

package transport_test

import (
	"net/http"
	"reflect"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// The terms route: setting the terms of a site saved and not verified yet. A
// site can be saved before its terms are chosen and cannot be verified without
// them, so this is how a publisher finishes one; a verified site's terms are
// not changed here.
//
// Legs: every write goes signed HTTP -> service -> repositories -> database,
// and the site is read back through the signed resume route. Audit rows are
// read one tier lower, through the audit repository, for the reason
// auditActions gives, and so is the second seat's grant in the per-seat test,
// for the reason secondSeatFor gives.

// freeSearch is a licensing the tests set over the charging one sites are
// saved with, so a write shows in what is read back.
func freeSearch() map[string]any {
	return map[string]any{"pricing_model": "free", "permitted_functions": []string{"search"}}
}

// termsBody is the terms route's request for one site.
func termsBody(actor consoleActorBody, publisherID, domain string, terms any) map[string]any {
	return map[string]any{
		"actor": actor, "publisher_id": publisherID, "domain": domain, "terms": terms,
		"sitemap_url": "https://" + domain + "/sitemap.xml", "import_replaces_manual_terms": true,
	}
}

// TestConsoleRegistration_SetsTheTermsOfASiteNotVerifiedYet: a site saved
// without terms gets them through the route, with the sitemap and the import's
// answer chosen beside them; the route answers with the site as it now stands,
// the resume route reads it back, the audit row records it, and the site can
// now be verified.
func TestConsoleRegistration_SetsTheTermsOfASiteNotVerifiedYet(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	body := siteBody(actor, publisherID, "stoa.example")
	body["terms"] = nil
	if status, saved := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), body); status != http.StatusOK {
		t.Fatalf("saving the site without terms = %d %v", status, saved)
	}

	status, set := f.post(t, consolePath(transport.ConsoleSetDomainTermsPath),
		termsBody(actor, publisherID, "stoa.example", freeSearch()))
	if status != http.StatusOK || set["state"] != repo.ListingStatePendingManifest {
		t.Fatalf("setting the terms = %d %v; want 200 and the site still not verified", status, set)
	}
	site := listedSite(t, f, actor, "stoa.example")
	if terms, _ := site["terms"].(map[string]any); terms["pricing_model"] != "free" {
		t.Fatalf("the site reads terms %v, want the free terms just set", site["terms"])
	}
	if site["sitemap_url"] != "https://stoa.example/sitemap.xml" || site["import_replaces_manual_terms"] != true {
		t.Fatalf("the site reads sitemap %v, import answer %v; want both set with the terms",
			site["sitemap_url"], site["import_replaces_manual_terms"])
	}
	details := auditDetailsOf(t, f, "SetDomainTerms")
	if len(details) != 1 || details[0]["domain"] != "stoa.example" {
		t.Fatalf("SetDomainTerms audit rows = %v; want one for stoa.example", details)
	}

	f.manifest.serve("stoa.example", publisherManifest("stoa.example", publisherID, consoleContributorHost))
	if checked := checkDomain(t, f, actor, publisherID, "stoa.example"); checked["passed"] != true {
		t.Fatalf("CheckDomain on a site whose terms are now set did not verify it: %v", checked)
	}
}

// TestConsoleRegistration_RefusesTermsTheRouteMayNotSet: every refusal writes
// nothing -- the site's terms read back as they were, and no SetDomainTerms
// audit row is written. A verified site is refused as a failed precondition;
// another publisher's site, one the caller holds no grant for and one that is
// not registered all answer not-found alike; terms that cannot be offered and a
// sitemap on another host are invalid requests.
func TestConsoleRegistration_RefusesTermsTheRouteMayNotSet(t *testing.T) {
	f := startConsoleFixture(t)
	owner := newActor("editor@publisher.example")
	ownerID := f.registerPublisher(t, owner, "Stoa Press")
	registerPending(t, f, owner, ownerID, "stoa.example")
	registerSite(t, f, owner, ownerID, "verified.stoa.example", "cloudflare")
	other := newActor("desk@harmonia.example")
	otherID := f.registerPublisher(t, other, "Harmonia Records")
	registerPending(t, f, other, otherID, "harmonia.example")

	offHost := termsBody(owner, ownerID, "stoa.example", freeSearch())
	offHost["sitemap_url"] = "https://elsewhere.example/sitemap.xml"
	cases := []termsRefusal{
		{
			"a verified site", termsBody(owner, ownerID, "verified.stoa.example", freeSearch()),
			http.StatusBadRequest, consoleapi.ErrorKindFailedPrecondition,
		},
		{
			"another publisher's site", termsBody(owner, ownerID, "harmonia.example", freeSearch()),
			http.StatusNotFound, consoleapi.ErrorKindNotFound,
		},
		{
			"a publisher without a grant", termsBody(owner, otherID, "harmonia.example", freeSearch()),
			http.StatusNotFound, consoleapi.ErrorKindNotFound,
		},
		{
			"a site not registered", termsBody(owner, ownerID, "foundry.example", freeSearch()),
			http.StatusNotFound, consoleapi.ErrorKindNotFound,
		},
		{
			"no terms", termsBody(owner, ownerID, "stoa.example", nil),
			http.StatusBadRequest, consoleapi.ErrorKindInvalidRequest,
		},
		{
			"no permitted use", termsBody(owner, ownerID, "stoa.example",
				map[string]any{"pricing_model": "free", "permitted_functions": []string{}}),
			http.StatusBadRequest, consoleapi.ErrorKindInvalidRequest,
		},
		{
			"charging with no rate", termsBody(owner, ownerID, "stoa.example",
				map[string]any{"pricing_model": "per_unit", "permitted_functions": []string{"search"}}),
			http.StatusBadRequest, consoleapi.ErrorKindInvalidRequest,
		},
		{
			"an unknown use", termsBody(owner, ownerID, "stoa.example",
				map[string]any{"pricing_model": "free", "permitted_functions": []string{"telepathy"}}),
			http.StatusBadRequest, consoleapi.ErrorKindInvalidRequest,
		},
		{"a sitemap on another host", offHost, http.StatusBadRequest, consoleapi.ErrorKindInvalidRequest},
	}

	before := map[string]any{
		"stoa.example":          listedTerms(t, f, owner, "stoa.example"),
		"verified.stoa.example": listedTerms(t, f, owner, "verified.stoa.example"),
		"harmonia.example":      listedTerms(t, f, other, "harmonia.example"),
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := f.post(t, consolePath(transport.ConsoleSetDomainTermsPath), tc.body)
			if status != tc.status || body["kind"] != tc.kind {
				t.Fatalf("= %d %v; want %d %s", status, body, tc.status, tc.kind)
			}
		})
	}
	after := map[string]any{
		"stoa.example":          listedTerms(t, f, owner, "stoa.example"),
		"verified.stoa.example": listedTerms(t, f, owner, "verified.stoa.example"),
		"harmonia.example":      listedTerms(t, f, other, "harmonia.example"),
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused call changed terms: before %v, after %v", before, after)
	}
	if n := countActions(t, f, "SetDomainTerms"); n != 0 {
		t.Fatalf("the refused calls wrote %d SetDomainTerms audit rows", n)
	}
}

// TestConsoleRegistration_TermsSetOnOneSeatLeaveTheOtherSeatAlone: a payee can
// hold the same site on two seats, each listing with its own terms, and setting
// the terms through one seat changes that seat's listing only. Both listings are
// pending, because the terms route only ever changes a pending one.
func TestConsoleRegistration_TermsSetOnOneSeatLeaveTheOtherSeatAlone(t *testing.T) {
	first := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := first.registerPublisher(t, actor, "Stoa Press")
	registerPending(t, first, actor, publisherID, "stoa.example")
	second := first.secondSeatFor(t, actor, publisherID)
	registerPending(t, second, actor, publisherID, "stoa.example")

	if status, set := first.post(t, consolePath(transport.ConsoleSetDomainTermsPath),
		termsBody(actor, publisherID, "stoa.example", freeSearch())); status != http.StatusOK {
		t.Fatalf("setting the terms through the first seat = %d %v", status, set)
	}
	if terms, _ := listedTerms(t, first, actor, "stoa.example").(map[string]any); terms["pricing_model"] != "free" {
		t.Fatalf("the first seat's terms = %v, want the free terms just set", terms)
	}
	if terms, _ := listedTerms(t, second, actor, "stoa.example").(map[string]any); terms["pricing_model"] != "per_unit" {
		t.Fatalf("the second seat's terms = %v, want its own terms untouched", terms)
	}
}

// termsRefusal is one call the terms route must refuse, and how.
type termsRefusal struct {
	name   string
	body   map[string]any
	status int
	kind   string
}

// listedTerms reads one of a person's sites back through the resume route and
// returns its terms.
func listedTerms(t *testing.T, f *consoleFixture, actor consoleActorBody, domain string) any {
	t.Helper()
	_, sites := listRegistrations(t, f, actor)
	for _, s := range sites {
		if s["domain"] == domain {
			return s["terms"]
		}
	}
	t.Fatalf("%s is not among the person's sites: %v", domain, sites)
	return nil
}
