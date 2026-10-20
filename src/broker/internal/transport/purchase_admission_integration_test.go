//go:build integration

package transport_test

// Admission: an item whose offer.exchange the Broker cannot route to, or does
// not approve for purchases, refuses the whole call with FAILED_PRECONDITION,
// naming that Exchange under ErrorDetail.metadata["exchange"]. Every item is
// checked before anything is sent, so one Exchange the Broker cannot use
// refuses the purchase rather than buying the rest.
//
// Each refusal is asserted from the Exchanges' side first: a Broker refusal
// promises nothing was bought, and a code alone cannot show where a request
// went.

import (
	"net/http"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/registry"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// assertNotAdmitted checks a refusal is the Broker's routing refusal for
// exchange, with cause in its message.
func assertNotAdmitted(t *testing.T, ce *connect.Error, exchange, cause string) {
	t.Helper()
	if ce.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v, want failed_precondition: %v", ce.Code(), ce)
	}
	detail := purchaseErrorDetail(t, ce)
	if got := detail.GetMetadata()["exchange"]; got != exchange {
		t.Errorf("metadata[exchange] = %q, want %q", got, exchange)
	}
	if detail.GetDomain() != "fora.v1.BrokerService" {
		t.Errorf("detail domain = %q, want the Broker's", detail.GetDomain())
	}
	if !strings.Contains(ce.Message(), cause) {
		t.Errorf("message %q does not name the cause %q", ce.Message(), cause)
	}
}

// TestPurchase_AnExchangeTheBrokerDoesNotApproveIsRefused pins the purchase
// trust gate. A DISCOVERED Exchange is one the Broker found named in some
// publisher's fora.json, and nobody approved it: it may quote prices, and it may
// not be paid. Otherwise anyone who can edit a publisher's fora.json could put
// themselves in the payment path.
func TestPurchase_AnExchangeTheBrokerDoesNotApproveIsRefused(t *testing.T) {
	env := newPurchaseTestEnv(t)
	env.register(t, env.exchangeDom, env.exchangeURL, repo.TrustLevelDiscovered)

	ce := env.refused(t, env.purchaseRequest(t, "tx-discovered", line{"offer-1", env.exchangeDom}))

	assertNothingSent(t, env.exchange)
	assertNotAdmitted(t, ce, env.exchangeDom, "not approved for purchases")
	assertPurchaseAudit(t, env, "REJECTED_EXCHANGE", "WARN", env.exchangeDom)
}

// TestPurchase_OneUnadmittedExchangeRefusesTheWholePurchase drives a purchase
// across an admitted Exchange and one nobody registered. The admitted Exchange
// is listed FIRST, so a Broker that admitted and sent group by group would
// already have bought from it.
func TestPurchase_OneUnadmittedExchangeRefusesTheWholePurchase(t *testing.T) {
	env := newPurchaseTestEnv(t)

	ce := env.refused(t, env.purchaseRequest(t, "tx-unregistered",
		line{"offer-ok", env.exchangeDom}, line{"offer-x", "nobody.example"}))

	assertNothingSent(t, env.exchange)
	assertNotAdmitted(t, ce, "nobody.example", "not a registered exchange")
}

// TestPurchase_AWithdrawnExchangeIsRefusedWithoutContact pins that a BLOCKED row
// gets no traffic at all — its manifest fetch included — and that the refusal
// does not disclose that an operator withdrew trust: it reads as unregistered.
func TestPurchase_AWithdrawnExchangeIsRefusedWithoutContact(t *testing.T) {
	env := newPurchaseTestEnv(t)
	env.register(t, env.exchangeDom, env.exchangeURL, "BLOCKED")

	ce := env.refused(t, env.purchaseRequest(t, "tx-withdrawn", line{"offer-1", env.exchangeDom}))

	assertNothingSent(t, env.exchange)
	if fetches := env.exchange.manifestFetches(); fetches != 0 {
		t.Errorf("manifest fetches = %d, want 0 — a withdrawn Exchange is refused before "+
			"the Broker asks it anything", fetches)
	}
	assertNotAdmitted(t, ce, env.exchangeDom, "not a registered exchange")
}

