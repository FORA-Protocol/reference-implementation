package tagsearch_test

import (
	"context"
	"errors"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

// refusingExtractor fails the test if the search asks the model anything.
type refusingExtractor struct{ t *testing.T }

func (x refusingExtractor) Extract(context.Context, string, []string) (tagsearch.Extraction, error) {
	x.t.Error("the extractor was asked, want the search refused first")
	return tagsearch.Extraction{}, nil
}

// foreignFilters is a filter value no music vocabulary produced.
type foreignFilters struct{}

func (foreignFilters) Empty() bool { return false }

// The search applies only filters its own vocabulary parsed. Anything else is
// refused before the model is asked, rather than read as no filter, and so is
// a missing value.
func TestSearcher_RefusesFiltersItDidNotParse(t *testing.T) {
	s := tagsearch.NewSearcher(testIndex(t), refusingExtractor{t})
	for name, f := range map[string]catalogsearch.Filters{
		"another vocabulary's value": foreignFilters{},
		"no value":                   nil,
	} {
		if _, err := s.Search(context.Background(), "calm", f); !errors.Is(err, catalogsearch.ErrForeignFilters) {
			t.Errorf("%s: err = %v, want catalogsearch.ErrForeignFilters", name, err)
		}
	}
}
