//go:build integration

package transport_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/importer"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk/sitemaptest"
)

// Every scenario here drives the PRODUCTION importer against a real Exchange:
// most hand one listing to the per-listing run (importer.RunAndRecord), and the
// scenarios about how a run ends drive the whole job (importer.RunAll), the way
// the fora-import command does. The entries it builds cross both contributor
// gates as signed PushResources calls, and the catalog is read back through
// DiscoverResources. Two reads have
// no public route and go through repository ports instead: whose row a page is
// (storedPage), and the outcome a run recorded on the listing (lastImport).

// TestImporter_FirstRunAndRerun: the first run imports every page under the
// listing's terms; a second run over the same sitemap re-upserts every page and
// changes nothing observable -- one offer per page, the same rate, no
// duplicate -- and no request is ever made to an article, only to the sitemap.
func TestImporter_FirstRunAndRerun(t *testing.T) {
	r := newImportRig(t, nil)
	pages := sitePages(3)
	r.sitemap.serve(sitemaptest.Urlset(pages...))

	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 3, Accepted: 3})
	for _, p := range pages {
		if n := r.offers(p); n != 1 {
			t.Fatalf("%s yields %d offers after the first run, want 1", p, n)
		}
		if got := r.pricing(p).GetRate(); got != "0.05" {
			t.Fatalf("%s is priced %q, want the rate the listing's licensing sets, 0.05", p, got)
		}
	}
	first := r.lastImport()
	if first.Outcome != repo.ImportOutcomeComplete || first.SuccessAt == nil {
		t.Fatalf("after a complete run the listing records %+v", first)
	}

	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 3, Accepted: 3})
	for _, p := range pages {
		if n := r.offers(p); n != 1 {
			t.Fatalf("%s yields %d offers after the rerun, want 1 (no duplicate)", p, n)
		}
	}
	for _, u := range r.fetch.requested() {
		if u != r.sitemap.url() {
			t.Fatalf("the importer requested %s; only the sitemap may be fetched", u)
		}
	}
}

// TestImporter_SitemapGrowsAndShrinks: a page added to the sitemap makes the
// catalog grow by exactly that page; a page dropped from a later sitemap keeps
// its entry, because the importer adds and updates and never withdraws.
func TestImporter_SitemapGrowsAndShrinks(t *testing.T) {
	r := newImportRig(t, nil)
	pages := sitePages(4)
	r.sitemap.serve(sitemaptest.Urlset(pages[:3]...))
	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 3, Accepted: 3})
	if r.offers(pages[3]) != 0 {
		t.Fatal("a page the sitemap does not list is in the catalog before it was added")
	}

	r.sitemap.serve(sitemaptest.Urlset(pages...))
	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 4, Accepted: 4})
	for _, p := range pages {
		if n := r.offers(p); n != 1 {
			t.Fatalf("%s yields %d offers after the page was added, want 1", p, n)
		}
	}

	r.sitemap.serve(sitemaptest.Urlset(pages[1:]...))
	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 3, Accepted: 3})
	if n := r.offers(pages[0]); n != 1 {
		t.Fatalf("the page dropped from the sitemap yields %d offers, want its entry kept (1)", n)
	}
}

// TestImporter_DenominationChangeReachesExistingEntries: the terms are built on
// every run from the listing's licensing in the deployment's denomination, so
// a change to the denomination re-prices the entries already imported at the
// next run -- the rate the publisher chose stays, the currency changes -- and
// adds no duplicate. The pages are the import's own, so the second run pushes
// every one of them again and skips none, and they stay the import's.
func TestImporter_DenominationChangeReachesExistingEntries(t *testing.T) {
	r := newImportRig(t, nil)
	pages := sitePages(2)
	r.sitemap.serve(sitemaptest.Urlset(pages...))
	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 2, Accepted: 2})
	if got := r.pricing(pages[0]).GetCurrency(); got != "EUR" {
		t.Fatalf("currency after the first run = %q, want EUR", got)
	}

	r.deps.TermsDefaults = termspreset.Defaults{Currency: "USD"}
	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 2, Accepted: 2})
	for _, p := range pages {
		if got := r.pricing(p); got.GetRate() != "0.05" || got.GetCurrency() != "USD" {
			t.Fatalf("%s is priced %s %s after the denomination changed, want 0.05 USD",
				p, got.GetRate(), got.GetCurrency())
		}
		if n := r.offers(p); n != 1 {
			t.Fatalf("%s yields %d offers after the denomination changed, want 1", p, n)
		}
		if !storedPage(t, r.h, p).AutoImported {
			t.Fatalf("%s, refreshed by the import, is no longer recorded as the import's", p)
		}
	}
}