// TestPurchase_ADownExchangeIsRefusedAndNotDialled takes the Exchange down the
// only way production can: one probe pass over a /healthz that refuses. The
// refusal names the health state, not a registration the registry plainly has.
// The message is what shows the health state did the work: an Exchange the
// Broker could not reach for any other reason is refused with another cause.
func TestPurchase_ADownExchangeIsRefusedAndNotDialled(t *testing.T) {
	ctx := t.Context()
	env := newPurchaseTestEnv(t)

	env.exchange.setHealthz(http.StatusServiceUnavailable)
	refreshOnce(t, registry.NewRefresher(
		env.exchangeRepo, env.endpoints, loopbackProbe(), testutil.DiscardLogger(), 0,
	), ctx)

	ce := env.refused(t, env.purchaseRequest(t, "tx-down", line{"offer-2", env.exchangeDom}))

	assertNothingSent(t, env.exchange)
	assertNotAdmitted(t, ce, env.exchangeDom, "health check")
}

// TestPurchase_AnExchangeWhoseManifestIsRefusedIsNotDialled serves a manifest at
// a document version this build does not accept. Admission reads it and refuses
// the Exchange; the signed purchase is never posted to the origin.
func TestPurchase_AnExchangeWhoseManifestIsRefusedIsNotDialled(t *testing.T) {
	env := newPurchaseTestEnv(t)
	refused := startExchangeSinkWithManifest(t, func(host string) []byte {
		return rwtestutil.ExchangeManifestAtVersion(host, "http://"+host, "9.9")
	})
	dom := env.addWireExchange(t, refused)

	ce := env.refused(t, env.purchaseRequest(t, "tx-refused-manifest", line{"offer-1", dom}))

	if refused.manifestFetches() == 0 {
		t.Error("the refused manifest was never read, so the refusal below is not about it")
	}
	assertNothingSent(t, refused)
	assertNotAdmitted(t, ce, dom, "advertises no endpoint")
}

// TestPurchase_ARegistryFaultIsAnInternalError takes the registry away for real
// by closing its pool. That is the Broker's own fault, so the purchase is
// INTERNAL and audited at ERROR as such, not filed against the Exchange.
func TestPurchase_ARegistryFaultIsAnInternalError(t *testing.T) {
	env := newPurchaseTestEnv(t)
	env.registryPool.Close()

	ce := env.refused(t, env.purchaseRequest(t, "tx-registry-down", line{"offer-1", env.exchangeDom}))

	assertNothingSent(t, env.exchange)
	if ce.Code() != connect.CodeInternal {
		t.Errorf("code = %v, want internal", ce.Code())
	}
	assertPurchaseAudit(t, env, "REJECTED_INTERNAL", "ERROR", env.exchangeDom)
}

// TestPurchase_AnUnsignedCallerLearnsNothingAboutTheRegistry pins that the
// registry answers nobody who has not proved who they are. offer.exchange
// arrives in the body, and admission answers it several distinguishable ways;
// read before verification, those answers would let anyone map the registry
// by naming domains. Every leg is refused identically by the verify seam,
// before the handler runs.
func TestPurchase_AnUnsignedCallerLearnsNothingAboutTheRegistry(t *testing.T) {
	env := newPurchaseTestEnv(t)
	env.register(t, "discovered.example", "https://discovered.example", repo.TrustLevelDiscovered)
	unsigned := unsignedBrokerClient(env.brokerURL)

	for _, exchange := range []string{"nobody.example", "discovered.example", env.exchangeDom} {
		req := env.purchaseRequest(t, "tx-unsigned-"+exchange, line{"offer-1", exchange})
		_, err := unsigned.ExecuteTransaction(t.Context(), connect.NewRequest(req))
		assertRequestAuthFailure(t, err, "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_MISSING")
	}
	assertNothingSent(t, env.exchange)
}
