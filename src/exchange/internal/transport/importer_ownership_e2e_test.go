//go:build integration

package transport_test

import (
	"strconv"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk/sitemaptest"
)

// Pages the sitemap lists that someone else already pushed. Every page pushed
// by hand here goes through fora-ingest's own pipeline (pushManual), signed
// with the importer's own contributor key and without its label, which is how
// a page gets into a console-registered site's catalog by hand. What agents see
// is read back through DiscoverResources: the terms, the title and metadata,
// and -- through the CoMP projection -- the resource id the page is sold under.
// Two reads have no public route and go through repository ports: whose row it
// is (catalog.auto_imported, through storedPage), and the outcome a run
// recorded on the listing (lastImport).

// TestImporter_PagesAnotherSourcePushed drives the ways a page the sitemap
// lists can already be in the catalog because someone pushed it by hand:
// without a content id or with one, with a percent-encoded path, and with the
// listing letting the import take such pages over or not. A page the listing
// does not hand over keeps everything it has, and the run skips it. A page it
// does hand over takes the listing's terms, keeps its title, metadata and row,
// and is the import's from then on. No submission is refused either way, and
// every other page of the sitemap imports.
func TestImporter_PagesAnotherSourcePushed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		path      string
		contentID string
		takeOver  bool
		// others is how many other pages the sitemap lists after the one pushed
		// by hand. The case of a content id left alone lists enough of them for
		// two submissions: a page keyed by content id used to get the whole
		// submission it rode in refused, and every later one never sent.
		others int
	}{
		{name: "no content id, left alone", path: "/news/by-hand", others: 2},
		{name: "content id, left alone", path: "/news/by-hand", contentID: "story-1", others: 300},
		{name: "no content id, taken over", path: "/news/by-hand", takeOver: true, others: 2},
		{name: "content id, taken over", path: "/news/by-hand", contentID: "story-1", takeOver: true, others: 2},
		{name: "percent-encoded path, left alone", path: "/news/caf%C3%A9", others: 2},
		{
			name: "percent-encoded path, taken over", path: "/news/caf%C3%A9", contentID: "story-2",
			takeOver: true, others: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newImportRigWith(t, importRigOptions{ImportReplacesManualTerms: tc.takeOver})
			page := "https://" + importDomain + tc.path
			rec := handRecord(tc.path)
			rec.ContentID = tc.contentID
			r.pushManual(r.h.server.URL, rec)
			row := resourceIDShown(t, r.h, page)
			others := sitePages(tc.others)
			r.sitemap.serve(sitemaptest.Urlset(append([]string{page}, others...)...))

			wantOut := repo.ImportOutcome{
				Outcome: repo.ImportOutcomeComplete, Entries: tc.others + 1, Accepted: tc.others, Skipped: 1,
			}
			price, currency := handRate, "USD"
			if tc.takeOver {
				wantOut.Accepted, wantOut.Skipped = tc.others+1, 0
				price, currency = "0.05", "EUR"
			}
			want(t, r.run(), wantOut)
			assertPageKept(t, r.h, page, row)
			if got := r.pricing(page); got.GetRate() != price || got.GetCurrency() != currency {
				t.Errorf("%s is priced %s %s after the run, want %s %s", page, got.GetRate(), got.GetCurrency(), price, currency)
			}
			if got := storedPage(t, r.h, page).AutoImported; got != tc.takeOver {
				t.Errorf("%s: auto_imported = %v after the run, want %v", page, got, tc.takeOver)
			}
			for _, p := range []string{others[0], others[len(others)-1]} {
				if r.offers(p) != 1 || r.pricing(p).GetRate() != "0.05" {
					t.Errorf("%s, a page nobody else pushed, did not import with the listing's terms", p)
				}
			}
		})
	}
}

// TestImporter_EverythingSkippedIsComplete: a sitemap whose every page someone
// else pushed, on a listing that does not let the import take them over, is a
// complete run that sends nothing. The pages stay as they are, no push is
// made, and the success timestamp moves: the catalog holds every page the
// sitemap lists.
func TestImporter_EverythingSkippedIsComplete(t *testing.T) {
	r := newImportRig(t, nil)
	pages := []string{"https://" + importDomain + "/news/by-hand-a", "https://" + importDomain + "/news/by-hand-b"}
	r.pushManual(r.h.server.URL, handRecord("/news/by-hand-a"), handRecord("/news/by-hand-b"))
	proxy := r.viaProxy(nil)
	r.sitemap.serve(sitemaptest.Urlset(pages...))

	want(t, r.run(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 2, Skipped: 2})
	if n := proxy.calls.Load(); n != 0 {
		t.Fatalf("a run with nothing to send made %d pushes", n)
	}
	if li := r.lastImport(); li.SuccessAt == nil || li.Skipped != 2 {
		t.Fatalf("an all-skipped run recorded %+v; want a success time and two skipped", li)
	}
	for _, p := range pages {
		if got := r.pricing(p).GetRate(); got != handRate {
			t.Errorf("%s is priced %s after the run, want its own %s", p, got, handRate)
		}
	}
}

// TestImporter_SkippedPagesStayOutOfARefusedRunsRanges: a run that skips a
// page and is then refused counts the skipped page once, as skipped, and
// numbers the submissions it sent without it. The page pushed by hand comes
// first in the sitemap, before the 300 pages the import sends. The publisher
// then stops authorizing the importer, so the Exchange refuses its first
// submission and the rest are never sent -- and the six counts still add up
// to the 301 entries built.
func TestImporter_SkippedPagesStayOutOfARefusedRunsRanges(t *testing.T) {
	r := newImportRig(t, nil)
	r.serveManifestUncached()
	const hand = "/news/by-hand"
	r.pushManual(r.h.server.URL, handRecord(hand))
	r.origin.setContributors() // the publisher no longer names the importer
	r.sitemap.serve(sitemaptest.Urlset(append([]string{"https://" + importDomain + hand}, sitePages(300)...)...))
	bound, err := ingest.EntriesPerSubmission()
	if err != nil {
		t.Fatal(err)
	}

	out := r.run()
	want(t, out, repo.ImportOutcome{
		Outcome: repo.ImportOutcomeFailed, Entries: 301, Skipped: 1, Refused: bound, Unsent: 300 - bound,
	})
	if wantRange := "(entries 0-" + strconv.Itoa(bound-1) + ")"; !strings.Contains(out.FailureReason, wantRange) {
		t.Fatalf("failure reason %q does not number the refused submission %s", out.FailureReason, wantRange)
	}
}
