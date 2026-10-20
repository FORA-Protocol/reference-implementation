package sitemap_test

import (
	"errors"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/sitemap"
)

const urlSet = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url>
    <loc>https://publisher.example/news/harbour-story</loc>
    <lastmod>2026-09-14</lastmod>
  </url>
  <url>
    <loc>https://publisher.example/news/second-story</loc>
  </url>
</urlset>`

func TestParseURLSet(t *testing.T) {
	t.Parallel()
	doc, err := sitemap.Parse(strings.NewReader(urlSet), 10)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Kind != sitemap.KindURLSet {
		t.Fatalf("kind = %v, want urlset", doc.Kind)
	}
	first, ok := doc.First()
	if !ok || first != "https://publisher.example/news/harbour-story" {
		t.Fatalf("first = %q (%v), want the first page", first, ok)
	}
	if len(doc.Locs) != 2 {
		t.Fatalf("locs = %v, want both pages", doc.Locs)
	}
}

func TestParseIndex(t *testing.T) {
	t.Parallel()
	const index = `<?xml version="1.0"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://publisher.example/sitemap-news.xml</loc></sitemap>
  <sitemap><loc>https://publisher.example/sitemap-pages.xml</loc></sitemap>
</sitemapindex>`
	doc, err := sitemap.Parse(strings.NewReader(index), 10)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Kind != sitemap.KindIndex {
		t.Fatalf("kind = %v, want sitemapindex", doc.Kind)
	}
	if first, _ := doc.First(); first != "https://publisher.example/sitemap-news.xml" {
		t.Fatalf("first = %q, want the first child sitemap", first)
	}
}

// The sitemap namespace appears as a default, as a prefix, or missing
// altogether; all three describe the same document.
func TestParseIgnoresNamespaceSpelling(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"prefixed": `<sm:urlset xmlns:sm="http://www.sitemaps.org/schemas/sitemap/0.9">` +
			`<sm:url><sm:loc>https://publisher.example/a</sm:loc></sm:url></sm:urlset>`,
		"no namespace": `<urlset><url><loc>https://publisher.example/a</loc></url></urlset>`,
		"upper case":   `<URLSET><URL><LOC>https://publisher.example/a</LOC></URL></URLSET>`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doc, err := sitemap.Parse(strings.NewReader(body), 10)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if first, _ := doc.First(); first != "https://publisher.example/a" {
				t.Fatalf("first = %q, want the page", first)
			}
		})
	}
}

// An image extension spells its media address <loc> as well. Taking that as the
// page would hand the preview a JPEG to read a title out of.
func TestParseSkipsExtensionLocations(t *testing.T) {
	t.Parallel()
	const withImages = `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"
	 xmlns:image="http://www.google.com/schemas/sitemap-image/1.1">
  <url>
    <loc>https://publisher.example/news/harbour-story</loc>
    <image:image><image:loc>https://publisher.example/img/harbour.jpg</image:loc></image:image>
  </url>
</urlset>`
	doc, err := sitemap.Parse(strings.NewReader(withImages), 10)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(doc.Locs) != 1 || doc.Locs[0] != "https://publisher.example/news/harbour-story" {
		t.Fatalf("locs = %v, want only the page", doc.Locs)
	}
}

// Reading stops at the cap, so asking for one page does not walk a sitemap with
// fifty thousand entries.
func TestParseStopsAtMaxLocs(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString(`<urlset>`)
	for i := range 100 {
		b.WriteString("<url><loc>https://publisher.example/page-")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString("</loc></url>")
	}
	b.WriteString(`</urlset>`)
	doc, err := sitemap.Parse(strings.NewReader(b.String()), 3)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(doc.Locs) != 3 {
		t.Fatalf("locs = %d, want the cap of 3", len(doc.Locs))
	}
}

func TestParseRejections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		body    string
		wantErr error
	}{
		{"html home page", `<!doctype html><html><body>hello</body></html>`, sitemap.ErrNotASitemap},
		{"empty document", ``, sitemap.ErrNotASitemap},
		{"unclosed element", `<urlset><url><loc>https://publisher.example/a`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := sitemap.Parse(strings.NewReader(tc.body), 10)
			if err == nil {
				t.Fatal("parse accepted a document it should have refused")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// An empty sitemap is a valid document with nothing in it, not a failure.
func TestParseEmptyURLSet(t *testing.T) {
	t.Parallel()
	doc, err := sitemap.Parse(strings.NewReader(`<urlset></urlset>`), 10)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := doc.First(); ok {
		t.Fatal("an empty sitemap reported a first location")
	}
}

func TestParseRefusesANonPositiveCap(t *testing.T) {
	t.Parallel()
	if _, err := sitemap.Parse(strings.NewReader(urlSet), 0); err == nil {
		t.Fatal("a cap of zero was accepted")
	}
}
