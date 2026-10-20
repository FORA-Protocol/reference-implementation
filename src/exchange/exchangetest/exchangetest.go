//go:build integration

// Package exchangetest runs the Exchange's publisher-registration surface in
// process, over a database the caller owns, for integration suites that need a
// real Exchange on the other end of the wire.
//
// Two suites use it. The Exchange's own transport tests drive the routes
// directly. The console's tests drive the console, which calls these routes the
// way it does in production. The Testing Doctrine forbids either suite from
// standing in for the Exchange, and Go's internal visibility keeps the console
// from composing the Exchange itself, so this package is the one place the
// composition is written: the production service over the production
// repositories, the production handler, the production signature middleware
// bound to one console directory, and the production public-surface wrapper,
// served over real HTTP.
//
// The one seam is Manifests, which answers what a publisher's site serves at its
// well-known path. That site is a host on the public internet in production and
// a host the test does not own here, so it is faked at the adapter boundary the
// service already has (service.ManifestChecker).
//
// The trading surface — catalog pushes, discovery, transactions and usage
// reports — is composed here too, by MountTrading, for the same reason: the
// Exchange's transport harness serves it through that function, so the service
// graph is written once. Registry fakes callers' key directories at the
// registry port, and the message builders and signing transport are the ones
// every suite sends with.
//
// It carries the integration build tag, like every suite that imports it, so a
// build without the tag never compiles a test harness into anything.
package exchangetest

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	wkserver "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/keypolicy"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/replay"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	exchangedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// Migrations are the Exchange's migrations, for a suite outside the Exchange
// whose shared database has to carry the Exchange's schema beside its own.
var Migrations = exchangedb.Migrations

// MigrationsDir and MigrationsTable are the arguments db.Migrate takes with
// Migrations.
const (
	MigrationsDir   = exchangedb.MigrationsDir
	MigrationsTable = exchangedb.MigrationsTable
)

// Options is the deployment the Exchange runs as.
type Options struct {
	// DirectoryURL is the console's Web Bot Auth directory origin: the ONE
	// directory the signature middleware resolves keys from.
	DirectoryURL string
	// ExchangeDomain is the seat's hostname. A site's manifest must name it.
	ExchangeDomain string
	// ContributorHost is the operator's catalog contributor. A site's manifest
	// must authorize it.
	ContributorHost string
	// DirectoryClient fetches the console's directory. Nil takes a client that
	// trusts the httptest TLS certificate StartDirectory serves under: the
	// production client refuses loopback addresses, which is where httptest
	// binds, and that guard is tested in its own package.
	DirectoryClient *http.Client
	// TermsDefaults are the deployment's currency and unit. The zero value
	// takes EUR, the production default.
	TermsDefaults termspreset.Defaults
	// WithoutSeat serves the surface without seeding a seat for
	// ExchangeDomain: a deployment whose operator has not provisioned it yet.
	// SeatID is then empty. A suite that seeds the seat itself, such as one
	// whose own harness serves transactions on it, sets it too.
	WithoutSeat bool
	// Manifests is what each publisher site serves. Nil takes a fresh set. A
	// suite whose catalog pushes resolve the payee through the same site
	// documents passes the set its push path reads, so the publisher a site
	// attests is the one the console registered.
	Manifests *Manifests
	// Billing is the adapter the publisher revenue reads come from, through
	// billing.RevenueLedgerFor as in production. Nil takes the in-memory
	// adapter, which keeps no settlement history, so every totals read answers
	// INTERNAL_ERROR.
	Billing billing.Adapter
	// LedgerCurrencies are the currencies Billing keeps a ledger in, in
	// configuration order; a totals read that names no currency is read in the
	// first. Nil takes billing.DemoCurrency alone, what the Exchange reports for
	// its free and in-memory tiers. It is not derived from TermsDefaults, which
	// the Exchange sets separately.
	LedgerCurrencies []string
	// SettlementBatch is the read service's PublisherReadConfig value of the
	// same name. Zero is the production default; a suite sets a small one to
	// make a narrowed totals read cross a batch boundary with a few sales.
	SettlementBatch int
	// Clock decides whether a pending usage report is overdue. Nil is the
	// system clock.
	Clock clock.Clock
	// Ledger makes the Exchange sell: the trading surface is served beside the
	// console routes, charges go through a TigerBeetle adapter on this ledger,
	// and the revenue reads come from the same adapter. Buyer then adds an
	// agent that buys. It cannot be combined with WithoutSeat or Billing, and
	// the ledger currency replaces LedgerCurrencies.
	Ledger *Ledger
	// FeeRateBps is the seat's platform fee in basis points, set through the
	// admin plane's SetTenantFeeRate once the seat exists. Zero is the schema
	// default.
	FeeRateBps int32
}

