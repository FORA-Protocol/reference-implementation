package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
	"github.com/redis/go-redis/v9"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentkeys"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/keypolicy"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/replay"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
)

// buildHTTPSigDeps constructs the RFC 9421 KeyResolver + ReplayStore used by
// the connectserver verify middleware. Every verification key is learned via
// well-known discovery — there is no static key file: each signature's key is
// resolved in the directory its own Signature-Agent member names. The Broker's
// directory (the origin of EXCHANGE_BROKER_WELLKNOWN_URL, also the revocation
// authority for the Broker's keys) carries the Broker relay key, and each
// agent's own directory carries that agent's keys. Redis, when REDIS_URL is
// set, backs the replay store; otherwise an in-memory store is used.
func buildHTTPSigDeps(
	ctx context.Context, logger *slog.Logger, fetchClient *http.Client,
) (helpers.KeyResolver, *replay.CoreAdapter, replay.Store, error) {
	// The fail-closed guard runs FIRST, before anything opens a connection or
	// starts a goroutine: a boot this guard is about to refuse must not dial
	// Redis, and must not start the per-agent revocation poller
	// (agentkeys.NewService starts it on ctx — the boot-guard test verifies
	// the refused boot leaks no goroutine). The URL is read through
	// requireBrokerWellKnownURL, which is also what brokerDirectoryHost reads it
	// through, so the resolver and the service's relay check cannot be bound to
	// two different Brokers.
	brokerOrigin, err := brokerDirectoryOrigin()
	if err != nil {
		return nil, nil, nil, err
	}
	var redisCli *redis.Client
	if dsn := runhttp.EnvOr("REDIS_URL", ""); dsn != "" {
		opts, err := redis.ParseURL(dsn)
		if err != nil {
			return nil, nil, nil, err
		}
		redisCli = redis.NewClient(opts)
		if pingErr := redisCli.Ping(ctx).Err(); pingErr != nil {
			_ = redisCli.Close()
			return nil, nil, nil, pingErr
		}
		logger.Info("exchange.httpsig.replay_store_ready", "addr", opts.Addr)
	} else {
		// Announce the fallback rather than leaving its absence as the only
		// evidence: a per-process replay store is correct at one instance and
		// unsafe above it, so the state has to be greppable. Matches the
		// Broker's broker.redis.disabled, level included.
		logger.Info("exchange.httpsig.replay_store_disabled")
	}
	// One store, two views of it. The SDK verify seam takes the adapter, which
	// folds a signature into a single nonce; the console surface verifies
	// through the SDK's pure face and records the (keyid, signature) pair
	// itself, so it takes the store. Sharing the store is what keeps a
	// signature replayed against the other surface from being first-seen here.
	store := replay.NewStore(redisCli, "httpsig:exchange:replay:")
	agent := agentkeys.NewService(ctx, fetchClient, logger)
	return wellKnownAwareResolver(ctx, brokerOrigin, agent, fetchClient, logger),
		replay.NewCoreAdapter(store), store, nil
}

// brokerDirectoryOrigin is the https origin of the Broker's key directory: the
// scheme and host of EXCHANGE_BROKER_WELLKNOWN_URL. A signature whose
// Signature-Agent member is exactly this origin is the Broker's, and its key is
// resolved in the Broker's directory under the Broker's revocation list.
//
// The boot is refused when the URL is not https or names no origin. A key
// directory is only ever fetched over https, and a Broker named by an http URL
// could never match a Signature-Agent member, which the profile admits only as
// an https origin — every relay would be refused while the Exchange looked
// healthy.
func brokerDirectoryOrigin() (string, error) {
	raw, err := requireBrokerWellKnownURL()
	if err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("EXCHANGE_BROKER_WELLKNOWN_URL %q is not a URL: %w", raw, err)
	}
	origin := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
	if err := helpers.CheckHTTPSOrigin(origin); err != nil {
		return "", fmt.Errorf("EXCHANGE_BROKER_WELLKNOWN_URL %q does not name an https origin: %w", raw, err)
	}
	return origin, nil
}

// requireBrokerWellKnownURL reads EXCHANGE_BROKER_WELLKNOWN_URL and refuses an
// empty value. The URL is mandatory by design: without a revocation authority
// ahead of the per-agent path, a key the Broker revoked would keep verifying
// for as long as some later delegate still resolves it. Rather than log a
// warning and boot fail-open, an unset URL refuses the boot — the caller
// propagates the error.
func requireBrokerWellKnownURL() (string, error) {
	url := runhttp.EnvOr("EXCHANGE_BROKER_WELLKNOWN_URL", "")
	if url == "" {
		return "", errors.New("EXCHANGE_BROKER_WELLKNOWN_URL is required: refusing to boot " +
			"without a revocation authority ahead of the per-agent well-known path " +
			"(a revoked key would otherwise keep verifying)")
	}
	return url, nil
}

