//go:build integration

package essearch_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
)

// The search against a real single-node cluster with security on: what the
// stand-in cannot prove, because here Elasticsearch renders the template, does
// the ranking, applies the filters and answers the request the Searcher really
// sends. The template is the one the operator documentation ships.
//
// Round-trip: Searcher -> net/http -> real _search/template (and
// _render/template for the probe) -> parsed URIs. The documents and the stored
// template are arranged through testutil, which plays the index owner, whose
// writes are outside the Broker.

func doc(url, title, body string) map[string]any {
	return map[string]any{"url": url, "title": title, "body": body}
}

// newsDoc is a document the example filters can select.
func newsDoc(url, title, language, published string) map[string]any {
	return map[string]any{"url": url, "title": title, "body": "text", "language": language, "date_published": published}
}

func assertURIs(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uris = %v, want %v", got, want)
	}
}

// search runs one query with filters through Parse, as the Broker does.
func search(t *testing.T, cfg essearch.Config, query string, filters map[string]any) []string {
	t.Helper()
	s := newSearcher(t, cfg)
	f, err := s.Parse(filters)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, err := s.Search(context.Background(), query, f)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	return got
}

// The shipped template boosts the title, so a title match outranks a body match.
func TestSearch_TitleMatchRanksFirst(t *testing.T) {
	es := acquireES(t)
	inTitle := "https://publisher.example/in-title"
	inBody := "https://publisher.example/in-body"
	seedCatalog(t, es,
		doc(inBody, "a long report about the weather", "aurora over the harbour that week"),
		doc(inTitle, "aurora over the harbour", "a long report about the weather that week"),
	)
	assertURIs(t, search(t, nodeConfig(t, es), "aurora", nil), []string{inTitle, inBody})
}

// Documents with equal scores come back in URL order, whatever order they were
// indexed in.
func TestSearch_EqualScoresOrderByURL(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es,
		doc(urlC, "same headline", "same text"),
		doc(urlA, "same headline", "same text"),
		doc(urlB, "same headline", "same text"),
	)
	assertURIs(t, search(t, nodeConfig(t, es), "headline", nil), []string{urlA, urlB, urlC})
}

func TestSearch_LimitCutsTheResult(t *testing.T) {
	es := acquireES(t)
	docs := make([]map[string]any, 5)
	for i := range docs {
		docs[i] = doc(fmt.Sprintf("https://publisher.example/%d", i), "same headline", "same text")
	}
	seedCatalog(t, es, docs...)
	cfg := nodeConfig(t, es)
	cfg.Limit = 2
	assertURIs(t, search(t, cfg, "headline", nil), []string{"https://publisher.example/0", "https://publisher.example/1"})
}

// Two documents can carry one URL, and one resource group must come of it.
func TestSearch_DuplicateDocumentsAreOneResult(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es,
		doc(urlA, "headline", "first copy"),
		doc(urlA, "headline", "second copy"),
		doc(urlB, "headline", "other"),
	)
	assertURIs(t, search(t, nodeConfig(t, es), "headline", nil), []string{urlA, urlB})
}

func TestSearch_NoMatchIsAnEmptyResult(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es, doc(urlA, "headline", "text"))
	assertURIs(t, search(t, nodeConfig(t, es), "quantum", nil), nil)
}

func TestSearch_ThroughAnAlias(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es, doc(urlA, "headline", "text"))
	if err := es.CreateAlias(context.Background(), catalogIndex, "catalog-current"); err != nil {
		t.Fatal(err)
	}
	cfg := nodeConfig(t, es)
	cfg.Index = "catalog-current"
	assertURIs(t, search(t, cfg, "headline", nil), []string{urlA})
}

// The declared filters reach the template, which applies them on the index.
func TestSearch_LanguageFilterNarrows(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es,
		newsDoc(urlA, "headline", "en", "2026-01-01T00:00:00Z"),
		newsDoc(urlB, "headline", "el", "2026-01-02T00:00:00Z"),
		newsDoc(urlC, "headline", "el", "2026-01-03T00:00:00Z"),
	)
	cfg := nodeConfig(t, es)
	assertURIs(t, search(t, cfg, "headline", map[string]any{"news.language": "el"}), []string{urlB, urlC})
	assertURIs(t, search(t, cfg, "headline", map[string]any{"news.language": "en"}), []string{urlA})
	assertURIs(t, search(t, cfg, "headline", map[string]any{"news.language": "de"}), nil)
}

func TestSearch_DateFilterNarrows(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es,
		newsDoc(urlA, "headline", "en", "2026-01-01T00:00:00Z"),
		newsDoc(urlB, "headline", "en", "2026-01-02T00:00:00Z"),
		newsDoc(urlC, "headline", "en", "2026-01-03T00:00:00Z"),
	)
	after := map[string]any{"news.date_published_after": "2026-01-02T00:00:00Z"}
	assertURIs(t, search(t, nodeConfig(t, es), "headline", after), []string{urlB, urlC})
}

// A request with filters and no query takes the template's match_all branch:
// every document that passes the filters, in URL order.
func TestSearch_FilterOnlyRequestAnswers(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es,
		newsDoc(urlC, "first", "el", "2026-01-01T00:00:00Z"),
		newsDoc(urlA, "second", "el", "2026-01-02T00:00:00Z"),
		newsDoc(urlB, "third", "en", "2026-01-03T00:00:00Z"),
	)
	assertURIs(t, search(t, nodeConfig(t, es), "", map[string]any{"news.language": "el"}), []string{urlA, urlC})
}

