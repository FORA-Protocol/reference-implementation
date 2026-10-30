//go:build !e2e

package main

import (
	"fmt"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
)

// billingIDNamespace in a production build: ids are always unsalted. A set
// EXCHANGE_BILLING_TB_ID_NAMESPACE is refused, not ignored. An operator who sets
// it expects salted ids, and a production Exchange that silently derived
// unsalted ones would share its platform and revenue accounts with any other
// Exchange on the same cluster.
func billingIDNamespace() (string, error) {
	if runhttp.EnvOr(billingIDNamespaceEnv, "") != "" {
		return "", fmt.Errorf("billing: %s is read only by e2e builds; a production Exchange "+
			"derives unsalted ledger ids, so unset it", billingIDNamespaceEnv)
	}
	return "", nil
}
