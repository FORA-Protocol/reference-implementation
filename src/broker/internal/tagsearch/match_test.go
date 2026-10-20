package tagsearch_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

// feedLine renders one catalog feed record whose ext carries the fields of
// the JSON object music as flat music.<field> keys.
func feedLine(path, music string) string {
	var fields map[string]any
	if err := json.Unmarshal([]byte(music), &fields); err != nil {
		panic(err)
	}
	ext := map[string]any{}
	for k, v := range fields {
		ext["music."+k] = v
	}
	line, err := json.Marshal(map[string]any{"domain": "music.example", "path": path, "terms": []any{}, "ext": ext})
	if err != nil {
		panic(err)
	}
	return string(line)
}

func uri(path string) string { return "https://music.example" + path }

// testIndex: a is calm slow soundtrack with vocals:false; b is calm fast
// electronica vlog without vocals metadata; c is epic medium soundtrack with
// vocals:true; d has no matching metadata except duration.
func testIndex(t *testing.T) *tagsearch.Index {
	t.Helper()
	feed := strings.Join([]string{
		feedLine("/a", `{"isrc":"USAAA2600001","genre":"soundtrack","mood":["calm","somber"],"bpm":70,"vocals":false,`+
			`"duration_seconds":60}`),
		feedLine("/b", `{"genre":"electronica","mood":["calm"],"bpm":124,"suitable_for":["vlog"],"duration_seconds":95}`),
		feedLine("/c", `{"genre":"soundtrack","mood":["epic"],"bpm":105,"vocals":true,"duration_seconds":90}`),
		feedLine("/d", `{"artist":"X","duration_seconds":30}`),
		`{"domain":"music.example","path":"/lyrics","terms":[]}`,
	}, "\n")
	ix, err := tagsearch.Load(strings.NewReader(feed), "https")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return ix
}

func ptr[T any](v T) *T { return &v }