// The same template sent as text gives the same answer as the stored one.
func TestSearch_TemplateFromSourceMatchesStored(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es,
		newsDoc(urlA, "headline", "en", "2026-01-01T00:00:00Z"),
		newsDoc(urlB, "headline", "el", "2026-01-02T00:00:00Z"),
	)
	stored := nodeConfig(t, es)
	inline := nodeConfig(t, es)
	inline.TemplateID, inline.TemplateSource = "", exampleTemplate(t)
	filters := map[string]any{"news.language": "el"}
	assertURIs(t, search(t, inline, "headline", filters), search(t, stored, "headline", filters))
	assertURIs(t, search(t, inline, "headline", nil), search(t, stored, "headline", nil))
}

// The query text is a template parameter, escaped as a JSON string: text shaped
// like the end of a clause stays text, and matches what its words match rather
// than the whole index.
func TestSearch_QueryTextCannotInjectClauses(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es,
		doc(urlA, "headline", "a note on match_all"),
		doc(urlB, "unrelated", "text"),
		doc(urlC, "other", "words"),
	)
	cfg := nodeConfig(t, es)
	injected := search(t, cfg, `"}}]}}, {"match_all": {}} {{"`, nil)
	assertURIs(t, injected, search(t, cfg, "match_all", nil))
	assertURIs(t, injected, []string{urlA})
}

// A template can project the URL instead of returning _source: a projected
// field comes back as an array, and the hit path reads its first element.
func TestSearch_FieldsProjectionPath(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es, doc(urlA, "headline", "text"))
	cfg := nodeConfig(t, es)
	cfg.TemplateID = ""
	cfg.TemplateSource = `{"query":{"match":{"title":"{{query}}"}},"size":"{{limit}}","_source":false,"fields":["url"]}`
	cfg.HitURLPath = "fields.url"
	assertURIs(t, search(t, cfg, "headline", nil), []string{urlA})
}

// The URL can be computed rather than stored: a script field builds it from a
// slug, and the hit path reads it like any projected field.
func TestSearch_ComputedURLThroughScriptFields(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es,
		map[string]any{"slug": "b", "title": "headline"},
		map[string]any{"slug": "a", "title": "headline"},
	)
	cfg := nodeConfig(t, es)
	cfg.TemplateID = ""
	cfg.TemplateSource = `{"query":{"match":{"title":"{{query}}"}},"size":"{{limit}}","_source":false,
		"sort":[{"_score":"desc"},{"slug":"asc"}],
		"script_fields":{"url":{"script":{"lang":"painless","source":"params.prefix + doc['slug'].value",
			"params":{"prefix":"https://publisher.example/articles/"}}}}}`
	cfg.HitURLPath = "fields.url"
	assertURIs(t, search(t, cfg, "headline", nil),
		[]string{"https://publisher.example/articles/a", "https://publisher.example/articles/b"})
}

func TestSearch_MissingIndexIsUnavailable(t *testing.T) {
	es := acquireES(t)
	if err := es.PutTemplate(context.Background(), templateID, exampleTemplate(t)); err != nil {
		t.Fatal(err)
	}
	cfg := nodeConfig(t, es)
	cfg.Index = "no-such-index"
	s := newSearcher(t, cfg)

	if _, err := s.Search(context.Background(), "q", none); !errors.Is(err, essearch.ErrUnavailable) {
		t.Fatalf("Search err = %v, want ErrUnavailable", err)
	} else if !strings.Contains(err.Error(), "index_not_found_exception") {
		t.Errorf("Search err = %q, want it to name the missing index", err)
	}
	if err := s.Probe(context.Background()); !errors.Is(err, essearch.ErrMisconfigured) {
		t.Errorf("Probe err = %v, want ErrMisconfigured", err)
	}
}

// A stored template that does not exist is the configuration, not an outage.
func TestTemplate_MissingStoredTemplateIsMisconfigured(t *testing.T) {
	es := acquireES(t)
	if err := es.CreateIndex(context.Background(), catalogIndex, catalogMapping); err != nil {
		t.Fatal(err)
	}
	s := newSearcher(t, nodeConfig(t, es))
	if err := s.Probe(context.Background()); !errors.Is(err, essearch.ErrMisconfigured) {
		t.Fatalf("Probe err = %v, want ErrMisconfigured", err)
	}
	if _, err := s.Search(context.Background(), "q", none); !errors.Is(err, essearch.ErrUnavailable) {
		t.Fatalf("Search err = %v, want ErrUnavailable", err)
	}
}

// A template that does not compile is refused at render, before any search.
func TestTemplate_MustacheErrorIsMisconfigured(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es, doc(urlA, "headline", "text"))
	cfg := nodeConfig(t, es)
	cfg.TemplateID, cfg.TemplateSource = "", `{"query":{{#query}}{"match_all":{}}}`
	if err := newSearcher(t, cfg).Probe(context.Background()); !errors.Is(err, essearch.ErrMisconfigured) {
		t.Fatalf("Probe err = %v, want ErrMisconfigured", err)
	}
}

func TestSearch_CancelledContextReturnsWithoutAnswer(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es, doc(urlA, "headline", "text"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newSearcher(t, nodeConfig(t, es)).Search(ctx, "headline", none)
	if !errors.Is(err, essearch.ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want ErrUnavailable wrapping the cancellation", err)
	}
}

// A control on the fixture, not on the Broker: the suite's memory budget depends
// on ES_JAVA_OPTS overriding the 2 GiB the test module writes into the container.
func TestNode_HeapIsCapped(t *testing.T) {
	es := acquireES(t)
	heap, err := es.HeapMaxBytes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	const maxHeap = 600 << 20
	if heap <= 0 || heap > maxHeap {
		t.Fatalf("heap_max_in_bytes = %d, want at most %d", heap, maxHeap)
	}
}
