//go:build integration

package exchangetest

import (
	"cmp"
	"crypto/ed25519"
	"math/big"
	"net/http"
	"testing"
	"time"

	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	audiencetest "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/replay"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/agentreg"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/regschema"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sor"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/wellknown"
)

// TradingDeps are what the trading surface is built over: the ports the
// catalog and exchange services take, and the seams a suite turns to drive one
// failure. The zero value of every seam is the production behaviour.
type TradingDeps struct {
	Pool    *pgxpool.Pool
	Queries sqlc.Querier
	// Registry resolves a caller's key from its directory, a host on the
	// public internet in production. Registry is the fake for it.
	Registry agentreg.Registry
	// Manifests answers what each publisher site serves, for the push path's
	// contributor and payee gates.
	Manifests service.ManifestCache
	Billing   billing.Adapter
	// Signer signs offers, and its public key is the one the well-known
	// document publishes.
	Signer *signing.Ed25519Signer
	// KeyStore holds the seat's delivery-URL keys.
	KeyStore signing.KeyStore
	// ExchangeDomain is the seat's hostname: what offers, pushes and the
	// recipient check all name.
	ExchangeDomain string
	// CallerKeys are the RFC 9421 keys the ExchangeService gate accepts, by
	// keyid (an RFC 7638 thumbprint). Trading.Resolver takes more afterwards.
	CallerKeys map[string]ed25519.PublicKey
	// Clock is nil for the system clock.
	Clock clock.Clock
	// Lifetimes shortens a tenant's offer and delivery-URL lifetimes; nil is
	// production's none.
	Lifetimes service.Lifetimes
	// TxRunner is nil for a runner over Pool. A failing runner drives the
	// persist-failure path.
	TxRunner sharedb.TxRunner
	// Audit decorates the audit repository rather than replacing it, so a test
	// repo can delegate to real appends and fail only once armed.
	Audit func(repo.AuditRepo) repo.AuditRepo
	// MaxSignatures bounds the inbound signature chain; 0 is unbounded.
	MaxSignatures int
	// BrokerHost is the Broker directory host a relayed request is recognised
	// by.
	BrokerHost string
	// SoR is nil for a fresh in-memory account System of Record.
	SoR sor.Adapter
	// JournalQueries is what the denial journal writes through; nil is
	// Queries. The journal still writes through the real repository, so a
	// querier over a closed pool makes every journal write fail in the driver.
	JournalQueries sqlc.Querier
	// DefaultTenantDomain names the seat Register reads its activation policy
	// from. Empty for a suite that never registers an agent.
	DefaultTenantDomain string
	// LedgerCurrencies are the currencies Billing keeps a ledger in, in
	// configuration order.
	LedgerCurrencies []string
	// DefaultAgentCredits is the welcome credit Register grants, by currency, as
	// cmd/server parses it from EXCHANGE_DEFAULT_AGENT_CREDIT. Nil grants none.
	DefaultAgentCredits map[string]*big.Rat
	// BillingRefGen is nil for random billing refs.
	BillingRefGen service.BillingRefGen
	// RegSchema is nil for no published registration schema.
	RegSchema *regschema.Schema
	// TermsDigest is empty for no terms versioning.
	TermsDigest string
}

// Trading is the mounted trading surface.
type Trading struct {
	Exchange *service.ExchangeService
	Catalog  *service.CatalogService
	// Resolver is the ExchangeService gate's key resolver, for a suite that
	// adds a caller after the surface is up.
	Resolver *helpers.StaticKeyResolver
}

// MountTrading builds the catalog and exchange services over d and mounts the
// Exchange's public trading surface on mux: CatalogService, ExchangeService,
// the well-known documents and the agents/register route, each through the
// function production builds it with, so a change to a mount reaches every
// suite that serves one. The caller wraps mux and serves it.
func MountTrading(t testing.TB, mux *http.ServeMux, d TradingDeps) *Trading {
	t.Helper()
	catalogSvc, exchangeSvc := tradingServices(d)
	resolver := helpers.NewStaticKeyResolver(d.CallerKeys)
	replayAdapter := replay.NewCoreAdapter(replay.NewMemoryStore(nil))
	// Production mounts the recipient check on both services, so a request
	// naming another Exchange is refused here as it is there.
	audience := audiencetest.MustInterceptor(t, d.ExchangeDomain)
	exchangeOpts, err := transport.ExchangeMountOptions(resolver, replayAdapter, d.MaxSignatures, audience)
	if err != nil {
		t.Fatalf("exchange mount options: %v", err)
	}
	exchangePath, exchangeHandler := connectserver.NewExchangeServiceHandler(
		transport.NewExchangeHandler(exchangeSvc), exchangeOpts...)
	mux.Handle(exchangePath, exchangeHandler)
	catalogOpts, err := transport.CatalogMountOptions(audience)
	if err != nil {
		t.Fatalf("catalog mount options: %v", err)
	}
	catalogPath, catalogHandler := foraconnect.NewCatalogServiceHandler(
		transport.NewCatalogHandler(catalogSvc, d.Registry), catalogOpts...)
	mux.Handle(catalogPath, catalogHandler)
	mountWellKnown(t, mux, d)
	transport.NewAgentsRegisterHandler(d.Registry, transport.AgentsRegisterOptions{}).RegisterRoutes(mux)
	return &Trading{Exchange: exchangeSvc, Catalog: catalogSvc, Resolver: resolver}
}

