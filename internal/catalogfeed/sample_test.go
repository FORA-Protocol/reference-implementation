package catalogfeed_test

import (
	"bytes"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/catalogfeed"
)

// The path a sample line records for a page. It is a parser over a page
// address, so it is tested as one: each case is an address a page read can
// return and the path the publisher's own feed would carry for it.
func TestPagePathKeepsOnlyTheEscapedPath(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]string{
		"https://publisher.example/news/a?utm_source=x": "/news/a",
		"https://publisher.example/news/a#comments":     "/news/a",
		"https://publisher.example/news/":               "/news/",
		"https://publisher.example":                     "/",
		"":                                              "/",
		"https://publisher.example/caf%C3%A9":           "/caf%C3%A9",
		"https://publisher.example/a%2Fb":               "/a%2Fb",
		"://not a url":                                  "/",
	} {
		if got := catalogfeed.PagePath(address); got != want {
			t.Errorf("PagePath(%q) = %q, want %q", address, got, want)
		}
	}
}

// A page read from the publisher's site becomes a line that parses and maps
// back to the page it came from: its domain, its path without the query, and
// its title.
func TestSampleForPageWritesTheLineForThatPage(t *testing.T) {
	t.Parallel()
	terms := richEntry().Terms
	line, warnings, err := catalogfeed.SampleForPage(
		"publisher.example", "https://publisher.example/news/a/?page=2",
		"Midnight Harbour", terms)
	if err != nil {
		t.Fatalf("SampleForPage: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("a line the check accepts carries warnings: %v", warnings)
	}
	records, err := catalogfeed.ParseJSONL(bytes.NewReader(line))
	if err != nil {
		t.Fatalf("the line does not parse: %v\n%s", err, line)
	}
	entries, err := catalogfeed.MapRecords(records)
	if err != nil || len(entries) != 1 {
		t.Fatalf("the line does not map to one entry: %v\n%s", err, line)
	}
	got := entries[0]
	if got.GetDomain() != "publisher.example" || got.GetPath() != "/news/a/" ||
		got.GetTitle() != "Midnight Harbour" {
		t.Fatalf("the line describes %s%s %q, want publisher.example/news/a/ %q",
			got.GetDomain(), got.GetPath(), got.GetTitle(), "Midnight Harbour")
	}
}

// A term the line cannot carry is an error, not a line with the term missing.
func TestSampleForPageRefusesTermsTheLineCannotCarry(t *testing.T) {
	t.Parallel()
	entry := richEntry()
	other := "0.05"
	entry.Terms[0].Pricing.UnitCost = &other
	line, _, err := catalogfeed.SampleForPage(
		"publisher.example", "https://publisher.example/news/a", "", entry.Terms)
	if err == nil {
		t.Fatalf("a line was built for terms it cannot carry: %s", line)
	}
}
