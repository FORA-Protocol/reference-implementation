package htmlmeta_test

import (
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/htmlmeta"
)

func TestExtractReadsTheHead(t *testing.T) {
	t.Parallel()
	const page = `<!doctype html>
<html><head>
  <meta charset="utf-8">
  <title>Midnight Harbour | Publisher</title>
  <meta property="og:title" content="Midnight Harbour">
  <link rel="canonical" href="https://publisher.example/news/harbour-story">
</head><body><h1>ignored</h1></body></html>`
	m := htmlmeta.Extract(strings.NewReader(page))
	if m.Title != "Midnight Harbour | Publisher" {
		t.Fatalf("title = %q", m.Title)
	}
	if m.OGTitle != "Midnight Harbour" {
		t.Fatalf("og title = %q", m.OGTitle)
	}
	if m.Canonical != "https://publisher.example/news/harbour-story" {
		t.Fatalf("canonical = %q", m.Canonical)
	}
}

// The tab title usually carries the site name; the Open Graph title is the
// headline the site asks other sites to show, so it wins.
func TestBestTitlePrefersOpenGraph(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		page string
		want string
	}{
		{
			"open graph present",
			`<head><title>Story | Site</title><meta property="og:title" content="Story"></head>`,
			"Story",
		},
		{"title only", `<head><title>Story | Site</title></head>`, "Story | Site"},
		{"neither", `<head></head>`, ""},
		{
			"open graph written with the name attribute",
			`<head><title>Story | Site</title><meta name="og:title" content="Story"></head>`,
			"Story",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := htmlmeta.Extract(strings.NewReader(tc.page)).BestTitle(); got != tc.want {
				t.Fatalf("best title = %q, want %q", got, tc.want)
			}
		})
	}
}

// A title written across several lines renders as one line in a browser, so it
// is read as one line here.
func TestExtractCollapsesWhitespace(t *testing.T) {
	t.Parallel()
	const page = "<head><title>\n  Midnight\n  Harbour\n</title></head>"
	if got := htmlmeta.Extract(strings.NewReader(page)).Title; got != "Midnight Harbour" {
		t.Fatalf("title = %q, want the collapsed form", got)
	}
}

// Entities are decoded, because the title is shown to a person, not re-parsed.
func TestExtractDecodesEntities(t *testing.T) {
	t.Parallel()
	const page = `<head><title>Ships &amp; Harbours &#8212; a study</title></head>`
	got := htmlmeta.Extract(strings.NewReader(page)).Title
	if !strings.Contains(got, "Ships & Harbours") {
		t.Fatalf("title = %q, want the entities decoded", got)
	}
}

// The rel attribute is a space-separated list, and a page may name several.
func TestExtractFindsCanonicalAmongSeveralRelations(t *testing.T) {
	t.Parallel()
	const page = `<head><link rel="shortlink canonical" href="https://publisher.example/a"></head>`
	if got := htmlmeta.Extract(strings.NewReader(page)).Canonical; got != "https://publisher.example/a" {
		t.Fatalf("canonical = %q", got)
	}
}

// A link that is not the canonical one must not be mistaken for it.
func TestExtractIgnoresOtherLinkRelations(t *testing.T) {
	t.Parallel()
	const page = `<head>
		<link rel="stylesheet" href="/style.css">
		<link rel="alternate" href="https://publisher.example/feed.xml">
	</head>`
	if got := htmlmeta.Extract(strings.NewReader(page)).Canonical; got != "" {
		t.Fatalf("canonical = %q, want empty", got)
	}
}

// The first declaration is the one a browser uses; a second does not replace it.
func TestExtractKeepsTheFirstDeclaration(t *testing.T) {
	t.Parallel()
	const page = `<head>
		<meta property="og:title" content="First">
		<meta property="og:title" content="Second">
		<link rel="canonical" href="https://publisher.example/first">
		<link rel="canonical" href="https://publisher.example/second">
	</head>`
	m := htmlmeta.Extract(strings.NewReader(page))
	if m.OGTitle != "First" || m.Canonical != "https://publisher.example/first" {
		t.Fatalf("got %+v, want the first of each", m)
	}
}

// Reading stops at the body: the three facts are head-only, so walking a long
// article would be work with no answer in it.
func TestExtractStopsAtTheBody(t *testing.T) {
	t.Parallel()
	const page = `<head><title>Real</title></head><body>
		<meta property="og:title" content="Injected">
		<link rel="canonical" href="https://attacker.example/">
	</body>`
	m := htmlmeta.Extract(strings.NewReader(page))
	if m.OGTitle != "" || m.Canonical != "" {
		t.Fatalf("got %+v, want nothing read from the body", m)
	}
	if m.Title != "Real" {
		t.Fatalf("title = %q", m.Title)
	}
}

// HTML parsing recovers by design: a page missing its closing tags is still a
// page, and a page with no title is not a failure.
func TestExtractToleratesMalformedPages(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"unclosed title": `<head><title>Story`,
		"no head at all": `<p>just text`,
		"empty document": ``,
		"not html":       `{"json": true}`,
	}
	for name, page := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// The assertion is that this returns rather than panicking or
			// blocking; the values are whatever the page actually carried.
			_ = htmlmeta.Extract(strings.NewReader(page))
		})
	}
}

func TestExtractIgnoresAttributeCase(t *testing.T) {
	t.Parallel()
	const page = `<head><META PROPERTY="og:title" CONTENT="Story"><LINK REL="canonical" HREF="/a"></head>`
	m := htmlmeta.Extract(strings.NewReader(page))
	if m.OGTitle != "Story" || m.Canonical != "/a" {
		t.Fatalf("got %+v, want both read despite the attribute case", m)
	}
}
