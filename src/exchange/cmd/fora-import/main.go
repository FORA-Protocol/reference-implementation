// Command fora-import imports and refreshes the catalog of every listing the
// seat has made import-eligible: ACTIVE, with a sitemap URL recorded at
// registration. For each listing it builds the license terms the publisher's
// licensing produces in the deployment's denomination, walks the sitemap
// within the configured limits, builds one entry per offered page carrying
// those terms, leaves out the pages another source pushed unless the listing
// lets the import take them over, and pushes the rest through the signed
// CatalogService.PushResources path as the operator's contributor identity,
// then records the outcome on the listing. Listings are processed one after
// another; one listing's failure does not stop the next.
//
// Runs never overlap. The run takes a session-level advisory lock on the
// Exchange database and holds it for the whole run; a second invocation that
// finds the lock held logs one line, writes no outcome and exits 0, so a
// scheduled run overlapping a manual one is not an error. A database failure
// while taking the lock IS an error and exits non-zero.
//
// The exit status is the pass/fail signal a timer or an operator reads: 0 when
// every eligible listing is complete, or when the run was skipped on lock
// contention; non-zero when any listing is failed, partial or unconfirmed, or
// when an outcome could not be recorded. Every listing outcome is logged as
// one structured line carrying the six counts and the reason, never the key
// or any signed request material.
//
// The run itself is importer.RunAll. This command reads its configuration
// from the environment, runs it, and turns the result into the exit status.
//
// Configuration is environment only, read once at start:
//
//	EXCHANGE_DSN               Exchange database (the same value the server uses)
//	EXCHANGE_DOMAIN            the seat's own hostname (the same value the server uses)
//	IMPORT_EXCHANGE_URL        where the Exchange is dialled, e.g. https://exchange.example
//	IMPORT_EXCHANGE_RECIPIENT  override of the recipient domain, only behind a port mapping
//	IMPORT_KEY_FILE            the contributor keypair JSON; its kid is the contributor hostname
//	IMPORT_REQUEST_TIMEOUT     one sitemap fetch (default 10s)
//	IMPORT_MAX_DOC_BYTES       one sitemap document (default 1048576)
//	IMPORT_MAX_DOCUMENTS       sitemap documents per listing (default 100)
//	IMPORT_MAX_RESOURCES       unique pages per listing (default 10000)
//	IMPORT_LISTING_TIMEOUT     walk plus submission for one listing (default 5m)
//	EXCHANGE_TERMS_CURRENCY    the currency terms are priced in (the same value the server uses)
//	EXCHANGE_TERMS_UNIT        the metering unit of a per-unit term (the same value the server uses)
//	EXCHANGE_LOG_LEVEL         as for the server
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/logging"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/denomination"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/importer"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk"
)

func configFromEnv() importer.Config {
	docBytes, _ := runhttp.EnvInt64("IMPORT_MAX_DOC_BYTES", sitemapwalk.DefaultMaxDocBytes, 1<<30)
	documents, _ := runhttp.EnvInt64("IMPORT_MAX_DOCUMENTS", sitemapwalk.DefaultMaxDocuments, 1_000_000)
	resources, _ := runhttp.EnvInt64("IMPORT_MAX_RESOURCES", sitemapwalk.DefaultMaxResources, 10_000_000)
	return importer.Config{
		DSN:               runhttp.EnvOr("EXCHANGE_DSN", ""),
		ExchangeDomain:    runhttp.EnvTrimmed("EXCHANGE_DOMAIN"),
		ExchangeURL:       runhttp.EnvTrimmed("IMPORT_EXCHANGE_URL"),
		ExchangeRecipient: runhttp.EnvTrimmed("IMPORT_EXCHANGE_RECIPIENT"),
		KeyPath:           runhttp.EnvTrimmed("IMPORT_KEY_FILE"),
		RequestTimeout:    runhttp.EnvDuration("IMPORT_REQUEST_TIMEOUT", 10*time.Second),
		ListingTimeout:    runhttp.EnvDuration("IMPORT_LISTING_TIMEOUT", 5*time.Minute),
		Limits: sitemapwalk.Limits{
			MaxDocBytes:  docBytes,
			MaxDocuments: int(documents),
			MaxResources: int(resources),
		},
		TermsDefaults: denomination.FromEnv(),
	}
}

func main() {
	const levelVar = "EXCHANGE_LOG_LEVEL"
	logger, err := logging.Setup(os.Stdout, levelVar, runhttp.EnvTrimmed(levelVar))
	// The run's one request id is minted before the run, so the exit line
	// below carries the same request_id as every line the run writes:
	// importer.RunAll keeps an id its context already carries.
	base := context.Background()
	ctx := reqctx.WithID(base, reqctx.IDOrNew(base))
	if err == nil {
		signalled, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		err = importer.RunAll(signalled, configFromEnv(), logger)
		stop()
	}
	if err != nil {
		logger.Error("importer.exit", "request_id", reqctx.IDFromContext(ctx), "err", err)
		os.Exit(1)
	}
}
