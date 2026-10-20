// Broker wiring builders: the well-known endpoint resolver, the offer
// Verifier, and the registry repositories run() composes. Split from main.go
// to keep both files under the file-length cap.
package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/offerkeys"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/probe"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/registry"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/transport"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/xclient"
)

// newDiscoveryEndpointResolver builds the SINGLE well-known endpoint resolver
// the discovery and purchase paths share (wired once into
// resolveDeps.Endpoints and consumed by both): each routed exchange's endpoint
// is resolved from the exchange's OWN /.well-known/fora.json (the registry is a
// trust allowlist only; no backfilled registry endpoint is ever queried).
// Scheme is deploy-aware (https prod, http e2e) via FORA_WELLKNOWN_SCHEME.
// The fetch client is the env-guarded SSRF client: the GetByDomain /
// endpointAllowed registry checks only bound the host STRING, so the guard's
// dial-time public-IP enforcement is what refuses a registered domain that
// rebinds to a private/metadata address. This matches the broker's other
// well-known fetches (guardedFetch for the prober + agent resolver), so the
// SSRF default lives once. The guard is SDK-owned; the client is constructed from
// resolvers.NewGuardedClientFromEnv (best-effort address + scheme guards, driven
// by SKIP_SSRF / ALLOW_INSECURE).
func newDiscoveryEndpointResolver() *resolvers.WellKnownEndpointResolver {
	return resolvers.NewWellKnownEndpointResolver(resolvers.WellKnownOptions{
		HTTP:   resolvers.NewGuardedClientFromEnv(),
		Scheme: wellKnownScheme(),
	})
}

// wellKnownScheme is the scheme every broker well-known fetch uses: the
// publisher probe, the endpoint resolver, the offer-key resolver and the health
// refresher's manifest fetch. https in production; FORA_WELLKNOWN_SCHEME=http on
// a stack with no TLS. Read in one place so the fetches cannot come to disagree
// about which scheme an exchange's documents are served over.
func wellKnownScheme() string {
	return runhttp.EnvOr("FORA_WELLKNOWN_SCHEME", "https")
}

// newHealthProbeClient builds the registry health refresher's probe client from
// the same SDK guarded factory every other broker fetch uses. The refresher
// dials the address an exchange advertises about ITSELF, and its verdict
// decides whether discovery and purchases are routed to that exchange — so an
// unguarded probe would let a registered domain that resolves to a private or
// metadata address answer its health check, turn its row live, and receive
// signed traffic. The manifest the refresher reads to find that health check
// goes through this client too, so both legs pass the same guard. Named here
// rather than inlined at the call site so the wiring test can assert what it
// returns.
func newHealthProbeClient() *http.Client {
	return resolvers.NewGuardedClientFromEnv()
}

// newRegistryRefresher builds the registry health refresher over the SAME
// endpoint resolver the discovery and purchase paths route through, so the
// probe measures the address routing uses. Its manifest fetches and health
// checks go through newHealthProbeClient over wellKnownScheme.
func newRegistryRefresher(
	exchangeRepo repo.ExchangeHealthRepo, endpoints registry.EndpointResolver, logger *slog.Logger,
) *registry.Refresher {
	reach := registry.ProbeTransport{HTTP: newHealthProbeClient(), Scheme: wellKnownScheme()}
	return registry.NewRefresher(exchangeRepo, endpoints, reach, logger, 0)
}

// newOfferVerifier builds the fail-closed offer Verifier the resolve core sorts
// every discovered offer through (the broker no longer relays
// unverified offers on the typed path). Keys resolve per exchange DOMAIN from
// the exchange's OWN WBA directory (the offer-signing key's only home after the
// WBA split), read through the SDK's directory fetcher over the same guarded
// client the endpoint resolver uses, and always over https:
// FORA_WELLKNOWN_SCHEME shapes manifest reads, never a key directory. The clock
// is injected (never time.Now inline) so the Verifier's expiry decisions are
// deterministic under test, matching every other broker wiring site.
func newOfferVerifier(clk clock.Clock) core.Verifier {
	return offerVerifierOver(clk, resolvers.NewGuardedClientFromEnv())
}

// offerVerifierOver is newOfferVerifier over a given directory-fetch client:
// production passes the SDK guarded client, and a test passes the client of the
// TLS server that stands in for an Exchange's key directory.
func offerVerifierOver(clk clock.Clock, fetch *http.Client) core.Verifier {
	resolver := offerkeys.New(offerkeys.Config{Client: fetch, Clk: clk})
	return core.NewVerifier(core.Strict, resolver, clk.Now)
}

