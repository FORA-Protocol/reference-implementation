package setup

import (
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
)

// TestNoReadmeForANetworkTheStepsHandNoPackage: the onboarding steps and the
// switch in EdgePackages.For both say which networks get a package. The switch
// never reaches the README for any other network, so the refusal is held here,
// against the renderer itself: if the two ever disagree, a README with a
// contact-us step and no commands is never written into a zip.
func TestNoReadmeForANetworkTheStepsHandNoPackage(t *testing.T) {
	t.Parallel()
	for _, network := range []cdn.Provider{cdn.ProviderFastly, cdn.ProviderAkamai, cdn.ProviderNone, "someothercdn"} {
		if _, err := renderReadme(readmeInput{network: network, domain: "publisher.example"}); err == nil {
			t.Errorf("%s: a README was written for a network the steps hand no package", network)
		}
	}
	for _, network := range cdn.SelfServe() {
		if _, err := renderReadme(readmeInput{network: network, domain: "publisher.example"}); err != nil {
			t.Errorf("%s: no README for a network that gets a package: %v", network, err)
		}
	}
}
