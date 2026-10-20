package tagsearch_test

import (
	"errors"
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

func TestParseExtraction(t *testing.T) {
	e, err := tagsearch.ParseExtraction("```json\n" + `{"include":["mood:calm"],"exclude":["vocals:true"],
		"min_bpm":100,"max_bpm":null,"max_duration_seconds":90.5,"note":"ignored"}` + "\n```")
	if err != nil {
		t.Fatalf("ParseExtraction: %v", err)
	}
	if !slices.Equal(e.Include, []string{"mood:calm"}) || !slices.Equal(e.Exclude, []string{"vocals:true"}) {
		t.Errorf("tags = %+v", e)
	}
	if e.MinBPM == nil || *e.MinBPM != 100 || e.MaxBPM != nil || e.MinDuration != nil ||
		e.MaxDuration == nil || *e.MaxDuration != 90.5 {
		t.Errorf("bounds = %+v", e)
	}
}

func TestParseExtractionAcceptsEmptyArrays(t *testing.T) {
	e, err := tagsearch.ParseExtraction(`{"include":[],"exclude":[]}`)
	if err != nil || len(e.Include) != 0 || len(e.Exclude) != 0 {
		t.Fatalf("ParseExtraction = %+v, %v", e, err)
	}
}

func TestParseExtractionRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"not json":          `calm music`,
		"array":             `[{"include":[],"exclude":[]}]`,
		"null":              `null`,
		"missing include":   `{"exclude":[]}`,
		"null exclude":      `{"include":[],"exclude":null}`,
		"null include tag":  `{"include":[null],"exclude":[]}`,
		"null exclude tag":  `{"include":[],"exclude":["vocals:true",null]}`,
		"tag not a string":  `{"include":[1],"exclude":[]}`,
		"include a string":  `{"include":"mood:calm","exclude":[]}`,
		"fractional bpm":    `{"include":[],"exclude":[],"min_bpm":99.5}`,
		"zero bpm":          `{"include":[],"exclude":[],"max_bpm":0}`,
		"bpm as string":     `{"include":[],"exclude":[],"min_bpm":"100"}`,
		"inverted bpm":      `{"include":[],"exclude":[],"min_bpm":120,"max_bpm":100}`,
		"negative duration": `{"include":[],"exclude":[],"min_duration_seconds":-1}`,
		"inverted duration": `{"include":[],"exclude":[],"min_duration_seconds":90,"max_duration_seconds":60}`,
		"trailing content":  `{"include":[],"exclude":[]} {"include":[],"exclude":[]}`,
		"truncated":         `{"include":["mood:calm"`,
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tagsearch.ParseExtraction(text); !errors.Is(err, tagsearch.ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
}