// tradingServices builds the catalog and exchange services the way the
// composition root does: every port over the production repository for it.
func tradingServices(d TradingDeps) (*service.CatalogService, *service.ExchangeService) {
	catalogSvc := service.NewCatalogService(service.CatalogDeps{
		Catalog:        repo.NewCatalogRepo(d.Queries),
		Tenants:        repo.NewTenantReadRepo(d.Queries),
		Sites:          repo.NewPublisherDomainReadRepo(d.Queries),
		Listings:       repo.NewPublisherListingReadRepo(d.Queries),
		Registry:       d.Registry,
		Manifests:      d.Manifests,
		TxRunner:       sharedb.PoolRunner{Pool: d.Pool},
		ExchangeDomain: d.ExchangeDomain,
	})
	// Set before any bootstrap or push, or the CoMP cache stays empty.
	// The profiles cmd/server's exchangeSupportedProfiles advertises: the
	// catalog pre-renders CoMP only for a profile it knows, and profile-aware
	// discovery projects it only when the Exchange advertises it.
	catalogSvc.SetSupportedProfiles(forawellknown.ExchangeProfiles())
	txRunner := cmp.Or[sharedb.TxRunner](d.TxRunner, sharedb.PoolRunner{Pool: d.Pool})
	audit := repo.NewAuditRepo(d.Queries)
	if d.Audit != nil {
		audit = d.Audit(audit)
	}
	accounts := d.SoR
	if accounts == nil {
		// Built only when needed: cmp.Or would construct a store every call.
		accounts = sor.NewInMemoryAdapter()
	}
	exchangeSvc := service.NewExchangeService(service.ExchangeDeps{
		TxRunner:      txRunner,
		Catalog:       catalogSvc,
		Tenants:       repo.NewTenantReadRepo(d.Queries),
		Sites:         repo.NewPublisherDomainReadRepo(d.Queries),
		Agents:        repo.NewAgentRepo(d.Queries),
		AgentReg:      d.Registry,
		Transactions:  repo.NewTransactionRepo(d.Queries),
		Obligations:   repo.NewObligationRepo(d.Queries),
		Evidence:      repo.NewEvidenceRepo(d.Queries),
		Events:        repo.NewTransactionEventRepo(journalQueries(d)),
		FeeOverrides:  repo.NewFeeOverrideRepo(d.Queries),
		Billing:       d.Billing,
		OfferSigner:   d.Signer,
		KeyStore:      d.KeyStore,
		SoR:           accounts,
		Audit:         audit,
		RegSchema:     d.RegSchema,
		BillingRefGen: d.BillingRefGen,
		// LedgerCurrencies is set explicitly, as cmd/server does:
		// ExchangeConfig has no currency default, and without one every
		// welcome-credit grant fails the billing gate.
		Config: service.ExchangeConfig{
			Exchange:            d.ExchangeDomain,
			SupportedProfiles:   forawellknown.ExchangeProfiles(),
			DefaultTenantDomain: d.DefaultTenantDomain,
			LedgerCurrencies:    d.LedgerCurrencies,
			DefaultAgentCredits: d.DefaultAgentCredits,
			TermsDigest:         d.TermsDigest,
			BrokerHost:          d.BrokerHost,
		},
		Clk:       d.Clock,
		Lifetimes: d.Lifetimes,
	})
	return catalogSvc, exchangeSvc
}

// mountWellKnown serves the Exchange's own documents, publishing d.Signer's
// key as the offer key. The document's clock is fixed so its validity window
// never depends on when a suite runs.
func mountWellKnown(t testing.TB, mux *http.ServeMux, d TradingDeps) {
	t.Helper()
	directorySigner, err := d.Signer.DirectorySigner()
	if err != nil {
		t.Fatalf("wellknown: %v", err)
	}
	wk, err := wellknown.New(wellknown.Config{
		Domain:            d.ExchangeDomain,
		Endpoint:          "/fora.v1.ExchangeService",
		CatalogEndpoint:   "/fora.v1.CatalogService",
		BaseCurrency:      billing.BaseCurrency(d.LedgerCurrencies),
		SupportedProfiles: []string{"fora-news-v1"},
		OfferKey:          d.Signer.PublicKey(),
		OfferSigner:       directorySigner,
		Clock:             clock.NewDeterministic(time.Unix(1700000000, 0).UTC()),
		KeyLifetime:       10 * 365 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("wellknown: %v", err)
	}
	wk.RegisterRoutes(mux)
}

// journalQueries is what d's denial journal writes through.
func journalQueries(d TradingDeps) sqlc.Querier {
	if d.JournalQueries != nil {
		return d.JournalQueries
	}
	return d.Queries
}
