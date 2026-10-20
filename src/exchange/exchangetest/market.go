//go:build integration

package exchangetest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tbtest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tigerbeetle"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
)

// ledgerCurrency is the currency of the one ledger every selling seat keeps.
const ledgerCurrency = billing.DemoCurrency

// Ledger is one TigerBeetle cluster the tests of a suite share. TigerBeetle has
// no reset, so each Exchange started over it salts every account id with a
// value unique to its test, and no two tests read one account.
type Ledger struct {
	shared *testutil.SharedTigerBeetle
	client *tigerbeetle.Client
}

// BootLedger dials the cluster a suite's TestMain started. The returned func
// closes the client, and the TestMain defers it.
func BootLedger(shared *testutil.SharedTigerBeetle) (*Ledger, func(), error) {
	client, closeClient, err := tbtest.BootClient(shared.Address)
	if err != nil {
		return nil, nil, err
	}
	return LedgerOver(shared, client), closeClient, nil
}

// LedgerOver is the Ledger of a cluster a suite's TestMain already dialed, for
// a suite that keeps one client of its own: it shares that client instead of
// opening a second one.
func LedgerOver(shared *testutil.SharedTigerBeetle, client *tigerbeetle.Client) *Ledger {
	return &Ledger{shared: shared, client: client}
}

// adapter is the seat's billing over the ledger, salted with salt.
func (l *Ledger) adapter(t testing.TB, salt string) billing.Adapter {
	t.Helper()
	return tbtest.Adapter(t, l.client, salt, ledgerCurrency)
}

// fund credits dollars to the agent account billingRef names, the operator's
// manual credit and the only way a ledger balance is ever created.
func (l *Ledger) fund(t testing.TB, salt, billingRef, dollars string) {
	t.Helper()
	tbtest.LedgerFor(t, l.client, ledgerCurrency).MustFundAgent(t, salt, billingRef, dollars)
}

// market is the selling side of an Exchange started with a Ledger: the trading
// surface on the seat, and the operator's catalog contributor that pushes the
// resources buyers buy.
type market struct {
	ctx      context.Context
	queries  sqlc.Querier
	exchange string
	seatID   string
	trading  *Trading
	registry *Registry
	ledger   *Ledger
	salt     string
	// contributor is the operator's contributor host, which every document the
	// console generates authorizes.
	contributor     string
	contributorPriv ed25519.PrivateKey
	url             string
	transport       http.RoundTripper
	buyers          int
}

// openMarket mounts the trading surface on mux, over the same database,
// manifests and billing as the console routes beside it, as a production
// Exchange serves both on its public listener.
func openMarket(
	t testing.TB, ctx context.Context, mux *http.ServeMux, pool *pgxpool.Pool, e *Exchange,
) *market {
	t.Helper()
	if e.SeatID == "" {
		t.Fatal("exchangetest: a ledger sells on the seat Start seeds, so it cannot be combined with WithoutSeat")
	}
	keys := signing.NewInMemoryKeyStore()
	seatPub, seatPriv := newKey(t)
	keys.PutEd25519(repotest.SeatEd25519KeyRef(e.SeatID), seatPub, seatPriv)
	offers, err := signing.GenerateEd25519Signer()
	if err != nil {
		t.Fatalf("offer signer: %v", err)
	}
	registry := NewRegistry()
	contributorPub, contributorPriv := newKey(t)
	registry.Put(e.opts.ContributorHost, contributorPub)
	queries := sqlc.New(pool)
	trading := MountTrading(t, mux, TradingDeps{
		Pool: pool, Queries: queries, Registry: registry, Manifests: e.Manifests,
		Billing: e.opts.Billing, Signer: offers, KeyStore: keys,
		ExchangeDomain: e.opts.ExchangeDomain, CallerKeys: map[string]ed25519.PublicKey{},
		Clock: e.opts.Clock, DefaultTenantDomain: e.opts.ExchangeDomain,
		LedgerCurrencies: e.opts.LedgerCurrencies,
	})
	if err := trading.Catalog.Bootstrap(ctx); err != nil {
		t.Fatalf("catalog bootstrap: %v", err)
	}
	return &market{
		ctx: ctx, queries: queries, exchange: e.opts.ExchangeDomain, seatID: e.SeatID,
		trading: trading, registry: registry, ledger: e.opts.Ledger, salt: e.salt,
		contributor: e.opts.ContributorHost, contributorPriv: contributorPriv,
	}
}

// serve records where the surface is served, once the server is up.
func (m *market) serve(server *httptest.Server) {
	m.url = server.URL
	m.transport = server.Client().Transport
	if m.transport == nil {
		m.transport = http.DefaultTransport
	}
}

// push lists one resource at domain+path under term, pushed as the operator's
// contributor. The seat accepts it only for a site whose listing is active and
// whose document authorizes that contributor, which is what a registration
// through the console leaves behind once the site is verified.
func (m *market) push(t testing.TB, domain, path, title string, term *forav1.LicenseTerm) {
	t.Helper()
	if err := m.pushAs(domain, path, title, term); err != nil {
		t.Fatalf("push %s%s: %v", domain, path, err)
	}
}

// pushAs is push without the verdict: it returns the Exchange's answer.
func (m *market) pushAs(domain, path, title string, term *forav1.LicenseTerm) error {
	client := foraconnect.NewCatalogServiceClient(
		&http.Client{Transport: SigningTransport(m.transport, m.contributor, m.contributorPriv)},
		m.url, connect.WithGRPC())
	entry := &forav1.ResourceEntry{Domain: domain, Path: path, Terms: []*forav1.LicenseTerm{term}}
	if title != "" {
		entry.Title = &title
	}
	_, err := client.PushResources(m.ctx, connect.NewRequest(
		PushRequest(m.exchange, m.seatID, m.contributor, []*forav1.ResourceEntry{entry})))
	return err
}

// PushAsContributor lists one resource at domain+path under term the way the
// operator's catalog contributor does, and returns the Exchange's answer rather
// than failing the test, so a suite can show a push being refused -- for a site
// that is saved but not verified, say. The Exchange must have been started
// with Options.Ledger, as Buyer says.
func (e *Exchange) PushAsContributor(t testing.TB, domain, path string, term *forav1.LicenseTerm) error {
	t.Helper()
	if e.market == nil {
		t.Fatal("exchangetest: PushAsContributor needs an Exchange started with Options.Ledger")
		return nil
	}
	return e.market.pushAs(domain, path, "", term)
}

// newKey generates an Ed25519 key pair.
func newKey(t testing.TB) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	return pub, priv
}
