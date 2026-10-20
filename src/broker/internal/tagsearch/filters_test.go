package tagsearch_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

func TestParseFilters(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want tagsearch.Filters
	}{
		{"nil object", nil, tagsearch.Filters{}},
		{"a label", map[string]any{"music.genre": "jazz"}, tagsearch.Filters{AnyOf: [][]string{{"genre:jazz"}}}},
		{
			"an array of labels, and every label key maps to its facet",
			map[string]any{
				"music.energy": []any{"low", "high"}, "music.instruments": "french horn",
				"music.mood": "calm", "music.suitable_for": "vlog",
			},
			tagsearch.Filters{AnyOf: [][]string{
				{"energy:low", "energy:high"}, {"instrument:french horn"}, {"mood:calm"}, {"use:vlog"},
			}},
		},
		{"vocals", map[string]any{"music.vocals": false}, tagsearch.Filters{AnyOf: [][]string{{"vocals:false"}}}},
		{"an isrc", map[string]any{"music.isrc": "USAAA2600001"}, tagsearch.Filters{AnyOf: [][]string{{"isrc:USAAA2600001"}}}},
		{
			"an array of isrcs",
			map[string]any{"music.isrc": []any{"USAAA2600001", "GBBBB2600002"}},
			tagsearch.Filters{AnyOf: [][]string{{"isrc:USAAA2600001", "isrc:GBBBB2600002"}}},
		},
		{
			"closed bpm range",
			map[string]any{"music.bpm_range": "115-125"},
			tagsearch.Filters{MinBPM: ptr(115), MaxBPM: ptr(125)},
		},
		{"open bpm ranges", map[string]any{"music.bpm_range": "120-"}, tagsearch.Filters{MinBPM: ptr(120)}},
		{
			"duration range",
			map[string]any{"music.duration_seconds_range": "-60.5"},
			tagsearch.Filters{MaxDuration: ptr(60.5)},
		},
		{"unknown keys are ignored", map[string]any{"music.tempo": "fast", "news.section": 1}, tagsearch.Filters{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := tagsearch.ParseFilters(c.raw)
			if err != nil {
				t.Fatalf("ParseFilters: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestParseFiltersRefusesMalformedValues(t *testing.T) {
	malformed := map[string][]any{
		"music.bpm_range":              {"fast", "", "-", "0-10", "10-5", "1.5-3", "10", 120.0, "+-"},
		"music.duration_seconds_range": {"a-b", "0-", "-Inf", "-inf", "60-30", 60.0},
		"music.genre":                  {"Jazz", " jazz", "", []any{}, []any{"jazz", 1}, 5.0, nil},
		"music.energy":                 {"extreme", []any{"low", "LOW"}},
		"music.vocals":                 {"no", 0.0, nil},
		"music.isrc":                   {"usaaa2600001", "US-AAA-26-00001", "USAAA260001", "", []any{}, 5.0, []any{"USAAA2600001", "x"}},
	}
	for key, values := range malformed {
		for _, v := range values {
			_, err := tagsearch.ParseFilters(map[string]any{key: v, "music.mood": "calm"})
			var ferr *tagsearch.FilterError
			if !errors.As(err, &ferr) || ferr.Key != key {
				t.Errorf("%s = %#v: err = %v, want a FilterError naming %s", key, v, err, key)
			}
		}
	}
}

func TestMatchFiltered(t *testing.T) {
	ix := testIndex(t)
	cases := []struct {
		name string
		e    tagsearch.Extraction
		f    tagsearch.Filters
		want []string
	}{
		{
			"OR within a key",
			tagsearch.Extraction{},
			tagsearch.Filters{AnyOf: [][]string{{"mood:epic", "mood:somber"}}},
			[]string{uri("/a"), uri("/c")},
		},
		{
			"AND across keys",
			tagsearch.Extraction{},
			tagsearch.Filters{AnyOf: [][]string{{"mood:calm"}, {"genre:soundtrack"}}},
			[]string{uri("/a")},
		},
		{
			"query tags rank only the tracks that pass, and never add others",
			tagsearch.Extraction{Include: []string{"mood:epic"}},
			tagsearch.Filters{AnyOf: [][]string{{"mood:calm"}}},
			[]string{uri("/a"), uri("/b")},
		},
		{
			"among passing tracks, the most facets win",
			tagsearch.Extraction{Include: []string{"genre:electronica"}},
			tagsearch.Filters{AnyOf: [][]string{{"mood:calm"}}},
			[]string{uri("/b")},
		},
		{
			"a query with no known tag keeps every passing track",
			tagsearch.Extraction{Include: []string{"mood:joyful"}},
			tagsearch.Filters{AnyOf: [][]string{{"genre:soundtrack"}}},
			[]string{uri("/a"), uri("/c")},
		},
		{
			"bpm-only filter, and an unknown bpm fails it",
			tagsearch.Extraction{},
			tagsearch.Filters{MinBPM: ptr(100)},
			[]string{uri("/b"), uri("/c")},
		},
		{
			"range-only filters and a query tag no passing track has keep every passing track",
			tagsearch.Extraction{Include: []string{"mood:somber"}},
			tagsearch.Filters{MinBPM: ptr(100)},
			[]string{uri("/b"), uri("/c")},
		},
		{
			"an isrc filter matches exactly",
			tagsearch.Extraction{},
			tagsearch.Filters{AnyOf: [][]string{{"isrc:USAAA2600001"}}},
			[]string{uri("/a")},
		},
		{
			"an isrc no track has matches nothing",
			tagsearch.Extraction{},
			tagsearch.Filters{AnyOf: [][]string{{"isrc:USAAA2600009"}}},
			[]string{},
		},
		{
			"filter and query bounds intersect",
			tagsearch.Extraction{MaxBPM: ptr(110)},
			tagsearch.Filters{MinBPM: ptr(100)},
			[]string{uri("/c")},
		},
		{
			"bounds that do not overlap match nothing",
			tagsearch.Extraction{MaxBPM: ptr(80)},
			tagsearch.Filters{MinBPM: ptr(100)},
			[]string{},
		},
		{
			"a value no track has matches nothing",
			tagsearch.Extraction{},
			tagsearch.Filters{AnyOf: [][]string{{"genre:funk"}}},
			[]string{},
		},
		{
			"an unknown vocals value fails a vocals filter",
			tagsearch.Extraction{},
			tagsearch.Filters{AnyOf: [][]string{{"vocals:false"}}},
			[]string{uri("/a")},
		},
		{
			"query exclusions stay hard filters",
			tagsearch.Extraction{Exclude: []string{"mood:somber"}},
			tagsearch.Filters{AnyOf: [][]string{{"genre:soundtrack"}}},
			[]string{uri("/c")},
		},
		{
			"duration filter",
			tagsearch.Extraction{},
			tagsearch.Filters{MaxDuration: ptr(60.0)},
			[]string{uri("/a"), uri("/d")},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ix.MatchFiltered(c.e, c.f); !slices.Equal(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
