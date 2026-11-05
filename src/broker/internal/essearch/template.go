package essearch

import (
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
)

// Filters are the search filters Parse accepted, keyed as the request named
// them. They reach the template nested by dot.
type Filters map[string]any

// Empty reports whether f constrains nothing.
func (f Filters) Empty() bool { return len(f) == 0 }

var _ catalogsearch.Filters = Filters(nil)

// templateRequest is the body of _search/template and _render/template: the
// stored template's id or the template's text, and its parameters.
type templateRequest struct {
	ID     string         `json:"id,omitempty"`
	Source string         `json:"source,omitempty"`
	Params templateParams `json:"params"`
}

// templateParams are the parameters every template receives. Query is left out
// when there is no query text, rather than sent empty: the template engine
// treats any string, the empty one included, as present, so an absent query is
// the only one a template can test for. Filters is left out when there is none.
type templateParams struct {
	Query   string         `json:"query,omitempty"`
	Filters map[string]any `json:"filters,omitempty"`
	Limit   int            `json:"limit"`
}

// request builds the body for one call.
func (s *Searcher) request(query string, f Filters) templateRequest {
	return templateRequest{
		ID:     s.cfg.TemplateID,
		Source: s.cfg.TemplateSource,
		Params: templateParams{Query: query, Filters: nest(f), Limit: s.cfg.Limit},
	}
}

// nest turns dotted keys into nested objects, so a template addresses
// news.language as filters.news.language. A key without a dot stays at the top.
// The schema refuses a key that is another key's prefix, so no value is ever
// replaced by an object or the other way round.
func nest(f Filters) map[string]any {
	if f.Empty() {
		return nil
	}
	out := make(map[string]any, len(f))
	for key, value := range f {
		names := strings.Split(key, ".")
		node := out
		for _, name := range names[:len(names)-1] {
			child, ok := node[name].(map[string]any)
			if !ok {
				child = map[string]any{}
				node[name] = child
			}
			node = child
		}
		node[names[len(names)-1]] = value
	}
	return out
}
