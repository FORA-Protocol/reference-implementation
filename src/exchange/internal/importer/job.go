package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk"
)

// LockKey is the advisory lock every run of the import job takes on the
// Exchange database. One fixed key: there is one importer per deployment and
// its runs must never overlap.
const LockKey int64 = 0x464F5241494D5054 // "FORAIMPT"

// Config is everything one run of the import job reads. The fora-import
// command reads it from the environment once, at start; each field names the
// variable it comes from.
type Config struct {
	// DSN is the Exchange database (EXCHANGE_DSN), the value the server uses.
	DSN string
	// ExchangeDomain is the seat's own hostname (EXCHANGE_DOMAIN), the value
	// the server uses.
	ExchangeDomain string
	// ExchangeURL is where the Exchange is dialled (IMPORT_EXCHANGE_URL).
	ExchangeURL string
	// ExchangeRecipient overrides the recipient domain, only behind a port
	// mapping (IMPORT_EXCHANGE_RECIPIENT).
	ExchangeRecipient string
	// KeyPath is the contributor keypair JSON (IMPORT_KEY_FILE); its kid is the
	// contributor hostname.
	KeyPath string
	// RequestTimeout bounds one sitemap fetch (IMPORT_REQUEST_TIMEOUT).
	RequestTimeout time.Duration
	// ListingTimeout bounds the walk plus submission for one listing
	// (IMPORT_LISTING_TIMEOUT).
	ListingTimeout time.Duration
	// Limits bound one listing's walk (IMPORT_MAX_DOC_BYTES,
	// IMPORT_MAX_DOCUMENTS, IMPORT_MAX_RESOURCES).
	Limits sitemapwalk.Limits
	// TermsDefaults is the denomination terms are priced in
	// (EXCHANGE_TERMS_CURRENCY, EXCHANGE_TERMS_UNIT). It must match the
	// server's, which previews terms to publishers in it, so both read it
	// through denomination.FromEnv.
	TermsDefaults termspreset.Defaults
}

// Validate names the first required setting that is missing, by the variable
// an operator sets it with.
func (c Config) Validate() error {
	switch {
	case c.DSN == "":
		return errors.New("EXCHANGE_DSN is required")
	case c.ExchangeDomain == "":
		return errors.New("EXCHANGE_DOMAIN is required")
	case c.ExchangeURL == "":
		return errors.New("IMPORT_EXCHANGE_URL is required")
	case c.KeyPath == "":
		return errors.New("IMPORT_KEY_FILE is required")
	}
	return nil
}

// ErrIncomplete is the run's failure when it processed every listing and at
// least one did not end complete, or an outcome could not be recorded.
var ErrIncomplete = errors.New("importer: not every listing completed")

