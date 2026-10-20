//go:build integration

package exchangetest

import (
	"crypto/ed25519"
	"fmt"
	"net/http"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/google/uuid"

	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// Buyer is an AI agent buying from the seat: registered for billing through the
// public Register RPC, funded on the ledger, and buying through discovery and
// ExecuteTransaction with the SDK's acceptance signatures, exactly as a real
// agent does.
type Buyer struct {
	m      *market
	id     string
	priv   ed25519.PrivateKey
	client foraconnect.ExchangeServiceClient
}

// Sale is one transaction a Buyer made.
type Sale struct {
	TransactionID string
	BillingID     string
	// URI is the resource bought.
	URI string
}

// startingBalance is what each Buyer is funded with, enough for any suite's
// purchases at the prices these suites charge.
const startingBalance = "100.00"

// Buyer adds a new agent to the seat and funds it. The Exchange must have been
// started with Options.Ledger.
//
// Two steps are arranged below the public surface, because in production both
// happen outside any request this Exchange serves. The agent's key is resolved
// from its own directory, a host the suite does not own, so Registry stands in
// for that lookup and the agent's row is written through the production agent
// repository, which is what directory self-signup writes. And its funds are a
// direct ledger credit, the operator's manual top-up and the ledger's only
// credit path.
func (e *Exchange) Buyer(t testing.TB) *Buyer {
	t.Helper()
	m := e.market
	if m == nil {
		t.Fatal("exchangetest: Buyer needs an Exchange started with Options.Ledger")
		return nil
	}
	m.buyers++
	id := fmt.Sprintf("buyer-%d.agents.example", m.buyers)
	pub, priv := newKey(t)
	m.registry.Put(id, pub)
	m.trading.Resolver.Put(rwtestutil.MustThumbprintPriv(priv), pub)
	if _, err := repo.NewAgentRepo(m.queries).Upsert(m.ctx, repo.Agent{
		ID:            id,
		PublicKey:     pub,
		DiscoveryURL:  "https://" + id + "/.well-known/http-message-signatures-directory",
		RequesterType: string(sqlc.ForaRequesterTypeAGENT),
	}); err != nil {
		t.Fatalf("agent %s: %v", id, err)
	}
	client := foraconnect.NewExchangeServiceClient(
		&http.Client{Transport: SigningTransport(m.transport, id, priv)}, m.url, connect.WithGRPC())
	resp, err := client.Register(m.ctx, connect.NewRequest(RegisterRequest(m.exchange, nil, "")))
	if err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
	m.ledger.fund(t, m.salt, resp.Msg.GetBillingRef(), startingBalance)
	return &Buyer{m: m, id: id, priv: priv, client: client}
}

// ID is the agent id the Exchange records for the Buyer's transactions.
func (b *Buyer) ID() string { return b.id }

// Buy lists one resource at domain+path under term, as the operator's catalog
// contributor does for a registered site, then discovers its offer and buys
// it. A denial fails the test, as Delivered says.
func (b *Buyer) Buy(t testing.TB, domain, path string, term *forav1.LicenseTerm) Sale {
	t.Helper()
	return b.BuyTitled(t, domain, path, "", term)
}

// BuyTitled is Buy for a resource listed with a title, the headline a page's
// catalog entry carries. An empty title lists it with none, as Buy does.
func (b *Buyer) BuyTitled(t testing.TB, domain, path, title string, term *forav1.LicenseTerm) Sale {
	t.Helper()
	b.m.push(t, domain, path, title, term)
	uri := "https://" + domain + path
	requester := Requester(b.id, b.id)
	found, err := b.client.DiscoverResources(b.m.ctx, connect.NewRequest(
		ResourceQuery(b.m.exchange, requester, []string{uri})))
	if err != nil {
		t.Fatalf("discover %s: %v", uri, err)
	}
	if len(found.Msg.GetOffers()) == 0 {
		t.Fatalf("no offer for %s", uri)
	}
	req := Purchase{
		Priv: b.priv, Requester: requester, Offer: found.Msg.GetOffers()[0], Key: "tx-" + uuid.NewString(),
	}.SignedRequest(t)
	resp, err := b.client.ExecuteTransaction(b.m.ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatalf("execute %s: %v", uri, err)
	}
	item := Delivered(t, resp.Msg, uri)
	return Sale{TransactionID: item.GetTransactionId(), BillingID: item.GetBillingId(), URI: uri}
}

// Report files the usage report the sale owes, and returns the error the
// Exchange answered with: nil when it accepted the report.
func (b *Buyer) Report(t testing.TB, s Sale, consumed int32) error {
	t.Helper()
	_, err := b.client.ReportUsage(b.m.ctx, connect.NewRequest(UsageReport(
		b.m.exchange, "r-"+uuid.NewString(), s.TransactionID, s.BillingID,
		&forav1.Usage{ConsumedQuantity: consumed})))
	return err
}