// brokerDirectoryHost is the canonical host of the Broker's key directory: what
// the service compares a verified Signature-Agent against to recognise a relayed
// ExecuteTransaction (service.ExchangeConfig.BrokerHost).
//
// It is derived from the SAME environment variable the revocation resolver above
// is bound to, so the Broker whose revocation list the transport trusts and the
// Broker whose relays the service authorizes are one party. Deriving it a second
// way — a separate variable, or a seeded fora.agents row — is how the two would
// come to name different hosts, and a relay would then be authenticated by one
// and unrecognised by the other.
//
// The refusal is requireBrokerWellKnownURL's, so a boot with no Broker
// configured stops here rather than silently treating every relay as a direct
// request. A URL that names no host is refused too: it cannot be compared
// against a caller's directory, so it would match nothing.
func brokerDirectoryHost() (string, error) {
	url, err := requireBrokerWellKnownURL()
	if err != nil {
		return "", err
	}
	host, err := agentid.FromDirectory(url)
	if err != nil {
		return "", fmt.Errorf("EXCHANGE_BROKER_WELLKNOWN_URL %q does not name a host: %w", url, err)
	}
	return host, nil
}

// wellKnownAwareResolver composes the Exchange's verification-key resolver.
// Each signature is resolved on its own, in the directory its own covered
// Signature-Agent member names, and against no other party's:
//
//   - A signature whose member is the Broker's origin (brokerOrigin, from the
//     REQUIRED EXCHANGE_BROKER_WELLKNOWN_URL) is resolved by a revocation-aware
//     SDK WBAKeyResolver bound to the Broker's directory, so a kid the Broker
//     has revoked (or whose validity window has lapsed) is rejected
//     authoritatively, and an outage of that directory fails closed.
//   - Every other signature goes to next — in production the per-agent
//     well-known resolver (ADR-009 D2), which fetches the signer's own
//     /.well-known/http-message-signatures-directory, under that directory's
//     own revocation list, so a never-before-seen agent can authenticate at the
//     transport layer and be lazy-registered by the service.
//
// The Broker's directory and revocation list therefore never decide an agent's
// signature: a request the agent signed alone does not depend on the Broker's
// directory being reachable, and the Broker's list cannot revoke an agent's key.
//
// brokerOrigin is read and validated once by the caller's boot guard
// (brokerDirectoryOrigin holds the rationale and the refusal) and bound here.
//
// What retires a rotated Broker relay key — both mechanisms, in different
// roles. Routine rotation: the Broker's next boot publishes only the new key,
// so a re-fetched directory stops resolving the old thumbprint within the
// directory cache TTL, and the published validity window (stamped as 90 days
// from the clock at each periodic document rebuild) bounds how long a stale
// cached copy can keep the old key verifying — the SDK resolver rejects a
// lapsed window as ErrKeyExpired, which is authoritative below. Compromise:
// the Broker's revocation list, consulted first by this composite, retires
// the thumbprint immediately, even while some directory still publishes it. Neither substitutes for the other:
// the window needs no operator action but takes up to its full span to bite;
// the revocation list is immediate but is an explicit operator act.
func wellKnownAwareResolver(
	ctx context.Context, brokerOrigin string, next helpers.KeyResolver, fetchClient *http.Client, logger *slog.Logger,
) helpers.KeyResolver {
	broker := brokerRevocationResolver(ctx, brokerOrigin, fetchClient, logger)
	return keypolicy.ResolverFunc(func(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
		if helpers.SignatureAgentFromContext(ctx) == brokerOrigin {
			return broker.Resolve(ctx, keyID)
		}
		return next.Resolve(ctx, keyID)
	})
}

// brokerLookupLimit bounds one lookup of the Broker's directory. The lookup runs
// on a context the request cannot cancel (see brokerRevocationResolver), which
// also drops the request's deadline, so without this a client with no timeout
// of its own could hold every request waiting on the directory indefinitely.
// It is a safety net, like the limit the SDK's endpoint resolver puts on its
// own shared fetch: the guarded client production injects already stops each
// fetch after 10 seconds, and that is the limit a slow Broker meets.
const brokerLookupLimit = 30 * time.Second

