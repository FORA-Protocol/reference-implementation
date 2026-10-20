package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/replay"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/agentreg"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// This file holds the Exchange's HTTP surface: which routes exist, what each
// mount verifies, and the middleware wrapped around all of them. It was split
// out of main.go when that file crossed the length cap; run() still owns the
// boot sequence and calls buildSurface once.

// muxInputs is the flat set run() has in hand when it builds the surface.
// newMuxDeps turns it into muxDeps, so the composition root states each value
// once rather than filling a fourteen-field literal inline.
type muxInputs struct {
	pool             interface{ Ping(context.Context) error }
	exchange         *service.ExchangeService
	catalog          *service.CatalogService
	agentRegistry    agentreg.Registry
	offerSigner      *signing.Ed25519Signer
	billing          billing.Adapter
	ledgerCurrencies []string
	admission        admissionDeps
	registration     registrationConfig
	console          consoleDeps
}

func newMuxDeps(in muxInputs) muxDeps {
	return muxDeps{
		pool:             in.pool,
		exchange:         in.exchange,
		catalog:          in.catalog,
		agentRegistry:    in.agentRegistry,
		offerSigner:      in.offerSigner,
		resolver:         in.admission.resolver,
		replay:           in.admission.replay,
		maxSignatures:    in.admission.maxSignatures,
		audience:         in.admission.audience,
		ledgerHealth:     ledgerHealthCheck(in.billing),
		ledgerCurrencies: in.ledgerCurrencies,
		registration:     in.registration,
		console:          in.console,
	}
}

// buildSurface assembles the Exchange's whole public HTTP handler -- the routes
// and the middleware around them -- and returns it with the well-known handler
// pair run() starts the refresher on.
//
// It is one call rather than two at the call site because the two halves have
// to agree: the console's signature check wraps the same mux the console's
// routes are mounted on, and a caller that built one without the other would
// serve registration routes that nothing verifies.
func buildSurface(logger *slog.Logger, d muxDeps) (http.Handler, server.Handlers, error) {
	mux, wk, err := buildMux(d)
	if err != nil {
		return nil, server.Handlers{}, err
	}
	return buildWrapped(logger, mux, d.console), wk, nil
}

// muxDeps collects everything buildMux needs so run() can stay linear and
// tests can assemble the Exchange's HTTP surface without booting real services.
type muxDeps struct {
	pool          interface{ Ping(context.Context) error }
	exchange      *service.ExchangeService
	catalog       *service.CatalogService
	agentRegistry agentreg.Registry
	offerSigner   *signing.Ed25519Signer
	// RFC 9421 verify deps for the ExchangeService mount, and for that mount
	// alone: they are injected into connectserver.NewExchangeServiceHandler,
	// whose verify seam resolves a caller by signature keyid.
	//
	// CatalogService reaches none of them. It mounts on the raw generated
	// handler (registerConnect below says why) and verifies its caller in
	// CatalogHandler.verifyCallerSignature, against a key resolved by caller_id
	// out of the agent registry rather than by keyid — after the Web Bot Auth
	// split the keyid is a thumbprint, which the agents table is not keyed on.
	resolver      helpers.KeyResolver
	replay        *replay.CoreAdapter
	maxSignatures int
	// ledgerCurrencies are the selected billing backend's ledger currencies in
	// configuration order. The well-known document publishes the FIRST of them
	// as base_currency, because that field holds a single value — so the order of
	// EXCHANGE_BILLING_LEDGER decides what this Exchange advertises. Never
	// empty: the demo tiers return their own one-element list and the TigerBeetle
	// path refuses to boot on an unset or unsupported EXCHANGE_BILLING_LEDGER.
	ledgerCurrencies []string
	// audience refuses a request addressed to a different Exchange. It is built
	// from EXCHANGE_DOMAIN — the identity this Exchange publishes and stamps into
	// its offers — and mounted on BOTH Connect surfaces, because an addressed
	// request reaches the catalog mount too.
	audience *foraaudience.Interceptor
	// ledgerHealth probes the billing ledger for /readyz. Nil when the selected
	// billing backend has no ledger (free, in-memory), which is the common case.
	ledgerHealth func(context.Context) error
	// registration carries the operator's registration schema and terms
	// versioning, read once so the manifest publishes exactly what the Register
	// gate will hold a caller to.
	registration registrationConfig
	// console carries the publisher-registration surface. Its zero value means
	// this deployment runs no publisher console, and buildMux then mounts none
	// of its routes.
	console consoleDeps
}