// TestImporter_ScopeAndDomainFilter: only pages on the registered domain and
// inside the listing's path scope are submitted; everything else the sitemap
// lists is left out without failing the run.
func TestImporter_ScopeAndDomainFilter(t *testing.T) {
	r := newImportRig(t, []string{"/news/"})
	inScope := "https://" + importDomain + "/news/in-scope"
	outOfScope := "https://" + importDomain + "/archive/out-of-scope"
	otherHost := "https://other.example/news/elsewhere"
	r.sitemap.serve(sitemaptest.Urlset(inScope, outOfScope, otherHost))

	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 1, Accepted: 1})
	if n := r.offers(inScope); n != 1 {
		t.Fatalf("the in-scope page yields %d offers, want 1", n)
	}
	if n := r.offers(outOfScope); n != 0 {
		t.Fatalf("the out-of-scope page yields %d offers, want 0", n)
	}
}

// TestImporter_ScopeFollowsTheEdgeRule: a page is inside the listing's scope by
// the rule the edge applies to every request it gates -- the path decoded,
// repeated slashes merged, case ignored -- so a page the edge sends bots to
// negotiate for is a page the import offers. A page whose path the rule cannot
// place, here one with a dot segment, is left out, and so is a page outside the
// scope. The encoded page is sold under its path exactly as the sitemap spells
// it: DiscoverResources looks a page up as written, so its one offer at that
// address shows the catalog path was not rewritten.
func TestImporter_ScopeFollowsTheEdgeRule(t *testing.T) {
	r := newImportRig(t, []string{"/news/", "/café/"})
	cased := "https://" + importDomain + "/NEWS/a"
	encoded := "https://" + importDomain + "/caf%C3%A9/b"
	dotted := "https://" + importDomain + "/news/../private/c"
	outside := "https://" + importDomain + "/archive/d"
	r.sitemap.serve(sitemaptest.Urlset(cased, encoded, dotted, outside))

	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 2, Accepted: 2})
	for _, page := range []string{cased, encoded} {
		if n := r.offers(page); n != 1 {
			t.Fatalf("%s yields %d offers, want 1", page, n)
		}
	}
	if n := r.offers(outside); n != 0 {
		t.Fatalf("the out-of-scope page yields %d offers, want 0", n)
	}
}

// TestImporter_OneEntryPerPathWhateverTheSchemeOrHostCase: a sitemap that lists
// one page over both http and https, and another with its host in upper case,
// yields one entry per path on the listing's own domain. The scheme is not part
// of a catalog path and a host compares without case, so neither spelling adds
// an entry or loses a page; both pages are read back through DiscoverResources.
func TestImporter_OneEntryPerPathWhateverTheSchemeOrHostCase(t *testing.T) {
	r := newImportRig(t, nil)
	secure := "https://" + importDomain + "/news/a"
	plain := "http://" + importDomain + "/news/a"
	upper := "https://" + strings.ToUpper(importDomain) + "/news/b"
	r.sitemap.serve(sitemaptest.Urlset(secure, plain, upper))

	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 2, Accepted: 2})
	for _, page := range []string{secure, "https://" + importDomain + "/news/b"} {
		if n := r.offers(page); n != 1 {
			t.Fatalf("%s yields %d offers, want 1", page, n)
		}
	}
}

