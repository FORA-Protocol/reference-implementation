package rsl_test

import (
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/rsl"
)

// parseRules builds a document whose rules are just address patterns, so a
// matching test says what it is about and nothing else.
func parseRules(t *testing.T, patterns ...string) *rsl.Document {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<rsl xmlns="https://rslstandard.org/rsl">`)
	for _, p := range patterns {
		b.WriteString(`<content url="` + p + `"><license><payment type="free"/></license></content>`)
	}
	b.WriteString(`</rsl>`)
	doc, err := rsl.Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func TestMatchPatternForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		pattern string
		page    string
		want    bool
	}{
		{"root matches everything", "/", "https://publisher.example/news/a", true},
		{"empty matches everything", "", "https://publisher.example/news/a", true},
		{"prefix matches below it", "/news/", "https://publisher.example/news/a", true},
		{"prefix does not match a sibling", "/news/", "https://publisher.example/sport/a", false},
		{"exact path without anchor is a prefix", "/news", "https://publisher.example/newsletter", true},
		{"anchored path must end there", "/news$", "https://publisher.example/newsletter", false},
		{"anchored path matches itself", "/news$", "https://publisher.example/news", true},
		{"star matches a run", "/news/*/full", "https://publisher.example/news/2026/full", true},
		{"star does not invent a segment", "/news/*/full", "https://publisher.example/news/2026", false},
		{"leading star matches anywhere", "*/full", "https://publisher.example/news/2026/full", true},
		{"star then anchor", "/news/*.html$", "https://publisher.example/news/a.html", true},
		{"star then anchor rejects a suffix", "/news/*.html$", "https://publisher.example/news/a.html.bak", false},
		// The anchored run appears twice in the path. Taking it at its first
		// occurrence leaves the match in the middle and reports no match,
		// although the "*" covers "a.html/b".
		{
			"anchored run is matched at the end, not at its first occurrence",
			"/news/*.html$", "https://publisher.example/news/a.html/b.html", true,
		},
		{
			"anchored run repeated with a segment between",
			"/p/*/x$", "https://publisher.example/p/1/x/2/x", true,
		},
		{
			"anchored run must still follow the earlier runs",
			"/news/*.html$", "https://publisher.example/a.html/news/b", false,
		},
		{"trailing star anchored matches the rest", "/news/*$", "https://publisher.example/news/a/b", true},
		{"relative pattern is anchored at the root", "news/", "https://publisher.example/news/a", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := parseRules(t, tc.pattern)
			_, ok := doc.Match(tc.page)
			if ok != tc.want {
				t.Fatalf("pattern %q against %q matched = %v, want %v", tc.pattern, tc.page, ok, tc.want)
			}
		})
	}
}

// The most specific statement the publisher made is the one that applies.
func TestMatchPrefersTheLongestPattern(t *testing.T) {
	t.Parallel()
	doc := parseRules(t, "/", "/news/2026/*", "/news/")
	got, ok := doc.Match("https://publisher.example/news/2026/harbour")
	if !ok {
		t.Fatal("no rule matched")
	}
	if got.URL != "/news/2026/*" {
		t.Fatalf("matched %q, want the longest pattern", got.URL)
	}
}

// What a missed anchored match actually costs. The publisher priced their
// articles and left the rest of the site free. A page whose path carries the
// anchored run twice still belongs to the priced rule; if that rule fails to
// match, the page does not go unpriced-and-reported, it silently falls to the
// free rule covering the whole site.
func TestAnchoredRuleStillWinsOverABroaderFreeRule(t *testing.T) {
	t.Parallel()
	body := `<rsl xmlns="https://rslstandard.org/rsl">` +
		`<content url="/"><license><payment type="free"/></license></content>` +
		`<content url="/news/*.html$"><license><payment type="purchase">` +
		`<amount currency="EUR">2.50</amount></payment></license></content>` +
		`</rsl>`
	doc, err := rsl.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, ok := doc.Match("https://publisher.example/news/a.html/b.html")
	if !ok {
		t.Fatal("no rule matched a page the document covers twice over")
	}
	if got.URL != "/news/*.html$" {
		t.Fatalf("matched %q, want the priced rule; the free rule for the whole site "+
			"would price this page at nothing", got.URL)
	}
}

// Two patterns of the same length are resolved by the order the publisher
// wrote them, so the answer never depends on map iteration.
func TestMatchBreaksTiesInDocumentOrder(t *testing.T) {
	t.Parallel()
	doc := parseRules(t, "/news/*", "/*/2026")
	got, ok := doc.Match("https://publisher.example/news/2026")
	if !ok {
		t.Fatal("no rule matched")
	}
	if got.URL != "/news/*" {
		t.Fatalf("matched %q, want the first of two equally specific patterns", got.URL)
	}
}

// An absolute pattern names a host, and a document may only license its own.
func TestMatchAbsolutePatterns(t *testing.T) {
	t.Parallel()
	doc := parseRules(t, "https://publisher.example/news/")
	if _, ok := doc.Match("https://publisher.example/news/a"); !ok {
		t.Fatal("an absolute pattern did not match its own host")
	}
	if _, ok := doc.Match("https://other.example/news/a"); ok {
		t.Fatal("an absolute pattern matched a different host")
	}
}

func TestMatchNoRuleApplies(t *testing.T) {
	t.Parallel()
	doc := parseRules(t, "/premium/")
	if _, ok := doc.Match("https://publisher.example/news/a"); ok {
		t.Fatal("a rule matched a page it does not cover")
	}
}

// A page address with no path is the home page.
func TestMatchTreatsABareHostAsTheRoot(t *testing.T) {
	t.Parallel()
	doc := parseRules(t, "/")
	if _, ok := doc.Match("https://publisher.example"); !ok {
		t.Fatal("the root rule did not match the home page")
	}
}

func TestMatchOnANilDocument(t *testing.T) {
	t.Parallel()
	var doc *rsl.Document
	if _, ok := doc.Match("https://publisher.example/a"); ok {
		t.Fatal("a nil document reported a match")
	}
}
