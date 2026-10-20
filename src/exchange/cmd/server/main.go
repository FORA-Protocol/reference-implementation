// Package main is the Exchange service entrypoint.
//
// The Exchange implements fora.v1.ExchangeService and fora.v1.CatalogService:
// catalog lookup, Ed25519 offer signing, transaction execution with a
// write-before-sign invariant, signed URL minting (Ed25519 or RSA CloudFront
// per tenant), and usage reporting.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/logging"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/agentreg"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	exchangedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sor"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/wellknown"
)

func main() {
	// `exchange healthcheck` probes the local /healthz and exits — the
	// distroless image has no shell or curl, so the compose healthcheck
	// execs the service binary itself.
	runhttp.MaybeHealthcheck("EXCHANGE_ADDR", ":8081")
	// An EXCHANGE_LOG_LEVEL that is not a level stops the boot here, before
	// anything connects, through the same exchange.exit record as every other
	// start-up failure.
	const levelVar = "EXCHANGE_LOG_LEVEL"
	logger, err := logging.Setup(os.Stdout, levelVar, runhttp.EnvTrimmed(levelVar))
	if err == nil {
		// Tie the lifecycle context to the process shutdown signals so background
		// goroutines started on it (the per-agent WBA revocation poller) stop on
		// shutdown instead of leaking. runhttp.ServeGroup installs its own signal
		// handler for HTTP drain; both registrations receive the signal. stop() is
		// called explicitly (not deferred) so it runs before a non-zero os.Exit.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = run(ctx, logger)
		stop()
	}
	if err != nil {
		logger.Error("exchange.exit", "err", err)
		os.Exit(1)
	}
}