// buildMux wires the Exchange's HTTP surface: healthz, Connect-Go RPCs, the
// well-known endpoints, and the public agents/register route. Admin-plane
// endpoints have been removed; any request to
// /admin/* falls through to http.ServeMux's 404. The well-known handler pair
// is returned alongside the mux so run() can start its periodic refresher.
func buildMux(d muxDeps) (*http.ServeMux, server.Handlers, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+healthzPath, healthzHandler(d.pool))
	mux.HandleFunc("GET /readyz", readyzHandler(d.pool, d.ledgerHealth))
	if err := registerConnect(mux, d); err != nil {
		return nil, server.Handlers{}, err
	}
	wk, err := registerWellKnown(mux, d)
	if err != nil {
		return nil, server.Handlers{}, err
	}
	transport.NewAgentsRegisterHandler(d.agentRegistry, transport.AgentsRegisterOptions{}).
		RegisterRoutes(mux)
	if d.console.enabled() {
		d.console.handler.RegisterRoutes(mux)
		d.console.reads.RegisterRoutes(mux)
	}
	return mux, wk, nil
}

func registerConnect(mux *http.ServeMux, d muxDeps) error {
	// connectserver.NewExchangeServiceHandler wraps the generated handler with
	// request-id (outermost) → RFC 9421 verify middleware → connect interceptors
	// (protovalidate bidirectional, via WithValidation). KeyResolver and
	// ReplayStore are injected by the application; the SDK orchestrates the
	// verify pass and replay dedup.
	//
	// The option set comes from the function the integration harness also calls,
	// so the mount those tests drive is this mount rather than a hand-kept copy
	// of it. A construction failure is a boot-time config fault: surfaced up the
	// boot chain (run() → main()) so the process exits non-zero, not a panic.
	svrOpts, err := transport.ExchangeMountOptions(d.resolver, d.replay, d.maxSignatures, d.audience)
	if err != nil {
		return fmt.Errorf("exchange mount options: %w", err)
	}
	path, h := connectserver.NewExchangeServiceHandler(transport.NewExchangeHandler(d.exchange), svrOpts...)
	mux.Handle(path, h)
	// CatalogService keeps the raw generated mount (its per-contributor RFC 9421
	// verification runs in CatalogSignatureMiddleware, not the SDK seam) but shares
	// the ExchangeService codec + protovalidate contract through the raw-mount
	// options transport.CatalogMountOptions composes — the SAME shared validation
	// engine, helpers.SharedValidator, that the ExchangeService path composes
	// through connectserver.WithValidation, so the mounts cannot drift onto forked
	// rulesets — plus the message read cap, so the push endpoint, which accepts
	// contributor-supplied payloads, decodes under the same bound as every other
	// RPC. The integration harness calls the same function, so the cap the tests
	// drive is the cap that ships. A construction failure is a boot-time config
	// fault: surfaced up the boot chain (run() → main()) so the process exits
	// non-zero, not a panic.
	catalogOpts, err := transport.CatalogMountOptions(d.audience)
	if err != nil {
		return fmt.Errorf("catalog mount options: %w", err)
	}
	cpath, ch := foraconnect.NewCatalogServiceHandler(
		transport.NewCatalogHandler(d.catalog, d.agentRegistry), catalogOpts...,
	)
	mux.Handle(cpath, ch)
	return nil
}
