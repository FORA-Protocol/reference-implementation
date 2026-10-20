//go:build integration

package transport_test

import (
	"reflect"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk/sitemaptest"
)

// What the importer does with a page it pushes again, and with rows it cannot
// push again. Pages are pushed the way production pushes them, by hand
// (pushManual) or by the importer's own client (pushLabelled), and read back
// through DiscoverResources, with two exceptions that go through the
// importer's repository port (storedPage), a persistence round-trip: whose row
// a page is (catalog.auto_imported), and, in the scenarios that must show a
// stored row untouched, the whole row -- resource id, URI, title, metadata and
// that flag -- because DiscoverResources shows only a projection of the stored
// metadata, and nothing of a document it cannot decode.
//
// Three scenarios need a row no signed push can create today: stored metadata
// a push would refuse, stored metadata that cannot be decoded, and a resource
// id from an earlier identity scheme. The push checks every entry by the same
// rules the importer applies, so those rows exist only where a catalog was
// written under older rules. storeRowDirectly writes each one through the
// production catalog repository, the port the push itself writes through.

// TestImporter_RefreshesItsOwnPagesKeepingWhatTheyCarry: a page whose last
// push was the import's is refreshed on every run, even on a listing that does
// not let the import take other pages over. It takes the listing's terms and
// keeps its content id, title and metadata, and no submission is refused.
func TestImporter_RefreshesItsOwnPagesKeepingWhatTheyCarry(t *testing.T) {
	r := newImportRig(t, nil)
	rec := handRecord("/news/labelled")
	rec.ContentID = "story-9"
	r.pushLabelled(rec)
	page := "https://" + importDomain + rec.Path
	row := resourceIDShown(t, r.h, page)
	r.sitemap.serve(sitemaptest.Urlset(page))

	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 1, Accepted: 1})
	assertPageKept(t, r.h, page, row)
	if got := r.pricing(page); got.GetRate() != "0.05" || got.GetCurrency() != "EUR" {
		t.Errorf("the import's own page is priced %s %s, want the listing's 0.05 EUR", got.GetRate(), got.GetCurrency())
	}
	if !storedPage(t, r.h, page).AutoImported {
		t.Error("the import's own page is no longer recorded as the import's")
	}
}

// TestImporter_APageItWroteThenPushedByHandIsLeftAlone: once someone pushes
// by hand a page the import created, the page is theirs. The next run, on a
// listing that does not let the import take pages over, skips it and leaves
// what they pushed.
func TestImporter_APageItWroteThenPushedByHandIsLeftAlone(t *testing.T) {
	r := newImportRig(t, nil)
	pages := sitePages(2)
	r.sitemap.serve(sitemaptest.Urlset(pages...))
	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 2, Accepted: 2})

	r.pushManual(r.h.server.URL, handRecord(strings.TrimPrefix(pages[0], "https://"+importDomain)))
	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 2, Accepted: 1, Skipped: 1})
	if got := r.pricing(pages[0]); got.GetRate() != handRate {
		t.Errorf("the page pushed by hand is priced %s after the run, want its own %s", got.GetRate(), handRate)
	}
	if got := singleOffer(t, r.h, pages[0]).GetTitle(); got != handTitle {
		t.Errorf("the page pushed by hand is titled %q after the run, want %q", got, handTitle)
	}
	if storedPage(t, r.h, pages[0]).AutoImported {
		t.Error("the page pushed by hand is recorded as the import's")
	}
}

// TestImporter_StoredDataItCannotPushAgainFailsTheListing: a page the import
// would take over whose stored metadata no longer passes the rules every push
// applies, or cannot be read at all, stops the listing before anything is
// signed. The run is failed, the reason names the page, nothing is pushed, and
// the row is as it was. The listing lets the import take pages over, so the
// page is one the run would push again.
func TestImporter_StoredDataItCannotPushAgainFailsTheListing(t *testing.T) {
	for _, tc := range []struct{ name, metadata, reason string }{
		{"metadata a push would refuse", `{"hash_method": "` + strings.Repeat("x", 65) + `"}`, "hash_method"},
		{"metadata that cannot be read", `{"content_hash": 7}`, "decode resource metadata"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newImportRigWith(t, importRigOptions{ImportReplacesManualTerms: true})
			stored := "https://" + importDomain + "/news/stored"
			r.storeRowDirectly(stored, r.h.tenantID+":"+stored, []byte(tc.metadata))
			before := storedPage(t, r.h, stored)
			fresh := sitePages(1)[0]
			r.sitemap.serve(sitemaptest.Urlset(stored, fresh))
			proxy := r.viaProxy(nil)

			out := r.run()
			want(t, out, repo.ImportOutcome{Outcome: repo.ImportOutcomeFailed})
			if !strings.Contains(out.FailureReason, stored) || !strings.Contains(out.FailureReason, tc.reason) {
				t.Fatalf("failure reason %q does not name the page and %q", out.FailureReason, tc.reason)
			}
			if n := proxy.calls.Load(); n != 0 || r.offers(fresh) != 0 {
				t.Fatalf("a listing that failed before signing pushed %d times", n)
			}
			if after := storedPage(t, r.h, stored); !reflect.DeepEqual(after, before) {
				t.Fatalf("the row was changed: %+v, was %+v", after, before)
			}
		})
	}
}