// RunAll is one run of the import job over every eligible listing of the seat,
// and the job's composition root: what the fora-import command runs.
//
// It checks the configuration before anything connects, reads the contributor
// key, builds the labelled catalog client, and then takes the job's
// session-level advisory lock, which it holds until it returns. A run that
// finds the lock held logs one line, writes no outcome and returns nil. It then
// opens the Exchange database, reads the seat's eligible listings and imports
// them one after another through RunAndRecord; one listing's failure does not
// stop the next. The result is nil when every listing completed, ErrIncomplete
// when any did not or its outcome could not be recorded, and any other error
// for a configuration, key or database failure.
func RunAll(ctx context.Context, cfg Config, logger *slog.Logger) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	// One request id per run: on every log line below and, through the
	// client, on every push's X-Request-ID, so the Exchange's lines for a
	// scheduled push and this run's lines can be read as one story. An id the
	// context already carries is kept. The same client labels every push as
	// the automatic import's.
	ctx = reqctx.WithID(ctx, reqctx.IDOrNew(ctx))
	logger = logger.With("request_id", reqctx.IDFromContext(ctx))
	kid, priv, err := ingest.LoadContributorKey(cfg.KeyPath)
	if err != nil {
		return err
	}
	signatureAgent, err := ingest.SignatureAgentFor(kid)
	if err != nil {
		return fmt.Errorf("IMPORT_KEY_FILE: %w", err)
	}
	client, err := NewCatalogClient(cfg.ExchangeURL, signatureAgent, priv,
		func() string { return reqctx.IDOrNew(ctx) })
	if err != nil {
		return err
	}
	recipient, err := foraaudience.RecipientOf(cfg.ExchangeURL, cfg.ExchangeRecipient)
	if err != nil {
		return fmt.Errorf("importer: %w", err)
	}

	release, held, err := db.TryAdvisoryLock(ctx, cfg.DSN, LockKey)
	if err != nil {
		return fmt.Errorf("importer: %w", err)
	}
	if !held {
		logger.Info("importer.skipped", "reason", "another import run holds the lock; no listing outcome was written")
		return nil
	}
	defer release()

	pool, err := db.Open(ctx, db.Config{DSN: cfg.DSN}, logger)
	if err != nil {
		return fmt.Errorf("importer: open database: %w", err)
	}
	defer pool.Close()
	queries := sqlc.New(pool)
	seat, err := repo.NewTenantReadRepo(queries).ByDomain(ctx, cfg.ExchangeDomain)
	if err != nil {
		return fmt.Errorf("importer: seat %q: %w", cfg.ExchangeDomain, err)
	}
	imports := repo.NewListingImportRepo(queries)
	listings, err := imports.Eligible(ctx, seat.ID)
	if err != nil {
		return fmt.Errorf("importer: %w", err)
	}
	deps := Deps{
		Client:         client,
		Pages:          repo.NewSitePagesRepo(queries),
		Fetch:          sitemapwalk.NewClient(cfg.RequestTimeout),
		Exchange:       recipient,
		CallerID:       kid,
		TermsDefaults:  cfg.TermsDefaults,
		Limits:         cfg.Limits,
		ListingTimeout: cfg.ListingTimeout,
	}
	denomination := cfg.TermsDefaults.Resolved()
	logger.Info("importer.start", "seat", seat.ID, "contributor", kid, "listings", len(listings),
		"currency", denomination.Currency, "unit", denomination.Unit)

	incomplete := importListings(ctx, deps, imports, listings, logger)
	logger.Info("importer.done", "listings", len(listings), "incomplete", incomplete)
	if incomplete > 0 {
		return fmt.Errorf("%w: %d of %d", ErrIncomplete, incomplete, len(listings))
	}
	return nil
}

// RunAndRecord imports one listing with Run and records the outcome on the
// listing's own row: the step every run of the job takes for each listing. The
// outcome is returned with the error when it could not be recorded, so the
// caller can still report what the run did.
func RunAndRecord(
	ctx context.Context, deps Deps, imports repo.ListingImportRepo, listing repo.ImportListing,
) (repo.ImportOutcome, error) {
	out := Run(ctx, deps, listing)
	n, err := imports.RecordOutcome(ctx, listing.TenantID, listing.Domain, out)
	if err != nil {
		return out, fmt.Errorf("importer: record the outcome of %s: %w", listing.Domain, err)
	}
	if n != 1 {
		return out, fmt.Errorf("importer: record the outcome of %s: %d rows written, want 1", listing.Domain, n)
	}
	return out, nil
}

// importListings runs every listing in turn, records each outcome and logs
// it, and returns how many listings did not complete: a failed, partial or
// unconfirmed run, or an outcome that could not be recorded.
func importListings(
	ctx context.Context, deps Deps, imports repo.ListingImportRepo,
	listings []repo.ImportListing, logger *slog.Logger,
) int {
	incomplete := 0
	for _, listing := range listings {
		out, err := RunAndRecord(ctx, deps, imports, listing)
		attrs := []any{
			"domain", listing.Domain, "publisher", listing.PublisherID, "outcome", out.Outcome,
			"entries", out.Entries, "accepted", out.Accepted, "refused", out.Refused,
			"unconfirmed", out.Unconfirmed, "unsent", out.Unsent, "skipped", out.Skipped,
			"reason", out.FailureReason,
		}
		if err != nil {
			incomplete++
			logger.Error("importer.listing.unrecorded", append(attrs, "err", err)...)
			continue
		}
		// Anything but complete fails the run. A partial or unconfirmed
		// listing is not "mostly done" for a timer that reports pass or fail;
		// its row says what happened, and the exit status is what gets an
		// operator to look at it.
		if out.Outcome != repo.ImportOutcomeComplete {
			incomplete++
			logger.Warn("importer.listing", attrs...)
			continue
		}
		logger.Info("importer.listing", attrs...)
	}
	return incomplete
}
