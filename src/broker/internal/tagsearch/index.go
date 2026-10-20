// Package tagsearch matches a structured tag extraction against an index of
// catalog resources built from the fora-music-v1 metadata of a catalog feed:
// the flat `music.<field>` keys of each record's `ext`.
//
// A tag is "facet:value", for example "mood:calm". The vocabulary is every tag
// present in the loaded feed except ISRC tags, which only search filters match,
// plus the fixed tempo and vocals tags, so the index needs no separate
// vocabulary file. A missing metadata field is unknown, never
// a default: it contributes no include match and fails any exclusion or range
// that constrains it.
package tagsearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/FORA-Protocol/protocol/sdk/go/profiles/music"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/catalogfeed"
)

// Facet names. The `use` facet is read from the `suitable_for` field.
const (
	facetGenre      = "genre"
	facetMood       = "mood"
	facetInstrument = "instrument"
	facetEnergy     = "energy"
	facetVocals     = "vocals"
	facetUse        = "use"
	facetTempo      = "tempo"
	// facetISRC holds the track's ISRC. Only search filters match it: its tags
	// stay out of the vocabulary, so the query model never sees or produces them.
	facetISRC = "isrc"
)

// isrcPattern is the fora-music-v1 ISRC format, the same pattern as the
// schema: 12 characters, no hyphens, upper case.
var isrcPattern = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`)

// Tempo bucket bounds in BPM: slow < 90, medium 90–119, fast >= 120.
const (
	tempoMediumFrom = 90
	tempoFastFrom   = 120
)

// fixedTags are always in the vocabulary, whatever the feed contains, so a
// negative request ("no vocals") stays expressible in an all-instrumental catalog.
var fixedTags = []string{
	"tempo:slow", "tempo:medium", "tempo:fast",
	"vocals:true", "vocals:false",
}

// Track is one indexed resource.
type Track struct {
	URI string
	// tags maps a facet to its values. A facet absent from the map is unknown.
	tags     map[string]map[string]bool
	bpm      *int
	duration *float64
}

// Index is the loaded catalog. It is read-only after Load and safe for
// concurrent use.
type Index struct {
	tracks []Track
	vocab  map[string]bool
}

// musicPrefix starts every fora-music-v1 key in `ext`.
const musicPrefix = "music."

// LoadFile loads the index from a JSONL catalog feed file. scheme is the URI
// scheme of the indexed resources, as in Load.
func LoadFile(path, scheme string) (*Index, error) {
	f, err := os.Open(path) //nolint:gosec // operator-configured path
	if err != nil {
		return nil, fmt.Errorf("tagsearch: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	ix, err := Load(f, scheme)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ix, nil
}

// Load builds the index from a JSONL catalog feed. A resource URI is
// scheme://domain/path, which must be the URI the Exchange catalog stores for
// the same record. Records without any `music.<field>` key are skipped. A malformed feed,
// an invalid matching field, or a feed with no indexed track is an error
// naming the record and field.
func Load(r io.Reader, scheme string) (*Index, error) {
	records, err := catalogfeed.ParseJSONL(r)
	if err != nil {
		return nil, fmt.Errorf("tagsearch: %w", err)
	}
	ix := &Index{vocab: map[string]bool{}}
	for _, rec := range records {
		uri := scheme + "://" + rec.Domain + rec.Path
		meta, ok, err := musicOf(rec.Ext)
		if err != nil {
			return nil, fmt.Errorf("tagsearch: record %s: %w", uri, err)
		}
		if !ok {
			continue
		}
		tr, err := trackFrom(uri, meta)
		if err != nil {
			return nil, fmt.Errorf("tagsearch: record %s: %w", uri, err)
		}
		for facet, values := range tr.tags {
			if facet == facetISRC {
				continue
			}
			for v := range values {
				ix.vocab[facet+":"+v] = true
			}
		}
		ix.tracks = append(ix.tracks, tr)
	}
	if len(ix.tracks) == 0 || len(ix.vocab) == 0 {
		return nil, errors.New("tagsearch: feed has no record with music.* matching metadata")
	}
	for _, t := range fixedTags {
		ix.vocab[t] = true
	}
	return ix, nil
}

// Vocabulary returns every known tag, sorted.
func (ix *Index) Vocabulary() []string {
	out := make([]string, 0, len(ix.vocab))
	for t := range ix.vocab {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Len returns the number of indexed tracks.
func (ix *Index) Len() int { return len(ix.tracks) }

func musicOf(ext json.RawMessage) (music.Music, bool, error) {
	var meta music.Music
	if len(ext) == 0 {
		return meta, false, nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(ext, &keys); err != nil {
		return meta, false, fmt.Errorf("ext: %w", err)
	}
	for k := range keys {
		if strings.HasPrefix(k, musicPrefix) {
			if err := json.Unmarshal(ext, &meta); err != nil {
				return meta, false, fmt.Errorf("ext music.*: %w", err)
			}
			return meta, true, nil
		}
	}
	return meta, false, nil
}

func trackFrom(uri string, m music.Music) (Track, error) {
	tr := Track{URI: uri, tags: map[string]map[string]bool{}}
	if m.ISRC != nil {
		if !isrcPattern.MatchString(*m.ISRC) {
			return tr, fmt.Errorf("ext music.isrc: %q is not a 12-character upper-case ISRC without hyphens", *m.ISRC)
		}
		tr.tags[facetISRC] = map[string]bool{*m.ISRC: true}
	}
	if m.Genre != nil {
		if err := tr.add(facetGenre, "genre", []string{*m.Genre}); err != nil {
			return tr, err
		}
	}
	for _, f := range []struct {
		facet, field string
		values       []string
	}{
		{facetMood, "mood", m.Mood},
		{facetInstrument, "instruments", m.Instruments},
		{facetUse, "suitable_for", m.SuitableFor},
	} {
		if f.values != nil {
			if err := tr.add(f.facet, f.field, f.values); err != nil {
				return tr, err
			}
		}
	}
	err := tr.addScalars(m)
	return tr, err
}

// addScalars records energy, vocals, bpm (with its tempo) and duration.
func (tr *Track) addScalars(m music.Music) error {
	if m.Energy != nil {
		if e := *m.Energy; !validEnergy(e) {
			return fmt.Errorf("ext music.energy: %q is not low, medium or high", e)
		}
		tr.tags[facetEnergy] = map[string]bool{*m.Energy: true}
	}
	if m.Vocals != nil {
		tr.tags[facetVocals] = map[string]bool{fmt.Sprint(*m.Vocals): true}
	}
	if m.BPM != nil {
		bpm := *m.BPM
		if bpm <= 0 {
			return fmt.Errorf("ext music.bpm: %d is not a positive integer", bpm)
		}
		tr.bpm = &bpm
		tr.tags[facetTempo] = map[string]bool{tempoOf(bpm): true}
	}
	if d := m.DurationSeconds; d != nil {
		if *d <= 0 {
			return fmt.Errorf("ext music.duration_seconds: %v is not a positive number", *d)
		}
		tr.duration = d
	}
	return nil
}

// add records the values of one facet. An empty list is a known empty facet.
func (tr *Track) add(facet, field string, values []string) error {
	set := map[string]bool{}
	for _, v := range values {
		if !validLabel(v) {
			return fmt.Errorf("ext music.%s: %q must be non-empty, trimmed, with no upper- or title-case letter", field, v)
		}
		set[v] = true
	}
	tr.tags[facet] = set
	return nil
}

func tempoOf(bpm int) string {
	switch {
	case bpm >= tempoFastFrom:
		return "fast"
	case bpm >= tempoMediumFrom:
		return "medium"
	default:
		return "slow"
	}
}
