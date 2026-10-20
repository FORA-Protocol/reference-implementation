//go:build integration

package repo_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
)

// TestPagesOfSite_ReadsOneSiteOfOneSeat pins which rows the automatic import
// compares a site's pages with: the seat's rows stored under that domain,
// whatever scheme their URI was built with, and nothing else -- not a domain
// that shares a prefix or a suffix with it, not a row whose path names the
// domain, and not another seat's row under the same domain.
//
// The rows are written through the production CatalogRepo, and both legs stop
// at the repository port. That is the only way to place two of the rows the
// question needs: the catalog push stores every entry under the one seat the
// deployment sells as, and builds every URI with one scheme. No RPC reads the
// catalog by site.
func TestPagesOfSite_ReadsOneSiteOfOneSeat(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	seat := repotest.SeedSeat(t, ctx, pool, repotest.Seat{Domain: "exchange.example"})
	otherSeat := repotest.SeedSeat(t, ctx, pool, repotest.Seat{Domain: "second-exchange.example"})
	const owner = "pub-site-pages"
	repotest.SeedPublisher(t, ctx, pool, owner, "Stoa Press")
	title := "On rivers"

	// The site's two rows: one written by the import, carrying a title, a
	// content id and metadata, and one under the other scheme.
	imported := repotest.CatalogRow{
		TenantID: seat, ResourceID: seat + ":publisher.example:story-a", URI: "https://publisher.example/news/a",
		Domain: "publisher.example", OwnerID: owner, Title: &title,
		Metadata: []byte(`{"content_id": "story-a"}`), AutoImported: true,
	}
	plain := repotest.CatalogRow{
		TenantID: seat, ResourceID: seat + ":http://publisher.example/news/b", URI: "http://publisher.example/news/b",
		Domain: "publisher.example", OwnerID: owner,
	}
	want := map[string]repo.StoredPage{
		imported.URI: {
			ResourceID: imported.ResourceID, URI: imported.URI, Title: &title,
			Metadata: imported.Metadata, AutoImported: true,
		},
		plain.URI: {ResourceID: plain.ResourceID, URI: plain.URI},
	}
	for _, row := range []repotest.CatalogRow{imported, plain} {
		repotest.SeedCatalogRow(t, ctx, pool, row)
	}
	// Rows the read must leave out, each under the domain its URI was built
	// from.
	for uri, domain := range map[string]string{
		"https://publisher.example.org/news/a":           "publisher.example.org",
		"https://x.publisher.example/news/a":             "x.publisher.example",
		"https://publisher.examplex/news/a":              "publisher.examplex",
		"https://other.example/publisher.example/news/a": "other.example",
	} {
		repotest.SeedCatalogRow(t, ctx, pool, repotest.CatalogRow{
			TenantID: seat, ResourceID: seat + ":" + uri, URI: uri, Domain: domain, OwnerID: owner,
		})
	}
	repotest.SeedCatalogRow(t, ctx, pool, repotest.CatalogRow{
		TenantID: otherSeat, ResourceID: otherSeat + ":https://publisher.example/news/c",
		URI: "https://publisher.example/news/c", Domain: "publisher.example", OwnerID: owner,
	})

	got, err := repo.NewSitePagesRepo(sqlc.New(pool)).PagesOfSite(ctx, seat, "publisher.example")
	if err != nil {
		t.Fatalf("PagesOfSite: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("PagesOfSite returned %d rows, want %d: %+v", len(got), len(want), got)
	}
	for _, page := range got {
		w, ok := want[page.URI]
		if !ok {
			t.Fatalf("PagesOfSite returned %s, which is not a row of the seat's site", page.URI)
		}
		assertStoredPage(t, page, w)
	}
}

// assertStoredPage compares one row the read returned with the row written. The
// metadata column is JSONB, which keeps a document in its own normal form
// rather than byte for byte, so the two documents are compared decoded.
func assertStoredPage(t *testing.T, got, want repo.StoredPage) {
	t.Helper()
	if got.ResourceID != want.ResourceID || got.AutoImported != want.AutoImported {
		t.Errorf("%s: resource id %q imported=%v, want %q imported=%v",
			got.URI, got.ResourceID, got.AutoImported, want.ResourceID, want.AutoImported)
	}
	if (got.Title == nil) != (want.Title == nil) || (got.Title != nil && *got.Title != *want.Title) {
		t.Errorf("%s: title %v, want %v", got.URI, got.Title, want.Title)
	}
	if !sameJSON(t, got.Metadata, want.Metadata) {
		t.Errorf("%s: metadata %s, want %s", got.URI, got.Metadata, want.Metadata)
	}
}

// sameJSON reports whether two stored documents say the same thing. Two absent
// documents are the same; an absent one and a present one are not.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var da, db any
	if err := json.Unmarshal(a, &da); err != nil {
		t.Fatalf("decode %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &db); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return reflect.DeepEqual(da, db)
}
