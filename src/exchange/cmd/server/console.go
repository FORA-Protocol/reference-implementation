package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/keypolicy"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/denomination"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// consoleDeps are what the publisher console's surface needs: the handlers
// that serve its registrations and its reads, and the verification they run
// behind. All are nil on a deployment that runs no console, and buildMux then
// mounts no routes at all — an unconfigured surface answers 404 rather than
// answering requests it cannot authenticate.
type consoleDeps struct {
	handler  *transport.ConsoleHandler
	reads    *transport.ConsoleReadHandler
	resolver helpers.KeyResolver
	replay   transport.ConsoleReplayStore
}

func (d consoleDeps) enabled() bool { return d.handler != nil }

// buildConsoleDeps wires the console surface from the environment.
//
// EXCHANGE_CONSOLE_WELLKNOWN_URL is what turns it on. Unset means this
// deployment runs no publisher console, which is the state every deployment is
// in before one is deployed, so it is a logged skip rather than a boot failure —
// unlike the Broker's directory, whose absence would silently disable
// revocation on a surface that IS mounted.
//
// EXCHANGE_CATALOG_CONTRIBUTOR_HOST is required once the surface is on. It is
// the host whose Web Bot Auth directory signs catalog pushes for every
// publisher here, and the generated manifest has to name it: a registration
// flow that activated listings without checking for it would hand publishers a
// manifest the operator cannot push against, and the failure would surface much
// later as a rejected import.
func buildConsoleDeps(ctx context.Context, logger *slog.Logger, in consoleWiring) (consoleDeps, error) {
	pool, queries := in.pool, in.queries
	directoryURL := runhttp.EnvOr("EXCHANGE_CONSOLE_WELLKNOWN_URL", "")
	if directoryURL == "" {
		logger.Info("exchange.console.disabled",
			"reason", "EXCHANGE_CONSOLE_WELLKNOWN_URL is not set")
		return consoleDeps{}, nil
	}
	consoleOrigin, err := transport.ConsoleDirectoryOrigin(directoryURL)
	if err != nil {
		return consoleDeps{}, fmt.Errorf("EXCHANGE_CONSOLE_WELLKNOWN_URL: %w", err)
	}
	contributorHost := runhttp.EnvOr("EXCHANGE_CATALOG_CONTRIBUTOR_HOST", "")
	if contributorHost == "" {
		return consoleDeps{}, errConsoleContributorHostRequired
	}
	terms := denomination.FromEnv()
	// One gate for both services, so registration and the reads decide who
	// may see a publisher from one seat and one set of grants.
	gate := service.NewPublisherGate(
		repo.NewPublisherGrantReadRepo(queries), repo.NewTenantReadRepo(queries), exchangeDomain())
	svc := service.NewRegistrationService(service.RegistrationDeps{
		PublishersRead: repo.NewPublisherReadRepo(queries),
		Publishers:     repo.NewPublisherWriteRepo(queries),
		Grants:         repo.NewPublisherGrantWriteRepo(queries),
		SitesRead:      repo.NewPublisherDomainReadRepo(queries),
		Sites:          repo.NewPublisherDomainWriteRepo(queries),
		Gate:           gate,
		ListingsRead:   repo.NewPublisherListingReadRepo(queries),
		Listings:       repo.NewPublisherListingWriteRepo(queries),
		Audit:          repo.NewAuditRepo(queries),
		TxRunner:       db.PoolRunner{Pool: pool},
		Manifests:      in.manifests,
		Config: service.RegistrationConfig{
			ExchangeDomain:  exchangeDomain(),
			ContributorHost: contributorHost,
			// The denomination a publisher's arrangement is priced in. It is the
			// deployment's, not theirs: they decide whether to charge and how
			// much, and this decides in what.
			TermsDefaults: terms,
		},
	})
	logger.Info("exchange.console.enabled",
		"console_wellknown_url", directoryURL, "catalog_contributor_host", contributorHost)
	// The revenue reads come from the ledger of the adapter the Exchange
	// charges through, so what a publisher is shown is what its charges
	// settled to. A read that names no currency is read in the ledger's base
	// currency, not the terms currency above. The two are set separately: the
	// terms currency defaults to EUR, and the free and in-memory tiers keep USD.
	reads := service.NewPublisherReadService(queries, service.PublisherReadWiring{
		Gate:   gate,
		Ledger: billing.RevenueLedgerFor(in.billing),
		Clk:    clock.System{},
		Config: service.PublisherReadConfig{
			Currencies:   in.ledgerCurrencies,
			ReportWindow: in.reportWindow,
		},
	})
	return consoleDeps{
		handler:  transport.NewConsoleHandler(svc),
		reads:    transport.NewConsoleReadHandler(reads),
		resolver: transport.ConsoleKeyResolver(consoleOrigin, consoleDirectoryResolver(ctx, logger, in.fetchClient)),
		replay:   in.replay,
	}, nil
}

// consoleWiring is what the console surface is built from: the database, the
// outbound client its checks fetch with, the manifest cache, the replay store
// its signatures are recorded in, the billing adapter whose ledger the revenue
// reads come from with the currencies it keeps, and the reporting window the
// ExchangeService mints obligations with.
type consoleWiring struct {
	pool             *pgxpool.Pool
	queries          *sqlc.Queries
	fetchClient      *http.Client
	manifests        *forawellknown.Cache
	replay           transport.ConsoleReplayStore
	billing          billing.Adapter
	ledgerCurrencies []string
	reportWindow     time.Duration
}

// consoleDirectoryResolver is the Web Bot Auth resolver the console's keys are
// read from. It is a resolver of its own rather than the one the agent path
// uses, because that one falls through to whatever directory a request names
// and this one must read exactly one.
//
// It shares the agent path's directory TTL, so a console key rotation takes
// effect on the same schedule an agent's does, and it shares the guarded HTTP
// client, so the console's directory is fetched under the same SSRF posture as
// every other outbound well-known read.
func consoleDirectoryResolver(
	ctx context.Context, logger *slog.Logger, fetchClient *http.Client,
) helpers.KeyResolver {
	return keypolicy.RunningWBAResolver(ctx, resolvers.WBAKeyResolverOptions{
		HTTP:   fetchClient,
		TTL:    runhttp.EnvDuration("EXCHANGE_DIRECTORY_TTL", 0),
		Logger: logger,
	})
}

// errConsoleContributorHostRequired refuses a boot that mounts the registration
// surface with no contributor to name. Every manifest the console generates
// lists that host, and CheckDomain requires it: without a value, registration
// would activate listings whose manifests authorize nobody to push their
// catalog, and the gap would surface as a rejected import days later.
var errConsoleContributorHostRequired = errors.New(
	"EXCHANGE_CATALOG_CONTRIBUTOR_HOST is required when EXCHANGE_CONSOLE_WELLKNOWN_URL is set: " +
		"the generated publisher manifest names the operator's catalog contributor, " +
		"and a domain check cannot pass without it")