// brokerFetchWiring bundles the outbound-fetch-dependent broker collaborators
// built at the composition root: the publisher probe and per-agent WBA key
// resolver (over one shared SDK-guarded client), the shared well-known endpoint
// resolver, and the fail-closed offer Verifier. Each fetch client is constructed
// from the SDK factory resolvers.NewGuardedClientFromEnv (best-effort address +
// scheme guards, driven by SKIP_SSRF / ALLOW_INSECURE).
type brokerFetchWiring struct {
	prober        *probe.Prober
	agentResolver helpers.KeyResolver
	endpoints     *resolvers.WellKnownEndpointResolver
	verifier      core.Verifier
}

// buildFetchWiring builds the probe/agent-resolver pair and the resolve-path
// endpoint resolver + offer Verifier.
func buildFetchWiring(ctx context.Context, logger *slog.Logger) brokerFetchWiring {
	prober, agentResolver := buildProbeWiring(ctx, logger)
	return brokerFetchWiring{
		prober:        prober,
		agentResolver: agentResolver,
		endpoints:     newDiscoveryEndpointResolver(),
		verifier:      newOfferVerifier(clock.System{}),
	}
}

// setupRegistryRepos builds the exchange/selection-log repositories and seeds
// the exchange trust registry from the bootstrap env.
func setupRegistryRepos(
	ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger,
) (*repo.PgxExchangeRepo, *repo.PgxSelectionLogRepo, error) {
	exchangeRepo := repo.NewExchangeRepo(pool)
	logRepo := repo.NewSelectionLogRepo(pool)
	if err := bootstrapRegistry(ctx, exchangeRepo, logger); err != nil {
		return nil, nil, fmt.Errorf("registry bootstrap: %w", err)
	}
	return exchangeRepo, logRepo, nil
}

// brokerIdentity is everything derived from BROKER_DOMAIN: the signed outbound
// relay leg, the keys the Broker publishes for peers to verify it, and the
// check that refuses a request addressed to somebody else. One domain, one
// place it is read, so the three cannot come to disagree about who this Broker
// is.
type brokerIdentity struct {
	relay      *xclient.Pool
	ownKeys    *transport.KeyRegistry
	ownSigners []helpers.Signer
	audience   *foraaudience.Interceptor
}

// brokerPublicOriginEnv names the Broker's own https origin: the
// Signature-Agent member every request the Broker signs carries, and so the
// origin whose /.well-known/http-message-signatures-directory an Exchange
// resolves the relay key in. It must be where this Broker's directory is
// served. Unset, it is https://<BROKER_DOMAIN>, the same default the Exchange
// applies to EXCHANGE_PUBLIC_ORIGIN.
const brokerPublicOriginEnv = "BROKER_PUBLIC_ORIGIN"

// brokerPublicOrigin reads BROKER_PUBLIC_ORIGIN and refuses the boot when the
// value is not an https origin (helpers.CheckHTTPSOrigin): scheme https, a
// lowercase host, a port only when it is not 443, and nothing else. A Broker
// that started with such a value would sign every request with a
// Signature-Agent every verifier refuses.
func brokerPublicOrigin(brokerDomain string) (string, error) {
	origin := runhttp.EnvOr(brokerPublicOriginEnv, "https://"+brokerDomain)
	if err := helpers.CheckHTTPSOrigin(origin); err != nil {
		return "", fmt.Errorf("%s: %w", brokerPublicOriginEnv, err)
	}
	return origin, nil
}

func setupRelayAndKeys(logger *slog.Logger, brokerDomain, origin string) (brokerIdentity, error) {
	relayHTTP, relayKey, err := newRelayHTTPClient(logger, origin)
	if err != nil {
		return brokerIdentity{}, fmt.Errorf("broker relay signing: %w", err)
	}
	var own []ed25519.PublicKey
	var ownSigners []helpers.Signer
	if relayKey != nil {
		// The served WBA directory publishes this key with a validity window
		// stamped from the signer clock at each document build. Verifiers
		// address it by its RFC 7638 thumbprint, not a broker-prefixed kid, and
		// accept it only because it signed the directory response.
		own = append(own, relayKey.Private.Public().(ed25519.PublicKey))
		relaySigner, err := relayKey.Signer()
		if err != nil {
			return brokerIdentity{}, fmt.Errorf("broker relay directory signer: %w", err)
		}
		ownSigners = append(ownSigners, relaySigner)
	}
	ownKeys, err := transport.NewKeyRegistry(own...)
	if err != nil {
		return brokerIdentity{}, fmt.Errorf("broker own-key registry: %w", err)
	}
	// Built at boot rather than per request, so a misconfigured BROKER_DOMAIN
	// stops the process instead of surfacing to callers as an internal error
	// that names nothing an operator can act on.
	audience, err := foraaudience.NewInterceptor(brokerDomain)
	if err != nil {
		return brokerIdentity{}, err
	}
	return brokerIdentity{
		relay:      xclient.NewPool(relayHTTP),
		ownKeys:    ownKeys,
		ownSigners: ownSigners,
		audience:   audience,
	}, nil
}
