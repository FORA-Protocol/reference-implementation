//go:build e2e

package main

import (
	"fmt"
	"regexp"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
)

// idNamespaceLabel is what the setting holds: lowercase letters, digits and
// hyphens, at most 63 characters, starting with a letter or a digit. The
// namespace is the label followed by a colon. A label cannot contain a colon,
// so no namespace is a prefix of another, and two Exchanges with different
// labels can never derive the same id.
var idNamespaceLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// billingIDNamespace in an e2e build: the namespace this Exchange salts its
// TigerBeetle ids with. The e2e stack bills every Exchange on one cluster, so
// the setting is required: an Exchange without it would derive the same
// platform and revenue accounts as its neighbours, and no balance test would
// notice, because the balance suites read only the buyer's own account.
func billingIDNamespace() (string, error) {
	label := runhttp.EnvOr(billingIDNamespaceEnv, "")
	if label == "" {
		return "", fmt.Errorf("billing: %s is required in an e2e build: the e2e Exchanges "+
			"share one TigerBeetle cluster, and each needs its own namespace", billingIDNamespaceEnv)
	}
	if !idNamespaceLabel.MatchString(label) {
		return "", fmt.Errorf("billing: invalid %s %q: want 1 to 63 lowercase letters, digits "+
			"and hyphens, starting with a letter or a digit", billingIDNamespaceEnv, label)
	}
	return label + ":", nil
}