// Exchange is the running Exchange.
type Exchange struct {
	// URL is the Exchange's origin.
	URL string
	// Server is the underlying test server, for a caller that needs its
	// transport or wants to close it early.
	Server *httptest.Server
	// SeatID is the tenant id of the one seat this Exchange sells as. Empty
	// when Options.WithoutSeat left it out.
	SeatID string
	// Manifests is what each publisher site serves at its well-known path.
	Manifests *Manifests

	opts   Options
	salt   string
	market *market
}

// Start seeds the seat into pool, unless Options.WithoutSeat says not to, and
// serves the registration surface over it, and the trading surface too when
// Options.Ledger is set.
// The server closes when the test ends.
func Start(t testing.TB, pool *pgxpool.Pool, o Options) *Exchange {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e := &Exchange{opts: o}
	e.withDefaults(t)
	if !o.WithoutSeat {
		e.SeatID = repotest.SeedSeat(t, ctx, pool, repotest.Seat{Domain: o.ExchangeDomain})
		if o.FeeRateBps != 0 {
			setSeatFee(t, ctx, pool, o.ExchangeDomain, e.SeatID, o.FeeRateBps)
		}
	}

	consoleOrigin, err := transport.ConsoleDirectoryOrigin(o.DirectoryURL)
	if err != nil {
		t.Fatalf("exchangetest: %v", err)
	}
	resolver := transport.ConsoleKeyResolver(consoleOrigin,
		keypolicy.RunningWBAResolver(ctx, resolvers.WBAKeyResolverOptions{
			HTTP: e.opts.DirectoryClient, Logger: testutil.DiscardLogger(),
		}))
	queries := sqlc.New(pool)
	// One gate for both services, as the composition root builds it.
	gate := service.NewPublisherGate(
		repo.NewPublisherGrantReadRepo(queries), repo.NewTenantReadRepo(queries), o.ExchangeDomain)
	mux := http.NewServeMux()
	transport.NewConsoleHandler(registrationService(pool, gate, e.Manifests, e.opts)).RegisterRoutes(mux)
	transport.NewConsoleReadHandler(service.NewPublisherReadService(queries, service.PublisherReadWiring{
		Gate:   gate,
		Ledger: billing.RevenueLedgerFor(e.opts.Billing),
		Clk:    o.Clock,
		Config: service.PublisherReadConfig{
			Currencies: e.opts.LedgerCurrencies, SettlementBatch: e.opts.SettlementBatch,
		},
	})).RegisterRoutes(mux)
	if o.Ledger != nil {
		e.market = openMarket(t, ctx, mux, pool, e)
	}
	guarded := transport.ConsoleSignatureMiddleware(resolver, replay.NewMemoryStore(nil), mux)
	e.Server = httptest.NewServer(transport.WrapPublicSurface(
		testutil.DiscardLogger(), guarded, runhttp.PublicSurfaceOptions{}))
	t.Cleanup(e.Server.Close)
	e.URL = e.Server.URL
	if e.market != nil {
		e.market.serve(e.Server)
	}
	return e
}

// withDefaults fills in every option the caller left out. A ledger brings its
// own billing, salted for this test, so the charges and the revenue reads go
// through one adapter.
func (e *Exchange) withDefaults(t testing.TB) {
	t.Helper()
	o := &e.opts
	if o.DirectoryClient == nil {
		o.DirectoryClient = testutil.HTTPTestTLSClient(5 * time.Second)
	}
	if o.TermsDefaults.Currency == "" {
		o.TermsDefaults.Currency = "EUR"
	}
	e.Manifests = o.Manifests
	if e.Manifests == nil {
		e.Manifests = NewManifests()
	}
	if o.Ledger != nil {
		if o.Billing != nil {
			t.Fatal("exchangetest: a ledger brings its own billing; set Ledger or Billing, not both")
		}
		e.salt = o.Ledger.shared.Salt(t)
		o.Billing = o.Ledger.adapter(t, e.salt)
		o.LedgerCurrencies = []string{ledgerCurrency}
	}
	if o.Billing == nil {
		o.Billing = billing.NewInMemoryAdapter(billing.InMemoryOptions{})
	}
	if o.LedgerCurrencies == nil {
		o.LedgerCurrencies = []string{billing.DemoCurrency}
	}
}