// brokerRevocationResolver resolves a Broker-signed kid against the Broker's
// WBA directory. A revoked/expired verdict is authoritative, and so is a
// directory that simply lacks the kid (ErrUnknownKey): the signature named the
// Broker, so no other directory may answer for it. A fetch/parse failure
// (ErrDirectoryUnavailable) FAILS CLOSED: the revocation channel is
// unavailable, so we cannot prove the kid is not revoked. The SDK resolver's
// revocation poller runs on ctx.
//
// The Broker's directory is a FIXED config origin (from
// EXCHANGE_BROKER_WELLKNOWN_URL). wellKnownAwareResolver routes a signature here
// only when its own member is that origin, and this delegate binds the
// context's Signature-Agent slot to the configured origin on every Resolve, so
// the lookup can only ever reach the configured Broker.
//
// A thumbprint the broker revoked but which its directory no longer lists
// resolves to ErrUnknownKey — the SDK gates the revocation snapshot only for
// directory-listed keys, since removal is deliberately not revocation. This
// delegate reports that case as revoked by consulting the SDK's
// revocation-set-membership accessor, so the refusal names the real reason.
//
// The lookup does not inherit the request's cancellation. One fetch of the
// Broker's directory serves every request waiting on it, because the SDK
// resolver shares a refresh per host, so a caller that disconnects must not cut
// it short for the others. Run on the request's own context, the fetch would
// fail with the departed caller's context, every request waiting on it would be
// refused, and the arm below would report the Broker unreachable, a Wake-on-call
// alert, while the Broker was answering. The Identity directory publisher
// (src/identity/internal/publisher) and the SDK's endpoint resolver follow the
// same rule for the work they share. The per-agent fallback keeps the request's
// context: the directory it fetches is named by the caller, and a detached
// fetch there would let a caller leave the Exchange waiting on a host of its
// choosing.
func brokerRevocationResolver(
	ctx context.Context, origin string, fetchClient *http.Client, logger *slog.Logger,
) helpers.KeyResolver {
	poll := runhttp.EnvDuration("EXCHANGE_REVOCATION_POLL_INTERVAL", 0)
	resolver := keypolicy.RunningWBAResolver(ctx, resolvers.WBAKeyResolverOptions{
		HTTP:         fetchClient,
		PollInterval: poll,
		TTL:          runhttp.EnvDuration("EXCHANGE_DIRECTORY_TTL", 0),
		Logger:       logger,
	})
	logger.Info("exchange.httpsig.wellknown_enabled",
		"broker_directory", origin, "poll_interval", poll)
	return keypolicy.ResolverFunc(func(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
		lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), brokerLookupLimit)
		defer cancel()
		pub, err := resolver.Resolve(helpers.WithSignatureAgent(lookupCtx, origin), keyID)
		switch {
		case err == nil:
			return pub, nil
		case errors.Is(err, helpers.ErrUnknownKey):
			// The directory was fetched and parsed but carries no such kid, so
			// the Broker-signed request is refused either way. The revocation
			// channel is authoritative about which thumbprints are REVOKED
			// regardless of directory membership, so a kid the Broker revoked and
			// then dropped from its directory is reported as revoked rather than
			// unknown: consult the SDK's revocation-set membership accessor. It
			// answers from the named directory's own list only, and origin is the
			// Broker directory this delegate serves, so the Broker's own list is
			// the one consulted.
			if resolver.Revoked(keyID, origin) {
				return nil, fmt.Errorf("%w: directory-absent keyid=%q on broker revocation list", resolvers.ErrKeyRevoked, keyID)
			}
			return nil, err
		case errors.Is(err, resolvers.ErrKeyRevoked), errors.Is(err, resolvers.ErrKeyExpired):
			return nil, err // authoritative negative
		default:
			// ErrDirectoryUnavailable (fetch/decode failure) and anything else:
			// the revocation channel is unavailable, so we CANNOT prove this kid
			// is not revoked. Fail closed rather than fall through to the
			// per-agent fallback, which would silently re-enable a
			// possibly-revoked kid on a well-known outage. Returning a
			// non-ErrUnknownKey error stops the CompositeResolver here. The WARN
			// is the metric signal — the outage is observable, never silent. The
			// lookup above does not depend on the request, so a caller that
			// disconnects cannot bring a request here: each record is the
			// Broker's document failing to arrive.
			logger.WarnContext(ctx, "exchange.httpsig.broker_wellknown_unavailable",
				"keyid", keyID, "broker_directory", origin, "err", err)
			return nil, err
		}
	})
}

// admissionDeps are the checks a request clears before a handler sees it: who
// signed it, whether that signature has been seen before, how many hops it may
// carry, and whether it names this Exchange at all. They travel together
// because they are one decision — admit this request or refuse it — and because
// building them in one place keeps run() linear.
type admissionDeps struct {
	resolver      helpers.KeyResolver
	replay        *replay.CoreAdapter
	maxSignatures int
	audience      *foraaudience.Interceptor
	// replayStore is the same store replay wraps, in the shape a surface that
	// verifies outside the SDK seam needs. The publisher-registration surface
	// is the one caller.
	replayStore replay.Store
}

// buildAdmissionDeps wires them from the environment.
//
// The recipient interceptor is built HERE, at boot, rather than per request: a
// misconfigured EXCHANGE_DOMAIN then stops the process, instead of refusing
// honest callers with an internal error that names nothing an operator can act
// on.
func buildAdmissionDeps(
	ctx context.Context, logger *slog.Logger, fetchClient *http.Client,
) (admissionDeps, error) {
	resolver, replayAdapter, replayStore, err := buildHTTPSigDeps(ctx, logger, fetchClient)
	if err != nil {
		return admissionDeps{}, err
	}
	audience, err := foraaudience.NewInterceptor(exchangeDomain())
	if err != nil {
		return admissionDeps{}, err
	}
	return admissionDeps{
		resolver:      resolver,
		replay:        replayAdapter,
		maxSignatures: int(exchangeMaxIntermediaryHops()),
		audience:      audience,
		replayStore:   replayStore,
	}, nil
}