// TestImporter_UnusableLicensingFailsTheListingAlone: a listing whose licensing
// is missing, cannot be read, or cannot be built into license terms is a
// failed run whose reason says which. It fetches nothing and submits nothing
// -- never an import under a default -- and it is an outcome like any other,
// not an error that stops the run. The column is NOT NULL and registration
// validates the document, so each shape is a row written some other way or one
// a later rule change made unbuildable; each is handed to the per-listing run
// directly rather than read back through Eligible.
func TestImporter_UnusableLicensingFailsTheListingAlone(t *testing.T) {
	for _, tc := range []struct{ name, terms, reason string }{
		{"missing", "", "no licensing recorded"},
		{"not a licensing document", "[1]", "cannot be read"},
		{
			"unbuildable", `{"pricing_model":"per_unit","rate":"","permitted_functions":["search"]}`,
			"cannot be turned into license terms",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newImportRig(t, nil)
			page := sitePages(1)[0]
			r.sitemap.serve(sitemaptest.Urlset(page))

			out := r.runListing(repo.ImportListing{
				TenantID: r.h.tenantID, Domain: importDomain, SitemapURL: r.sitemap.url(),
				Terms: []byte(tc.terms),
			})
			want(t, out, repo.ImportOutcome{Outcome: repo.ImportOutcomeFailed})
			if !strings.Contains(out.FailureReason, tc.reason) {
				t.Fatalf("failure reason %q does not say %q", out.FailureReason, tc.reason)
			}
			if len(r.fetch.requested()) != 0 || r.offers(page) != 0 {
				t.Fatal("a listing without usable licensing was walked or imported")
			}
		})
	}
}

// TestImporter_InvalidEntryNamesThePage: a page the Exchange would refuse (a
// path over the protocol's 2048-character limit) fails the listing before
// anything is signed, and the reason names the page -- without the
// credentials its address carries, when it carries any.
func TestImporter_InvalidEntryNamesThePage(t *testing.T) {
	path := "/" + strings.Repeat("a", 2049)
	for _, tc := range []struct{ name, page, named string }{
		{"a plain address", "https://" + importDomain + path, "https://" + importDomain + path},
		{"an address with credentials", "https://user:secret@" + importDomain + path, "https://xxxxx@" + importDomain + path},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newImportRig(t, nil)
			r.sitemap.serve(sitemaptest.Urlset(tc.page))
			proxy := r.viaProxy(nil)

			out := r.run()
			want(t, out, repo.ImportOutcome{Outcome: repo.ImportOutcomeFailed})
			if !strings.Contains(out.FailureReason, tc.named) || strings.Contains(out.FailureReason, "secret") {
				t.Fatalf("failure reason %q does not name the page as %q, or repeats its credentials",
					out.FailureReason, tc.named)
			}
			if proxy.calls.Load() != 0 || r.offers("https://"+importDomain+path) != 0 {
				t.Fatal("an invalid listing was submitted")
			}
		})
	}
}

// TestImporter_InvalidTermsFailBeforeTheWalk: terms the Exchange would refuse
// -- here built in a metering unit the protocol does not register, the shape a
// mistyped EXCHANGE_TERMS_UNIT produces -- fail the listing before the sitemap
// is fetched, so an empty sitemap cannot turn bad settings into a complete run
// with a fresh success timestamp.
func TestImporter_InvalidTermsFailBeforeTheWalk(t *testing.T) {
	r := newImportRig(t, nil)
	r.deps.TermsDefaults = termspreset.Defaults{Currency: "EUR", Unit: "widgets"}
	r.sitemap.serve(sitemaptest.Urlset())

	out := r.run()
	want(t, out, repo.ImportOutcome{Outcome: repo.ImportOutcomeFailed})
	if !strings.Contains(out.FailureReason, "are invalid") || !strings.Contains(out.FailureReason, "pricing.unit") {
		t.Fatalf("failure reason %q does not name the invalid unit", out.FailureReason)
	}
	if len(r.fetch.requested()) != 0 {
		t.Fatal("a listing with invalid terms was walked")
	}
	if last := r.lastImport(); last.AttemptAt == nil || last.SuccessAt != nil {
		t.Fatalf("recorded attempt=%v success=%v; want an attempt and no success", last.AttemptAt, last.SuccessAt)
	}
}

// TestImporter_QueryPagesAreLeftOut: a catalog path cannot carry a query, and
// "/news/a?utm_source=sitemap" is not proof that "/news/a" is a page, so a
// query-bearing sitemap URL is left out -- and the other pages still import,
// instead of the one URL failing the whole listing.
func TestImporter_QueryPagesAreLeftOut(t *testing.T) {
	r := newImportRig(t, nil)
	plain := "https://" + importDomain + "/news/a"
	tracked := "https://" + importDomain + "/news/b?utm_source=sitemap"
	r.sitemap.serve(sitemaptest.Urlset(tracked, plain))

	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 1, Accepted: 1})
	if n := r.offers(plain); n != 1 {
		t.Fatalf("the plain page yields %d offers, want 1", n)
	}
	for _, p := range []string{tracked, "https://" + importDomain + "/news/b"} {
		if n := r.offers(p); n != 0 {
			t.Fatalf("%s yields %d offers, want 0", p, n)
		}
	}
}

