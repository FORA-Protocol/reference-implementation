package forawellknown

// ExchangeProfiles returns the extension profiles this repository's Exchange
// supports, in the order it advertises them. fora-comp-v1 renders the CoMP
// projection. fora-news-v1 and fora-music-v1 are pass-through: the Exchange
// forwards the publisher's ext keys for the profile unchanged inside the
// signed offer and does not validate them.
//
// The Exchange publishes this list in its own manifest. The console and the
// onboarding preview use it as the default for the supported_profiles they
// write into a publisher's authorized-exchange entry, so both describe the
// same Exchange. Each call returns a new slice.
func ExchangeProfiles() []string {
	return []string{"fora-news-v1", "fora-comp-v1", "fora-music-v1"}
}
