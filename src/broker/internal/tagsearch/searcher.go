package tagsearch

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
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

// Search returns the matching URIs, best first. Without filters it matches
// the query's tags as Match does. With filters it returns the tracks that pass
// every filter, ranked by the query's tags (MatchFiltered), and asks the
// extractor only when there is a query. An empty result means nothing matched.
// An error is an extractor failure and is returned unchanged.
func (s *Searcher) Search(ctx context.Context, query string, f Filters) ([]string, error) {
	var e Extraction
	if query != "" || f.Empty() {
		var err error
		if e, err = s.extractor.Extract(ctx, query, s.vocab); err != nil {
			return nil, err
		}
	}
	e = s.index.Known(e)
	var uris []string
	if f.Empty() {
		uris = s.index.Match(e)
	} else {
		uris = s.index.MatchFiltered(e, f)
	}
	reqctx.FromContext(ctx).InfoContext(ctx, "broker.query_search",
		"include", e.Include, "exclude", e.Exclude,
		"min_bpm", e.MinBPM, "max_bpm", e.MaxBPM,
		"min_duration_seconds", e.MinDuration, "max_duration_seconds", e.MaxDuration,
		"filters", f.AnyOf, "filter_min_bpm", f.MinBPM, "filter_max_bpm", f.MaxBPM,
		"filter_min_duration_seconds", f.MinDuration, "filter_max_duration_seconds", f.MaxDuration,
		"matches", len(uris))
	return uris, nil
}