// TestImporter_UnauthorizedContributorIsRefused: a manifest that does not
// authorize the contributor makes the Exchange refuse the first submission; the
// run is failed with the refused count, nothing accepted, the rest unsent, and
// the catalog unchanged.
func TestImporter_UnauthorizedContributorIsRefused(t *testing.T) {
	r := newImportRig(t, nil)
	r.origin.setContributors() // the publisher never named the importer
	pages := sitePages(300)
	r.sitemap.serve(sitemaptest.Urlset(pages...))

	bound, err := ingest.EntriesPerSubmission()
	if err != nil {
		t.Fatal(err)
	}
	out := r.run()
	want(t, out, repo.ImportOutcome{Outcome: repo.ImportOutcomeFailed, Entries: 300, Refused: bound, Unsent: 300 - bound})
	if out.FailureReason == "" {
		t.Fatal("a refused run records no reason")
	}
	if r.offers(pages[0]) != 0 || r.offers(pages[299]) != 0 {
		t.Fatal("an unauthorized contributor's entries entered the catalog")
	}
	if li := r.lastImport(); li.SuccessAt != nil {
		t.Fatalf("a failed run set the success timestamp: %v", li.SuccessAt)
	}
}

// TestImporter_PartialWhenALaterBatchIsRefused: the first submission is
// accepted, then the publisher revokes the contributor and the Exchange refuses
// the second. The run is partial: the accepted entries stay, the refused ones
// never enter, and the success timestamp does not move. A partial listing fails
// the whole job, which is what makes the command exit non-zero.
func TestImporter_PartialWhenALaterBatchIsRefused(t *testing.T) {
	r := newImportRig(t, nil)
	r.serveManifestUncached()
	pages := sitePages(300)
	r.sitemap.serve(sitemaptest.Urlset(pages...))
	r.viaProxy(func(n int, _ http.ResponseWriter, _ *http.Request) bool {
		if n == 2 {
			r.origin.setContributors()
		}
		return false
	})

	bound, err := ingest.EntriesPerSubmission()
	if err != nil {
		t.Fatal(err)
	}
	logs, err := r.runJob()
	if !errors.Is(err, importer.ErrIncomplete) {
		t.Fatalf("the job returned %v; want ErrIncomplete", err)
	}
	want(t, r.recorded(), repo.ImportOutcome{Outcome: repo.ImportOutcomePartial, Entries: 300, Accepted: bound, Refused: 300 - bound})
	logs.AssertLoggedOnce(t, "importer.listing", "WARN")
	if r.offers(pages[0]) != 1 || r.offers(pages[299]) != 0 {
		t.Fatal("the accepted batch is not in the catalog, or the refused one is")
	}
	if li := r.lastImport(); li.SuccessAt != nil || li.Outcome != repo.ImportOutcomePartial {
		t.Fatalf("a partial run recorded %+v", li)
	}
}

// TestImporter_UnconfirmedWhenTheVerdictNeverArrives: the first submission is
// accepted, the second is held past the listing deadline, so no verdict comes
// back. The run is unconfirmed, whatever was accepted before, and the deadline
// is shown to cover submission as well as the walk. An unconfirmed listing
// fails the whole job, as a partial one does.
func TestImporter_UnconfirmedWhenTheVerdictNeverArrives(t *testing.T) {
	r := newImportRig(t, nil)
	pages := sitePages(300)
	r.sitemap.serve(sitemaptest.Urlset(pages...))
	r.deps.ListingTimeout = 3 * time.Second
	r.viaProxy(func(n int, w http.ResponseWriter, req *http.Request) bool {
		if n != 2 {
			return false
		}
		select {
		case <-req.Context().Done():
		case <-time.After(8 * time.Second):
		}
		http.Error(w, "held past the deadline", http.StatusServiceUnavailable)
		return true
	})

	bound, err := ingest.EntriesPerSubmission()
	if err != nil {
		t.Fatal(err)
	}
	logs, err := r.runJob()
	if !errors.Is(err, importer.ErrIncomplete) {
		t.Fatalf("the job returned %v; want ErrIncomplete", err)
	}
	want(t, r.recorded(), repo.ImportOutcome{Outcome: repo.ImportOutcomeUnconfirmed, Entries: 300, Accepted: bound, Unconfirmed: 300 - bound})
	logs.AssertLoggedOnce(t, "importer.listing", "WARN")
	if r.offers(pages[0]) != 1 {
		t.Fatal("the accepted batch is not in the catalog")
	}
	if li := r.lastImport(); li.SuccessAt != nil || li.Outcome != repo.ImportOutcomeUnconfirmed {
		t.Fatalf("an unconfirmed run recorded %+v", li)
	}
}

