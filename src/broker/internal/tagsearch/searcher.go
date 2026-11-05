package tagsearch

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
)

// Extractor turns a query into an extraction, given the vocabulary.
type Extractor interface {
	Extract(ctx context.Context, query string, vocabulary []string) (Extraction, error)
}

// Searcher resolves a free-text query to catalog URIs: the extractor turns
// the query into tags, and the index matches them.
type Searcher struct {
	index     *Index
	extractor Extractor
	vocab     []string
}

// NewSearcher returns a Searcher over index using extractor.
func NewSearcher(index *Index, extractor Extractor) *Searcher {
	return &Searcher{index: index, extractor: extractor, vocab: index.Vocabulary()}
}

// Parse is the search's filter vocabulary: fora-music-v1's, as MusicVocabulary
// reads it.
func (s *Searcher) Parse(raw map[string]any) (catalogsearch.Filters, error) {
	return MusicVocabulary{}.Parse(raw)
}

// Search returns the matching URIs, best first. f must be what Parse returned;
// anything else is catalogsearch.ErrForeignFilters. Without filters it matches
// the query's tags as Match does. With filters it returns the tracks that pass
// every filter, ranked by the query's tags (MatchFiltered), and asks the
// extractor only when there is a query. An empty result means nothing matched.
// An extractor failure is returned unchanged.
func (s *Searcher) Search(ctx context.Context, query string, f catalogsearch.Filters) ([]string, error) {
	filters, ok := f.(Filters)
	if !ok {
		return nil, catalogsearch.ErrForeignFilters
	}
	var e Extraction
	if query != "" || filters.Empty() {
		var err error
		if e, err = s.extractor.Extract(ctx, query, s.vocab); err != nil {
			return nil, err
		}
	}
	e = s.index.Known(e)
	var uris []string
	if filters.Empty() {
		uris = s.index.Match(e)
	} else {
		uris = s.index.MatchFiltered(e, filters)
	}
	reqctx.FromContext(ctx).InfoContext(ctx, "broker.query_search",
		"include", e.Include, "exclude", e.Exclude,
		"min_bpm", e.MinBPM, "max_bpm", e.MaxBPM,
		"min_duration_seconds", e.MinDuration, "max_duration_seconds", e.MaxDuration,
		"filters", filters.AnyOf, "filter_min_bpm", filters.MinBPM, "filter_max_bpm", filters.MaxBPM,
		"filter_min_duration_seconds", filters.MinDuration, "filter_max_duration_seconds", filters.MaxDuration,
		"matches", len(uris))
	return uris, nil
}