func TestMatch(t *testing.T) {
	ix := testIndex(t)
	cases := []struct {
		name string
		e    tagsearch.Extraction
		want []string
	}{
		{
			"or within a facet",
			tagsearch.Extraction{Include: []string{"mood:epic", "mood:somber"}},
			[]string{uri("/a"), uri("/c")},
		},
		{
			"only the tracks matching the most facets are returned",
			tagsearch.Extraction{Include: []string{"mood:calm", "genre:soundtrack"}},
			[]string{uri("/a")},
		},
		{
			"facets outrank tags",
			tagsearch.Extraction{Include: []string{"mood:calm", "mood:somber", "genre:electronica"}},
			[]string{uri("/b")},
		},
		{
			"the best partial match is returned when no track matches every facet",
			tagsearch.Extraction{Include: []string{"mood:epic", "use:vlog"}},
			[]string{uri("/b"), uri("/c")},
		},
		{
			"a filtered-out track does not raise the bar",
			tagsearch.Extraction{
				Include: []string{"mood:calm", "genre:soundtrack"}, Exclude: []string{"mood:somber"},
			},
			[]string{uri("/b"), uri("/c")},
		},
		{
			"more tags rank first within equal facets",
			tagsearch.Extraction{Include: []string{"mood:calm", "mood:somber"}},
			[]string{uri("/a"), uri("/b")},
		},
		{
			"equal score ties break by uri",
			tagsearch.Extraction{Include: []string{"mood:calm"}},
			[]string{uri("/a"), uri("/b")},
		},
		{
			"tempo derived from bpm",
			tagsearch.Extraction{Include: []string{"tempo:fast"}},
			[]string{uri("/b")},
		},
		{
			"exclusion drops matches and unknown fields",
			tagsearch.Extraction{
				Include: []string{"mood:calm"}, Exclude: []string{"vocals:true"},
			},
			[]string{uri("/a")},
		},
		{
			"exclude only returns every passing track",
			tagsearch.Extraction{Exclude: []string{"vocals:false"}},
			[]string{uri("/c")},
		},
		{
			"bpm bounds are inclusive hard filters",
			tagsearch.Extraction{
				Include: []string{"genre:soundtrack"}, MinBPM: ptr(70), MaxBPM: ptr(104),
			},
			[]string{uri("/a")},
		},
		{
			"duration bounds are inclusive hard filters",
			tagsearch.Extraction{
				Include: []string{"mood:calm", "mood:epic"}, MaxDuration: ptr(90.0),
			},
			[]string{uri("/a"), uri("/c")},
		},
		{
			"tempo tags still score with bpm bounds",
			tagsearch.Extraction{
				Include: []string{"tempo:fast", "genre:soundtrack"}, MinBPM: ptr(60),
			},
			[]string{uri("/a"), uri("/b"), uri("/c")},
		},
		{
			"tempo exclusion still applies with bpm bounds",
			tagsearch.Extraction{
				Include: []string{"genre:soundtrack"}, Exclude: []string{"tempo:slow"}, MinBPM: ptr(60),
			},
			[]string{uri("/c")},
		},
		{"range only matches nothing", tagsearch.Extraction{MinBPM: ptr(1)}, nil},
		{
			"unknown tags are ignored",
			tagsearch.Extraction{Include: []string{"mood:joyful", "tempo:fast"}},
			[]string{uri("/b")},
		},
		{"only unknown tags match nothing", tagsearch.Extraction{Include: []string{"mood:joyful"}}, nil},
		{
			"no match",
			tagsearch.Extraction{Include: []string{"use:vlog"}, Exclude: []string{"genre:electronica"}},
			nil,
		},
		{"contradiction matches nothing", tagsearch.Extraction{
			Include: []string{"mood:epic"}, Exclude: []string{"mood:epic"},
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ix.Match(tc.e); !slices.Equal(got, tc.want) {
				t.Errorf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKnownDropsUnknownAndDuplicates(t *testing.T) {
	ix := testIndex(t)
	got := ix.Known(tagsearch.Extraction{
		Include: []string{"mood:calm", "mood:calm", "license:personal", "use:vlog"},
		Exclude: []string{"vocals:true", "vocals"},
	})
	if !slices.Equal(got.Include, []string{"mood:calm", "use:vlog"}) || !slices.Equal(got.Exclude, []string{"vocals:true"}) {
		t.Errorf("Known = %+v", got)
	}
}

func TestVocabulary(t *testing.T) {
	v := testIndex(t).Vocabulary()
	for _, want := range []string{"genre:electronica", "mood:somber", "use:vlog", "tempo:medium", "vocals:true", "vocals:false"} {
		if !slices.Contains(v, want) {
			t.Errorf("vocabulary lacks %q: %v", want, v)
		}
	}
	if !slices.IsSorted(v) {
		t.Errorf("vocabulary not sorted: %v", v)
	}
	// ISRCs are for exact-match filters only; the query model never sees them.
	if i := slices.IndexFunc(v, func(tag string) bool { return strings.HasPrefix(tag, "isrc:") }); i >= 0 {
		t.Errorf("vocabulary holds the ISRC tag %q", v[i])
	}
}

func TestLoadRejectsInvalidFeeds(t *testing.T) {
	cases := map[string]string{
		"invalid jsonl":        `{"domain":`,
		"wrong type":           feedLine("/x", `{"mood":"calm"}`),
		"upper case":           feedLine("/x", `{"genre":"Soundtrack"}`),
		"empty value":          feedLine("/x", `{"instruments":[""]}`),
		"bad energy":           feedLine("/x", `{"energy":"loud"}`),
		"hyphenated isrc":      feedLine("/x", `{"genre":"jazz","isrc":"US-AAA-26-00001"}`),
		"lower-case isrc":      feedLine("/x", `{"genre":"jazz","isrc":"usaaa2600001"}`),
		"artist not a string":  feedLine("/x", `{"genre":"jazz","artist":5}`),
		"fractional bpm":       feedLine("/x", `{"bpm":90.5}`),
		"negative duration":    feedLine("/x", `{"duration_seconds":-1}`),
		"field of wrong type":  `{"domain":"music.example","path":"/x","terms":[],"ext":{"music.genre":5}}`,
		"no music key":         `{"domain":"music.example","path":"/x","terms":[]}`,
		"nested music object":  `{"domain":"music.example","path":"/x","terms":[],"ext":{"music":{"genre":"jazz"}}}`,
		"no matching metadata": feedLine("/x", `{"artist":"X","duration_seconds":30}`),
	}
	for name, feed := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tagsearch.Load(strings.NewReader(feed), "https"); err == nil {
				t.Fatal("Load succeeded, want error")
			}
		})
	}
}

func TestLoadErrorNamesRecordAndField(t *testing.T) {
	_, err := tagsearch.Load(strings.NewReader(feedLine("/bad", `{"energy":"loud"}`)), "https")
	if err == nil || !strings.Contains(err.Error(), uri("/bad")) || !strings.Contains(err.Error(), "music.energy") {
		t.Fatalf("error = %v, want record URI and field", err)
	}
}