// TestImporter_SourceFailuresKeepEntries: once a catalog exists, an
// unreachable sitemap, a malformed one and a walk that hits a limit are each a
// failed run that submits nothing and leaves every entry in place.
func TestImporter_SourceFailuresKeepEntries(t *testing.T) {
	r := newImportRig(t, nil)
	pages := sitePages(3)
	r.sitemap.serve(sitemaptest.Urlset(pages...))
	proxy := r.viaProxy(nil)
	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 3, Accepted: 3})
	pushes := proxy.calls.Load()

	r.sitemap.fail(http.StatusInternalServerError)
	out := r.run()
	want(t, out, repo.ImportOutcome{Outcome: repo.ImportOutcomeFailed})
	if !strings.Contains(out.FailureReason, string(sitemapwalk.ReasonSource)) {
		t.Fatalf("an unreachable sitemap failed with %q", out.FailureReason)
	}

	r.sitemap.serve("<urlset><url><loc>https://publisher.example/news/x</loc></url")
	out = r.run()
	want(t, out, repo.ImportOutcome{Outcome: repo.ImportOutcomeFailed})
	if !strings.Contains(out.FailureReason, string(sitemapwalk.ReasonParser)) {
		t.Fatalf("a malformed sitemap failed with %q", out.FailureReason)
	}

	r.sitemap.serve(sitemaptest.Urlset(pages...))
	r.deps.Limits = sitemapwalk.Limits{MaxResources: 2}
	out = r.run()
	want(t, out, repo.ImportOutcome{Outcome: repo.ImportOutcomeFailed})
	if !strings.Contains(out.FailureReason, string(sitemapwalk.ReasonResources)) {
		t.Fatalf("a capped walk failed with %q", out.FailureReason)
	}

	if proxy.calls.Load() != pushes {
		t.Fatal("a failed run submitted something")
	}
	for _, p := range pages {
		if n := r.offers(p); n != 1 {
			t.Fatalf("%s yields %d offers after the source failures, want its entry kept (1)", p, n)
		}
	}
	if li := r.lastImport(); li.Outcome != repo.ImportOutcomeFailed || li.SuccessAt == nil {
		t.Fatalf("after the failures the listing records %+v; the earlier success must stay", li)
	}
}

// TestImporter_EmptySitemapIsCompleteAndKeepsEntries: a valid empty sitemap is
// a complete run of zero entries -- nothing is pushed, nothing is withdrawn,
// and the success timestamp advances.
func TestImporter_EmptySitemapIsCompleteAndKeepsEntries(t *testing.T) {
	r := newImportRig(t, nil)
	pages := sitePages(2)
	r.sitemap.serve(sitemaptest.Urlset(pages...))
	proxy := r.viaProxy(nil)
	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 2, Accepted: 2})
	before := r.lastImport()
	pushes := proxy.calls.Load()

	r.sitemap.serve(sitemaptest.Urlset())
	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete})
	if proxy.calls.Load() != pushes {
		t.Fatal("an empty sitemap was submitted")
	}
	for _, p := range pages {
		if n := r.offers(p); n != 1 {
			t.Fatalf("%s yields %d offers after an empty sitemap, want its entry kept (1)", p, n)
		}
	}
	after := r.lastImport()
	if after.SuccessAt == nil || !after.SuccessAt.After(*before.SuccessAt) {
		t.Fatalf("an empty complete run did not advance the success timestamp: %v -> %v", before.SuccessAt, after.SuccessAt)
	}
}
