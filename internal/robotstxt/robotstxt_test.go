package robotstxt_test

import (
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/robotstxt"
)

func TestParseCollectsBothPointers(t *testing.T) {
	t.Parallel()
	const body = `User-agent: *
Disallow: /admin

Sitemap: https://publisher.example/sitemap.xml
License: https://publisher.example/licensing/rsl.xml
Sitemap: https://publisher.example/sitemap-news.xml
`
	d, err := robotstxt.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{
		"https://publisher.example/sitemap.xml",
		"https://publisher.example/sitemap-news.xml",
	}
	if len(d.Sitemaps) != len(want) {
		t.Fatalf("sitemaps = %v, want %v", d.Sitemaps, want)
	}
	for i, w := range want {
		if d.Sitemaps[i] != w {
			t.Fatalf("sitemaps[%d] = %q, want %q (document order)", i, d.Sitemaps[i], w)
		}
	}
	if len(d.Licenses) != 1 || d.Licenses[0] != "https://publisher.example/licensing/rsl.xml" {
		t.Fatalf("licenses = %v, want the one license pointer", d.Licenses)
	}
}

// The format does not fix the case of a field name, and sites write all three.
func TestParseIgnoresFieldNameCaseAndSpacing(t *testing.T) {
	t.Parallel()
	const body = "  sitemap :   https://publisher.example/a.xml  \nSITEMAP:https://publisher.example/b.xml\n"
	d, err := robotstxt.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(d.Sitemaps) != 2 {
		t.Fatalf("sitemaps = %v, want both spellings read", d.Sitemaps)
	}
	if d.Sitemaps[0] != "https://publisher.example/a.xml" {
		t.Fatalf("sitemaps[0] = %q, want the value trimmed", d.Sitemaps[0])
	}
}

func TestParseDropsComments(t *testing.T) {
	t.Parallel()
	const body = `# Sitemap: https://publisher.example/commented-out.xml
Sitemap: https://publisher.example/live.xml # the real one
`
	d, err := robotstxt.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(d.Sitemaps) != 1 || d.Sitemaps[0] != "https://publisher.example/live.xml" {
		t.Fatalf("sitemaps = %v, want only the uncommented value, trimmed", d.Sitemaps)
	}
}

// A robots.txt is best effort: lines that carry nothing usable are skipped
// rather than failing the document.
func TestParseSkipsUnusableLines(t *testing.T) {
	t.Parallel()
	const body = "not a directive at all\nSitemap:\nSitemap:   \nAllow: /\n"
	d, err := robotstxt.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(d.Sitemaps) != 0 || len(d.Licenses) != 0 {
		t.Fatalf("got %+v, want nothing collected", d)
	}
}

func TestParseEmptyDocument(t *testing.T) {
	t.Parallel()
	d, err := robotstxt.Parse(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(d.Sitemaps) != 0 || len(d.Licenses) != 0 {
		t.Fatalf("got %+v, want nothing collected", d)
	}
}

// A file that is nothing but sitemap lines stops contributing past the bound,
// so a caller never walks an unbounded slice.
func TestParseBoundsOneField(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for range 200 {
		b.WriteString("Sitemap: https://publisher.example/a.xml\n")
	}
	d, err := robotstxt.Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(d.Sitemaps) != 64 {
		t.Fatalf("sitemaps = %d, want the bound of 64", len(d.Sitemaps))
	}
}
