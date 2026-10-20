// Command server runs the publisher console: the operator-side web application
// a publisher signs in to, registers their business and sites through, and
// downloads their setup files from.
//
// The browser talks only to this service. Everything it decides is decided by
// the Exchange, which this service calls server-side over a signed connection:
// the console says which person is acting, the Exchange says what that person
// may do. Nothing a publisher owns is stored here.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/logging"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pemkeys"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/session"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/app"
	consoledb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
)

// defaultAddr is where the console listens when none is configured.
const defaultAddr = ":8085"

func main() {
	// `console healthcheck` probes the local health route and exits. The
	// runtime image carries no shell and no curl, so the container's probe is
	// this binary, the shape every service here uses.
	runhttp.MaybeHealthcheck("CONSOLE_ADDR", defaultAddr)
	// A CONSOLE_LOG_LEVEL that is not a level stops the boot here, before
	// anything connects, through the same console.exit record as every other
	// start-up failure.
	const levelVar = "CONSOLE_LOG_LEVEL"
	logger, err := logging.Setup(os.Stdout, levelVar, runhttp.EnvTrimmed(levelVar))
	if err == nil {
		err = run(context.Background(), logger)
	}
	if err != nil {
		logger.Error("console.exit", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := loadConfig(ctx, logger)
	if err != nil {
		return err
	}
	handler, err := app.Build(cfg)
	if err != nil {
		return fmt.Errorf("build console: %w", err)
	}
	runhttp.Serve("console", runhttp.EnvOr("CONSOLE_ADDR", defaultAddr), handler, logger)
	return nil
}

// loadConfig reads the deployment's settings and refuses a configuration that
// could not serve a publisher.
//
// Every refusal here is one a person would otherwise meet as a broken page: an
// unset signing key means no call to the Exchange can be verified, an unset
// session key means every sign-in is lost on the next restart. Stopping the
// boot costs a restart; the alternative costs a publisher's afternoon.
func loadConfig(ctx context.Context, logger *slog.Logger) (app.Config, error) {
	pool, err := db.Setup(ctx, db.SetupOptions{
		DSN:             runhttp.EnvOr("CONSOLE_DSN", ""),
		Migrations:      consoledb.Migrations,
		MigrationsDir:   consoledb.MigrationsDir,
		MigrationsTable: consoledb.MigrationsTable,
	}, logger)
	if err != nil {
		return app.Config{}, fmt.Errorf("db setup: %w", err)
	}
	if pool == nil {
		return app.Config{}, errors.New("CONSOLE_DSN is required")
	}
	sessionKey, err := sessionKeyFromEnv()
	if err != nil {
		return app.Config{}, err
	}
	upstream, err := upstreamFromEnv(ctx)
	if err != nil {
		return app.Config{}, err
	}
	signingKey, err := signingKeyFromEnv()
	if err != nil {
		return app.Config{}, err
	}
	exchange, err := exchangeFromEnv(signingKey)
	if err != nil {
		return app.Config{}, err
	}
	publicOrigin := runhttp.EnvOr("CONSOLE_PUBLIC_ORIGIN", "")
	if publicOrigin == "" {
		// A sign-out is sent back here by the login provider, which needs the
		// absolute address; without it nobody could sign out.
		return app.Config{}, errors.New("CONSOLE_PUBLIC_ORIGIN is required")
	}
	// One SSRF-guarded client backs every outbound read of a publisher's own
	// site. The guard is SDK-owned; it is built once, here, and injected, so
	// this is the one place to look to see what posture the console reaches
	// the internet under.
	fetcher, err := pagefetch.New(
		pagefetch.NewClient(pagefetch.ClientOptions{
			Timeout: runhttp.EnvDuration("CONSOLE_FETCH_TIMEOUT", 10*time.Second),
		}),
		pagefetch.Limits{Scheme: runhttp.EnvOr("CONSOLE_FETCH_SCHEME", "https")},
	)
	if err != nil {
		return app.Config{}, fmt.Errorf("page fetcher: %w", err)
	}
	detector, err := cdn.NewDetector(net.DefaultResolver,
		runhttp.EnvDuration("CONSOLE_DNS_TIMEOUT", 3*time.Second))
	if err != nil {
		return app.Config{}, fmt.Errorf("cdn detector: %w", err)
	}
	return app.Config{
		Logger:        logger,
		Accounts:      repo.NewAccountRepo(sqlc.New(pool)),
		Upstream:      upstream,
		SessionKey:    sessionKey,
		SecureCookies: strings.HasPrefix(publicOrigin, "https://"),
		PublicOrigin:  publicOrigin,
		Exchange:      exchange,
		SigningKey:    signingKey,
		Deployment:    deploymentFromEnv(),
		Pages:         fetcher,
		Detector:      detectorAdapter{detector},
		SupportEmail:  runhttp.EnvOr("CONSOLE_SUPPORT_EMAIL", ""),
		DefaultRate:   runhttp.EnvOr("CONSOLE_DEFAULT_RATE", ""),
	}, nil
}

// sessionKeyFromEnv reads the 32-byte key the cookies are sealed under.
//
// Unlike the identity registry, there is no ephemeral fallback. That service's
// sealed cookies live for the length of one sign-up; these carry a working
// day's session, and generating a key per process would sign every publisher
// out on each deploy and on each replica they happened to reach.
func sessionKeyFromEnv() ([]byte, error) {
	raw := runhttp.EnvTrimmed("CONSOLE_SESSION_KEY")
	if raw == "" {
		return nil, errors.New("CONSOLE_SESSION_KEY is required: " +
			"a generated-per-process key would sign every publisher out on each restart " +
			"and on each replica they reached")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("CONSOLE_SESSION_KEY is not base64: %w", err)
	}
	if len(key) != session.KeyLen {
		return nil, fmt.Errorf("CONSOLE_SESSION_KEY must decode to %d bytes, got %d",
			session.KeyLen, len(key))
	}
	return key, nil
}

// upstreamFromEnv builds the login-provider client.
func upstreamFromEnv(ctx context.Context) (*oidcup.Zitadel, error) {
	clientID, err := runhttp.EnvOrFile("CONSOLE_OIDC_CLIENT_ID")
	if err != nil {
		return nil, err
	}
	clientSecret, err := runhttp.EnvOrFile("CONSOLE_OIDC_CLIENT_SECRET")
	if err != nil {
		return nil, err
	}
	issuer := runhttp.EnvTrimmed("CONSOLE_OIDC_ISSUER")
	redirect := runhttp.EnvTrimmed("CONSOLE_OIDC_REDIRECT_URL")
	switch {
	case issuer == "":
		return nil, errors.New("CONSOLE_OIDC_ISSUER is required")
	case clientID == "":
		return nil, errors.New("CONSOLE_OIDC_CLIENT_ID is required")
	case clientSecret == "":
		return nil, errors.New("CONSOLE_OIDC_CLIENT_SECRET is required")
	case redirect == "":
		return nil, errors.New("CONSOLE_OIDC_REDIRECT_URL is required")
	}
	return oidcup.New(ctx, oidcup.Config{
		Issuer:       issuer,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirect,
		Scopes:       strings.Fields(runhttp.EnvOr("CONSOLE_OIDC_SCOPES", "openid profile email")),
	})
}

// signingKeyFromEnv reads the console's own Ed25519 key.
//
// One key, read once, used for two things that have to agree: signing every
// call to the Exchange, and publishing the public half at this console's key
// directory. Reading it twice, or configuring the two halves separately, is how
// a deployment ends up signing with a key its directory does not carry.
func signingKeyFromEnv() (ed25519.PrivateKey, error) {
	pemPath := runhttp.EnvTrimmed("CONSOLE_SIGNING_KEY_FILE")
	if pemPath == "" {
		return nil, errors.New("CONSOLE_SIGNING_KEY_FILE is required: " +
			"every call to the Exchange is signed, and the Exchange verifies it against " +
			"the key this console publishes")
	}
	raw, err := os.ReadFile(pemPath) //nolint:gosec // the path is operator configuration, not caller input
	if err != nil {
		return nil, fmt.Errorf("read CONSOLE_SIGNING_KEY_FILE: %w", err)
	}
	priv, err := pemkeys.ParseEd25519Private(raw)
	if err != nil {
		return nil, fmt.Errorf("CONSOLE_SIGNING_KEY_FILE: %w", err)
	}
	return priv, nil
}

// exchangeFromEnv builds the signed client the console calls the Exchange with.
func exchangeFromEnv(priv ed25519.PrivateKey) (*exchangeclient.Client, error) {
	directory, err := directoryOriginFromEnv()
	if err != nil {
		return nil, err
	}
	return exchangeclient.New(exchangeclient.Config{
		BaseURL:    runhttp.EnvTrimmed("CONSOLE_EXCHANGE_URL"),
		Directory:  directory,
		SigningKey: priv,
		Timeout:    runhttp.EnvDuration("CONSOLE_EXCHANGE_TIMEOUT", 15*time.Second),
	})
}

// directoryOriginFromEnv is the https origin the console names as its own key
// directory in every signature it sends: CONSOLE_DIRECTORY_ORIGIN, or
// CONSOLE_PUBLIC_ORIGIN when that is unset. The console serves its directory
// itself, so the origin must reach this console. The boot is refused when the
// value is not an https origin (helpers.CheckHTTPSOrigin), because the
// Exchange refuses every signature that names anything else.
//
// It is a setting of its own, defaulting to the public origin, because the two
// can differ: the public origin is also the address a browser uses, which a
// deployment may serve over plain http on an internal network while the
// Exchange reaches the console's directory through a TLS proxy.
func directoryOriginFromEnv() (string, error) {
	origin := runhttp.EnvOr("CONSOLE_DIRECTORY_ORIGIN", runhttp.EnvTrimmed("CONSOLE_PUBLIC_ORIGIN"))
	if err := helpers.CheckHTTPSOrigin(origin); err != nil {
		return "", fmt.Errorf("CONSOLE_DIRECTORY_ORIGIN (default CONSOLE_PUBLIC_ORIGIN): %w", err)
	}
	return origin, nil
}

// deploymentFromEnv reads what every generated file carries from this side.
func deploymentFromEnv() setup.Deployment {
	return setup.Deployment{
		ExchangeDomain:   runhttp.EnvOr("CONSOLE_EXCHANGE_DOMAIN", "exchange.example"),
		ExchangeEndpoint: runhttp.EnvOr("CONSOLE_EXCHANGE_URL", "https://exchange.example"),
		ContributorHost:  runhttp.EnvOr("CONSOLE_CONTRIBUTOR_HOST", ""),
		SupportedProfiles: strings.Fields(runhttp.EnvOr(
			"CONSOLE_SUPPORTED_PROFILES", strings.Join(forawellknown.ExchangeProfiles(), " "))),
		EdgeReleaseRef: runhttp.EnvOr("CONSOLE_EDGE_RELEASE_REF", ""),
		EdgePackageDir: runhttp.EnvOr("CONSOLE_EDGE_PACKAGE_DIR", ""),
		ExchangeWBAURL: runhttp.EnvOr("CONSOLE_EXCHANGE_WBA_URL", ""),
	}
}

// detectorAdapter gives the detector the error-returning shape the pages
// depend on.
//
// The detector itself cannot fail: a lookup that answers nothing is a site
// behind no network it recognises, which is an answer. The port takes an error
// anyway, so that a deployment which resolves a site some other way -- a
// lookup service, a cached answer -- can report a failure rather than report
// "no network" and have a publisher told their CDN is unsupported.
type detectorAdapter struct{ inner *cdn.Detector }

func (d detectorAdapter) Detect(
	ctx context.Context, host string, hdr http.Header,
) (cdn.Detection, error) {
	return d.inner.Detect(ctx, host, hdr), nil
}
