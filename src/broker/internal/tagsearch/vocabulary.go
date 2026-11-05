package tagsearch

import (
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
)

// MusicVocabulary is the fora-music-v1 filter vocabulary on its own, for a
// Broker with no catalog search: a malformed value is still the caller's fault
// there, and is refused as such before the filters themselves are refused for
// want of a search to apply them.
type MusicVocabulary struct{}

// Parse reads the fora-music-v1 keys with ParseFilters: keys the profile does
// not define are ignored, and a malformed defined key is a
// *catalogsearch.FilterError.
func (MusicVocabulary) Parse(raw map[string]any) (catalogsearch.Filters, error) {
	f, err := ParseFilters(raw)
	if err != nil {
		return nil, err
	}
	return f, nil
}
