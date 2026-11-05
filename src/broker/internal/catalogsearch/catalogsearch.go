package catalogsearch

import (
	"context"
	"errors"
)

// Filters is a provider's parsed view of a request's search_filters. The zero
// value of each provider's type constrains nothing.
type Filters interface {
	// Empty reports whether the filters constrain nothing.
	Empty() bool
}

// FilterError is one offending search_filters key. Unsupported means the
// vocabulary does not apply the key, so the request is valid but cannot be
// answered as asked; otherwise the key's value is malformed. Key "" names the
// whole object rather than one key.
//
// Reason is the vocabulary's own text, which the caller returns to the agent:
// it says what the key requires. Whether it also quotes the refused value is
// the vocabulary's choice, and the fora-music-v1 vocabulary does.
type FilterError struct {
	Key         string
	Unsupported bool
	Reason      string
}

func (e *FilterError) Error() string {
	if e.Key == "" {
		return "search_filters: " + e.Reason
	}
	return "search_filters " + e.Key + ": " + e.Reason
}

// Vocabulary admits a request's search_filters before any search runs.
type Vocabulary interface {
	// Parse returns the filters the search will apply, or a *FilterError for
	// the first offending key in sorted key order. A nil or empty raw object is
	// no filter.
	Parse(raw map[string]any) (Filters, error)
}

// Searcher resolves a query and the parsed filters to catalog URIs, best
// first. An empty result means nothing matched; an error means the search
// could not run. A Searcher is also the Vocabulary that produced f: it is
// handed only values its own Parse returned.
type Searcher interface {
	Vocabulary
	Search(ctx context.Context, query string, f Filters) ([]string, error)
}

// ErrForeignFilters is what a Searcher returns when it is handed Filters that
// another vocabulary parsed. resolve.NewService makes the configured search its
// own vocabulary, so the error is unreachable through it.
var ErrForeignFilters = errors.New("catalogsearch: filters parsed by another vocabulary")
