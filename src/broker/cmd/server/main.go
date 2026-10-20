// Package main is the Broker service entrypoint.
//
// The Broker receives agent requests (free-form queries or URIs), discovers
// candidate URLs (EXA), probes each domain for /.well-known/fora.json, routes
// to the appropriate Exchange when an exchange represents the publisher,
// and falls back to a bare-URL response otherwise.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentkeys"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/logging"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/replay"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/budget"
	brokerdb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/exa"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/probe"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/purchase"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/rediscli"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/registry"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/resolve"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/signing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/transport"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/xclient"
)

func main() {
	// `broker healthcheck` probes the local /healthz and exits — the
	// distroless image has no shell or curl, so the compose healthcheck
	// execs the service binary itself.
	runhttp.MaybeHealthcheck("BROKER_ADDR", ":8082")
	// A BROKER_LOG_LEVEL that is not a level stops the boot here, before
	// anything connects, through the same broker.exit record as every other
	// start-up failure.
	const levelVar = "BROKER_LOG_LEVEL"
	logger, err := logging.Setup(os.Stdout, levelVar, runhttp.EnvTrimmed(levelVar))
	if err == nil {
		err = run(logger)
	}
	if err != nil {
		logger.Error("broker.exit", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	// Tie the lifecycle context to the process shutdown signals so background
	// goroutines started on it (the per-agent WBA revocation poller and the
	// registry refresher) stop on shutdown instead of leaking. runhttp.Serve
	// installs its own signal handler for HTTP drain; both registrations receive
	// the signal.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The Broker's identity is read first, before anything connects: a
	// Signature-Agent origin every verifier would refuse stops the boot here
	// rather than after the database and Redis are up.
	brokerDomain := runhttp.EnvOr("BROKER_DOMAIN", "broker.local")
	brokerOrigin, err := brokerPublicOrigin(brokerDomain)
	if err != nil {
		return err
	}

	pool, err := setupPool(ctx, logger)
	if err != nil {
		return err
	}
	defer pool.Close()

	redisCli, err := rediscli.Setup(ctx, runhttp.EnvOr("REDIS_URL", ""), logger)
	if err != nil {
		return fmt.Errorf("redis setup: %w", err)
	}
	if redisCli != nil {
		defer func() { _ = redisCli.Close() }()
	}

	brokerID := runhttp.EnvOr("BROKER_ID", "broker-local")
	signer, err := signing.LoadFromEnv(brokerDomain, clock.System{})
	if err != nil {
		return fmt.Errorf("signer setup: %w", err)
	}

	exchangeRepo, logRepo, err := setupRegistryRepos(ctx, pool, logger)
	if err != nil {
		return err
	}

	querySearcher, err := buildQuerySearcher(ctx, logger)
	if err != nil {
		return err
	}
	fetchWiring := buildFetchWiring(ctx, logger)
	identity, err := setupRelayAndKeys(logger, brokerDomain, brokerOrigin)
	if err != nil {
		return err
	}

	mux, wk, err := buildBrokerMux(brokerMuxDeps{
		pool: pool,
		resolveDeps: resolve.Deps{
			Exchanges: exchangeRepo,
			Log:       logRepo,
			Discovery: buildDiscoveryClient(logger),
			Query:     querySearcher,
			Prober:    fetchWiring.prober,
			Exchange:  identity.relay,
			Budget:    budget.Select(redisCli, 0, clock.System{}),
			Endpoints: fetchWiring.endpoints,
			Clk:       clock.System{},
			Verifier:  fetchWiring.verifier,
		},
		signer:        signer,
		brokerID:      brokerID,
		ownKeys:       identity.ownKeys,
		ownSigners:    identity.ownSigners,
		agentResolver: fetchWiring.agentResolver,
		redisCli:      redisCli,
		audience:      identity.audience,
	})
	if err != nil {
		return err
	}

	// Launch refresher goroutine — fire-and-forget; newHealthProbeClient says why the probe dials through the guard.
	go newRegistryRefresher(exchangeRepo, fetchWiring.endpoints, logger).Run(ctx)

	// Keep the served discovery documents fresh: their validity windows are
	// stamped from the signer clock at each build, so a long-lived Broker must
	// rebuild them periodically — the windows exist to expire stale cached
	// copies, never the live service.
	go wk.RunRefresher(ctx, transport.WellKnownRebuildInterval, logger)

	// Request-id is outermost so every route (healthz, well-known, revocation) carries
	// request_id in context and echoes X-Request-ID on responses. The
	// BrokerService connectserver handler (mounted inside buildBrokerMux) adds its
	// own request-id layer outermost to the /fora.v1.BrokerService subtree so that
	// subtree carries the id on the reject path too. Having it here as well means
	// ALL routes carry it, not just the BrokerService surface.
	runhttp.Serve("broker", runhttp.EnvOr("BROKER_ADDR", ":8082"), buildWrapped(logger, mux), logger)
	return nil
}

// setupPool opens and migrates the Broker's database, refusing a boot with no
// BROKER_DSN.
func setupPool(ctx context.Context, logger *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := db.Setup(ctx, db.SetupOptions{
		DSN:             runhttp.EnvOr("BROKER_DSN", ""),
		Migrations:      brokerdb.Migrations,
		MigrationsDir:   brokerdb.MigrationsDir,
		MigrationsTable: brokerdb.MigrationsTable,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("db setup: %w", err)
	}
	if pool == nil {
		return nil, errors.New("BROKER_DSN is required")
	}
	return pool, nil
}

// buildWrapped assembles the Broker's outermost HTTP middleware: the shared
// public-surface stack around the Broker's mux, with the deployment-shape
// switches read from the environment here at the composition root (see
// runhttp.PublicSurfaceOptionsFromEnv for the fail-closed opt-in rationale).
func buildWrapped(logger *slog.Logger, mux http.Handler) http.Handler {
	return transport.WrapPublicSurface(logger, mux, runhttp.PublicSurfaceOptionsFromEnv())
}

// newRelayHTTPClient loads the broker-relay private key and builds an
// *http.Client whose transport stamps the Broker's Web Bot Auth request
// signature, naming origin as its Signature-Agent, on every outbound
// Broker→Exchange call. When the key file is absent the returned client
// carries the guard but no signer — callers hitting a signed Exchange will get
// 401 but the Broker will still boot (useful for local dev without bootstrap).
//
// Both returns dial through the SDK guarded client, never http.DefaultTransport.
// Nothing operator-written bounds this leg's target: the purchase service reads its
// admission off the row it fetched by DOMAIN and then POSTs the agent's signed
// offer to whatever that exchange's own well-known advertises. The SDK's host
// anchoring compares host and port and deliberately leaves the scheme to the
// transport, so without the guard an exchange registered as https can advertise
// http and receive a signed offer in the clear. The identity service holds the
// same contract for the same leg.
//
// The factory's client is taken apart and reassembled rather than used whole,
// because the signer has to sit ABOVE the dial: the signing round-tripper wraps
// the guarded one, and the factory's redirect policy is carried across so a
// redirect cannot walk the request onto a scheme the first hop was refused for.
// Only the timeout is the relay's own.
func newRelayHTTPClient(logger *slog.Logger, origin string) (*http.Client, *xclient.RelayKey, error) {
	guarded := resolvers.NewGuardedClientFromEnv()
	relayClient := func(rt http.RoundTripper) *http.Client {
		return &http.Client{
			Timeout:       10 * time.Second,
			Transport:     rt,
			CheckRedirect: guarded.CheckRedirect,
		}
	}

	path := runhttp.EnvOr("BROKER_RELAY_KEY_FILE", "deploy/broker/broker-key.json")
	_, statErr := os.Stat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		logger.Warn("broker.relay.key_absent", "path", path)
		return relayClient(guarded.Transport), nil, nil
	}
	if statErr != nil {
		return nil, nil, fmt.Errorf("broker-relay stat %s: %w", path, statErr)
	}
	key, err := xclient.LoadRelayKey(path)
	if err != nil {
		return nil, nil, err
	}
	logger.Info("broker.relay.signing", "keyid", key.KeyID)
	signing, err := xclient.NewSigningTransport(
		guarded.Transport, key, origin, 30*time.Second, clock.System{},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("broker-relay signing transport: %w", err)
	}
	return relayClient(signing), key, nil
}

// brokerMuxDeps collects everything buildBrokerMux wires onto the HTTP
// surface; grouping them here keeps run() under the funlen cap without
// exceeding the per-function-arg limit.
type brokerMuxDeps struct {
	pool interface {
		Ping(ctx context.Context) error
	}
	resolveDeps resolve.Deps
	signer      *signing.CoSigner
	brokerID    string
	// ownKeys carries the Broker's own published keys (the relay key) for the
	// served WBA directory. It plays no part in verifying inbound signatures —
	// see agentSig1Resolver.
	ownKeys *transport.KeyRegistry
	// ownSigners hold the private halves of ownKeys, so the served directory's
	// response is signed by every key it lists.
	ownSigners []helpers.Signer
	// agentResolver is the per-agent well-known resolver — the way every agent
	// key is learned. REQUIRED: buildBrokerMux refuses a nil (tests that never
	// verify a signature pass transporttest.NeverResolves()).
	agentResolver helpers.KeyResolver
	// redisCli backs the BrokerService seam's replay store (Redis when present,
	// in-memory otherwise).
	redisCli *redis.Client
	// audience refuses a request addressed to a different party. Nothing this
	// interceptor sees names the Broker today: DiscoveryRequest carries no
	// recipient field, and the items of a TransactionRequest sent to
	// ExecuteTransaction name the Exchanges the Broker routes them to, which the
	// interceptor knows not to compare against the Broker. The fan-out legs the
	// Broker AUTHORS name the Exchange each one goes to, and each Exchange checks
	// them with this same interceptor. Mounted here so an agent-facing RPC that
	// gains a recipient later is guarded the day it does rather than the day
	// somebody remembers.
	audience *foraaudience.Interceptor
}

// agentSig1Resolver returns the resolver the agent-facing surface verifies an
// agent's sig1 with: the /fora.v1.BrokerService connectserver verify gate drives
// the SDK verifier with this one resolver (the per-agent well-known lookup,
// which learns every agent key from the signer's own directory). If a surface
// ever needs a genuinely different trust policy, encode it as an explicit
// parameter here rather than forking a second copy.
//
// The Broker's own-key registry is deliberately NOT a delegate here: it holds
// only keys whose private halves never sign an inbound request (the relay key
// signs outbound Broker→Exchange calls), so a lookup over it could never match
// an inbound signature — it exists solely to build the served WBA directory.
//
// Revocation is scoped to the signer: an agent's key is revoked only by its own
// directory's revocation list, never by another party's. The resolver this
// returns is the SDK WBAKeyResolver behind internal/agentkeys, which reads each
// signer's directory under that directory's own list, so a key its agent
// revoked is refused here. The Broker's own list, which it publishes via
// BROKER_REVOCATION_URL/_FILE for the Exchange to poll, revokes only the
// Broker's keys, so this Broker has no reason to consume it. The Exchange
// resolves each signature the same way, in the directory that signature's own
// member names; on a purchase this Broker relays, it verifies the agent's
// acceptances against the agent key it holds for that directory.
func (d brokerMuxDeps) agentSig1Resolver() helpers.KeyResolver {
	return d.agentResolver
}

// buildBrokerMux assembles the Broker's HTTP surface: healthz, the canonical
// fora.v1.BrokerService Connect endpoint (Resolve and ExecuteTransaction) and
// the unified /.well-known/fora.json route.
// It also returns the
// well-known handler pair so run() can keep the served documents fresh with
// RunRefresher — the published validity windows are stamped at build time, so
// a never-rebuilt document lapses under uptime.
//
// Both failure modes here are wiring bugs at the composition root, so they
// fail construction loudly as errors — the same contract NewKeyRegistry has —
// and run() propagates them instead of this function panicking mid-wire.
func buildBrokerMux(d brokerMuxDeps) (*http.ServeMux, server.Handlers, error) {
	// The inbound resolver is REQUIRED: production wiring always builds one
	// (buildProbeWiring has no off switch), and a test that never presents a
	// signature passes transporttest.NeverResolves() explicitly.
	if d.agentResolver == nil {
		return nil, server.Handlers{},
			errors.New("broker mux: agentResolver is required (tests use transporttest.NeverResolves())")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz(d.pool))
	resolveHandler := resolve.NewService(d.resolveDeps)
	// ADR-019: the canonical fora.v1.BrokerService Connect endpoint is the SOLE
	// agent surface over the resolve and purchase business cores (the bespoke
	// POST /broker/v1/resolve and /broker/v1/exchange/execute routes were
	// removed). connectserver.NewBrokerServiceHandler wraps the generated handler
	// with request-id (outermost) → RFC 9421 verify middleware → connect
	// interceptors (protovalidate bidirectional). MaxSignatures is unset (0 =
	// unbounded): the hop bound is an Exchange-terminal policy; bounding it at the
	// Broker too would double-count the relay hop the Broker is about to add.
	// The resolver and replay store for the BrokerService surface are
	// constructed inline from agentResolver and redisCli.
	brokerResolver := d.agentSig1Resolver()
	replayStore := replay.NewCoreAdapter(replay.NewStore(d.redisCli, "httpsig:broker:replay:"))
	// Built by the function the integration harness builds it with, so the mount
	// those tests drive is this mount. It carries the nil-audience refusal too —
	// the same refusal the Exchange's mounts make, made by the same function:
	// a nil interceptor mounts cleanly and checks nothing, which is the one
	// failure mode this whole package exists to prevent.
	svrOpts, err := transport.BrokerMountOptions(brokerResolver, replayStore, d.audience)
	if err != nil {
		return nil, server.Handlers{}, err
	}
	// The purchase service resolves each signed offer.exchange to the endpoint
	// the Exchange advertises in its own /.well-known/fora.json through the SAME
	// shared resolver discovery uses (resolveDeps.Endpoints) — one guarded
	// instance carrying the env-guarded SSRF client, so a registered domain that
	// rebinds to a private address is refused at dial time, not merely
	// string-checked by the registry gate before the fetch.
	purchaseSvc := purchase.New(d.resolveDeps.Exchanges, d.resolveDeps.Endpoints, d.resolveDeps.Exchange)
	connectPath, connectHandler := connectserver.NewBrokerServiceHandler(
		transport.NewBrokerConnectHandler(resolveHandler, purchaseSvc), svrOpts...,
	)
	mux.Handle(connectPath, connectHandler)
	// BROKER_REVOCATION_URL is advertised in the WBA directory as revocation_url so
	// the Exchange learns where to poll revocations; BROKER_REVOCATION_FILE is the
	// operator's revoked-thumbprint lever served at the route below. Both unset →
	// no revocation channel (the directory omits revocation_url; the route serves
	// the empty, nothing-revoked baseline).
	revURL := runhttp.EnvOr("BROKER_REVOCATION_URL", "")
	wk, err := transport.NewWellKnown(transport.WellKnownConfig{
		Signer:        d.signer,
		BrokerID:      d.brokerID,
		Keys:          d.ownKeys,
		KeySigners:    d.ownSigners,
		RevocationURL: revURL,
	})
	if err != nil {
		return nil, server.Handlers{}, fmt.Errorf("broker well-known manifest build failed: %w", err)
	}
	wk.RegisterRoutes(mux)
	mux.Handle("GET "+forawellknown.RevocationPath,
		transport.NewRevocationHandler(runhttp.EnvOr("BROKER_REVOCATION_FILE", "")))
	return mux, wk, nil
}

// buildAgentResolver builds the per-agent well-known transport-key resolver
// (ADR-009 D2): it resolves an agent's key from the agent's own
// /.well-known/http-message-signatures-directory (the WBA directory the
// Signature-Agent header names) so every agent's sig1 verifies at the
// BrokerService gate. This is the only way agent keys are learned — there is
// no static key file and no off switch, so there is nothing to announce at
// boot; mirrors the Exchange-side resolver, which is equally silent.
func buildAgentResolver(ctx context.Context, fetch *http.Client, logger *slog.Logger) helpers.KeyResolver {
	return agentkeys.NewService(ctx, fetch, logger)
}

// buildProbeWiring constructs the SSRF-guarded fetch client shared by the
// publisher .well-known probe and the per-agent WBA key resolver, then builds
// both. A resolve request carries a caller-influenced domain, so the probe must
// not be steerable onto internal targets; the guard is SDK-owned, constructed
// once here at the composition root from the SDK factory and injected. SKIP_SSRF
// permits docker-internal hosts and ALLOW_INSECURE permits plaintext http in the
// compose/e2e stack.
func buildProbeWiring(
	ctx context.Context, logger *slog.Logger,
) (*probe.Prober, helpers.KeyResolver) {
	guardedFetch := resolvers.NewGuardedClientFromEnv()
	prober := probe.New(guardedFetch, logger, probe.Options{Scheme: wellKnownScheme()})
	return prober, buildAgentResolver(ctx, guardedFetch, logger)
}

// bootstrapRegistry seeds broker.exchanges from the operator's registry file.
// With BROKER_REGISTRY_FILE unset it seeds nothing and the Broker routes to no
// Exchange until one is added — deliberately, so no deployment inherits example
// data it then has to clean out. The empty case returns BEFORE the decoder: a
// zero-byte YAML document decodes to io.EOF, which would fail the boot outright.
func bootstrapRegistry(ctx context.Context, m repo.ExchangeRepo, logger *slog.Logger) error {
	path := runhttp.EnvOr("BROKER_REGISTRY_FILE", "")
	if path == "" {
		logger.WarnContext(ctx, "broker.registry.no_bootstrap")
		return nil
	}
	entries, err := registry.LoadFromFile(path)
	if err != nil {
		return err
	}
	return registry.Reconcile(ctx, m, entries)
}

func buildDiscoveryClient(logger *slog.Logger) exa.DiscoveryClient {
	apiKey := runhttp.EnvOr("EXA_API_KEY", "")
	if apiKey == "" {
		logger.Info("broker.exa.disabled")
		return &exa.StaticClient{}
	}
	client, err := exa.NewClient(nil, apiKey, exa.Options{})
	if err != nil {
		logger.Warn("broker.exa.init_failed", "err", err)
		return &exa.StaticClient{}
	}
	return client
}

func healthz(pool interface {
	Ping(ctx context.Context) error
},
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pool != nil {
			if err := pool.Ping(r.Context()); err != nil {
				http.Error(w, "db unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}
