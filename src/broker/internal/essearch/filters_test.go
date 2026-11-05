package essearch_test

import (
	"errors"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/searchfilters"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch/essearchtest"
)

func loadSchema(t *testing.T, doc string) *searchfilters.Schema {
	t.Helper()
	s, err := searchfilters.Load([]byte(doc))
	if err != nil || s == nil {
		t.Fatalf("searchfilters.Load = %v, %v", s, err)
	}
	return s
}

const newsSchema = `{"type":"object","additionalProperties":false,"properties":{
	"news.language":{"type":"string","pattern":"^[a-z]{2}$"}}}`

// filterError is the *catalogsearch.FilterError behind err, or a test failure.
func filterError(t *testing.T, err error) *catalogsearch.FilterError {
	t.Helper()
	var ferr *catalogsearch.FilterError
	if !errors.As(err, &ferr) {
		t.Fatalf("err = %v, want a *catalogsearch.FilterError", err)
	}
	return ferr
}

// Without a schema the search applies no filter, so any key is refused as
// unsupported: the first in sorted order, whatever its value.
func TestParse_WithoutSchemaEveryKeyIsUnsupported(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	s := newSearcher(t, es.Config())

	_, err := s.Parse(map[string]any{"news.language": "en", "music.genre": 5.0})

	if ferr := filterError(t, err); !ferr.Unsupported || ferr.Key != "music.genre" {
		t.Errorf("FilterError = %+v, want music.genre unsupported", ferr)
	}
	for _, raw := range []map[string]any{nil, {}} {
		f, err := s.Parse(raw)
		if err != nil || !f.Empty() {
			t.Errorf("Parse(%v) = %v, %v; want no filter", raw, f, err)
		}
	}
}

// With a schema, Parse is the schema's decision, carried one to one.
func TestParse_WithSchemaDelegates(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	cfg := es.Config()
	cfg.Filters = loadSchema(t, newsSchema)
	s := newSearcher(t, cfg)

	f, err := s.Parse(map[string]any{"news.language": "en"})
	if err != nil || f.Empty() {
		t.Fatalf("accepted filter: %v, %v", f, err)
	}
	if got, ok := f.(essearch.Filters); !ok || got["news.language"] != "en" {
		t.Errorf("parsed = %#v, want the accepted object", f)
	}

	cases := map[string]struct {
		raw         map[string]any
		key         string
		unsupported bool
	}{
		"an undeclared key":     {map[string]any{"news.author": "x"}, "news.author", true},
		"a malformed value":     {map[string]any{"news.language": "english"}, "news.language", false},
		"an oversized object":   {map[string]any{"news.language": strings.Repeat("a", 17000)}, "", false},
		"a declared and an odd": {map[string]any{"news.language": "en", "news.zone": "x"}, "news.zone", true},
	}
	for name, c := range cases {
		_, err := s.Parse(c.raw)
		ferr := filterError(t, err)
		if ferr.Key != c.key || ferr.Unsupported != c.unsupported || ferr.Reason == "" {
			t.Errorf("%s: FilterError = %+v, want key %q unsupported=%v", name, ferr, c.key, c.unsupported)
		}
	}
}