// setupExchangeDB opens the Exchange's database and applies its migrations.
// EXCHANGE_DSN is required: an Exchange cannot run without its catalog and
// transaction store.
func setupExchangeDB(ctx context.Context, logger *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := db.Setup(ctx, db.SetupOptions{
		DSN:             runhttp.EnvOr("EXCHANGE_DSN", ""),
		Migrations:      exchangedb.Migrations,
		MigrationsDir:   exchangedb.MigrationsDir,
		MigrationsTable: exchangedb.MigrationsTable,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("db setup: %w", err)
	}
	if pool == nil {
		return nil, errors.New("EXCHANGE_DSN is required")
	}
	return pool, nil
}

// run is main() minus the os.Exit call; splitting it this way lets defers fire
// cleanly regardless of which stage fails.
func run(ctx context.Context, logger *slog.Logger) error {
	// First, before anything connects. It reads only the environment, and a
	// schema this Exchange could not enforce stops the boot, so resolving it
	// here spends a typo's cost on one parse rather than on a database
	// connection, a migration run, a catalog bootstrap and a ledger
	// health-check per restart of the crash loop it causes. The single value
	// stays in scope for both construction sites below, so wiring the Register
	// gate later never needs a second read.
	registration, err := loadRegistrationConfig(logger)
	if err != nil {
		return err
	}
	pool, err := setupExchangeDB(ctx, logger)
	if err != nil {
		return err
	}
	defer pool.Close()

	keys, err := setupDemoKeys(logger)
	if err != nil {
		return err
	}
	offerSigner, keystore := keys.offerSigner, keys.keystore

	// One SSRF-guarded HTTP client backs every outbound .well-known fetch:
	// agent registration, catalog contributor-authz, and the broker revocation
	// poll. The guard is SDK-owned; construct it once here at the composition root
	// and inject it into every fetch site. It refuses loopback/link-local/private
	// destinations unless SKIP_SSRF is set, and refuses plaintext http unless
	// ALLOW_INSECURE is set (compose/dev only).
	fetchClient := resolvers.NewGuardedClientFromEnv()

	queries := sqlc.New(pool)
	agentRegistry := buildAgentRegistry(queries, fetchClient)
	catalogSvc, err := buildCatalog(ctx, pool, queries, agentRegistry, fetchClient)
	if err != nil {
		return err
	}
	billingAdapter, ledgerCurrencies, sorAdapter, adaptersCleanup, err := selectAdapters(ctx, logger)
	if err != nil {
		return err
	}
	defer adaptersCleanup()
	// The tenant boot step runs AFTER the billing adapter is selected, because
	// the welcome-credit configuration is validated against the currencies that
	// adapter bills in: a credit in a currency the deployment does not carry
	// fails boot here rather than failing the first Register.
	defaultAgentCredits, err := bootTenantConfig(ctx, logger, queries, ledgerCurrencies)
	if err != nil {
		return err
	}
	// The reporting window agents are held to when a seat sets none: the
	// Exchange mints obligations with it and the console's reads show it.
	reportWindow := service.DefaultReportWindow
	// Empty in a production build; the e2e build's control service otherwise.
	controls := e2eControls(logger, queries)
	exchangeSvc, err := buildExchange(buildSvcDeps{
		pool: pool, queries: queries, catalog: catalogSvc,
		offerSigner:         offerSigner,
		keystore:            keystore,
		billing:             billingAdapter,
		ledgerCurrencies:    ledgerCurrencies,
		defaultAgentCredits: defaultAgentCredits,
		reportWindow:        reportWindow,
		agentRegistry:       agentRegistry,
		sor:                 sorAdapter,
		registration:        registration,
		lifetimes:           controls.lifetimes,
	})
	if err != nil {
		return err
	}
	admission, err := buildAdmissionDeps(ctx, logger, fetchClient)
	if err != nil {
		return err
	}
	console, err := buildConsoleDeps(ctx, logger, consoleWiring{
		pool: pool, queries: queries, fetchClient: fetchClient,
		manifests: newManifestCache(fetchClient), replay: admission.replayStore,
		billing: billingAdapter, ledgerCurrencies: ledgerCurrencies, reportWindow: reportWindow,
	})
	if err != nil {
		return err
	}
	wrapped, wk, err := buildSurface(logger, newMuxDeps(muxInputs{
		pool: pool, exchange: exchangeSvc, catalog: catalogSvc,
		agentRegistry: agentRegistry, offerSigner: offerSigner,
		billing: billingAdapter, ledgerCurrencies: ledgerCurrencies,
		admission: admission, registration: registration, console: console,
	}))
	if err != nil {
		return err
	}
	// The served discovery documents embed validity windows read from the
	// clock at build time, so a long-lived process must rebuild them
	// periodically or it eventually serves only lapsed windows.
	go wk.RunRefresher(ctx, wellknown.RebuildInterval, logger)

	return serveExchangeAndAdmin(ctx, logger, pool, queries, wrapped, admission.audience, controls.adminRoutes)
}

// buildCatalog wires the catalog service and brings its discovery trie up from
// the database, so run() carries one call rather than the whole sequence.
func buildCatalog(
	ctx context.Context,
	pool *pgxpool.Pool,
	queries *sqlc.Queries,
	agentRegistry agentreg.Registry,
	fetchClient forawellknown.HTTPDoer,
) (*service.CatalogService, error) {
	// EXCHANGE_CATALOG_URI_SCHEME defaults to https; compose overrides to http
	// so catalog URIs route through the in-network edge worker.
	service.SetCatalogURIScheme(runhttp.EnvOr("EXCHANGE_CATALOG_URI_SCHEME", ""))
	svc := service.NewCatalogService(service.CatalogDeps{
		Catalog:        repo.NewCatalogRepo(queries),
		Tenants:        repo.NewTenantReadRepo(queries),
		Sites:          repo.NewPublisherDomainReadRepo(queries),
		Listings:       repo.NewPublisherListingReadRepo(queries),
		Registry:       agentRegistry,
		Manifests:      newManifestCache(fetchClient),
		TxRunner:       db.PoolRunner{Pool: pool},
		ExchangeDomain: exchangeDomain(),
	})
	// Pre-render CoMP for the advertised profiles at rebuild. Same
	// single source as the manifest + ExchangeConfig; set BEFORE Bootstrap so the
	// first rebuild renders.
	svc.SetSupportedProfiles(exchangeSupportedProfiles())
	if err := svc.Bootstrap(ctx); err != nil {
		return nil, err
	}
	return svc, nil
}

// buildSvcDeps groups the inputs buildExchange needs so the
// run() call site stays under the funlen cap.
type buildSvcDeps struct {
	pool        *pgxpool.Pool
	queries     *sqlc.Queries
	catalog     *service.CatalogService
	offerSigner *signing.Ed25519Signer
	keystore    *signing.InMemoryKeyStore
	billing     billing.Adapter
	// ledgerCurrencies are the selected billing backend's ledger currencies in
	// configuration order, threaded into ExchangeConfig.LedgerCurrencies so the
	// welcome-credit grants and the adapter agree on what may be granted.
	ledgerCurrencies []string
	// defaultAgentCredits is the welcome credit parsed once at boot from
	// EXCHANGE_DEFAULT_AGENT_CREDIT, threaded into
	// ExchangeConfig.DefaultAgentCredits, where Register reads it.
	defaultAgentCredits map[string]*big.Rat
	// reportWindow is the reporting window an obligation gets when the seat's
	// policy sets none. The console's publisher reads show agents that duty,
	// so run passes one value to both.
	reportWindow  time.Duration
	agentRegistry agentreg.Registry
	sor           sor.Adapter
	// registration is the operator's registration settings, read once by
	// loadRegistrationConfig. buildExchange threads its terms digest into
	// ExchangeConfig so the Register gate holds callers to exactly the digest the
	// manifest publishes, rather than reading the environment a second time.
	registration registrationConfig
	// lifetimes shortens a tenant's offer and delivery-URL lifetimes. nil in a
	// production build; the e2e control service in an e2e build.
	lifetimes service.Lifetimes
}

// buildAgentRegistry wires Gate-1 self-signup, which reads a caller's own Web
// Bot Auth key directory to learn its signing key. The read is the SDK's
// directory fetcher: always over https from the identity's own authority, with
// no redirect and a signed response, so FORA_WELLKNOWN_{SCHEME,PORT} — which
// still shape the Gate-2 publisher-manifest fetch (newManifestCache) — do not
// reach it. The two gates read different documents on the same host: keys come
// from the directory, and the publisher's commercial overlay carries none.
func buildAgentRegistry(queries *sqlc.Queries, fetchClient *http.Client) agentreg.Registry {
	return agentreg.New(agentreg.Config{
		Repo: repo.NewAgentRepo(queries),
		HTTP: fetchClient,
	})
}

// buildExchange wires the ExchangeService that serves the canonical
// DiscoverResources / ExecuteTransaction / ReportUsage RPCs. The deprecated
// OffersService (ListOffers / AcceptOffer / Report) was removed in
// W3 of the proto-rename wave.
//
// It reads the Broker's directory host here, beside the Exchange's own domain and
// the default tenant, because all three are identities this composition root
// resolves once from the environment. The read fails the boot when
// EXCHANGE_BROKER_WELLKNOWN_URL is unset or names no host — the same refusal the
// transport's revocation resolver makes, arriving earlier.
func buildExchange(d buildSvcDeps) (*service.ExchangeService, error) {
	brokerHost, err := brokerDirectoryHost()
	if err != nil {
		return nil, err
	}
	return service.NewExchangeService(service.ExchangeDeps{
		TxRunner:      db.PoolRunner{Pool: d.pool},
		Catalog:       d.catalog,
		Tenants:       repo.NewTenantReadRepo(d.queries),
		Sites:         repo.NewPublisherDomainReadRepo(d.queries),
		Agents:        repo.NewAgentRepo(d.queries),
		AgentReg:      d.agentRegistry,
		Transactions:  repo.NewTransactionRepo(d.queries),
		Obligations:   repo.NewObligationRepo(d.queries),
		Evidence:      repo.NewEvidenceRepo(d.queries),
		Events:        repo.NewTransactionEventRepo(d.queries),
		FeeOverrides:  repo.NewFeeOverrideRepo(d.queries),
		Billing:       d.billing,
		OfferSigner:   d.offerSigner,
		KeyStore:      d.keystore,
		SoR:           d.sor,
		RegSchema:     d.registration.schema,
		Audit:         repo.NewAuditRepo(d.queries),
		BillingRefGen: uuid.NewString,
		Clk:           clock.System{},
		Lifetimes:     d.lifetimes,
		Config: service.ExchangeConfig{
			Exchange:          exchangeDomain(),
			SupportedProfiles: exchangeSupportedProfiles(),
			// The single default tenant a Register reads its
			// activate_new_agents_by_default policy from (ADR-021 §5 decision 1).
			DefaultTenantDomain: defaultTenantDomain(),
			LedgerCurrencies:    d.ledgerCurrencies,
			DefaultAgentCredits: d.defaultAgentCredits,
			ReportWindow:        d.reportWindow,
			TermsDigest:         d.registration.termsDigest,
			BrokerHost:          brokerHost,
		},
	}), nil
}

// exchangeDomain is this Exchange's published IDENTITY: the domain it stamps
// into the offers it issues, serves its manifest under, and answers to as the
// recipient of an addressed request.
//
// One function rather than the same EnvOr call repeated at each reader. The four
// readers — the catalog service, the offer-issuing config, the served manifest,
// and the recipient check — must agree on one value: an Exchange that issues
// offers naming one domain while refusing requests that name it is broken in a
// way each site looks correct on its own.
//
// It is NOT the host the process listens on. An Exchange at exchange.example may
// serve its API from api.exchange.example, and a deployment that put the
// listening host here would refuse every request that addressed it correctly.
func exchangeDomain() string {
	return runhttp.EnvOr("EXCHANGE_DOMAIN", "exchange.fora.local")
}

// buildWrapped assembles the Exchange's outermost HTTP middleware: the shared
// public-surface stack around CatalogSignatureMiddleware → mux (see
// transport.WrapPublicSurface), with the deployment-shape switches read from
// the environment here at the composition root (see
// runhttp.PublicSurfaceOptionsFromEnv for the fail-closed opt-in rationale).
// ExchangeService and CatalogService RPC verification is delegated to
// connectserver handlers (registered in registerConnect), which each carry
// their own request-id + RFC 9421 verify layers.
func buildWrapped(logger *slog.Logger, mux http.Handler, console consoleDeps) http.Handler {
	inner := mux
	if console.enabled() {
		// Inside WrapPublicSurface, so a refused console request still carries
		// its request id; outside the mux, so the signature is checked before
		// routing decides which registration handler would have run.
		inner = transport.ConsoleSignatureMiddleware(console.resolver, console.replay, mux)
	}
	return transport.WrapPublicSurface(logger, inner, runhttp.PublicSurfaceOptionsFromEnv())
}