// registrationService builds the service the way the Exchange's composition
// root does: every port over the production repository for it.
func registrationService(
	pool *pgxpool.Pool, gate *service.PublisherGate, manifests *Manifests, o Options,
) *service.RegistrationService {
	queries := sqlc.New(pool)
	return service.NewRegistrationService(service.RegistrationDeps{
		PublishersRead: repo.NewPublisherReadRepo(queries),
		Publishers:     repo.NewPublisherWriteRepo(queries),
		Grants:         repo.NewPublisherGrantWriteRepo(queries),
		SitesRead:      repo.NewPublisherDomainReadRepo(queries),
		Sites:          repo.NewPublisherDomainWriteRepo(queries),
		Gate:           gate,
		ListingsRead:   repo.NewPublisherListingReadRepo(queries),
		Listings:       repo.NewPublisherListingWriteRepo(queries),
		Audit:          repo.NewAuditRepo(queries),
		TxRunner:       sharedb.PoolRunner{Pool: pool},
		Manifests:      manifests,
		Config: service.RegistrationConfig{
			ExchangeDomain:  o.ExchangeDomain,
			ContributorHost: o.ContributorHost,
			TermsDefaults:   o.TermsDefaults,
		},
	})
}

// PublisherManifest is the document a correctly configured site serves for
// this Exchange: it names the seat, attests publisherID, and authorizes the
// operator's catalog contributor.
func (e *Exchange) PublisherManifest(domain, publisherID string) *forawellknown.Manifest {
	return PublisherManifest(e.opts.ExchangeDomain, e.opts.ContributorHost, domain, publisherID)
}

// PublisherManifest builds a publisher document naming exchangeDomain with
// publisherID as the payee. An empty contributor leaves the catalog
// contributors out, which is the document a check refuses for that reason.
//
// It is built by the same builder the publisher console generates the file
// with, from the same inputs a deployment gives it, and read back through the
// parser the Exchange uses, so a test serves the document a publisher would
// actually download rather than a hand-written approximation of it.
func PublisherManifest(exchangeDomain, contributor, domain, publisherID string) *forawellknown.Manifest {
	raw, err := wkserver.BuildPublisher(wkserver.Publisher{
		Domain:            domain,
		ExchangeDomain:    exchangeDomain,
		ExchangeEndpoint:  "https://" + exchangeDomain,
		ResourceOwnerID:   publisherID,
		ContributorHost:   contributor,
		SupportedProfiles: []string{"fora-news-v1"},
	})
	if err != nil {
		panic(err)
	}
	m, err := forawellknown.ParseManifest(raw, forawellknown.RolePublisher)
	if err != nil {
		panic(err)
	}
	return m
}

// StartDirectory serves a Web Bot Auth directory publishing priv's public key,
// the way the console publishes its own key, and returns its https origin. It is
// a real origin rather than a resolver stub, because what the Exchange is
// trusted to do includes reading the ONE configured directory and no other. It
// is written by the production directory writer: over https, under the
// directory media type, signed by the key it lists.
func StartDirectory(t testing.TB, priv ed25519.PrivateKey) string {
	t.Helper()
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("exchangetest: directory key is not Ed25519")
	}
	now := clock.System{}.Now()
	raw, err := wkserver.BuildWBA(wkserver.WBAConfig{
		Keys: wkserver.StaticKeys(forawellknown.NewKey(pub, now.Add(-time.Hour), now.Add(24*time.Hour))),
	})
	if err != nil {
		t.Fatalf("build wba directory: %v", err)
	}
	signers := []helpers.Signer{Signer(priv)}
	signatures := wkserver.NewSignatureCache(nil, 0)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != forawellknown.WBAPath {
			http.NotFound(w, r)
			return
		}
		_ = wkserver.WriteDirectory(w, r, raw, signers, signatures)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