// TestImporter_LeavesARowItCannotPushUnderItsOwnID: a row whose resource id
// has neither form this Exchange builds -- here one keyed by a content id
// without the site, as an earlier identity scheme stored it -- cannot be pushed
// again under its own identity. The run leaves it alone even on a listing that
// lets the import take pages over, counts it as skipped, and imports the rest.
func TestImporter_LeavesARowItCannotPushUnderItsOwnID(t *testing.T) {
	r := newImportRigWith(t, importRigOptions{ImportReplacesManualTerms: true})
	stored := "https://" + importDomain + "/news/legacy"
	r.storeRowDirectly(stored, r.h.tenantID+":legacy-story", nil)
	before := storedPage(t, r.h, stored)
	fresh := sitePages(1)[0]
	r.sitemap.serve(sitemaptest.Urlset(stored, fresh))

	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 2, Accepted: 1, Skipped: 1})
	if after := storedPage(t, r.h, stored); !reflect.DeepEqual(after, before) {
		t.Fatalf("the row the import cannot push again was changed: %+v, was %+v", after, before)
	}
	if r.offers(fresh) != 1 {
		t.Fatal("the rest of the sitemap did not import")
	}
}

// TestImporter_UnreadableCatalogFailsTheListing: a run that cannot read the
// rows the site already has cannot tell whose pages are whose, so it fails the
// listing before anything is signed rather than push over pages it cannot see.
// The read fails for real: its pool is closed, which is what a lost database
// connection does to the port, and no row is faked.
func TestImporter_UnreadableCatalogFailsTheListing(t *testing.T) {
	r := newImportRig(t, nil)
	r.sitemap.serve(sitemaptest.Urlset(sitePages(2)...))
	proxy := r.viaProxy(nil)
	r.deps.Pages = repo.NewSitePagesRepo(sqlc.New(closedPool(t)))

	out := r.run()
	want(t, out, repo.ImportOutcome{Outcome: repo.ImportOutcomeFailed})
	if !strings.Contains(out.FailureReason, "read the site's catalog") {
		t.Fatalf("failure reason %q does not say the catalog could not be read", out.FailureReason)
	}
	if n := proxy.calls.Load(); n != 0 {
		t.Fatalf("a listing whose catalog could not be read pushed %d times", n)
	}
}

// TestImporter_APageWithTwoRowsIsLeftAlone: a page with one row under each URI
// scheme -- what a change to the Exchange's URI scheme after pages were stored
// leaves behind -- cannot be pushed again under one row's identity without
// touching the other. The run leaves both rows as they are, even on a listing
// that lets the import take pages over, and counts the page as skipped.
func TestImporter_APageWithTwoRowsIsLeftAlone(t *testing.T) {
	r := newImportRigWith(t, importRigOptions{ImportReplacesManualTerms: true})
	const path = "/news/twice"
	r.pushManual(r.h.server.URL, handRecord(path))
	current := "https://" + importDomain + path
	earlier := "http://" + importDomain + path
	r.storeRowDirectly(earlier, r.h.tenantID+":"+earlier, nil)
	before := []repo.StoredPage{storedPage(t, r.h, current), storedPage(t, r.h, earlier)}
	r.sitemap.serve(sitemaptest.Urlset(current))

	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 1, Skipped: 1})
	if after := []repo.StoredPage{storedPage(t, r.h, current), storedPage(t, r.h, earlier)}; !reflect.DeepEqual(after, before) {
		t.Fatalf("the rows of a page with two were changed: %+v, were %+v", after, before)
	}
	if got := r.pricing(current).GetRate(); got != handRate {
		t.Errorf("the page with two rows is priced %s after the run, want its own %s", got, handRate)
	}
}
