package tagsearch

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Filters are the fora-music-v1 search filters of a request: required
// constraints on the tracks a search may return. The zero value has no filter.
type Filters struct {
	// AnyOf holds one tag set per filter key. A track must have at least one
	// tag of every set: OR within a key, AND across keys.
	AnyOf       [][]string
	MinBPM      *int
	MaxBPM      *int
	MinDuration *float64
	MaxDuration *float64
}

// Empty reports whether f constrains nothing.
func (f Filters) Empty() bool {
	return len(f.AnyOf) == 0 && f.MinBPM == nil && f.MaxBPM == nil && f.MinDuration == nil && f.MaxDuration == nil
}

// FilterError is a defined filter key whose value does not have the key's
// format.
type FilterError struct {
	Key    string
	Reason string
}

func (e *FilterError) Error() string {
	return fmt.Sprintf("search_filters %s: %s", e.Key, e.Reason)
}

// labelKeys maps each label filter key to the facet it constrains.
var labelKeys = map[string]string{
	"music.genre":        facetGenre,
	"music.mood":         facetMood,
	"music.instruments":  facetInstrument,
	"music.suitable_for": facetUse,
	"music.energy":       facetEnergy,
}

// ParseFilters reads the fora-music-v1 keys of a search_filters object, as
// decoded from JSON. Keys the profile does not define are ignored. A defined
// key with a malformed value is a *FilterError. Values are not checked against
// the catalog: a well-formed value no track has matches nothing.
func ParseFilters(raw map[string]any) (Filters, error) {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys) // the first malformed key reported does not depend on map order
	var f Filters
	for _, key := range keys {
		if err := f.add(key, raw[key]); err != nil {
			return Filters{}, &FilterError{Key: key, Reason: err.Error()}
		}
	}
	return f, nil
}

func (f *Filters) add(key string, v any) error {
	var err error
	switch key {
	case "music.isrc":
		var tags []string
		if tags, err = isrcTags(v); err == nil {
			f.AnyOf = append(f.AnyOf, tags)
		}
	case "music.vocals":
		b, ok := v.(bool)
		if !ok {
			return errors.New("must be a boolean")
		}
		f.AnyOf = append(f.AnyOf, []string{facetVocals + ":" + strconv.FormatBool(b)})
	case "music.bpm_range":
		f.MinBPM, f.MaxBPM, err = parseRange(v, parseBPM)
	case "music.duration_seconds_range":
		f.MinDuration, f.MaxDuration, err = parseRange(v, parseSeconds)
	default:
		facet, ok := labelKeys[key]
		if !ok {
			return nil
		}
		var tags []string
		if tags, err = labelTags(facet, v); err == nil {
			f.AnyOf = append(f.AnyOf, tags)
		}
	}
	return err
}

// labelTags reads a label or a non-empty array of labels as facet tags.
func labelTags(facet string, v any) ([]string, error) {
	values, err := oneOrMany(v, "a label or an array of labels")
	if err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(values))
	for _, e := range values {
		s, ok := e.(string)
		if !ok || !validLabel(s) {
			return nil, fmt.Errorf("%v is not a label: non-empty, trimmed, with no upper- or title-case letter", e)
		}
		if facet == facetEnergy && !validEnergy(s) {
			return nil, fmt.Errorf("%q is not low, medium or high", s)
		}
		tags = append(tags, facet+":"+s)
	}
	return tags, nil
}

// isrcTags reads an ISRC or a non-empty array of ISRCs as exact-match tags.
func isrcTags(v any) ([]string, error) {
	values, err := oneOrMany(v, "an ISRC or an array of ISRCs")
	if err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(values))
	for _, e := range values {
		s, ok := e.(string)
		if !ok || !isrcPattern.MatchString(s) {
			return nil, fmt.Errorf("%v is not a 12-character upper-case ISRC without hyphens", e)
		}
		tags = append(tags, facetISRC+":"+s)
	}
	return tags, nil
}

// oneOrMany reads a filter value that is one string or a non-empty array. It
// returns the values unchecked; the caller checks each one.
func oneOrMany(v any, what string) ([]any, error) {
	switch x := v.(type) {
	case string:
		return []any{x}, nil
	case []any:
		if len(x) == 0 {
			return nil, errors.New("must not be an empty array")
		}
		return x, nil
	default:
		return nil, errors.New("must be " + what)
	}
}

// parseRange reads "MIN-MAX", "MIN-" or "-MAX" into inclusive bounds.
func parseRange[T int | float64](v any, parse func(string) (T, bool)) (lo, hi *T, err error) {
	s, ok := v.(string)
	if !ok {
		return nil, nil, errors.New("must be a string MIN-MAX, MIN- or -MAX")
	}
	los, his, found := strings.Cut(s, "-")
	if !found || (los == "" && his == "") {
		return nil, nil, fmt.Errorf("%q is not MIN-MAX, MIN- or -MAX", s)
	}
	for _, b := range []struct {
		text string
		dst  **T
	}{{los, &lo}, {his, &hi}} {
		if b.text == "" {
			continue
		}
		n, ok := parse(b.text)
		if !ok {
			return nil, nil, fmt.Errorf("%q in %q is not a positive number of the right kind", b.text, s)
		}
		*b.dst = &n
	}
	if lo != nil && hi != nil && *lo > *hi {
		return nil, nil, fmt.Errorf("%q has MIN greater than MAX", s)
	}
	return lo, hi, nil
}

func parseBPM(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

func parseSeconds(s string) (float64, bool) {
	n, err := strconv.ParseFloat(s, 64)
	return n, err == nil && n > 0 && !math.IsInf(n, 0)
}

// validLabel reports whether s is a canonical label: non-empty, with no
// leading or trailing Unicode whitespace and no upper-case or title-case
// letter. The fora-music-v1 schema's Label pattern states the same rule with
// the classes [\t-\r\u0085\p{Z}] (exactly unicode.IsSpace) and \p{Lu}\p{Lt}
// (unicode.IsUpper, unicode.IsTitle), so a label the schema accepts loads.
func validLabel(s string) bool {
	return s != "" && strings.TrimSpace(s) == s &&
		!strings.ContainsFunc(s, func(r rune) bool { return unicode.IsUpper(r) || unicode.IsTitle(r) })
}

func validEnergy(s string) bool {
	return s == "low" || s == "medium" || s == "high"
}
