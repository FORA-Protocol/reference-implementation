package tagsearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

// Extraction is the structured form of a natural-language query. Nil bounds
// are unbounded; bounds are inclusive.
type Extraction struct {
	Include     []string `json:"include"`
	Exclude     []string `json:"exclude"`
	MinBPM      *int     `json:"min_bpm"`
	MaxBPM      *int     `json:"max_bpm"`
	MinDuration *float64 `json:"min_duration_seconds"`
	MaxDuration *float64 `json:"max_duration_seconds"`
}

// ErrMalformed marks model output that does not satisfy the extraction schema.
var ErrMalformed = errors.New("malformed extraction")

type rawExtraction struct {
	Include     *[]*string `json:"include"`
	Exclude     *[]*string `json:"exclude"`
	MinBPM      *float64   `json:"min_bpm"`
	MaxBPM      *float64   `json:"max_bpm"`
	MinDuration *float64   `json:"min_duration_seconds"`
	MaxDuration *float64   `json:"max_duration_seconds"`
}

// ParseExtraction parses and validates one JSON object of model output. A
// surrounding Markdown code fence is tolerated. Every schema violation wraps
// ErrMalformed. Tags are not checked against a vocabulary here; Index.Known
// drops unknown ones.
func ParseExtraction(text string) (Extraction, error) {
	body := stripFence(text)
	dec := json.NewDecoder(strings.NewReader(body))
	var raw rawExtraction
	if err := dec.Decode(&raw); err != nil {
		return Extraction{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Extraction{}, fmt.Errorf("%w: trailing content after the JSON object", ErrMalformed)
	}
	include, err := tagList("include", raw.Include)
	if err != nil {
		return Extraction{}, err
	}
	exclude, err := tagList("exclude", raw.Exclude)
	if err != nil {
		return Extraction{}, err
	}
	return withBounds(Extraction{Include: include, Exclude: exclude}, raw)
}

// tagList requires an array whose every element is a string. Pointer elements
// keep a JSON null from decoding silently into "".
func tagList(name string, v *[]*string) ([]string, error) {
	if v == nil {
		return nil, fmt.Errorf("%w: %s must be an array", ErrMalformed, name)
	}
	out := make([]string, 0, len(*v))
	for _, t := range *v {
		if t == nil {
			return nil, fmt.Errorf("%w: %s holds a null element", ErrMalformed, name)
		}
		out = append(out, *t)
	}
	return out, nil
}

// withBounds validates and copies the BPM and duration bounds of raw into e.
func withBounds(e Extraction, raw rawExtraction) (Extraction, error) {
	var err error
	if e.MinBPM, err = bpmBound("min_bpm", raw.MinBPM); err != nil {
		return Extraction{}, err
	}
	if e.MaxBPM, err = bpmBound("max_bpm", raw.MaxBPM); err != nil {
		return Extraction{}, err
	}
	if e.MinBPM != nil && e.MaxBPM != nil && *e.MinBPM > *e.MaxBPM {
		return Extraction{}, fmt.Errorf("%w: min_bpm exceeds max_bpm", ErrMalformed)
	}
	for _, d := range []struct {
		name string
		v    *float64
	}{{"min_duration_seconds", raw.MinDuration}, {"max_duration_seconds", raw.MaxDuration}} {
		if d.v != nil && *d.v <= 0 {
			return Extraction{}, fmt.Errorf("%w: %s must be positive", ErrMalformed, d.name)
		}
	}
	e.MinDuration, e.MaxDuration = raw.MinDuration, raw.MaxDuration
	if e.MinDuration != nil && e.MaxDuration != nil && *e.MinDuration > *e.MaxDuration {
		return Extraction{}, fmt.Errorf("%w: min_duration_seconds exceeds max_duration_seconds", ErrMalformed)
	}
	return e, nil
}

func bpmBound(name string, v *float64) (*int, error) {
	if v == nil {
		return nil, nil
	}
	if *v <= 0 || *v != math.Trunc(*v) {
		return nil, fmt.Errorf("%w: %s must be a positive integer", ErrMalformed, name)
	}
	n := int(*v)
	return &n, nil
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimPrefix(s, "json")
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}
