package tagsearch

import (
	"cmp"
	"slices"
	"sort"
	"strings"
)

// Known returns the extraction with every tag outside the vocabulary dropped
// and duplicates removed, keeping the first occurrence order.
func (ix *Index) Known(e Extraction) Extraction {
	e.Include = ix.known(e.Include)
	e.Exclude = ix.known(e.Exclude)
	return e
}

func (ix *Index) known(tags []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range tags {
		if ix.vocab[t] && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

type scored struct {
	uri          string
	facets, tags int
}

// Match returns the URIs of the best-matching tracks for the extraction, best
// first. It applies Known first. An extraction with no known include or
// exclude tag matches nothing, even when it has ranges.
//
// Include tags are OR within a facet and scored across facets: a track needs
// at least one include-tag match, and only the tracks that match the most
// distinct facets are returned. So "calm music for a vlog" returns the tracks
// that are both calm and for vlogs when there are any, and otherwise the
// tracks that are one or the other. The returned tracks rank by distinct tags
// matched, then URI. Exclusions and BPM or duration bounds are hard filters
// that a missing field fails. With no include tags every passing track has the
// same score, so all of them are returned. There is no other cap.
func (ix *Index) Match(e Extraction) []string {
	e = ix.Known(e)
	if len(e.Include) == 0 && len(e.Exclude) == 0 {
		return nil
	}
	return ix.rank(e, nil, true)
}

// MatchFiltered returns the tracks that pass every filter, ranked by the
// extraction as Match ranks them. It applies Known to the extraction first.
// Filter bounds and extraction bounds are intersected, so bounds that do not
// overlap match nothing. Unlike Match, an extraction with no known tag does
// not empty the result: every track that passes the filters is returned. When
// some passing tracks match include tags, only those that match the most
// facets are returned.
func (ix *Index) MatchFiltered(e Extraction, f Filters) []string {
	e = ix.Known(e)
	e.MinBPM, e.MaxBPM = maxBound(e.MinBPM, f.MinBPM), minBound(e.MaxBPM, f.MaxBPM)
	e.MinDuration, e.MaxDuration = maxBound(e.MinDuration, f.MinDuration), minBound(e.MaxDuration, f.MaxDuration)
	return ix.rank(e, f.AnyOf, false)
}

// rank scores the tracks that pass the extraction's hard filters and every tag
// set of anyOf, and returns the ones that match the most facets, best first.
// With needInclude (Match) a track also needs at least one include-tag match.
func (ix *Index) rank(e Extraction, anyOf [][]string, needInclude bool) []string {
	var hits []scored
	for i := range ix.tracks {
		tr := &ix.tracks[i]
		if !tr.passesFilters(e) || !tr.hasAnyOfEach(anyOf) {
			continue
		}
		facets, tags := tr.score(e.Include)
		if needInclude && len(e.Include) > 0 && tags == 0 {
			continue
		}
		hits = append(hits, scored{uri: tr.URI, facets: facets, tags: tags})
	}
	sort.Slice(hits, func(a, b int) bool {
		x, y := hits[a], hits[b]
		if x.facets != y.facets {
			return x.facets > y.facets
		}
		if x.tags != y.tags {
			return x.tags > y.tags
		}
		return x.uri < y.uri
	})
	for i, h := range hits {
		if h.facets < hits[0].facets {
			hits = hits[:i]
			break
		}
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.uri
	}
	return out
}

// hasAnyOfEach reports whether the track has at least one tag of every set.
// An unknown facet matches no tag.
func (tr *Track) hasAnyOfEach(anyOf [][]string) bool {
	for _, set := range anyOf {
		if !slices.ContainsFunc(set, tr.has) {
			return false
		}
	}
	return true
}

func (tr *Track) has(tag string) bool {
	facet, value := split(tag)
	return tr.tags[facet][value]
}

// maxBound and minBound intersect two optional bounds: the tighter one wins,
// and a missing bound does not constrain.
func maxBound[T cmp.Ordered](a, b *T) *T {
	if a == nil || (b != nil && *b > *a) {
		return b
	}
	return a
}

func minBound[T cmp.Ordered](a, b *T) *T {
	if a == nil || (b != nil && *b < *a) {
		return b
	}
	return a
}

func (tr *Track) passesFilters(e Extraction) bool {
	for _, t := range e.Exclude {
		facet, value := split(t)
		values, known := tr.tags[facet]
		if !known || values[value] {
			return false
		}
	}
	return inBounds(tr.bpm, e.MinBPM, e.MaxBPM) && inBounds(tr.duration, e.MinDuration, e.MaxDuration)
}

// inBounds reports whether v is inside the inclusive bounds. With no bound
// set, any v passes; with a bound set, an unknown v fails.
func inBounds[T int | float64](v, lo, hi *T) bool {
	if lo == nil && hi == nil {
		return true
	}
	return v != nil && (lo == nil || *v >= *lo) && (hi == nil || *v <= *hi)
}

func (tr *Track) score(include []string) (facets, tags int) {
	matched := map[string]bool{}
	for _, t := range include {
		if tr.has(t) {
			tags++
			matched[facetOf(t)] = true
		}
	}
	return len(matched), tags
}

func facetOf(tag string) string {
	f, _ := split(tag)
	return f
}

func split(tag string) (facet, value string) {
	facet, value, _ = strings.Cut(tag, ":")
	return facet, value
}
