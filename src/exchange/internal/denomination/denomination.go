// Package denomination reads the deployment's denomination -- the currency and
// the metering unit every publisher's licensing is priced in -- from the
// environment. It is the one reading of EXCHANGE_TERMS_CURRENCY and
// EXCHANGE_TERMS_UNIT: the server's console surface previews terms in it and
// the catalog importer prices imported entries in it on every run, so the two
// programs must read it the same way or a publisher's pages carry a price the
// console never showed them.
package denomination

import (
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

// FromEnv reads the denomination. An unset currency is EUR; an unset unit is
// left empty, which termspreset reads as its own default.
func FromEnv() termspreset.Defaults {
	return termspreset.Defaults{
		Currency: runhttp.EnvOr("EXCHANGE_TERMS_CURRENCY", "EUR"),
		Unit:     runhttp.EnvOr("EXCHANGE_TERMS_UNIT", ""),
	}
}
