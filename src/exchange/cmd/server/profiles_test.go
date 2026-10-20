package main

import (
	"slices"
	"testing"
)

// The served manifest advertises every profile the Exchange supports. The
// music and news profiles are pass-through, so advertising them promises only
// that the publisher's ext object reaches the signed offer unchanged.
func TestManifestAdvertisesTheSupportedProfiles(t *testing.T) {
	t.Setenv("EXCHANGE_REGISTRATION_SCHEMA", "")
	t.Setenv("EXCHANGE_TERMS_URI", "")
	t.Setenv("EXCHANGE_TERMS_DIGEST", "")

	m, _ := servedManifest(t)

	got := m.GetSupportedProfiles()
	for _, want := range []string{"fora-news-v1", "fora-comp-v1", "fora-music-v1"} {
		if !slices.Contains(got, want) {
			t.Errorf("supported_profiles = %v, want it to contain %q", got, want)
		}
	}
}
